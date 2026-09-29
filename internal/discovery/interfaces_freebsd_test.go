//go:build freebsd && integration

package discovery

import (
	"context"
	"fmt"
	"net"
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

func TestNativeFreeBSDInterfaceDiscovery(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires root in a disposable FreeBSD VM with GRAPHWAN_TEST_VM=1")
	}
	if got, want := unsafe.Sizeof(bsdCloneRequest{}), uintptr((unix.SIOCIFGCLONERS>>16)&0x1fff); got != want {
		t.Fatalf("if_clonereq ABI size %d, ioctl encodes %d", got, want)
	}
	command := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "/sbin/ifconfig", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ifconfig %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	cloners, err := bsdCloners()
	if err != nil || !cloners["tap"] || !cloners["tun"] {
		t.Fatalf("kernel cloner list: %v, %v", cloners, err)
	}
	identity := []byte("native-freebsd")
	snapshot := func() map[string]model.ID {
		t.Helper()
		endpoints, err := Interfaces(identity, 24752)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]model.ID{}
		for _, endpoint := range endpoints {
			if endpoint.Validate() != nil || endpoint.Source != model.Interface || endpoint.Transport != model.TCP && endpoint.Transport != model.UDP {
				t.Fatalf("invalid automatic endpoint: %+v", endpoint)
			}
			if _, duplicate := result[endpoint.URL]; duplicate {
				t.Fatalf("duplicate endpoint: %s", endpoint.URL)
			}
			result[endpoint.URL] = endpoint.ID
		}
		return result
	}
	baseline := snapshot()
	if len(baseline) == 0 {
		t.Fatal("VM needs an up physical or guest NIC with a unicast address")
	}
	created := map[string]string{}
	var epairPeer string
	for i, driver := range []string{"epair", "tap", "tun", "bridge"} {
		name := command(driver, "create")
		iface, err := net.InterfaceByName(name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			current, err := net.InterfaceByName(name)
			if err == nil && current.Index == iface.Index {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if out, err := exec.CommandContext(ctx, "/sbin/ifconfig", name, "destroy").CombinedOutput(); err != nil {
					t.Errorf("destroy %s: %v: %s", name, err, out)
				}
			}
		})
		if driver == "epair" {
			epairPeer = strings.TrimSuffix(name, "a") + "b"
			command(epairPeer, "up")
		}
		alias := fmt.Sprintf("eth-gw%x%c", os.Getpid(), 'a'+i)
		if driver == "epair" {
			alias = fmt.Sprintf("tun-gw%x", os.Getpid())
		}
		command(name, "name", alias)
		name = alias // Cleanup follows the renamed interface, guarded by index.
		created[driver] = name
		if driver == "tun" {
			command(name, "inet", fmt.Sprintf("198.18.%d.1/24", 33+i), fmt.Sprintf("198.18.%d.2", 33+i), "alias", "up")
		} else {
			command(name, "inet", fmt.Sprintf("198.18.%d.1/24", 33+i), "alias", "up")
		}
		// An editable group must not be the basis for classifying an overlay.
		command(name, "-group", driver)
	}
	ep := created["epair"]
	command(ep, "inet6", "-auto_linklocal", "no_dad", "-ifdisabled")
	command(ep, "inet6", "fd42:6766::1/64", "alias")
	check := func(addresses ...string) {
		t.Helper()
		got := snapshot()
		for endpointURL, id := range baseline {
			if got[endpointURL] != id {
				t.Fatalf("changed unrelated NIC endpoint: %s", endpointURL)
			}
			delete(got, endpointURL)
		}
		addresses = append(addresses, fixtureLinkLocalAddresses(t, ep, epairPeer)...)
		want := map[string]bool{}
		for _, address := range addresses {
			for _, kind := range []string{"tcp", "udp"} {
				u := url.URL{Scheme: kind, Host: net.JoinHostPort(address, "24752")}
				want[u.String()] = true
			}
		}
		if len(got) != len(want) {
			t.Fatalf("unexpected discovered endpoints: %v, want %v", got, want)
		}
		for endpointURL := range got {
			if !want[endpointURL] {
				t.Fatalf("advertised excluded interface: %s", endpointURL)
			}
		}
	}
	check("198.18.33.1", "fd42:6766::1")
	before := snapshot()
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("unchanged interface enumeration changed endpoint IDs")
	}
	command(ep, "inet6", "fd42:6766::1", "-alias")
	check("198.18.33.1")
	command(ep, "down")
	check()
	command(ep, "up")
	check("198.18.33.1")
	command(ep, "inet", "198.18.33.1", "-alias")
	check()
	command(ep, "inet", "198.18.33.2/24", "alias")
	check("198.18.33.2")
}
