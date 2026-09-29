//go:build (openbsd || netbsd) && integration

package tunnel_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/agent"
	"github.com/graphwan/graphwan/internal/tunnel"
)

const recoveryRegistry = "/var/run/graphwan-tun"

func TestNativePersistentBSDCrashRecovery(t *testing.T) {
	requireTestPersistentBSD(t)
	if address := os.Getenv("GRAPHWAN_CRASH_CHILD_ADDRESS"); address != "" {
		device, err := tunnel.Open(tunnel.Config{Address: netip.MustParsePrefix(address), MTU: persistentTestMTU()})
		if err != nil {
			t.Fatal(err)
		}
		defer device.Close()
		fmt.Println("GRAPHWAN_READY " + device.Name())
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	for _, address := range []string{"10.240.45.1/24", "fd42:6795::1/64"} {
		t.Run(address, func(t *testing.T) {
			child, original, record := crashPersistentBSDChild(t, address)
			// Another process must not recover a device whose record is still locked.
			other, err := tunnel.Open(tunnel.Config{Address: netip.MustParsePrefix("10.240.46.1/24"), MTU: 1280})
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			current, err := net.InterfaceByName(original.Name)
			if err != nil || current.Index != original.Index {
				t.Fatal("live owner was removed:", err)
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := child.Wait(); err == nil {
				t.Fatal("child was not killed")
			}
			if _, err := net.InterfaceByName(original.Name); err != nil {
				t.Fatal("test did not leave a cloned TUN behind:", err)
			}
			config := tunnel.Config{Address: netip.MustParsePrefix(address), MTU: persistentTestMTU()}
			device, err := tunnel.Open(config)
			if err != nil {
				t.Fatal("restart could not reuse the address/subnet:", err)
			}
			defer device.Close()
			if _, err := net.InterfaceByName(original.Name); err == nil {
				t.Fatal("orphan interface remains")
			}
			if hasNativeRoute(t, original, config) {
				t.Fatal("orphan route remains")
			}
			if _, err := os.Stat(filepath.Join(recoveryRegistry, record)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("orphan record remains:", err)
			}
			checkConfiguredNativeTunnel(t, device, config, config.Address.Addr().Next())
		})
	}
}

func crashPersistentBSDChild(t *testing.T, address string) (*exec.Cmd, *net.Interface, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativePersistentBSDCrashRecovery$", "-test.v")
	child.Env = append(os.Environ(), "GRAPHWAN_CRASH_CHILD_ADDRESS="+address)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close() })
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			_ = child.Wait()
		}
	})
	var name string
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		if after, ok := strings.CutPrefix(scanner.Text(), "GRAPHWAN_READY "); ok {
			name = after
			break
		}
	}
	if name == "" {
		t.Fatalf("child never configured a TUN: %v", scanner.Err())
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	// This exact interface is owned by this test's child. Cleanup never targets
	// any pre-existing interface if the assertion fails before recovery runs.
	t.Cleanup(func() {
		current, err := net.InterfaceByName(name)
		if err == nil && current.Index == iface.Index {
			_ = exec.Command("/sbin/ifconfig", name, "destroy").Run()
		}
	})
	raw, err := exec.Command("/sbin/ifconfig", name).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range strings.Fields(string(raw)) {
		parts := strings.Split(strings.Trim(field, "\""), ":")
		if len(parts) == 3 && parts[0] == "graphwan" && parts[2] == strconv.Itoa(iface.Index) {
			if _, err := os.Stat(filepath.Join(recoveryRegistry, parts[1])); err != nil {
				t.Fatal(err)
			}
			return child, iface, parts[1]
		}
	}

	t.Fatal("child did not retain an ownership record")
	return nil, nil, ""
}

func TestNativePersistentBSDRecoveryOwnership(t *testing.T) {
	requireTestPersistentBSD(t)
	for _, changed := range []string{"replacement", "description"} {
		t.Run(changed, func(t *testing.T) {
			child, original, token := crashPersistentBSDChild(t, "10.240.47.1/24")
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			command := func(args ...string) {
				t.Helper()
				if raw, err := exec.Command("/sbin/ifconfig", append([]string{original.Name}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("ifconfig %v: %v: %s", args, err, raw)
				}
			}
			if changed == "replacement" {
				command("destroy")
				command("create")
				command("mtu", "1400")
				// OpenBSD allocates a fresh index. NetBSD immediately reuses freed
				// indices: an administrator copying both the name and the complete
				// ownership marker transfers ownership there. Its ordinary unmarked
				// replacement must still be preserved.
				if runtime.GOOS == "openbsd" {
					command("description", "graphwan:"+token+":"+strconv.Itoa(original.Index))
				}
				t.Cleanup(func() { command("destroy") })
			} else {
				command("description", "retained-by-administrator")
			}
			before, err := net.InterfaceByName(original.Name)
			if err != nil {
				t.Fatal(err)
			}
			device, err := tunnel.Open(tunnel.Config{Address: netip.MustParsePrefix("10.240.48.1/24"), MTU: 1280})
			if err != nil {
				t.Fatal(err)
			}
			defer device.Close()
			after, err := net.InterfaceByName(original.Name)
			if err != nil || before.Index != after.Index || before.MTU != after.MTU {
				t.Fatal("recovery changed a revoked/replaced interface:", err)
			}
			if _, err := os.Stat(filepath.Join(recoveryRegistry, token)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("revoked record was not retired:", err)
			}
		})
	}
}

func TestNativePersistentBSDRecoveryWithoutOpen(t *testing.T) {
	requireTestPersistentBSD(t)
	child, original, token := crashPersistentBSDChild(t, "10.240.49.1/24")
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	_, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewDataPlane(t.Context(), identity, agent.DataPlaneOptions{BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := net.InterfaceByName(original.Name); err == nil {
		t.Fatal("startup recovery left a removed Network's TUN")
	}
	if _, err := os.Stat(filepath.Join(recoveryRegistry, token)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("record remains:", err)
	}
	if err := tunnel.Recover(); err != nil {
		t.Fatal("repeated recovery:", err)
	}
}

func TestNativePersistentBSDRecoveryIncompleteRecord(t *testing.T) {
	requireTestPersistentBSD(t)
	// Model SIGKILL after the immutable record is created but before any kernel
	// mutation. Empty records are complete records, not parse failures that can
	// block every subsequent Agent startup.
	var token [16]byte
	rand.Read(token[:])
	path := filepath.Join(recoveryRegistry, hex.EncodeToString(token[:]))
	if err := os.MkdirAll(recoveryRegistry, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unpublished lease remains:", err)
	}
}

func TestNativePersistentBSDRecoveryRecordProtection(t *testing.T) {
	requireTestPersistentBSD(t)
	if err := os.MkdirAll(recoveryRegistry, 0700); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"symlink", "hardlink", "public", "nonroot"} {
		t.Run(kind, func(t *testing.T) {
			var token [16]byte
			rand.Read(token[:])
			record := filepath.Join(recoveryRegistry, hex.EncodeToString(token[:]))
			directory, err := os.MkdirTemp(recoveryRegistry, "test-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(directory) })
			target := filepath.Join(directory, "unrelated")
			if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(target, record); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, record); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(record, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { os.Remove(record) })
			switch kind {
			case "public":
				if err := os.Chmod(record, 0644); err != nil {
					t.Fatal(err)
				}
			case "nonroot":
				if err := os.Chown(record, 65534, -1); err != nil {
					t.Fatal(err)
				}
			}
			if err := tunnel.Recover(); err == nil {
				t.Fatal("accepted an unsafe ownership record")
			}
			if _, err := os.Lstat(record); err != nil {
				t.Fatal("removed a rejected record:", err)
			}
			if raw, err := os.ReadFile(target); err != nil || string(raw) != "preserve" {
				t.Fatal("changed an unrelated target:", err)
			}
		})
	}
}

func TestNativePersistentBSDRecoveryConcurrentClose(t *testing.T) {
	requireTestPersistentBSD(t)
	for range 30 {
		device, err := tunnel.Open(tunnel.Config{Address: netip.MustParsePrefix("10.240.50.1/24"), MTU: 1280})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- tunnel.Recover() }()
		closeError := device.Close()
		recoveryError := <-done
		if closeError != nil || recoveryError != nil {
			t.Fatalf("close/recovery race: close=%v, recovery=%v", closeError, recoveryError)
		}
	}
}

func requireTestPersistentBSD(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires root in a disposable BSD VM with GRAPHWAN_TEST_VM=1")
	}
}
func persistentTestMTU() int {
	if runtime.GOOS == "netbsd" {
		return 1500
	}
	return 9000
}
