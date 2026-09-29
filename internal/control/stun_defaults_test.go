package control_test

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
)

func TestEnrollmentDefaultSTUNCanBeCustomizedAndDisabled(t *testing.T) {
	h := setup(t)
	h.login(t)
	enrolled, _ := h.enroll(t, "automatic discovery")
	state := h.state(t)
	defaults := model.DefaultSTUNServers()
	if len(defaults) == 0 || !slices.Equal(state.Agents[0].STUNServers, defaults) {
		t.Fatal("enrollment omitted public STUN defaults")
	}
	snapshot, err := routing.Compile(state, enrolled.AgentID)
	if err != nil || !slices.Equal(snapshot.STUNServers, defaults) {
		t.Fatal("defaults did not reach the Agent snapshot", err)
	}
	snapshot.STUNServers[0] = "changed.invalid:1"
	defaults[0] = "changed.invalid:2"
	if !slices.Equal(state.Agents[0].STUNServers, model.DefaultSTUNServers()) {
		t.Fatal("default list aliases mutable configuration")
	}
	patch := func(body any) model.State {
		t.Helper()
		current := h.state(t)
		h.request(t, http.MethodPatch, "/api/v1/agents/"+string(enrolled.AgentID), body, map[string]string{"If-Match": strconv.FormatUint(current.Revision, 10)}, http.StatusOK)
		return h.state(t)
	}
	custom := []string{"127.0.0.1:3478", "tcp://127.0.0.1:3478"}
	patch(map[string]any{"stun_servers": custom})
	state = patch(map[string]any{"name": "renamed", "exclude_container_ips": true})
	snapshot, err = routing.Compile(state, enrolled.AgentID)
	if err != nil || !snapshot.ExcludeContainerIPs {
		t.Fatal("container filter missing from snapshot", err)
	}
	if !slices.Equal(state.Agents[0].STUNServers, custom) {
		t.Fatal("unrelated edit reset custom discovery")
	}
	patch(map[string]any{"stun_servers": []string{}})
	state = patch(map[string]any{"listen_port": 24753})
	snapshot, err = routing.Compile(state, enrolled.AgentID)
	if err != nil || len(snapshot.STUNServers) != 0 {
		t.Fatal("explicitly disabled discovery was restored", err)
	}
	h.enroll(t, "another agent")
	state = h.state(t)
	if len(state.Agents[0].STUNServers) != 0 || !slices.Equal(state.Agents[1].STUNServers, model.DefaultSTUNServers()) {
		t.Fatal("enrollment overwrote another Agent's settings")
	}
}
