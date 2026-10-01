//go:build linux || darwin || windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/kardianos/service"
)

// The same executable hosts the service, including the Windows SCM dispatcher.
// Runtime shutdown uses cancellation so TUN devices and database locks are released.
type managedProgram struct {
	run    func(context.Context) error
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *managedProgram) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	go func() {
		err := p.run(ctx)
		if ctx.Err() == nil {
			slog.Error("service exited unexpectedly", "error", err)
			// Return a failure to the supervisor; do not leave a dead worker
			// reported as a running Windows service.
			os.Exit(1)
		}
		close(p.done)
	}()
	return nil
}

func (p *managedProgram) Stop(service.Service) error {
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-time.After(25 * time.Second):
		return errors.New("service shutdown timed out")
	}
}

func (p *managedProgram) Shutdown(s service.Service) error { return p.Stop(s) }

type eventWriter struct{ logger service.Logger }

func (w eventWriter) Write(b []byte) (int, error) {
	err := w.logger.Info(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

var serviceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$`)

func serviceConfig(role, name, executable, data, listen, hosts string) (*service.Config, error) {
	if !serviceNamePattern.MatchString(name) {
		return nil, errors.New("service name must contain 1-80 letters, digits, dots, underscores or hyphens and start with a letter or digit")
	}
	// These characters have special expansion rules in supervisor templates.
	// Reject them rather than silently installing a different command.
	for _, value := range []string{executable, data, listen, hosts} {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.TrimSpace(value) != value {
			return nil, errors.New("service paths and arguments cannot contain control characters or leading/trailing whitespace")
		}
		if runtime.GOOS != "windows" && strings.ContainsAny(value, "\\'") {
			return nil, errors.New("service paths and arguments cannot contain backslashes or single quotes on Unix")
		}
		if strings.ContainsAny(value, "\x00\r\n\t%$\"") {
			return nil, errors.New("service paths and arguments cannot contain control characters, percent, dollar or double quote")
		}
	}
	args := []string{role, "service", "run", "--service-name", name, "--data-dir", data}
	if role == "server" {
		args = append(args, "--listen", listen, "--tls-hosts", hosts)
	}
	c := &service.Config{
		Name: name, DisplayName: "GraphWAN " + role, Description: "GraphWAN " + role,
		Executable: executable, Arguments: args, WorkingDirectory: data,
		Option: service.KeyValue{
			"RunAtLoad": true, "KeepAlive": true,
			"OnFailure": "restart", "OnFailureDelayDuration": "3s",
			"SystemdScript": graphwanSystemdUnit,
		},
	}
	if runtime.GOOS == "darwin" {
		c.Option["LogOutput"] = true
		c.Option["LogDirectory"] = "/var/log"
	}
	return c, nil
}

func runManagedService(role string, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Printf("Usage: graphwan %s service install|start|stop|restart|status|uninstall [flags]\nUse ACTION --help for flags. install enables startup at boot; start launches it now.\n", role)
		return nil
	}
	if len(args) == 0 {
		return errors.New("usage: graphwan " + role + " service install|start|stop|restart|status|uninstall [--service-name NAME] [--data-dir DIR]")
	}
	action := args[0]
	switch action {
	case "install", "start", "stop", "restart", "status", "uninstall", "run":
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
	fs := flag.NewFlagSet(role+" service "+action, flag.ContinueOnError)
	name := fs.String("service-name", "graphwan-"+role, "system service name")
	defaultData := "./graphwan-agent-data"
	if role == "server" {
		defaultData = "./graphwan-data"
	}
	data := fs.String("data-dir", defaultData, "existing registered/initialized data directory (install only)")
	var listen, hosts string
	if role == "server" {
		fs.StringVar(&listen, "listen", "127.0.0.1:8443", "server listen address (install only)")
		fs.StringVar(&hosts, "tls-hosts", "localhost,127.0.0.1,::1", "TLS hostnames/IPs (install only)")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if action != "install" && action != "run" {
		var invalid string
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "service-name" {
				invalid = f.Name
			}
		})
		if invalid != "" {
			return fmt.Errorf("--%s applies only to service installation", invalid)
		}
	}
	absoluteData, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	config, err := serviceConfig(role, *name, executable, absoluteData, listen, hosts)
	if err != nil {
		return err
	}
	p := &managedProgram{run: func(ctx context.Context) error {
		if role == "agent" {
			return runAgentContext(context.WithValue(ctx, managedServiceKey{}, *name), []string{"run", "--data-dir", absoluteData})
		}
		return runServerContext(context.WithValue(ctx, managedServiceKey{}, *name), []string{"--data-dir", absoluteData, "--listen", listen, "--tls-hosts", hosts})
	}}
	s, err := service.New(p, config)
	if err != nil {
		return err
	}
	if runtime.GOOS == "linux" && service.Platform() != "linux-systemd" {
		return errors.New("service management on Linux requires systemd")
	}
	switch action {
	case "run":
		if runtime.GOOS == "windows" && !service.Interactive() {
			logger, err := s.Logger(nil)
			if err != nil {
				return err
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(eventWriter{logger}, nil)))
		}
		return s.Run()
	case "install":
		db := "agent.db"
		if role == "server" {
			db = "controller.db"
		}
		info, err := os.Stat(filepath.Join(absoluteData, db))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("initialize/register %s first, using the same --data-dir (missing %s)", role, db)
		}
		if role == "agent" {
			if err := prepareAgentPlatform(context.Background()); err != nil {
				return err
			}
		}
		if err := s.Install(); err != nil {
			return err
		}
		fmt.Printf("Installed %s\nExecutable: %s\nData directory: %s\nRun: graphwan %s service start --service-name %s\n", *name, executable, absoluteData, role, *name)
		return nil
	case "status":
		status, err := s.Status()
		if errors.Is(err, service.ErrNotInstalled) {
			fmt.Printf("%s: not installed\n", *name)
			return nil
		}
		if err != nil {
			return err
		}
		label := map[service.Status]string{service.StatusRunning: "running", service.StatusStopped: "stopped", service.StatusUnknown: "unknown"}
		fmt.Printf("%s: %s\n", *name, label[status])
		return nil
	case "start":
		err = s.Start()
	case "stop":
		err = s.Stop()
	case "restart":
		err = s.Restart()
	case "uninstall":
		status, statusErr := s.Status()
		if errors.Is(statusErr, service.ErrNotInstalled) {
			return statusErr
		}
		if statusErr != nil || status == service.StatusRunning {
			if err := s.Stop(); err != nil {
				return err
			}
		}
		err = s.Uninstall()
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s completed\n", *name, action)
	return nil
}

const graphwanSystemdUnit = `[Unit]
Description={{.Description}}
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart={{.Path|cmd}}{{range .Arguments}} {{.|cmd}}{{end}}
WorkingDirectory={{.WorkingDirectory}}
Restart=on-failure
RestartSec=3
TimeoutStopSec=30
UMask=0077

[Install]
WantedBy=multi-user.target
`
