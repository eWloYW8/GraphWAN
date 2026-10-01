//go:build linux || darwin || windows

package main

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// SCM stop/shutdown must cancel the worker and wait for resource cleanup.
func TestManagedProgramStop(t *testing.T) {
	cleaned := make(chan struct{})
	p := &managedProgram{run: func(ctx context.Context) error {
		<-ctx.Done()
		close(cleaned)
		return ctx.Err()
	}}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- p.Shutdown(nil) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not reach worker")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("stop returned before cleanup")
	}
}

func TestServiceConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data with spaces")
	exe := filepath.Join(t.TempDir(), "graphwan")
	c, err := serviceConfig("agent", "graphwan-test", exe, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agent", "service", "run", "--service-name", "graphwan-test", "--data-dir", dir}
	if !reflect.DeepEqual(c.Arguments, want) || c.WorkingDirectory != dir || c.Executable != exe {
		t.Fatalf("service lost its exact identity/paths: %+v", c)
	}
	for _, name := range []string{"../other", "bad\nName", "-other", "bad name"} {
		if _, err := serviceConfig("agent", name, exe, dir, "", ""); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	badPaths := []string{"/tmp/%h", "/tmp/$HOME", "/tmp/line\nbreak", "/tmp/\"quoted"}
	if runtime.GOOS != "windows" {
		badPaths = append(badPaths, "/tmp/back\\slash", "/tmp/'quote")
	}
	for _, path := range badPaths {
		if _, err := serviceConfig("agent", "graphwan-test", exe, path, "", ""); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	c, err = serviceConfig("server", "graphwan-test", exe, dir, "127.0.0.1:9443", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Arguments[len(c.Arguments)-4:], []string{"--listen", "127.0.0.1:9443", "--tls-hosts", "localhost"}) {
		t.Fatal(c.Arguments)
	}
}
