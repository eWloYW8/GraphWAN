//go:build linux && integration

package discovery

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestNativeInterfaceDiscovery(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_NETNS") != "1" {
		t.Skip("requires root in an isolated network namespace with GRAPHWAN_TEST_NETNS=1")
	}
	self, _ := os.Readlink("/proc/self/ns/net")
	init, _ := os.Readlink("/proc/1/ns/net")
	if self == init {
		t.Fatal("must isolate the host network")
	}
	links := []netlink.Link{
		&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "tun-real-nic"}, PeerName: "peer-nic"},
		&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "eth-dummy"}},
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "eth-bridge"}},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "eth-tun"}, Mode: netlink.TUNTAP_MODE_TUN, Flags: netlink.TUNTAP_DEFAULTS},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "eth-tap"}, Mode: netlink.TUNTAP_MODE_TAP, Flags: netlink.TUNTAP_DEFAULTS},
	}
	for i, link := range links {
		if err := netlink.LinkAdd(link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { netlink.LinkDel(link) })
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+link.Attrs().Name+"/addr_gen_mode", []byte("1"), 0600); err != nil {
			t.Fatal(err)
		}
		v6local, _ := netlink.ParseAddr(fmt.Sprintf("fe80::33:%d/64", i+1))
		v6local.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(link, v6local); err != nil {
			t.Fatal(err)
		}
		address := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(198, 18, 33, byte(i+1)), Mask: net.CIDRMask(24, 32)}}
		if err := netlink.AddrAdd(link, address); err != nil {
			t.Fatal(err)
		}
		linkLocal := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(169, 254, 33, byte(i+1)), Mask: net.CIDRMask(16, 32)}}
		if err := netlink.AddrAdd(link, linkLocal); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	peer, err := netlink.LinkByName("peer-nic")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/peer-nic/addr_gen_mode", []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(peer); err != nil {
		t.Fatal(err)
	}
	v6, _ := netlink.ParseAddr("fd42:6766::1/64")
	v6.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(links[0], v6); err != nil {
		t.Fatal(err)
	}
	check := func(want []string) {
		t.Helper()
		got, err := Interfaces([]byte("native"), 24752)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, endpoint := range got {
			for _, address := range want {
				if strings.Contains(endpoint.URL, address) {
					seen[address]++
				}
			}
		}
		if len(got) != 2*len(want) {
			t.Fatalf("unexpected discovered endpoints: %+v", got)
		}
		for _, address := range want {
			if seen[address] != 2 {
				t.Fatalf("missing TCP/UDP address %s: %+v", address, got)
			}
		}
	}
	check([]string{"198.18.33.1", "fd42:6766::1", "169.254.33.1", "fe80::33:1%25tun-real-nic"})
	// With filtering enabled, an unbridged container veth is excluded unless it
	// carries the namespace's default route. Names do not determine eligibility.
	filtered, err := InterfacesWithOptions([]byte("native"), 24752, InterfaceOptions{ExcludeContainerIPs: true})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("container endpoints leaked: %v %v", filtered, err)
	}
	route := &netlink.Route{LinkIndex: links[0].Attrs().Index, Gw: net.ParseIP("198.18.33.254")}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatal(err)
	}
	filtered, err = InterfacesWithOptions([]byte("native"), 24752, InterfaceOptions{ExcludeContainerIPs: true})
	if err != nil || len(filtered) != 8 {
		t.Fatalf("containerized Agent lost its uplink: %v %v", filtered, err)
	}
	if err := netlink.RouteDel(route); err != nil {
		t.Fatal(err)
	}

	if err := netlink.AddrDel(links[0], v6); err != nil {
		t.Fatal(err)
	}
	check([]string{"198.18.33.1", "169.254.33.1", "fe80::33:1%25tun-real-nic"})
	if err := netlink.LinkSetDown(links[0]); err != nil {
		t.Fatal(err)
	}
	check(nil)
	if err := netlink.LinkSetUp(links[0]); err != nil {
		t.Fatal(err)
	}
	v6local, _ := netlink.ParseAddr("fe80::33:1/64")
	v6local.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(links[0], v6local); err != nil {
		t.Fatal(err)
	}

	check([]string{"198.18.33.1", "169.254.33.1", "fe80::33:1%25tun-real-nic"})
	linkLocal, _ := netlink.ParseAddr("169.254.33.1/16")
	if err := netlink.AddrDel(links[0], linkLocal); err != nil {
		t.Fatal(err)
	}
	check([]string{"198.18.33.1", "fe80::33:1%25tun-real-nic"})
	if err := netlink.AddrDel(links[0], v6local); err != nil {
		t.Fatal(err)
	}
	check([]string{"198.18.33.1"})
}
