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

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/cluster"
	"github.com/eWloYW8/GraphWAN/internal/control"
	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/eWloYW8/GraphWAN/internal/transport"
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
	case "_apply-update":
		return runUpdateHelper(args[1:])
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
	if len(args) > 0 && args[0] == "service" {
		return runManagedService("server", args[1:])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServerContext(ctx, args)
}

func runServerContext(parent context.Context, args []string) error {
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
	ctx, stop := context.WithCancel(parent)
	defer stop()
	updater, err := managedUpdater(ctx, *data)
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		reload, err := serveController(ctx, *listen, *data, *hosts, *insecure, updater)
		if err != nil {
			return err
		}
		if !reload {
			return nil
		}
	}
	return nil
}

func serveController(ctx context.Context, listen, data, hosts string, insecure bool, updater agent.Updater) (bool, error) {
	db, err := store.Open(filepath.Join(data, "controller.db"))
	if err != nil {
		return false, err
	}
	defer db.Close()
	if err := cluster.InstallPending(db); err != nil {
		return false, err
	}
	app, err := control.New(db, control.Options{
		Version: version, Updater: updater,
		Password:        os.Getenv("GRAPHWAN_ADMIN_PASSWORD"),
		GeoIPDirectory:  filepath.Join(data, "geoip"),
		UpdateDirectory: filepath.Join(data, "updates"),
	})
	if err != nil {
		return false, err
	}
	defer app.Close()
	if err := os.WriteFile(filepath.Join(data, "ca.pem"), app.Authority().PEM, 0644); err != nil {
		return false, err
	}
	server := &http.Server{Addr: listen, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	var carriers *controltransport.Server
	var runtime *cluster.Runtime
	if !insecure {
		listener, err := transport.ListenTCP(ctx, listen)
		if err != nil {
			return false, err
		}
		defer listener.Close()
		identity, err := cluster.LoadIdentity(db)
		if err != nil {
			return false, err
		}
		endpoints, err := cluster.InterfaceEndpoints(identity, listener.Addr())
		if err != nil {
			return false, err
		}
		if err = cluster.Initialize(db, app.Authority(), &identity, endpoints); err != nil {
			return false, err
		}
		runtime, err = app.StartCluster(identity, data)
		if err != nil {
			return false, err
		}
		defer runtime.Close()
		tlsHosts := append(strings.Split(hosts, ","), (model.Server{ID: identity.ID}).TLSName())
		server.TLSConfig, err = app.Authority().ServerTLS(tlsHosts)
		if err != nil {
			return false, err
		}
		carriers = controltransport.NewServer(listener, app, server.TLSConfig)
		defer carriers.Close()
		runtime.StartDiscovery(listener)
	}
	done := make(chan error, 1)
	go func() {
		if insecure {
			done <- server.ListenAndServe()
		} else {
			done <- carriers.Serve()
		}
	}()
	slog.Info("GraphWAN controller starting", "listen", listen, "tls", !insecure, "ca", filepath.Join(data, "ca.pem"))
	reload := false
	select {
	case err := <-done:
		if controltransport.Closed(err) {
			return false, nil
		}
		return false, err
	case <-ctx.Done():
	case <-app.Reload():
		reload = true
	}
	app.Close()
	if runtime != nil {
		runtime.Close()
	}
	if carriers != nil {
		carriers.Close()
		err = <-done
		carriers.Wait()
	} else {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			server.Close()
			return false, err
		}
		err = <-done
	}
	if controltransport.Closed(err) {
		return reload, nil
	}
	return false, err
}
