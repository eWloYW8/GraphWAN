package gateway

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func applyPlatform(ctx context.Context, id model.ID, entries []Entry) error {
	name := "graphwan_" + string(id)
	if !automatic(entries) {
		if err := compatibilityForwarding(ctx, id, nil); err != nil {
			return err
		}
		// No dependency on nft for Agents that have never configured a gateway.
		if _, err := command(ctx, "nft", "", "list", "table", "inet", name); err != nil {
			return nil
		}
		_, err := command(ctx, "nft", "", "delete", "table", "inet", name)
		return err
	}
	// Validate the complete transaction before changing shared forwarding flags.
	script := linuxRules(name, entries)
	if _, err := command(ctx, "nft", script, "-c", "-f", "-"); err != nil {
		return err
	}
	families := map[bool]bool{}
	for _, e := range entries {
		if e.Mode != model.GatewayOff {
			families[e.Overlay.Addr().Is4()] = true
		}
	}
	for ipv4 := range families {
		path := "/proc/sys/net/ipv6/conf/all/forwarding"
		if ipv4 {
			path = "/proc/sys/net/ipv4/ip_forward"
		}
		if err := os.WriteFile(path, []byte("1\n"), 0644); err != nil {
			return fmt.Errorf("enable forwarding: %w", err)
		}
	}
	if _, err := command(ctx, "nft", script, "-f", "-"); err != nil {
		return err
	}
	return compatibilityForwarding(ctx, id, entries)
}

// Replacing a dedicated table is one atomic nft transaction. iifname in the NAT
// hook scopes masquerading to packets that actually entered through this TUN.
func linuxRules(name string, entries []Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\ndelete table inet %s\nadd table inet %s\n", name, name, name)
	fmt.Fprintf(&b, "add chain inet %s forward { type filter hook forward priority -10; policy accept; }\n", name)
	fmt.Fprintf(&b, "add chain inet %s nat { type nat hook postrouting priority 99; policy accept; }\n", name)
	for _, e := range entries {
		family := "ip6"
		if e.Overlay.Addr().Is4() {
			family = "ip"
		}
		action := "accept"
		if e.Mode == model.GatewayOff {
			action = "return"
		}
		fmt.Fprintf(&b, "add rule inet %s forward iifname %q %s daddr %s %s\n", name, e.Interface, family, e.Subnet, action)
		fmt.Fprintf(&b, "add rule inet %s forward oifname %q %s saddr %s %s\n", name, e.Interface, family, e.Subnet, action)
		action = "return"
		if e.Mode == model.GatewaySNAT {
			action = "masquerade"
		}
		fmt.Fprintf(&b, "add rule inet %s nat iifname %q oifname != %q %s daddr %s %s daddr != %s %s\n", name, e.Interface, e.Interface, family, e.Subnet, family, e.Overlay, action)
	}
	return b.String()
}
