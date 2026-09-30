package link

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
)

type Edge struct {
	mu           sync.Mutex
	links        map[string]*Link
	preferred    string
	active       string
	replacements map[string]string
	aliases      map[string]string
	switched     time.Time
	selection    *selectionState
}

func newEdge(preferred string) *Edge {
	return &Edge{links: map[string]*Link{}, preferred: preferred, replacements: map[string]string{}, aliases: map[string]string{}}
}

// Replace excludes a redundant session from selection before closing it. A saved
// preference for its candidate follows the retained session on the same IP path.
func (e *Edge) Replace(old, replacement string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.links[old] == nil || e.links[replacement] == nil || old == replacement {
		return
	}
	for candidate, id := range e.aliases {
		if id == old {
			e.aliases[candidate] = replacement
		}
	}
	for id, target := range e.replacements {
		if target == old {
			e.replacements[id] = replacement
		}
	}
	e.aliases[e.links[old].Info().CandidateID] = replacement
	e.replacements[old] = replacement
}

func (e *Edge) ForgetReplacement(old string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l := e.links[old]; l != nil && e.aliases[l.Info().CandidateID] == e.replacements[old] {
		delete(e.aliases, l.Info().CandidateID)
	}
	delete(e.replacements, old)
}
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
	delete(e.replacements, id)
	for old, target := range e.replacements {
		if target == id {
			delete(e.replacements, old)
		}
	}
	for candidate, target := range e.aliases {
		if target == id {
			delete(e.aliases, candidate)
		}
	}
	if e.active == id {
		e.active = ""
	}
}
func (e *Edge) Send(ctx context.Context, frame []byte) error {
	return e.SendBatch(ctx, [][]byte{frame})
}
func (e *Edge) SendBatch(ctx context.Context, frames [][]byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	selected := e.sendLinkLocked()
	if selected == nil {
		return ErrUnavailable
	}
	return selected.SendBatch(ctx, frames)
}
func (e *Edge) SendOwnedBatch(ctx context.Context, frames []*packetbuf.Buffer) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	selected := e.sendLinkLocked()
	if selected == nil {
		packetbuf.ReleaseAll(frames)
		return ErrUnavailable
	}
	return selected.SendOwnedBatch(ctx, frames)
}
func (e *Edge) sendLinkLocked() *Link {
	// Coordinated selection is maintained by Tick and authenticated control
	// messages. A data packet must not scan or format every standby Link.
	selected := e.links[e.active]
	if e.selection == nil {
		selected = e.selectLocked(time.Now())
	} else if selected != nil && !selected.healthy() {
		selected = nil
	}
	return selected
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
		if active != nil && active.healthy() {
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
		if l.healthy() && l.Created().After(newest[l.Info().CandidateID]) {
			newest[l.Info().CandidateID] = l.Created()
		}
	}
	var best, preferred *Link
	var bestRTT float64
	for id, link := range e.links {
		if replacement := e.links[e.replacements[id]]; replacement != nil && replacement.healthy() {
			continue
		}
		stats := link.Stats()
		if !stats.Healthy || link.RenewalDue() && link.Created().Before(newest[stats.CandidateID]) {
			continue
		}
		if best == nil || stats.RTTMillis < bestRTT || stats.RTTMillis == bestRTT && id < best.ID() {
			best = link
			bestRTT = stats.RTTMillis
		}
		if (stats.CandidateID == e.preferred || e.aliases[e.preferred] == id) && (preferred == nil || (stats.RTTMillis < preferred.Stats().RTTMillis || stats.RTTMillis == preferred.Stats().RTTMillis && id < preferred.ID())) {
			preferred = link
		}
	}
	selected := best
	if preferred != nil {
		selected = preferred
	}
	return selected
}
