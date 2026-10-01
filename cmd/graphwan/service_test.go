//go:build linux || darwin || windows

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/kardianos/service"
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

// Execute the generated start function with fake supervisor commands: paths and
// arguments must survive shell parsing without expansion or command injection.
func TestLinuxServiceArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell")
	}
	values := []string{"/tmp/bin with spaces", "/tmp/data; `echo INJECTED`", "{{.Name}}", "quote'and$expansion", ""}
	for _, source := range []string{graphwanOpenRCScript, graphwanProcdScript} {
		for _, helper := range []bool{false, true} {
			config := &service.Config{Name: "graphwan-test", Executable: values[0], WorkingDirectory: values[1], Arguments: values[2:], Option: service.KeyValue{}}
			configureLinuxScripts(config, helper)
			var script bytes.Buffer
			if err := template.Must(template.New("init").Parse(source)).Execute(&script, config); err != nil {
				t.Fatal(err)
			}

			mockDir := t.TempDir()
			for _, name := range []string{"supervise-daemon", "start-stop-daemon"} {
				if err := os.WriteFile(filepath.Join(mockDir, name), []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if source == graphwanProcdScript {
				script.WriteString("\nprocd_open_instance() { :; }\nprocd_close_instance() { :; }\nprocd_set_param() { [ \"$1\" != command ] || { shift; printf '%s\\000' \"$@\"; }; }\nstart_service\n")
			} else {
				script.WriteString("\nebegin() { :; }\neend() { return \"$1\"; }\nRC_SVCNAME=graphwan-test\nstart\n")
			}
			cmd := exec.Command("sh", "-c", script.String())
			cmd.Env = append(os.Environ(), "PATH="+mockDir+":"+os.Getenv("PATH"))

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			args := strings.Split(string(out), "\x00")
			for _, want := range values {
				found := false
				for _, arg := range args[:len(args)-1] {
					if arg == want {
						found = true
					}
				}
				if !found {
					t.Fatalf("helper=%v lost argument %q: %q", helper, want, args)
				}
			}
		}
	}
}
