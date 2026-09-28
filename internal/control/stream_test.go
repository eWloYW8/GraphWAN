package control

import (
	"math"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestTelemetryAdmission(t *testing.T) {
	snapshot, err := routing.Compile(testutil.Topology(), testutil.ID(10))
	if err != nil {
		t.Fatal(err)
	}
	link := model.LinkStatus{NetworkID: testutil.ID(1), EdgeID: testutil.ID(40), LinkID: "live-1", Transport: model.UDP, Healthy: true, Active: true, RTTMillis: 12}
	report := model.AgentReport{AppliedRevision: snapshot.Revision, Links: []model.LinkStatus{link}}
	if err := validateReport(snapshot, report); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*model.AgentReport){
		"future revision":    func(r *model.AgentReport) { r.AppliedRevision++ },
		"foreign edge":       func(r *model.AgentReport) { r.Links[0].EdgeID = testutil.ID(41) },
		"disabled transport": func(r *model.AgentReport) { r.Links[0].Transport = model.WSS },
		"invalid latency":    func(r *model.AgentReport) { r.Links[0].RTTMillis = math.NaN() },
		"invalid loss":       func(r *model.AgentReport) { r.Links[0].Loss = math.Inf(1) },
		"two active links": func(r *model.AgentReport) {
			extra := r.Links[0]
			extra.LinkID = "live-2"
			r.Links = append(r.Links, extra)
		},
		"unhealthy active link": func(r *model.AgentReport) { r.Links[0].Healthy = false },
	} {
		t.Run(name, func(t *testing.T) {
			r := report
			r.Links = append([]model.LinkStatus{}, report.Links...)
			change(&r)
			if err := validateReport(snapshot, r); err == nil {
				t.Fatal("invalid telemetry accepted")
			}
		})
	}
}

func TestTelemetryRemovesStalePathsWithoutMutatingStoredReports(t *testing.T) {
	state := testutil.Topology()
	id := state.Agents[0].ID
	link := model.LinkStatus{NetworkID: state.Networks[0].ID, EdgeID: state.Networks[0].Edges[0].ID, LinkID: "session", Transport: model.UDP, Active: true, Healthy: true, TXBytes: 1234}
	s := &Server{statuses: map[model.ID]model.AgentStatus{id: {AgentID: id, Connected: true, LastSeen: time.Now().Add(-time.Minute), AgentReport: model.AgentReport{Links: []model.LinkStatus{link}}}}}
	result := s.telemetry(state)
	if result[0].Connected || result[0].Links[0].Active || result[0].Links[0].Healthy || result[0].Links[0].TXBytes != 1234 {
		t.Fatal("stale runtime was presented as live or historical counters lost")
	}
	if !s.statuses[id].Links[0].Healthy || !s.statuses[id].Connected {
		t.Fatal("serializing telemetry mutated shared reports")
	}
	state.Networks[0].Edges = nil
	if len(s.telemetry(state)[0].Links) != 0 {
		t.Fatal("removed edge remained in telemetry")
	}
}
