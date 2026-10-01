package agent

import (
	"github.com/eWloYW8/GraphWAN/internal/model"
	"reflect"
)

// Idle resource/RTT samples ride on the periodic report. Configuration, errors,
// link transitions and user-data counters still trigger the next two-second tick.
func reportChanged(previous, next model.AgentReport) bool {
	if !reflect.DeepEqual(previous.Update, next.Update) || previous.Version != next.Version || previous.AppliedRevision != next.AppliedRevision || previous.RoutingHash != next.RoutingHash || previous.ConfigError != next.ConfigError || previous.RuntimeError != next.RuntimeError || len(previous.Links) != len(next.Links) {
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
