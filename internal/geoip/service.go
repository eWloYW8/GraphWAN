package geoip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

const (
	cacheTTL        = 7 * 24 * time.Hour
	retryDelay      = 15 * time.Minute
	requestInterval = 650 * time.Millisecond // Below IP.SB's 100/minute and 5/second limits.
	maxCacheEntries = 8192
)

type Location struct {
	IP          string  `json:"ip"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	City        string  `json:"city"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
}
type AgentLocation struct {
	PublicIPs []string  `json:"public_ips"`
	Location  *Location `json:"location,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}
type Result struct {
	Agents   map[model.ID]AgentLocation `json:"agents"`
	Pending  bool                       `json:"pending"`
	Error    string                     `json:"error,omitempty"`
	Database string                     `json:"database,omitempty"` // Retained for API compatibility; identifies the provider.
}
type cacheEntry struct {
	Location  *Location `json:"location,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	RetryAt   time.Time `json:"retry_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}
type Service struct {
	mu                   sync.Mutex
	wg                   sync.WaitGroup
	ctx                  context.Context
	cancel               context.CancelFunc
	directory            string
	cache                map[netip.Addr]cacheEntry
	loading, closed      bool
	nextRequest, retryAt time.Time
	client               *http.Client
	log                  *slog.Logger
}

// New loads a small local cache. Network requests run only when a geography
// consumer requests public endpoints, never on the routing or Agent paths.
func New(directory string, log *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	if log == nil {
		log = slog.Default()
	}
	s := &Service{directory: directory, ctx: ctx, cancel: cancel, log: log,
		cache: make(map[netip.Addr]cacheEntry), client: &http.Client{Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.loadCache()
	return s
}

func (s *Service) Locations(agents []model.Agent) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	result := Result{Agents: make(map[model.ID]AgentLocation, len(agents)), Database: "IP.SB"}
	var due []netip.Addr
	seen := make(map[netip.Addr]bool)
	for _, agent := range agents {
		ips := publicEndpoints(agent.Endpoints, now)
		entry := AgentLocation{PublicIPs: make([]string, 0, len(ips))}
		for _, ip := range ips {
			entry.PublicIPs = append(entry.PublicIPs, ip.String())
			cached := s.cache[ip]
			if entry.Location == nil && cached.Location != nil {
				entry.Location = cached.Location
			}
			if cached.Error != "" {
				result.Error = "IP.SB lookup failed; cached locations remain available. Retrying later."
			}
			if !seen[ip] && !now.Before(cached.ExpiresAt) && !now.Before(cached.RetryAt) {
				due = append(due, ip)
				seen[ip] = true
			}
		}
		if len(ips) == 0 {
			entry.Reason = "No valid public IP endpoint"
		} else if entry.Location == nil {
			entry.Reason = "Public IP location unavailable from IP.SB"
		}
		result.Agents[agent.ID] = entry
	}
	if len(due) > 0 && !s.closed && !s.loading && !now.Before(s.retryAt) {
		s.loading = true
		s.wg.Add(1)
		go s.refresh(due)
	}
	result.Pending = s.loading
	return result
}

// One serial worker deduplicates overlapping panel requests and bounds API use.
// Stale successful results survive both network failures and Server restarts.
func (s *Service) refresh(ips []netip.Addr) {
	defer s.wg.Done()
	defer func() { s.mu.Lock(); s.loading = false; s.mu.Unlock() }()
	for _, ip := range ips {
		timer := time.NewTimer(time.Until(s.nextRequest))
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.nextRequest = time.Now().Add(requestInterval)
		location, cooldown, err := s.lookup(ip)
		if s.ctx.Err() != nil {
			return
		}
		now := time.Now()
		s.mu.Lock()
		entry := s.cache[ip]
		if err != nil {
			entry.RetryAt = now.Add(retryDelay)
			entry.Error = "IP.SB lookup failed"
			if cooldown > 0 {
				s.retryAt = now.Add(cooldown)
			}
		} else {
			entry = cacheEntry{Location: location, ExpiresAt: now.Add(cacheTTL)}
		}
		if len(s.cache) >= maxCacheEntries {
			// Evict the oldest entry instead of letting endpoint churn grow the cache.
			var oldest netip.Addr
			var expires time.Time
			for key, value := range s.cache {
				if !oldest.IsValid() || value.ExpiresAt.Before(expires) {
					oldest, expires = key, value.ExpiresAt
				}
			}
			delete(s.cache, oldest)
		}
		s.cache[ip] = entry
		s.mu.Unlock()
		if err != nil {
			s.log.Warn("IP.SB GeoIP lookup failed", "ip", ip, "error", err)
		}
		if err := s.saveCache(); err != nil {
			s.log.Warn("GeoIP cache save failed", "error", err)
		}
		if cooldown > 0 {
			return
		}
	}
}

func validCoordinates(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}

func (s *Service) lookup(ip netip.Addr) (*Location, time.Duration, error) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, "https://api.ip.sb/geoip/"+ip.String(), nil)
	if err != nil {
		return nil, retryDelay, err
	}
	req.Header.Set("User-Agent", "GraphWAN-GeoIP/1.0 (+https://github.com/eWloYW8/GraphWAN)")
	req.Header.Set("Accept", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return nil, retryDelay, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cooldown := retryDelay
		if value := response.Header.Get("Retry-After"); value != "" {
			if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
				cooldown = max(cooldown, time.Duration(seconds)*time.Second)
			} else if date, err := http.ParseTime(value); err == nil {
				cooldown = max(cooldown, time.Until(date))
			}
		}
		return nil, cooldown, fmt.Errorf("IP.SB HTTP %d", response.StatusCode)
	}
	const limit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, retryDelay, err
	}
	if len(body) > limit {
		return nil, retryDelay, errors.New("IP.SB response exceeds size limit")
	}
	var record struct {
		IP          string   `json:"ip"`
		Latitude    *float64 `json:"latitude"`
		Longitude   *float64 `json:"longitude"`
		City        string   `json:"city"`
		Country     string   `json:"country"`
		CountryCode string   `json:"country_code"`
	}
	if err := json.Unmarshal(body, &record); err != nil {
		return nil, retryDelay, err
	}
	returnedIP, err := netip.ParseAddr(record.IP)
	if err != nil || returnedIP.Unmap() != ip {
		return nil, retryDelay, errors.New("IP.SB returned a different IP")
	}
	// Missing coordinates mean unlocated, never an invented point at (0, 0).
	if record.Latitude == nil || record.Longitude == nil {
		return nil, 0, nil
	}
	if !validCoordinates(*record.Latitude, *record.Longitude) {
		return nil, retryDelay, errors.New("IP.SB returned invalid coordinates")
	}
	return &Location{IP: ip.String(), Latitude: *record.Latitude, Longitude: *record.Longitude,
		City: record.City, Country: record.Country, CountryCode: record.CountryCode}, 0, nil
}

func (s *Service) loadCache() {
	if s.directory == "" {
		return
	}
	f, err := os.Open(filepath.Join(s.directory, "ip-sb-cache.json"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.log.Warn("GeoIP cache open failed", "error", err)
		return
	}
	defer f.Close()
	var entries map[netip.Addr]cacheEntry
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 || json.Unmarshal(data, &entries) != nil {
		s.log.Warn("Ignoring invalid GeoIP cache")
		return
	}
	for ip, entry := range entries {
		if !publicIP(ip) || ip != ip.Unmap() {
			continue
		}
		if entry.Location != nil && (entry.Location.IP != ip.String() || !validCoordinates(entry.Location.Latitude, entry.Location.Longitude)) {
			continue
		}
		s.cache[ip] = entry
		if len(s.cache) >= maxCacheEntries {
			break
		}
	}
}

func (s *Service) saveCache() error {
	if s.directory == "" {
		return nil
	}
	s.mu.Lock()
	data, err := json.Marshal(s.cache)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.directory, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.directory, ".ip-sb-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.directory, "ip-sb-cache.json"))
}

func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	s.client.CloseIdleConnections()
}
