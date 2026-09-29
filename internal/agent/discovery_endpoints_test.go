package agent

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func TestDiscoveryRetainsPublicMappingsUnderEndpointPressure(t *testing.T) {
	now := time.Now()
	var interfaces []model.Endpoint
	for i := range 80 {
		interfaces = append(interfaces, model.Endpoint{ID: model.ID(fmt.Sprintf("%032x", i+1)), Source: model.Interface, Transport: model.UDP, URL: fmt.Sprintf("udp://[fe80::%x%%25veth%d]:24752", i+1, i)})
	}
	public := model.Endpoint{ID: model.ID(fmt.Sprintf("%032x", 1001)), Source: model.Interface, Transport: model.TCP, URL: "tcp://[2001:db8::1]:24752"}
	private := model.Endpoint{ID: model.ID(fmt.Sprintf("%032x", 1002)), Source: model.Interface, Transport: model.UDP, URL: "udp://192.168.1.3:24752"}
	interfaces = append(interfaces, public, private)
	mapping := model.Endpoint{ID: model.ID(fmt.Sprintf("%032x", 2001)), Source: model.Observed, Transport: model.UDP, URL: "udp://192.0.2.5:32752", ExpiresAt: now.Add(time.Minute)}
	got, total := selectDiscoveredEndpoints(nil, interfaces, []model.Endpoint{mapping}, now)
	if total != 83 || len(got) != model.MaxEndpoints {
		t.Fatalf("wrong bound: total=%d selected=%d", total, len(got))
	}
	for _, want := range []model.Endpoint{mapping, public, private} {
		if !slices.ContainsFunc(got, func(e model.Endpoint) bool { return e.ID == want.ID }) {
			t.Fatalf("useful endpoint evicted by link-local inventory: %s", want.URL)
		}
	}
	slices.Reverse(interfaces)
	reordered, _ := selectDiscoveredEndpoints(nil, interfaces, []model.Endpoint{mapping}, now)
	if !slices.Equal(got, reordered) {
		t.Fatal("selection depends on interface enumeration order")
	}
	// Reserve all but two slots with manual entries; the fresh mapping and global
	// interface must survive, ahead of private and link-local interfaces.
	manual := make([]model.Endpoint, model.MaxEndpoints-2)
	for i := range manual {
		manual[i] = model.Endpoint{Source: model.Manual, URL: fmt.Sprintf("tcp://192.0.2.10:%d", 10000+i)}
	}
	got, _ = selectDiscoveredEndpoints(manual, interfaces, []model.Endpoint{mapping}, now)
	if len(got) != 2 || !slices.Contains(got, mapping) || !slices.Contains(got, public) {
		t.Fatal("manual capacity or public priority violated", got)
	}
	manual = append(manual, model.Endpoint{Source: model.Manual, URL: "tcp://192.0.2.11:1"}, model.Endpoint{Source: model.Manual, URL: "tcp://192.0.2.11:2"})
	got, _ = selectDiscoveredEndpoints(manual, interfaces, []model.Endpoint{mapping}, now)
	if len(got) != 0 {
		t.Fatal("manual entries did not reserve the full allowance")
	}
}

func TestDiscoveryDeduplicatesAndExpiresMappings(t *testing.T) {
	now := time.Now()
	iface := model.Endpoint{ID: model.NewID(), Source: model.Interface, Transport: model.UDP, URL: "udp://192.0.2.5:24752"}
	same := model.Endpoint{ID: model.NewID(), Source: model.Observed, Transport: model.UDP, URL: iface.URL, ExpiresAt: now.Add(time.Minute)}
	expired := model.Endpoint{ID: model.NewID(), Source: model.Observed, Transport: model.TCP, URL: "tcp://192.0.2.6:24752", ExpiresAt: now}
	manual := model.Endpoint{ID: model.NewID(), Source: model.Manual, Transport: model.TCP, URL: "tcp://192.0.2.7:24752"}
	duplicate := manual
	duplicate.Source = model.Observed
	duplicate.ExpiresAt = now.Add(time.Minute)
	got, total := selectDiscoveredEndpoints([]model.Endpoint{manual}, []model.Endpoint{iface, iface}, []model.Endpoint{same, expired, duplicate}, now)
	if total != 1 || len(got) != 1 || got[0] != iface {
		t.Fatal("lost interface identity, retained expired mapping or duplicated a manual URL", got, total)
	}
}
