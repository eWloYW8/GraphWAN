package gateway

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

// nft base-chain ACCEPT cannot override a later iptables FORWARD policy DROP.
// Install a scoped jump in that chain when the compatibility tools are present.
// Unrelated nft/firewalld base chains and explicit security policies remain intact.
func compatibilityForwarding(ctx context.Context, id model.ID, entries []Entry) error {
	digest := sha256.Sum256([]byte(id))
	chain := fmt.Sprintf("GW%x", digest[:12])
	for _, ipv4 := range []bool{true, false} {
		name := "ip6tables"
		if ipv4 {
			name = "iptables"
		}
		if _, err := tool(name); err != nil {
			continue
		}
		var selected []Entry
		for _, e := range entries {
			if e.Overlay.Addr().Is4() == ipv4 {
				selected = append(selected, e)
			}
		}
		if !automatic(selected) {
			// A missing chain is normal on a host that has never enabled this mode.
			if _, err := command(ctx, name, "", "-w", "5", "-S", chain); err != nil {
				continue
			}
			for i := 0; i < 8; i++ {
				if _, err := command(ctx, name, "", "-w", "5", "-C", "FORWARD", "-j", chain); err != nil {
					break
				}
				if _, err := command(ctx, name, "", "-w", "5", "-D", "FORWARD", "-j", chain); err != nil {
					return err
				}
			}
			if _, err := command(ctx, name, "", "-w", "5", "-F", chain); err != nil {
				return err
			}
			if _, err := command(ctx, name, "", "-w", "5", "-X", chain); err != nil {
				return err
			}
			continue
		}
		if _, err := command(ctx, name, "", "-w", "5", "-S", chain); err != nil {
			if _, err := command(ctx, name, "", "-w", "5", "-N", chain); err != nil {
				return err
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "*filter\n:%s - [0:0]\n-F %s\n", chain, chain)
		for _, e := range selected {
			action := "ACCEPT"
			if e.Mode == model.GatewayOff {
				action = "RETURN"
			}
			fmt.Fprintf(&b, "-A %s -i %s -d %s -j %s\n", chain, e.Interface, e.Subnet, action)
			fmt.Fprintf(&b, "-A %s -o %s -s %s -j %s\n", chain, e.Interface, e.Subnet, action)
		}
		b.WriteString("COMMIT\n")
		if _, err := command(ctx, name+"-restore", b.String(), "--wait", "5", "--noflush"); err != nil {
			return err
		}
		if _, err := command(ctx, name, "", "-w", "5", "-C", "FORWARD", "-j", chain); err != nil {
			if _, err := command(ctx, name, "", "-w", "5", "-I", "FORWARD", "1", "-j", chain); err != nil {
				return err
			}
		}
	}
	return nil
}
