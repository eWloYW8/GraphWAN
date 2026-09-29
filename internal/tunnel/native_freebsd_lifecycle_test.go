//go:build freebsd && integration

package tunnel

import (
	"bufio"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFreeBSDFallbackCleanup(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires disposable root VM")
	}
	device, err := Open(Config{Address: netip.MustParsePrefix("fd42:6780::1/64"), MTU: 1280})
	if err != nil {
		t.Fatal(err)
	}
	d := device.(*framedDevice)
	defer d.Close()
	iface, err := net.InterfaceByName(d.Name())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := d.file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ioctlError error
	if err := raw.Control(func(fd uintptr) { ioctlError = unix.IoctlSetPointerInt(int(fd), tunSetTransient, 0) }); err != nil {
		t.Fatal(err)
	}
	if ioctlError != nil && ioctlError != unix.ENOTTY {
		t.Fatal(ioctlError)
	}
	d.cleanup = func() error { return destroyFreeBSDInterface(d.Name(), iface.Index) }
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.InterfaceByName(d.Name()); err == nil {
		t.Fatal("explicit close leaked interface")
	}
}

func TestFreeBSDCrashCleanup(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires disposable root VM")
	}
	version, err := unix.SysctlUint32("kern.osreldate")
	if err != nil {
		t.Fatal(err)
	}
	if version < 1501000 {
		t.Skip("kernel-owned TUN destruction requires FreeBSD 15.1")
	}
	if os.Getenv("GRAPHWAN_TUN_CRASH_CHILD") == "1" {
		device, err := Open(Config{Address: netip.MustParsePrefix("10.240.34.1/24"), MTU: 1280})
		if err != nil {
			t.Fatal(err)
		}
		defer device.Close()
		fmt.Println(device.Name())
		for {
			time.Sleep(time.Hour)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFreeBSDCrashCleanup$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "GRAPHWAN_TUN_CRASH_CHILD=1")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := out.(*os.File).SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() {
		t.Fatalf("child did not create TUN: %v", scanner.Err())
	}
	name := scanner.Text()
	if _, err := net.InterfaceByName(name); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := net.InterfaceByName(name); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SIGKILL left owned interface")
		}
		time.Sleep(time.Millisecond)
	}
}
