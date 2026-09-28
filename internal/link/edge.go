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
	selection *selectionState
}

func newEdge(preferred string) *Edge { return &Edge{links: map[string]*Link{}, preferred: preferred} }
func (e *Edge) SetPreferred(candidate string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.preferred = candidate
}
func (e *Edge) Add(link *Link) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.selection != nil {
		link.setActive(false)
	}
	e.links[link.ID()] = link
}
func (e *Edge) Remove(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.selection != nil {
		delete(e.selection.terms, id)
	}
	delete(e.links, id)
	if e.active == id {
		e.active = ""
	}
}
func (e *Edge) Send(ctx context.Context, frame []byte) error {
	e.mu.Lock()
	selected := e.selectLocked(time.Now())
	defer e.mu.Unlock()
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
	if e.selection != nil {
		e.advanceLocked(now)
		active := e.links[e.active]
		if active != nil && active.Stats().Healthy {
			return active
		}
		return nil
	}
	selected := e.bestLocked(now)
	e.activateLocked(selected, now)
	return selected
}
func (e *Edge) activateLocked(selected *Link, now time.Time) {
	id := ""
	if selected != nil {
		id = selected.ID()
	}
	if e.active != id {
		if old := e.links[e.active]; old != nil {
			old.setActive(false)
		}
		e.active, e.switched = id, now
		if selected != nil {
			selected.setActive(true)
		}
	}
}
func (e *Edge) bestLocked(now time.Time) *Link {
	// An old session remains usable until a healthy replacement for the same
	// candidate exists. Prefer that replacement before retiring the old session.
	newest := map[string]time.Time{}
	for _, l := range e.links {
		if l.Stats().Healthy && l.Created().After(newest[l.Info().CandidateID]) {
			newest[l.Info().CandidateID] = l.Created()
		}
	}
	var best, preferred, current *Link
	var bestRTT, currentRTT float64
	for id, link := range e.links {
		stats := link.Stats()
		if !stats.Healthy || link.RenewalDue() && link.Created().Before(newest[stats.CandidateID]) {
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
		if stats.CandidateID == e.preferred && (preferred == nil || (stats.RTTMillis < preferred.Stats().RTTMillis || stats.RTTMillis == preferred.Stats().RTTMillis && id < preferred.ID())) {
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
	return selected
}
