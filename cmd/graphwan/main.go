package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/graphwan/graphwan/internal/control"
	"github.com/graphwan/graphwan/internal/store"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("graphwan failed", "error", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: graphwan server | agent | network create/list | node list | edge add | version (use --help after a command)")
	}
	switch args[0] {
	case "version":
		fmt.Println("GraphWAN", version)
		return nil
	case "agent":
		return runAgent(args[1:])
	case "server":
		return runServer(args[1:])
	case "network", "node", "edge":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runAdmin(ctx, args, os.Stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8443", "HTTP(S) listen address")
	data := fs.String("data-dir", "./graphwan-data", "private persistent state directory")
	hosts := fs.String("tls-hosts", "localhost,127.0.0.1,::1", "comma-separated TLS hostnames/IPs")
	insecure := fs.Bool("http", false, "serve plaintext HTTP on loopback for local development")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *insecure {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("--http requires a literal loopback listen address")
		}
	}
	db, err := store.Open(filepath.Join(*data, "controller.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	app, err := control.New(db, control.Options{Password: os.Getenv("GRAPHWAN_ADMIN_PASSWORD")})
	if err != nil {
		return err
	}
	defer app.Close()
	if err := os.WriteFile(filepath.Join(*data, "ca.pem"), app.Authority().PEM, 0644); err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	if !*insecure {
		server.TLSConfig, err = app.Authority().ServerTLS(strings.Split(*hosts, ","))
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		if *insecure {
			done <- server.ListenAndServe()
		} else {
			done <- server.ListenAndServeTLS("", "")
		}
	}()
	slog.Info("GraphWAN controller starting", "listen", *listen, "tls", !*insecure, "ca", filepath.Join(*data, "ca.pem"))
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app.Close()
	if err := server.Shutdown(shutdown); err != nil {
		server.Close()
		return err
	}
	err = <-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
