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

	"github.com/graphwan/graphwan/internal/agent"
	"github.com/graphwan/graphwan/internal/model"
)

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	server := fs.String("server", "", "controller HTTPS origin")
	data := fs.String("data-dir", "./graphwan-agent-data", "private persistent agent directory")
	name := fs.String("name", "", "agent display name for first enrollment")
	ca := fs.String("ca", "", "PEM CA certificate to trust for controller HTTPS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *server == "" {
		return errors.New("agent requires --server")
	}
	if *name == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return err
		}
		*name = hostname
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if *ca != "" {
		raw, err := os.ReadFile(*ca)
		if err != nil {
			return err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return errors.New("CA file contains no certificates")
		}
	}
	cache, err := agent.OpenCache(filepath.Join(*data, "agent.db"))
	if err != nil {
		return err
	}
	defer cache.Close()
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
	client, err = agent.NewClient(cache, runtime, agent.Options{Server: *server, Name: *name, EnrollmentToken: os.Getenv("GRAPHWAN_ENROLLMENT_TOKEN"), Roots: roots, Version: version})
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("agent: %w", err)
	}
	return nil
}
