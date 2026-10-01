package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func applyPlatform(ctx context.Context, id model.ID, entries []Entry) error {
	anchor := "com.apple/graphwan_" + string(id)
	if !automatic(entries) {
		// Flush only our child anchor, never the host's root rules or shared states.
		if _, err := command(ctx, "pfctl", "", "-a", anchor, "-sr"); err != nil {
			return nil
		}
		_, err := command(ctx, "pfctl", "", "-a", anchor, "-F", "rules")
		return err
	}
	for _, e := range entries {
		if e.Mode == model.GatewaySNAT {
			return errors.New("SNAT gateway mode is currently supported only on Linux; use route or off on macOS")
		}
	}
	info, err := command(ctx, "pfctl", "", "-s", "info")
	if err != nil {
		return err
	}
	filtering := strings.Contains(info, "Status: Enabled")
	if filtering {
		root, err := command(ctx, "pfctl", "", "-sr")
		if err != nil {
			return err
		}
		if !strings.Contains(root, `anchor "com.apple/*"`) {
			return errors.New("automatic gateway requires the standard com.apple/* anchor when PF is enabled; restore the system anchor or use off mode")
		}
	}
	var rules strings.Builder
	families := map[bool]bool{}
	for _, e := range entries {
		if e.Mode == model.GatewayOff {
			continue
		}
		family := "inet6"
		if e.Overlay.Addr().Is4() {
			family = "inet"
		}
		families[e.Overlay.Addr().Is4()] = true
		fmt.Fprintf(&rules, "pass in quick on %q %s from %s to %s keep state\n", e.Interface, family, e.Overlay, e.Subnet)
		fmt.Fprintf(&rules, "pass out quick on %q %s from %s to %s keep state\n", e.Interface, family, e.Subnet, e.Overlay)
	}
	if _, err := command(ctx, "pfctl", rules.String(), "-n", "-a", anchor, "-f", "-"); err != nil {
		return err
	}
	for ipv4 := range families {
		key := "net.inet6.ip6.forwarding=1"
		if ipv4 {
			key = "net.inet.ip.forwarding=1"
		}
		if _, err := command(ctx, "sysctl", "", "-w", key); err != nil {
			return err
		}
	}
	// Keep the owned anchor current even while PF is disabled, without enabling
	// the host firewall or replacing its root configuration.
	_, err = command(ctx, "pfctl", rules.String(), "-a", anchor, "-f", "-")
	return err
}
