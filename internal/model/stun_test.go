package model_test

import (
	"testing"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestSTUNConfigurationValidationAndIsolation(t *testing.T) {
	for _, servers := range [][]string{nil, {"stun.example.test:3478", "[2001:db8::1]:3478"}, {"127.0.0.1:1"}} {
		if err := model.ValidateSTUNServers(servers); err != nil {
			t.Fatal(servers, err)
		}
	}
	for _, servers := range [][]string{{"stun.example.test"}, {"stun://host:3478"}, {"user@host:3478"}, {"host:0"}, {"host:65536"}, {"[::]:3478"}, {"224.0.0.1:3478"}, {"[fe80::1%eth0]:3478"}, {"Host:3478", "host:3478"}, {"a:1", "b:1", "c:1", "d:1", "e:1"}} {
		if err := model.ValidateSTUNServers(servers); err == nil {
			t.Fatal("invalid servers accepted:", servers)
		}
	}
	s := testutil.Topology()
	s.Agents[0].STUNServers = []string{"stun.example.test:3478"}
	copy := s.Clone()
	copy.Agents[0].STUNServers[0] = "changed:3478"
	if s.Agents[0].STUNServers[0] != "stun.example.test:3478" {
		t.Fatal("state aliases STUN servers")
	}
	snapshot, err := routing.Compile(s, s.Agents[0].ID)
	if err != nil || len(snapshot.STUNServers) != 1 {
		t.Fatal("missing compiled setting:", err)
	}
	cloned := snapshot.Clone()
	cloned.STUNServers[0] = "bad:0"
	if snapshot.STUNServers[0] != "stun.example.test:3478" || cloned.Validate(cloned.AgentID) == nil {
		t.Fatal("invalid or aliased cached setting")
	}
	other, err := routing.Compile(s, s.Agents[1].ID)
	if err != nil || len(other.STUNServers) != 0 {
		t.Fatal("another agent received STUN settings")
	}
}
