package link

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

// Selection is an authenticated control message received on the proposed Link.
// Its link identity is implicit in the channel, never supplied by the remote.
type Selection struct {
	kind     byte
	term     [16]byte
	sequence uint64
}

type selectionState struct {
	leader              bool
	term                [16]byte
	sequence            uint64
	target              string
	accepted, confirmed bool
	retry               time.Time
	// A Link belongs to exactly one leader incarnation. This also bounds memory
	// by live Links rather than accumulating all historical negotiation terms.
	terms map[string][16]byte
}

// NewCoordinatedEdge elects the lexicographically smaller Node ID as selector.
// Both endpoints must have distinct validated Node IDs from the same Edge.
func NewCoordinatedEdge(local, remote model.ID, preferred string) *Edge {
	e := newEdge(preferred)
	e.selection = &selectionState{leader: local < remote, terms: map[string][16]byte{}}
	if e.selection.leader {
		rand.Read(e.selection.term[:])
	}
	return e
}

// Tick maintains selection even when no user packets or reports are requested.
func (e *Edge) Tick() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.selection != nil {
		e.advanceLocked(time.Now())
	}
}

func (l *Link) sendSelection(kind byte, term [16]byte, sequence uint64) {
	raw := make([]byte, 25)
	raw[0] = kind
	copy(raw[1:17], term[:])
	binary.BigEndian.PutUint64(raw[17:], sequence)
	select {
	case l.control <- raw:
	default: // The Edge retries; never block forwarding on a full control queue.
	}
}

func (e *Edge) advanceLocked(now time.Time) {
	s := e.selection
	if active := e.links[e.active]; active != nil && !active.healthy() {
		e.activateLocked(nil, now)
	}
	if !s.leader {
		return
	}
	best := e.bestLocked(now)
	if best == nil {
		return
	}
	if best.ID() != s.target {
		s.sequence++
		s.target, s.accepted, s.confirmed, s.retry = best.ID(), false, false, time.Time{}
	}
	if s.confirmed || now.Before(s.retry) {
		return
	}
	kind := byte(prepareMessage)
	if s.accepted {
		kind = commitMessage
	}
	best.sendSelection(kind, s.term, s.sequence)
	s.retry = now.Add(100 * time.Millisecond)
}

// HandleSelection never trusts an announcement from another Edge, an unhealthy
// Link, a stale generation or the endpoint which is not the elected selector.
func (e *Edge) HandleSelection(l *Link, message Selection) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.selection
	if s == nil || e.links[l.ID()] != l || !l.healthy() {
		return
	}
	now := time.Now()
	if s.leader {
		if message.term != s.term || message.sequence != s.sequence || l.ID() != s.target {
			return
		}
		switch message.kind {
		case acceptMessage:
			if !s.accepted {
				s.accepted = true
				e.activateLocked(l, now)
			}
			l.sendSelection(commitMessage, s.term, s.sequence)
			s.retry = now.Add(100 * time.Millisecond)
		case confirmMessage:
			if s.accepted {
				s.confirmed = true
			}
		}
		return
	}
	if message.kind != prepareMessage && message.kind != commitMessage {
		return
	}
	if term, ok := s.terms[l.ID()]; ok && term != message.term {
		return
	}
	s.terms[l.ID()] = message.term
	if message.term != s.term {
		if message.kind != prepareMessage {
			return
		}
		// Restarted/reconfigured leaders establish fresh authenticated channels.
		// Wait for all healthy channels from the previous incarnation to leave;
		// delayed old datagrams cannot reset the new incarnation's sequence.
		for id, term := range s.terms {
			if term == s.term && e.links[id] != nil && e.links[id].healthy() {
				return
			}
		}
		e.activateLocked(nil, now)
		s.term, s.sequence, s.target = message.term, 0, ""
		s.accepted, s.confirmed = false, false
	}
	if message.sequence < s.sequence {
		return
	}
	if message.sequence == s.sequence && l.ID() != s.target {
		return
	}
	switch message.kind {
	case prepareMessage:
		if message.sequence > s.sequence {
			// Quiesce the previous sender BEFORE granting the new Link. The leader
			// continues on the old Link until this acceptance arrives, then switches
			// and commits. Thus the two senders never select different Links.
			e.activateLocked(nil, now)
			s.sequence, s.target, s.accepted, s.confirmed = message.sequence, l.ID(), true, false
		}
		l.sendSelection(acceptMessage, s.term, s.sequence)
	case commitMessage:
		if message.sequence != s.sequence || !s.accepted {
			return
		}
		e.activateLocked(l, now)
		s.confirmed = true
		l.sendSelection(confirmMessage, s.term, s.sequence)
	}
}

// CanRetire keeps the old session until both senders have completed a switch.
// Unselected standby sessions may then be removed after their replacement heals.
func (e *Edge) CanRetire(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active != id && (e.selection == nil || e.selection.confirmed && e.selection.target != id)
}
