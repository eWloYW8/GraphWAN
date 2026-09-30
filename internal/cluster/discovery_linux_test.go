//go:build linux && integration

package cluster

import (
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestNativeServerInterfaceFiltering(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_NETNS") != "1" {
		t.Skip("requires an isolated root network namespace")
	}
	self, _ := os.Readlink("/proc/self/ns/net")
	host, _ := os.Readlink("/proc/1/ns/net")
	if self == host {
		t.Fatal("must isolate the host network")
	}
	links := []netlink.Link{
		&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "uplink"}, PeerName: "uplink-peer"},
		&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "container"}, PeerName: "container-peer"},
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "eth-bridge"}},
		&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "eth-dummy"}},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "eth-tun"}, Mode: netlink.TUNTAP_MODE_TUN, Flags: netlink.TUNTAP_DEFAULTS},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "eth-tap"}, Mode: netlink.TUNTAP_MODE_TAP, Flags: netlink.TUNTAP_DEFAULTS},
	}
	for i, link := range links {
		if err := netlink.LinkAdd(link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { netlink.LinkDel(link) })
		if err := netlink.AddrAdd(link, &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(198, 18, 42, byte(i+1)), Mask: net.CIDRMask(24, 32)}}); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"uplink-peer", "container-peer"} {
		peer, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = netlink.LinkSetUp(peer); err != nil {
			t.Fatal(err)
		}
	}
	route := &netlink.Route{LinkIndex: links[0].Attrs().Index, Gw: net.ParseIP("198.18.42.254")}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatal(err)
	}
	id := Identity{Key: make([]byte, 64)}
	got, err := InterfaceEndpoints(id, &net.TCPAddr{IP: net.IPv4zero, Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, ep := range got {
		if _, err := http.NewRequest("GET", ep.Origin(), nil); err != nil {
			t.Fatalf("unusable server origin %q: %v", ep.Origin(), err)
		}
		if ep.Transport != "tcp" {
			t.Fatal("non-TCP server entrance")
		}
		if err = ep.Validate(); err != nil {
			t.Fatal(err)
		}
		if ep.URL == "tcp://198.18.42.1:8443" {
			seen = true
			continue
		}
		if !strings.Contains(ep.URL, "%25uplink]") {
			t.Fatalf("virtual/container entrance leaked: %s", ep.URL)
		}
	}
	if !seen {
		t.Fatal("containerized Server lost its default-route uplink")
	}
	for _, address := range []string{"198.18.42.2", "198.18.42.3", "198.18.42.4", "198.18.42.5", "198.18.42.6"} {
		got, err = InterfaceEndpoints(id, &net.TCPAddr{IP: net.ParseIP(address), Port: 8443})
		if err != nil || len(got) != 0 {
			t.Fatalf("explicit bind bypassed filtering: %s %v %v", address, got, err)
		}
	}
	if err = netlink.RouteDel(route); err != nil {
		t.Fatal(err)
	}
	got, err = InterfaceEndpoints(id, &net.TCPAddr{IP: net.IPv4zero, Port: 8443})
	if err != nil || len(got) != 0 {
		t.Fatalf("container attachment advertised after route removal: %v %v", got, err)
	}
}
