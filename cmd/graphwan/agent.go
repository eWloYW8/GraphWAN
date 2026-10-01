package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

func runAgent(args []string) error {
	if len(args) > 0 && args[0] == "service" {
		return runManagedService("agent", args[1:])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runAgentContext(ctx, args)
}

func runAgentContext(parent context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "install-wintun":
			return runInstallWintun(parent, args[1:])
		case "enroll":
			return runAgentEnroll(args[1:])
		case "run":
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	server := fs.String("server", "", "controller HTTPS origin (first enrollment only)")
	defaultTransport := os.Getenv("GRAPHWAN_SERVER_TRANSPORT")
	if defaultTransport == "" {
		defaultTransport = "tcp"
	}
	serverTransport := fs.String("server-transport", defaultTransport, "first enrollment carrier: tcp, websocket, grpc or wss")
	data := fs.String("data-dir", "./graphwan-agent-data", "private persistent agent directory")
	name := fs.String("name", "", "agent display name for first enrollment")
	ca := fs.String("ca", "", "PEM CA certificate to trust for controller HTTPS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *name == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return err
		}
		*name = hostname
	}
	cache, err := agent.OpenCache(filepath.Join(*data, "agent.db"))
	if err != nil {
		return err
	}
	defer cache.Close()
	reg, err := cache.Registration()
	if err != nil {
		return err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if reg != nil && len(reg.CA) > 0 {
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(reg.CA) {
			return errors.New("invalid cached CA")
		}
	} else if *ca != "" {
		raw, err := os.ReadFile(*ca)
		if err != nil {
			return err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return errors.New("CA file contains no certificates")
		}
	}
	if reg == nil && *server == "" {
		return errors.New("first enrollment requires --server")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := prepareAgentPlatform(ctx); err != nil {
		return err
	}
	var client *agent.Client
	runtime, err := agent.NewDataPlane(ctx, cache.PrivateKey(), agent.DataPlaneOptions{Endpoints: func(endpoints []model.Endpoint) error {
		if client == nil {
			return nil
		}
		return client.SetEndpoints(endpoints)
	}})
	if err != nil {
		return err
	}
	defer runtime.Close()
	client, err = agent.NewClient(cache, runtime, agent.Options{Server: *server, ServerTransport: *serverTransport, Name: *name, EnrollmentToken: os.Getenv("GRAPHWAN_ENROLLMENT_TOKEN"), Roots: roots, Version: version})
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("agent: %w", err)
	}
	return nil
}

// runAgentEnroll reads invitations from stdin/files/environment so secrets need
// not appear in process arguments. Explicit file input takes precedence over env.
func runAgentEnroll(args []string) error {
	fs := flag.NewFlagSet("agent enroll", flag.ContinueOnError)
	file := fs.String("invitation-file", "", "invitation file, or - for stdin (default: GRAPHWAN_AGENT_INVITATION)")
	data := fs.String("data-dir", "./graphwan-agent-data", "private persistent agent directory")
	name := fs.String("name", "", "agent name (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	raw := os.Getenv("GRAPHWAN_AGENT_INVITATION")
	if *file != "" {
		var reader io.Reader = os.Stdin
		if *file != "-" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		b, err := io.ReadAll(io.LimitReader(reader, model.MaxEnrollmentInvitation+1))
		if err != nil {
			return err
		}
		raw = string(b)
	}
	if strings.TrimSpace(raw) == "" {
		return errors.New("provide --invitation-file PATH, --invitation-file - for stdin, or GRAPHWAN_AGENT_INVITATION")
	}
	invite, err := model.ParseAgentInvitation(raw)
	if err != nil {
		return err
	}
	if *name == "" {
		*name, err = os.Hostname()
		if err != nil {
			return err
		}
	}
	cache, err := agent.OpenCache(filepath.Join(*data, "agent.db"))
	if err != nil {
		return err
	}
	defer cache.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	reg, err := agent.Enroll(ctx, cache, invite, *name)
	if err != nil {
		return err
	}
	fmt.Printf("Agent registered: %s\nData directory: %s\n", reg.AgentID, *data)
	return nil
}
