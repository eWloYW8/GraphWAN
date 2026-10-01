// Package gateway manages only the host rules for explicitly enabled gateways.
// It never installs client-side routes to advertised external subnets.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type Entry struct {
	Interface string
	Overlay   netip.Prefix
	Subnet    netip.Prefix
	Mode      model.GatewayMode
}

type Manager struct {
	id          model.ID
	entries     []Entry
	initialized bool
	dirty       bool
}

var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$`)

func (m *Manager) Apply(ctx context.Context, id model.ID, entries []Entry) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if m.id != "" && m.id != id {
		return errors.New("gateway identity changed")
	}
	next := slices.Clone(entries)
	for _, e := range next {
		if !interfaceName.MatchString(e.Interface) || !e.Overlay.IsValid() || !e.Subnet.IsValid() {
			return errors.New("invalid gateway interface or prefix")
		}
	}
	slices.SortFunc(next, func(a, b Entry) int {
		if c := strings.Compare(a.Interface, b.Interface); c != 0 {
			return c
		}
		if a.Subnet.Bits() != b.Subnet.Bits() {
			return b.Subnet.Bits() - a.Subnet.Bits()
		}
		return strings.Compare(a.Subnet.String(), b.Subnet.String())
	})
	if m.initialized && !m.dirty && slices.Equal(m.entries, next) {
		return nil
	}
	m.id = id
	m.initialized = true
	if err := applyPlatform(ctx, id, next); err != nil {
		// Restore owned rules after partial command failures, never disable shared
		// forwarding settings that another application may now rely on.
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rollback := applyPlatform(recovery, id, m.entries)
		m.dirty = rollback != nil
		return errors.Join(err, rollback)
	}
	m.entries, m.initialized = next, true
	m.dirty = false
	return nil
}

func (m *Manager) Close() error {
	if !m.initialized {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return m.Apply(ctx, m.id, nil)
}

func automatic(entries []Entry) bool {
	for _, e := range entries {
		if e.Mode != model.GatewayOff {
			return true
		}
	}
	return false
}

// Resolve administrative tools even when a foreground user's PATH lacks sbin.
func tool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		if p, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("automatic gateway requires %s", name)
}

func command(ctx context.Context, name, input string, args ...string) (string, error) {
	path, err := tool(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = strings.NewReader(input)
	// Commands have bounded inputs; cap diagnostic output independently.
	out := &limitedOutput{}
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	if err != nil {
		return out.text.String(), fmt.Errorf("%s: %w: %s", name, err, out.text.String())
	}
	return out.text.String(), nil
}

type limitedOutput struct{ text strings.Builder }

func (w *limitedOutput) Write(b []byte) (int, error) {
	n := len(b)
	if remaining := 8192 - w.text.Len(); remaining > 0 {
		if len(b) > remaining {
			b = b[:remaining]
		}
		w.text.Write(b)
	}
	return n, nil
}
