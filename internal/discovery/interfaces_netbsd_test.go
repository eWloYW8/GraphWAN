//go:build netbsd && integration

package discovery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/graphwan/graphwan/internal/model"
	"golang.org/x/sys/unix"
)

func TestNativeNetBSDInterfaceDiscovery(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires root in a disposable NetBSD VM with GRAPHWAN_TEST_VM=1")
	}
	if got, want := unsafe.Sizeof(bsdCloneRequest{}), uintptr((unix.SIOCIFGCLONERS>>16)&0x1fff); got != want {
		t.Fatalf("cloner ABI: %d bytes, ioctl expects %d", got, want)
	}
	fixture := os.Getenv("GRAPHWAN_TEST_INTERFACE")
	if fixture == "" {
		t.Fatal("GRAPHWAN_TEST_INTERFACE must name a spare, unconfigured guest NIC")
	}
	physical, err := net.InterfaceByName(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if physical.Flags&net.FlagUp != 0 {
		t.Fatal("fixture NIC must initially be down")
	}
	addresses, err := physical.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	originalAddresses := map[string]bool{}
	for _, address := range addresses {
		originalAddresses[address.String()] = true
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().IsGlobalUnicast() {
			t.Fatal("fixture NIC already has a unicast address")
		}
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/sbin/ifconfig", args...)
		cmd.WaitDelay = time.Second
		raw, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(raw)), err
	}
	command := func(args ...string) string {
		t.Helper()
		raw, err := run(args...)
		if err != nil {
			t.Fatalf("ifconfig %v: %v: %s", args, err, raw)
		}
		return raw
	}
	if strings.Contains(command(fixture), "description:") {
		t.Fatal("fixture NIC must have no existing description")
	}
	cloners, err := bsdCloners()
	if err != nil || !cloners["tap"] || !cloners["tun"] {
		t.Fatalf("kernel cloner list: %v, %v", cloners, err)
	}
	snapshot := func() map[string]model.ID {
		t.Helper()
		endpoints, err := Interfaces([]byte("native-netbsd"), 24752)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]model.ID{}
		for _, endpoint := range endpoints {
			if endpoint.Validate() != nil || endpoint.Source != model.Interface || endpoint.Transport != model.TCP && endpoint.Transport != model.UDP {
				t.Fatalf("invalid automatic endpoint: %+v", endpoint)
			}
			if _, exists := result[endpoint.URL]; exists {
				t.Fatal("duplicate endpoint:", endpoint.URL)
			}
			result[endpoint.URL] = endpoint.ID
		}
		return result
	}
	baseline := snapshot()
	if len(baseline) == 0 {
		t.Fatal("VM needs a separate configured NIC for the discovery baseline")
	}
	t.Cleanup(func() {
		current, err := net.InterfaceByName(fixture)
		if err != nil || current.Index != physical.Index {
			t.Error("fixture NIC identity changed")
			return
		}
		for _, address := range []string{"198.18.50.1", "198.18.50.2"} {
			_, _ = run(fixture, "inet", address, "delete")
		}
		_, _ = run(fixture, "inet6", "fd42:6750::1", "delete")
		command(fixture, "-description")
		command(fixture, "down")
		current, err = net.InterfaceByName(fixture)
		if err != nil {
			t.Error(err)
			return
		}
		remaining, err := current.Addrs()
		if err != nil {
			t.Error(err)
			return
		}
		for _, address := range remaining {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil && prefix.Addr().Is6() && prefix.Addr().IsLinkLocalUnicast() && !originalAddresses[address.String()] {
				command(fixture, "inet6", prefix.Addr().String(), "delete")
			}
		}
	})
	command(fixture, "inet", "198.18.50.1/24", "alias", "up")
	command(fixture, "inet6", "fd42:6750::1/64", "alias")
	command(fixture, "description", "tap9999")
	created := []string{}
	for i, driver := range []string{"tap", "tun", "bridge", "vether", "vlan", "agr", "lagg"} {
		if !cloners[driver] {
			t.Fatal("missing native fixture cloner:", driver)
		}
		name := ""
		for attempt := range 16 {
			candidate := fmt.Sprintf("%s%d", driver, 100+os.Getpid()%500+attempt)
			output, err := run(candidate, "create")
			if err == nil {
				name = candidate
				break
			}
			if _, lookup := net.InterfaceByName(candidate); lookup != nil {
				t.Fatalf("create %s: %v: %s", candidate, err, output)
			}
		}
		if name == "" {
			t.Fatal("could not reserve a fixture interface")
		}
		iface, err := net.InterfaceByName(name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			current, err := net.InterfaceByName(name)
			if err == nil && current.Index == iface.Index {
				if driver == "vlan" {
					// NetBSD 11 if_detach waits for link work while holding the
					// interface lock that the worker needs. Disconnect the parent
					// and let its DOWN notification finish before destroying it.
					command(name, "-vlanif", fixture)
					deadline := time.Now().Add(5 * time.Second)
					for {
						_, err := run("-s", name)
						if status, ok := err.(*exec.ExitError); ok && status.ExitCode() == 1 {
							break
						}
						if err != nil {
							t.Fatal("query VLAN link state:", err)
						}
						if time.Now().After(deadline) {
							t.Fatal("VLAN link did not become down")
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
				command(name, "destroy")
			}
		})
		if driver == "vlan" {
			command(name, "vlan", "1234", "vlanif", fixture)
		}
		command(name, "description", fixture+" physical")
		if driver == "bridge" || driver == "agr" || driver == "lagg" {
			command(name, "up")
		} else {
			args := []string{name, "inet", fmt.Sprintf("198.18.%d.1/24", 60+i)}
			if driver == "tun" {
				args = append(args, fmt.Sprintf("198.18.%d.2", 60+i))
			}
			command(append(args, "alias", "up")...)
		}
		created = append(created, name)
	}
	// All fixture clones must be rejected even when their descriptions resemble
	// hardware. Bridges and aggregations are also checked without relying on L3.
	all, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	classes, err := physicalInterfaces(all)
	if err != nil {
		t.Fatal(err)
	}
	if !classes[physical.Index] {
		t.Fatal("physical guest NIC was rejected")
	}
	for _, name := range created {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if classes[iface.Index] {
			t.Fatal("clone classified as physical:", name)
		}
	}
	check := func(addresses ...string) {
		t.Helper()
		got := snapshot()
		for endpoint, id := range baseline {
			if got[endpoint] != id {
				t.Fatal("changed unrelated endpoint:", endpoint)
			}
			delete(got, endpoint)
		}
		addresses = append(addresses, fixtureLinkLocalAddresses(t, fixture)...)
		want := map[string]bool{}
		for _, address := range addresses {
			for _, kind := range []string{"tcp", "udp"} {
				u := url.URL{Scheme: kind, Host: net.JoinHostPort(address, "24752")}
				want[u.String()] = true
			}
		}
		if len(got) != len(want) {
			t.Fatalf("discovered %v, want %v", got, want)
		}
		for endpoint := range got {
			if !want[endpoint] {
				t.Fatal("advertised virtual or stale endpoint:", endpoint)
			}
		}
	}
	check("198.18.50.1", "fd42:6750::1")
	before := snapshot()
	command(fixture, "description", "bridge9999")
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("description changed endpoint IDs")
	}
	command(fixture, "inet6", "fd42:6750::1", "delete")
	check("198.18.50.1")
	command(fixture, "down")
	check()
	command(fixture, "up")
	check("198.18.50.1")
	command(fixture, "inet", "198.18.50.1", "delete")
	check()
	command(fixture, "inet", "198.18.50.2/24", "alias")
	check("198.18.50.2")
	// Stale identities must fail the entire classification, never publish a
	// partial snapshot from a mixture of old interface indexes and new names.
	stale := *physical
	stale.Flags |= net.FlagUp
	stale.Name = "missing"
	if got, err := physicalInterfaces([]net.Interface{stale}); err == nil || got != nil {
		t.Fatal("stale interface identity accepted")
	}
}
