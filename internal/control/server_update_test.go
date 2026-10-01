package control

import (
	"github.com/eWloYW8/GraphWAN/internal/cluster"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"testing"
	"time"
)

func TestServerUpdateSerialization(t *testing.T) {
	now := time.Now()
	id := model.NewID()
	server := model.Server{Update: &model.UpdateRequest{ID: id, CreatedAt: now, Asset: model.ReleaseAsset{Version: "v1.2.3"}}}
	for _, test := range []struct {
		name    string
		status  cluster.Status
		pending bool
	}{
		{"awaiting delivery", cluster.Status{}, true},
		{"downloading", cluster.Status{Update: &model.UpdateStatus{RequestID: id, Phase: "downloading"}}, true},
		{"startup check", cluster.Status{Version: "v1.2.3", Update: &model.UpdateStatus{RequestID: id, Phase: "installing"}}, true},
		{"failed", cluster.Status{Update: &model.UpdateStatus{RequestID: id, Phase: "failed"}}, false},
		{"succeeded", cluster.Status{Update: &model.UpdateStatus{RequestID: id, Phase: "succeeded"}}, false},
		{"old completion", cluster.Status{Update: &model.UpdateStatus{RequestID: model.NewID(), Phase: "succeeded"}}, true},
		{"older release without updater", cluster.Status{Version: "v1.2.3"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := serverUpdatePending(server, test.status, now); got != test.pending {
				t.Fatalf("pending=%v, want %v", got, test.pending)
			}
		})
	}
	if serverUpdatePending(server, cluster.Status{}, now.Add(31*time.Minute)) {
		t.Fatal("expired request blocks new updates")
	}
	clone := model.CloneServers([]model.Server{server})
	clone[0].Update.ID = model.NewID()
	if clone[0].Update.ID == server.Update.ID {
		t.Fatal("update request was not deeply cloned")
	}
}
