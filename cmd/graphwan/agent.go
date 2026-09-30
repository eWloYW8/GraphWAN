package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

func runAgent(args []string) error {
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
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
