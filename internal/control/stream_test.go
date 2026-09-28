package control

import (
	"math"
	"testing"

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
