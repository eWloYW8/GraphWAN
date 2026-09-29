package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/peer"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

type introduction struct {
	Candidate string `json:"candidate"`
	Target    string `json:"target,omitempty"`
	Endpoint  string `json:"endpoint"`
	Scope     string `json:"scope,omitempty"`
}

func receiveIntroduction(ctx context.Context, channel *peer.Channel) (introduction, error) {
	raw, err := channel.Receive(ctx)
	if err != nil {
		return introduction{}, err
	}
	var intro introduction
	if len(raw) < 2 || len(raw) > 256 || raw[0] != 0 {
		return intro, errors.New("missing link introduction")
	}
	if err := json.Unmarshal(raw[1:], &intro); err != nil {
		return intro, err
	}
	if len(intro.Candidate) != 32 || len(intro.Endpoint) != 32 || (intro.Scope != "" && len(intro.Scope) != 32) {
		return intro, errors.New("invalid candidate identity")
	}
	return intro, nil
}
func (g *group) schedule() {
	defer g.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		g.edge.Tick()
		g.retireSessions()
		cfg := g.policy.Load()
		candidates := g.candidates(cfg, true)
		valid := map[string]bool{}
		for _, candidate := range candidates {
			valid[candidate.ID] = true
		}
		g.mu.Lock()
		for id, state := range g.attempts {
			if !valid[id] && !state.inflight {
				delete(g.attempts, id)
			}
		}
		g.mu.Unlock()
		for _, candidate := range candidates {
			g.mu.Lock()
			if g.ctx.Err() != nil {
				g.mu.Unlock()
				return
			}
			connected := false
			for _, l := range g.links {
				select {
				case <-l.Done():
					continue
				default:
				}
				if l.Info().CandidateID == candidate.ID && !l.RenewalDue() {
					connected = true
					break
				}
			}
			state := g.attempts[candidate.ID]
			if state == nil {
				state = &attempt{candidate: candidate}
				g.attempts[candidate.ID] = state
			}
			if connected || state.inflight || time.Now().Before(state.next) {
				g.mu.Unlock()
				continue
			}
			select {
			case g.mesh.slots <- struct{}{}:
			default:
				g.mu.Unlock()
				continue
			}
			// The candidate list was built outside the lock. A configuration
			// update may have revoked it before this slot became available.
			if !g.allowsCandidateLocked(g.policy.Load(), candidate) {
				<-g.mesh.slots
				g.mu.Unlock()
				continue
			}
			dialCtx, cancel := context.WithTimeout(g.ctx, 12*time.Second)
			state.inflight, state.candidate, state.cancel = true, candidate, cancel
			g.wg.Add(1)
			g.mu.Unlock()
			go func() {
				defer cancel()
				defer g.wg.Done()
				defer func() { <-g.mesh.slots }()
				err := g.dial(dialCtx, candidate)
				g.mu.Lock()
				defer g.mu.Unlock()
				state.inflight, state.cancel = false, nil
				if err == nil {
					state.delay = time.Second
					state.next = time.Time{}
					return
				}
				state.delay = min(max(time.Second, state.delay*2), 30*time.Second)
				state.next = time.Now().Add(state.delay/2 + time.Duration(rand.Int64N(int64(state.delay/2)+1)))
			}()
		}
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (g *group) dial(ctx context.Context, candidate link.Candidate) error {
	parsed, err := url.Parse(candidate.Endpoint.URL)
	if err != nil {
		return err
	}
	target, err := candidate.DialTarget()
	if err != nil {
		return err
	}
	address, err := transport.EndpointDialAddress(candidate.Endpoint, candidate.Family, target)
	if err != nil {
		return err
	}
	var raw transport.Conn
	if candidate.Endpoint.Transport == model.TCP && candidate.Method == link.Punch {
		conn, err := g.mesh.dialTCPPunch(ctx, candidate, g.policy.Load().peer.PublicKey)
		if err != nil {
			return err
		}
		raw = conn
	} else if candidate.Endpoint.Transport == model.TCP {
		dialer := net.Dialer{}
		conn, err := dialer.DialContext(ctx, "tcp"+strconv.Itoa(candidate.Family), address)
		if err != nil {
			return err
		}
		raw = transport.NewStream(conn)
	} else if candidate.Endpoint.Transport == model.WS || candidate.Endpoint.Transport == model.WSS {
		conn, err := transport.DialWebSocketAt(ctx, candidate.Endpoint, candidate.Family, g.policy.Load().peer.PublicKey, nil, target)
		if err != nil {
			return err
		}
		raw = conn
	} else if candidate.Endpoint.Transport == model.GRPC {
		conn, err := transport.DialGRPCAt(ctx, candidate.Endpoint, candidate.Family, g.policy.Load().peer.PublicKey, nil, target)
		if err != nil {
			return err
		}
		raw = conn
	} else if candidate.Endpoint.Transport == model.QUIC {
		conn, err := g.mesh.quic.DialAt(ctx, candidate.Endpoint, candidate.Family, g.policy.Load().peer.PublicKey, target)
		if err != nil {
			return err
		}
		raw = conn
	} else {
		if !target.IsValid() {
			target, err = netip.ParseAddr(parsed.Hostname())
		}
		if err != nil || !target.IsValid() {
			return errors.New("unresolved UDP candidate")
		}
		port, err := strconv.ParseUint(parsed.Port(), 10, 16)
		if err != nil {
			return err
		}
		conn, err := g.mesh.udp.Dial(netip.AddrPortFrom(target, uint16(port)))
		if err != nil {
			return err
		}
		raw = conn
	}
	channel, err := peer.Dial(ctx, raw, g.mesh.secure(g.policy.Load(), candidate.Endpoint.Transport))
	if err != nil {
		return err
	}
	intro := introduction{Candidate: candidate.ID, Endpoint: endpointFingerprint(candidate.Endpoint), Scope: link.ScopeIdentity(candidate.Scope)}
	if candidate.Target.IsValid() {
		intro.Target = candidate.Target.String()
	}
	encoded, _ := json.Marshal(intro)
	if err := channel.Send(ctx, append([]byte{0}, encoded...)); err != nil {
		channel.Close()
		return err
	}
	g.register(channel, candidate)
	return nil
}

// Retire only older sessions with a healthy replacement for the same candidate,
// after both endpoints have finished selecting their current common Link.
func (g *group) retireSessions() {
	g.mu.Lock()
	newest := map[string]time.Time{}
	for _, l := range g.links {
		candidate := l.Info().CandidateID
		if l.Stats().Healthy && l.Created().After(newest[candidate]) {
			newest[candidate] = l.Created()
		}
	}
	retire := []*link.Link{}
	for _, l := range g.links {
		if l.RenewalDue() && l.Created().Before(newest[l.Info().CandidateID]) && g.edge.CanRetire(l.ID()) {
			retire = append(retire, l)
		}
	}
	g.mu.Unlock()
	for _, l := range retire {
		l.Close()
	}
}
