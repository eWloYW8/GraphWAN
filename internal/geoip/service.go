package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	maxminddb "github.com/oschwald/maxminddb-golang/v2"
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
	Agents       map[model.ID]AgentLocation `json:"agents"`
	Pending      bool                       `json:"pending"`
	Error        string                     `json:"error,omitempty"`
	Database     string                     `json:"database,omitempty"`
	DatabaseDate string                     `json:"database_date,omitempty"`
}

type Service struct {
	mu                    sync.Mutex
	wg                    sync.WaitGroup
	ctx                   context.Context
	cancel                context.CancelFunc
	directory, customPath string
	reader                *maxminddb.Reader
	month                 string
	loading, closed       bool
	retryAt               time.Time
	lastError             string
	log                   *slog.Logger
}

// New does no disk or network I/O. A database is loaded/downloaded only when
// an administrator opens the globe or requests a node's location information.
func New(directory, customPath string, log *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	if log == nil {
		log = slog.Default()
	}
	return &Service{directory: directory, customPath: customPath, ctx: ctx, cancel: cancel, log: log}
}

func (s *Service) Locations(agents []model.Agent) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	candidates := make([][]netip.Addr, len(agents))
	hasPublicIP := false
	for i, agent := range agents {
		candidates[i] = publicEndpoints(agent.Endpoints, now)
		hasPublicIP = hasPublicIP || len(candidates[i]) > 0
	}
	if hasPublicIP {
		s.ensure(now)
	}
	result := Result{Agents: make(map[model.ID]AgentLocation, len(agents)), Pending: s.loading, Error: s.lastError}
	if s.reader != nil {
		result.Database = s.reader.Metadata.DatabaseType
		result.DatabaseDate = s.reader.Metadata.BuildTime().UTC().Format("2006-01-02")
	}
	// Deduplicate shared IPs and UDP/TCP advertisements within the response.
	cache := map[netip.Addr]*Location{}
	for i, agent := range agents {
		ips := candidates[i]
		entry := AgentLocation{PublicIPs: make([]string, 0, len(ips))}
		for _, ip := range ips {
			entry.PublicIPs = append(entry.PublicIPs, ip.String())
		}
		switch {
		case len(ips) == 0:
			entry.Reason = "No valid public IP endpoint"
		case s.reader == nil:
			entry.Reason = "GeoIP database unavailable"
		default:
			for _, ip := range ips {
				location, found := cache[ip]
				if !found {
					location = lookup(s.reader, ip)
					cache[ip] = location
				}
				if location != nil {
					entry.Location = location
					break
				}
			}
			if entry.Location == nil {
				entry.Reason = "Public IP not located in the GeoIP database"
			}
		}
		result.Agents[agent.ID] = entry
	}
	return result
}

func lookup(reader *maxminddb.Reader, ip netip.Addr) *Location {
	var record struct {
		Location struct {
			Latitude  *float64 `maxminddb:"latitude"`
			Longitude *float64 `maxminddb:"longitude"`
		} `maxminddb:"location"`
		City struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"city"`
		Country struct {
			Names   map[string]string `maxminddb:"names"`
			ISOCode string            `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	result := reader.Lookup(ip)
	if !result.Found() || result.Decode(&record) != nil || record.Location.Latitude == nil || record.Location.Longitude == nil {
		return nil
	}
	lat, lon := *record.Location.Latitude, *record.Location.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return nil
	}
	return &Location{IP: ip.String(), Latitude: lat, Longitude: lon, City: record.City.Names["en"], Country: record.Country.Names["en"], CountryCode: record.Country.ISOCode}
}

// Called under mu; a single background load serves every HTTP request. An old
// database stays usable while the current month downloads or a refresh fails.
func (s *Service) ensure(now time.Time) {
	month := now.UTC().Format("2006-01")
	if s.closed || s.loading || now.Before(s.retryAt) || s.reader != nil && (s.customPath != "" || s.month == month) {
		return
	}
	s.loading = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.load(month)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.loading = false
		if err != nil && !s.closed {
			s.retryAt = time.Now().Add(15 * time.Minute)
			s.lastError = "GeoIP database unavailable; retry later or configure GRAPHWAN_GEOIP_DB."
			if s.reader != nil {
				s.lastError = "GeoIP update failed; using the cached database."
			}
			s.log.Warn("GeoIP database load failed", "error", err)
		} else {
			s.lastError = ""
		}
	}()
}

func open(path string) (*maxminddb.Reader, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(strings.ToLower(reader.Metadata.DatabaseType), "city") {
		reader.Close()
		return nil, errors.New("GeoIP requires a city MMDB with coordinates")
	}
	return reader, nil
}

func (s *Service) install(reader *maxminddb.Reader, month string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		reader.Close()
		return context.Canceled
	}
	if s.reader != nil {
		s.reader.Close()
	}
	s.reader, s.month = reader, month
	return nil
}

func (s *Service) load(month string) error {
	if s.customPath != "" {
		reader, err := open(s.customPath)
		if err != nil {
			return err
		}
		return s.install(reader, month)
	}
	if s.directory == "" {
		return errors.New("GeoIP storage directory is not configured")
	}
	name := "dbip-city-lite-" + month + ".mmdb"
	destination := filepath.Join(s.directory, name)
	if reader, err := open(destination); err == nil {
		return s.install(reader, month)
	}
	// Reuse the most recent local database across restarts, including offline.
	s.mu.Lock()
	loaded := s.reader != nil
	s.mu.Unlock()
	if !loaded {
		files, _ := filepath.Glob(filepath.Join(s.directory, "dbip-city-lite-????-??.mmdb"))
		slices.Sort(files)
		for i := len(files) - 1; i >= 0; i-- {
			if reader, err := open(files[i]); err == nil {
				if err := s.install(reader, ""); err != nil {
					return err
				}
				break
			}
		}
	}
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		return err
	}
	if err := s.download(destination, month); err != nil {
		return err
	}
	reader, err := open(destination)
	if err != nil {
		return err
	}
	if err = s.install(reader, month); err != nil {
		return err
	}
	// Managed files only. Keep the current and immediately previous month.
	previous, _ := time.Parse("2006-01", month)
	keepPrevious := "dbip-city-lite-" + previous.AddDate(0, -1, 0).Format("2006-01") + ".mmdb"
	files, _ := filepath.Glob(filepath.Join(s.directory, "dbip-city-lite-????-??.mmdb"))
	for _, file := range files {
		if filepath.Base(file) != name && filepath.Base(file) != keepPrevious {
			_ = os.Remove(file)
		}
	}
	return nil
}

func (s *Service) download(destination, month string) error {
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://download.db-ip.com/free/dbip-city-lite-"+month+".mmdb.gz", nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "GraphWAN-GeoIP")
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("invalid GeoIP download redirect")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GeoIP download HTTP %d", response.StatusCode)
	}
	zipped, err := gzip.NewReader(io.LimitReader(response.Body, 128<<20))
	if err != nil {
		return err
	}
	defer zipped.Close()
	temp, err := os.CreateTemp(s.directory, ".geoip-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	const maxSize = 256 << 20
	n, err := io.Copy(temp, io.LimitReader(zipped, maxSize+1))
	if err != nil {
		return err
	}
	if n > maxSize {
		return errors.New("GeoIP database exceeds size limit")
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	verified, err := open(temp.Name())
	if err != nil {
		return err
	}
	verified.Close()
	return os.Rename(temp.Name(), destination)
}

func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reader != nil {
		s.reader.Close()
		s.reader = nil
	}
}
