package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/graphwan/graphwan/internal/model"
)

// browserEvent is a replaceable snapshot, not an unbounded event log. Reconnects
// get complete desired state; subsequent events include state only when changed.
type browserEvent struct {
	At       time.Time           `json:"at"`
	Revision uint64              `json:"revision"`
	State    *model.State        `json:"state,omitempty"`
	Agents   []model.AgentStatus `json:"agents"`
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	session, ok := s.auth.find(r)
	if !ok {
		fail(w, 401, "authentication required")
		return
	}
	cookie, _ := r.Cookie(cookieName)
	key := tokenHash(cookie.Value)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		fail(w, 503, "controller shutting down")
		return
	}
	if s.eventCount >= 32 || s.eventClients[key] >= 4 {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		fail(w, 429, "too many live views")
		return
	}
	s.eventCount++
	s.eventClients[key]++
	s.streamWG.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.eventCount--
		s.eventClients[key]--
		if s.eventClients[key] == 0 {
			delete(s.eventClients, key)
		}
		s.mu.Unlock()
		s.streamWG.Done()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	// Each write has its own deadline; a stalled browser cannot hold server locks
	// or accumulate snapshots. HTTP/2 supports per-stream deadlines as well.
	defer rc.SetWriteDeadline(time.Time{})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	expiry := time.NewTimer(time.Until(session.Expires))
	defer expiry.Stop()
	var revision uint64
	first := true
	for {
		if _, ok := s.auth.find(r); !ok {
			return
		}
		state, err := s.db.Read()
		if err != nil {
			return
		}
		event := browserEvent{At: time.Now(), Revision: state.Revision, Agents: s.telemetry(state)}
		if first || state.Revision != revision {
			event.State = &state
		}
		data, err := json.Marshal(event)
		if err != nil {
			return
		}
		if err := rc.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
		first, revision = false, state.Revision
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-expiry.C:
			return
		case <-ticker.C:
		}
	}
}
