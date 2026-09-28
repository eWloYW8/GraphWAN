package link

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/graphwan/graphwan/internal/model"
)

type Edge struct {
	mu        sync.Mutex
	links     map[string]*Link
	preferred string
	active    string
	switched  time.Time
}

func NewEdge(preferred string) *Edge { return &Edge{links: map[string]*Link{}, preferred: preferred} }
func (e *Edge) SetPreferred(candidate string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.preferred = candidate
}
func (e *Edge) Add(link *Link) { e.mu.Lock(); defer e.mu.Unlock(); e.links[link.ID()] = link }
func (e *Edge) Remove(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.links, id)
	if e.active == id {
		e.active = ""
	}
}
func (e *Edge) Send(ctx context.Context, frame []byte) error {
	e.mu.Lock()
	selected := e.selectLocked(time.Now())
	e.mu.Unlock()
	if selected == nil {
		return ErrUnavailable
	}
	return selected.Send(ctx, frame)
}
func (e *Edge) Report() []model.LinkStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := e.selectLocked(time.Now())
	result := make([]model.LinkStatus, 0, len(e.links))
	for _, link := range e.links {
		stats := link.Stats()
		stats.Active = link == active && stats.Healthy
		result = append(result, stats)
	}
	slices.SortFunc(result, func(a, b model.LinkStatus) int { return cmp.Compare(a.LinkID, b.LinkID) })
	return result
}
func (e *Edge) selectLocked(now time.Time) *Link {
	var best, preferred, current *Link
	var bestRTT, currentRTT float64
	for id, link := range e.links {
		stats := link.Stats()
		if !stats.Healthy {
			continue
		}
		if id == e.active {
			current = link
			currentRTT = stats.RTTMillis
		}
		if best == nil || stats.RTTMillis < bestRTT || stats.RTTMillis == bestRTT && id < best.ID() {
			best = link
			bestRTT = stats.RTTMillis
		}
		if stats.CandidateID == e.preferred && (preferred == nil || stats.RTTMillis < preferred.Stats().RTTMillis) {
			preferred = link
		}
	}
	selected := best
	if preferred != nil {
		selected = preferred
	} else if current != nil && best != nil && best != current {
		// Automatic switching needs a meaningful improvement and a short hold time.
		// Explicit preference and failure recovery bypass both forms of hysteresis.
		if now.Sub(e.switched) < 2*time.Second || currentRTT-bestRTT < max(2, currentRTT*0.15) {
			selected = current
		}
	}
	if selected == nil {
		e.active = ""
		return nil
	}
	if e.active != selected.ID() {
		e.active = selected.ID()
		e.switched = now
	}
	return selected
}
