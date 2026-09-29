package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type listedNode struct {
	NetworkID   model.ID `json:"network_id"`
	NetworkName string   `json:"network_name"`
	model.Node
}

func runAdmin(ctx context.Context, args []string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: graphwan network create/list | node list | edge add [flags]")
	}
	command := args[0] + " " + args[1]
	switch command {
	case "network create", "network list", "node list", "edge add":
	default:
		return fmt.Errorf("unknown administrative command %q", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	server := fs.String("server", "", "controller HTTPS origin (required)")
	ca := fs.String("ca", "", "additional trusted CA PEM file")
	plain := fs.Bool("http", false, "allow literal loopback HTTP for local development")
	timeout := fs.Duration("timeout", 30*time.Second, "total operation timeout")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: graphwan %s [flags]\nAuthenticate with GRAPHWAN_ADMIN_PASSWORD. Output is JSON.\n", command)
		fs.PrintDefaults()
	}
	var name, cidr, cipher, network, a, b, transports, preferred string
	var mtu int
	var weight uint64
	var enabled, v4, v6, punch bool
	if command == "network create" {
		fs.StringVar(&name, "name", "", "network name (required)")
		fs.StringVar(&cidr, "cidr", "", "canonical IPv4 or IPv6 subnet (required)")
		fs.IntVar(&mtu, "mtu", model.DefaultMTU, "overlay MTU")
		fs.StringVar(&cipher, "cipher", string(model.ChaCha20Poly1305), "network cipher suite")
	}
	if command == "node list" || command == "edge add" {
		fs.StringVar(&network, "network", "", "network ID (required for edge add; optional for node list)")
	}
	if command == "edge add" {
		fs.StringVar(&a, "a", "", "first Node ID (required)")
		fs.StringVar(&b, "b", "", "second Node ID (required)")
		fs.Uint64Var(&weight, "weight", 1, "positive edge weight, at most 4294967295")
		fs.StringVar(&transports, "transports", "udp,tcp", "comma-separated udp,tcp,quic,ws,wss,grpc")
		fs.BoolVar(&enabled, "enabled", true, "enable this edge")
		fs.BoolVar(&v4, "ipv4-direct", true, "allow IPv4 direct connections")
		fs.BoolVar(&v6, "ipv6-direct", true, "allow IPv6 direct connections")
		fs.BoolVar(&punch, "hole-punch", true, "allow hole punching")
		fs.StringVar(&preferred, "preferred-candidate", "", "preferred Candidate ID; empty selects automatically")
	}
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments; boolean flags use --flag=false")
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	var created model.Network
	var edge model.Edge
	if command == "network create" {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("--cidr: %w", err)
		}
		created = model.Network{ID: model.NewID(), Name: name, CIDR: prefix, MTU: mtu, Cipher: model.CipherSuite(cipher), Nodes: []model.Node{}, Edges: []model.Edge{}}
		s := model.EmptyState()
		s.Networks = append(s.Networks, created)
		if err := s.Validate(); err != nil {
			return err
		}
	}
	if network != "" || command == "edge add" {
		if err := model.ID(network).Validate(); err != nil {
			return fmt.Errorf("--network: %w", err)
		}
	}
	if command == "edge add" {
		for _, id := range []string{a, b} {
			if err := model.ID(id).Validate(); err != nil {
				return fmt.Errorf("--a and --b require Node IDs: %w", err)
			}
		}
		if a == b || weight == 0 || weight > 1<<32-1 || (!v4 && !v6 && !punch) || len(preferred) > 256 {
			return errors.New("edge requires distinct Nodes, a positive uint32 weight, a connection method and a preference of at most 256 bytes")
		}
		edge = model.Edge{ID: model.NewID(), A: model.ID(a), B: model.ID(b), Weight: uint32(weight), Enabled: enabled, Methods: model.ConnectionMethods{IPv4Direct: v4, IPv6Direct: v6, HolePunch: punch}, PreferredCandidate: preferred}
		seen := map[model.Transport]bool{}
		for _, raw := range strings.Split(transports, ",") {
			kind := model.Transport(strings.TrimSpace(raw))
			if !kind.Valid() || seen[kind] {
				return fmt.Errorf("invalid or duplicate transport %q", kind)
			}
			seen[kind] = true
			edge.Transports = append(edge.Transports, kind)
		}
	}
	client, err := newAdminClient(*server, *ca, *plain)
	if err != nil {
		return err
	}
	defer client.http.CloseIdleConnections()
	password := os.Getenv("GRAPHWAN_ADMIN_PASSWORD")
	if len(password) == 0 || len(password) > 1024 {
		return errors.New("set GRAPHWAN_ADMIN_PASSWORD to the controller's administrator password (1–1024 bytes)")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := client.login(ctx, password); err != nil {
		return err
	}
	defer client.logout()
	var state model.State
	if err := client.request(ctx, "GET", "/api/v1/state", nil, nil, 200, &state); err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return fmt.Errorf("invalid controller state: %w", err)
	}
	revision := state.Revision
	var result any
	switch command {
	case "network list":
		result = struct {
			Revision uint64          `json:"revision"`
			Networks []model.Network `json:"networks"`
		}{revision, append([]model.Network{}, state.Networks...)}
	case "node list":
		nodes := []listedNode{}
		found := network == ""
		for _, n := range state.Networks {
			if network != "" && n.ID != model.ID(network) {
				continue
			}
			found = true
			for _, node := range n.Nodes {
				nodes = append(nodes, listedNode{n.ID, n.Name, node})
			}
		}
		if !found {
			return fmt.Errorf("network %s not found", network)
		}
		result = struct {
			Revision uint64       `json:"revision"`
			Nodes    []listedNode `json:"nodes"`
		}{revision, nodes}
	case "network create":
		state, err = client.change(ctx, "POST", "/api/v1/networks", created, revision, 201)
		if err != nil {
			return err
		}
		result = struct {
			Revision uint64        `json:"revision"`
			Network  model.Network `json:"network"`
		}{state.Revision, created}
	case "edge add":
		index := -1
		for i := range state.Networks {
			if state.Networks[i].ID == model.ID(network) {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("network %s not found", network)
		}
		state.Networks[index].Edges = append(state.Networks[index].Edges, edge)
		if err := state.Validate(); err != nil {
			return err
		}
		state, err = client.change(ctx, "PUT", "/api/v1/networks/"+network, state.Networks[index], revision, 200)
		if err != nil {
			return err
		}
		result = struct {
			Revision uint64     `json:"revision"`
			Edge     model.Edge `json:"edge"`
		}{state.Revision, edge}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return fmt.Errorf("operation succeeded but JSON output failed: %w", err)
	}
	return nil
}
