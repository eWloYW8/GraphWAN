package agent

import (
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func reportDue(compact, force, settling, changed bool, elapsed time.Duration) bool {
	interval := 15 * time.Second
	if compact {
		interval = time.Minute
	}
	return force || settling || changed || elapsed >= interval
}

// Idle resource/RTT samples ride on the periodic report. Configuration, errors,
// link transitions and user-data counters still trigger the next two-second tick.
func reportChanged(previous, next model.AgentReport) bool {
	if previous.Version != next.Version || previous.AppliedRevision != next.AppliedRevision || previous.ConfigError != next.ConfigError || previous.RuntimeError != next.RuntimeError || len(previous.Links) != len(next.Links) {
		return true
	}
	old := make(map[string]model.LinkStatus, len(previous.Links))
	for _, link := range previous.Links {
		link.RTTMillis, link.Loss = 0, 0
		old[link.LinkID] = link
	}
	for _, link := range next.Links {
		link.RTTMillis, link.Loss = 0, 0
		if prior, ok := old[link.LinkID]; !ok || prior != link {
			return true
		}
	}
	return false
}
