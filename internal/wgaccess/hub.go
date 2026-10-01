package wgaccess

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"golang.org/x/crypto/blake2s"
	"golang.zx2c4.com/wireguard/device"
)

type Send func(context.Context, []byte, netip.AddrPort, []byte) error
type network struct {
	probes      *pinger
	id          model.ID
	fingerprint string
	publicKey   string
	macKey      [32]byte
	peers       map[model.ID]model.Peer
	byKey       map[string]model.Peer
	bind        *bind
	tun         *memoryTUN
	device      *device.Device
}
type indexed struct {
	network *network
	until   time.Time
}
type networkTable struct{ networks map[model.ID]*network }

type Hub struct {
	active   atomic.Pointer[networkTable]
	mu       sync.Mutex
	ctx      context.Context
	identity ed25519.PrivateKey
	port     uint16
	send     Send
	receive  func(model.ID, []byte)
	networks map[model.ID]*network
	indices  map[uint32][]indexed
}

func New(ctx context.Context, identity ed25519.PrivateKey, port uint16, send Send, receive func(model.ID, []byte)) *Hub {
	return &Hub{ctx: ctx, identity: identity, port: port, send: send, receive: receive, networks: map[model.ID]*network{}, indices: map[uint32][]indexed{}}
}
func (h *Hub) recordIndex(index uint32, n *network) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for id, values := range h.indices {
		retained := values[:0]
		for _, value := range values {
			if now.Before(value.until) {
				retained = append(retained, value)
			}
		}
		if len(retained) == 0 {
			delete(h.indices, id)
		} else {
			h.indices[id] = retained
		}
	}
	entries := h.indices[index]
	for i := range entries {
		if entries[i].network == n {
			entries[i].until = time.Now().Add(10 * time.Minute)
			return
		}
	}
	h.indices[index] = append(entries, indexed{n, time.Now().Add(10 * time.Minute)})
}
func (h *Hub) Receive(raw []byte, remote netip.AddrPort, control []byte) {
	if len(raw) < 8 {
		return
	}
	h.mu.Lock()
	var targets []*network
	if raw[0] == 1 || raw[0] == 2 {
		if len(raw) < 32 {
			h.mu.Unlock()
			return
		}
		for _, n := range h.networks {
			mac, _ := blake2s.New128(n.macKey[:])
			mac.Write(raw[:len(raw)-32])
			if subtle.ConstantTimeCompare(mac.Sum(nil), raw[len(raw)-32:len(raw)-16]) == 1 {
				targets = append(targets, n)
			}
		}
	} else {
		for _, entry := range h.indices[binary.LittleEndian.Uint32(raw[4:8])] {
			if h.networks[entry.network.id] == entry.network && time.Now().Before(entry.until) {
				targets = append(targets, entry.network)
			}
		}
	}
	h.mu.Unlock()
	// Rare receiver-index collisions are tried in each matching device; only the
	// correct session can authenticate. Indices are never rewritten on the wire.
	for _, n := range targets {
		n.bind.enqueue(raw, remote, control)
	}
}

// Apply is serialized by Mesh. Prepare all replacements before publishing;
// ordinary endpoint/telemetry refreshes retain existing WireGuard sessions.
func (h *Hub) Apply(snapshot model.Snapshot) error {
	next := map[model.ID]*network{}
	var prepared []*network
	cleanup := func() {
		for _, n := range prepared {
			n.device.Close()
		}
	}
	h.mu.Lock()
	old := h.networks
	h.mu.Unlock()
	for _, cfg := range snapshot.Networks {
		var peers []model.Peer
		for _, p := range cfg.Peers {
			if p.Node.WireGuard != nil {
				p.Node = p.Node.Clone()
				p.Node.WireGuard.Endpoint = ""
				peers = append(peers, p)
			}
		}
		if len(peers) == 0 {
			continue
		}
		type peerIdentity struct {
			Node, Edge model.ID
			Address    netip.Addr
			Key        string
		}
		identities := make([]peerIdentity, 0, len(peers))
		for _, p := range peers {
			identities = append(identities, peerIdentity{p.Node.ID, p.Edge.ID, p.Node.Address, p.Node.WireGuard.PublicKey})
		}
		raw, _ := json.Marshal(struct {
			MTU    int
			Source netip.Addr
			Peers  []peerIdentity
		}{cfg.MTU, cfg.Self.Address, identities})
		fingerprint := string(raw)
		if prior := old[cfg.ID]; prior != nil && prior.fingerprint == fingerprint {
			next[cfg.ID] = prior
			continue
		}
		secret, err := hkdf.Key(sha256.New, h.identity.Seed(), nil, "GraphWAN WireGuard network v1/"+string(cfg.ID), 32)
		if err != nil {
			cleanup()
			return err
		}
		key, err := ecdh.X25519().NewPrivateKey(secret)
		if err != nil {
			cleanup()
			return err
		}
		n := &network{id: cfg.ID, fingerprint: fingerprint, publicKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), peers: map[model.ID]model.Peer{}, byKey: map[string]model.Peer{}}
		n.probes = newPinger(cfg.Self.Address, peers)
		n.macKey = blake2s.Sum256(append([]byte("mac1----"), key.PublicKey().Bytes()...))
		n.bind = &bind{hub: h, owner: n}
		n.tun = newTUN(cfg.MTU, cfg.ID, func(id model.ID, raw []byte) {
			h.mu.Lock()
			current := h.networks[id] == n
			h.mu.Unlock()
			if current && !n.probes.consume(raw) {
				h.receive(id, raw)
			}
		})
		n.device = device.NewDevice(n.tun, n.bind, device.NewLogger(device.LogLevelSilent, ""))
		prepared = append(prepared, n)
		var config strings.Builder
		fmt.Fprintf(&config, "private_key=%s\n", hex.EncodeToString(secret))
		clear(secret)
		for _, p := range peers {
			key := hex.EncodeToString(p.PublicKey)
			n.peers[p.Node.ID] = p
			n.byKey[key] = p
			fmt.Fprintf(&config, "public_key=%s\nallowed_ip=%s/%d\n", key, p.Node.Address, p.Node.Address.BitLen())
		}
		if err = n.device.IpcSet(config.String()); err != nil {
			cleanup()
			return err
		}
		if err = n.device.Up(); err != nil {
			cleanup()
			return err
		}
		next[cfg.ID] = n
	}
	h.mu.Lock()
	h.networks = next
	if len(next) == 0 {
		h.active.Store(nil)
	} else {
		h.active.Store(&networkTable{networks: next})
	}
	for index, entries := range h.indices {
		retained := entries[:0]
		for _, entry := range entries {
			if next[entry.network.id] == entry.network && time.Now().Before(entry.until) {
				retained = append(retained, entry)
			}
		}
		if len(retained) == 0 {
			delete(h.indices, index)
		} else {
			h.indices[index] = retained
		}
	}
	h.mu.Unlock()
	for id, n := range old {
		if next[id] != n {
			n.device.Close()
		}
	}
	return nil
}
func (h *Hub) SendFrame(networkID, peer model.ID, frame []byte) (bool, error) {
	active := h.active.Load()
	if active == nil {
		return false, nil
	}
	n := active.networks[networkID]
	if n == nil {
		return false, nil
	}
	if _, ok := n.peers[peer]; !ok {
		return false, nil
	}
	parsed, err := packet.ParseView(frame)
	if err != nil {
		return true, err
	}
	return true, n.tun.send(parsed.Payload)
}
func (h *Hub) Close() {
	h.mu.Lock()
	old := h.networks
	h.networks = map[model.ID]*network{}
	h.active.Store(nil)
	h.indices = map[uint32][]indexed{}
	h.mu.Unlock()
	for _, n := range old {
		n.device.Close()
	}
}
func (h *Hub) Report() []model.LinkStatus {
	h.mu.Lock()
	networks := make([]*network, 0, len(h.networks))
	for _, n := range h.networks {
		networks = append(networks, n)
	}
	h.mu.Unlock()
	var out []model.LinkStatus
	for _, n := range networks {
		data, err := n.device.IpcGet()
		if err != nil {
			continue
		}
		stats := map[string]map[string]string{}
		var fields map[string]string
		for _, line := range strings.Split(data, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if k == "public_key" {
				fields = map[string]string{}
				stats[v] = fields
			} else if fields != nil {
				fields[k] = v
			}
		}
		for key, p := range n.byKey {
			f := stats[key]
			seconds, _ := strconv.ParseInt(f["last_handshake_time_sec"], 10, 64)
			nanos, _ := strconv.ParseInt(f["last_handshake_time_nsec"], 10, 64)
			var handshake time.Time
			if seconds > 0 {
				handshake = time.Unix(seconds, nanos)
			}
			healthy := !handshake.IsZero() && time.Since(handshake) < 3*time.Minute
			rx, _ := strconv.ParseUint(f["rx_bytes"], 10, 64)
			tx, _ := strconv.ParseUint(f["tx_bytes"], 10, 64)
			request, rtt, measured, valid := n.probes.sample(p.Node.ID, healthy, time.Now())
			if request != nil {
				if err := n.tun.send(request); err != nil {
					n.probes.failed(p.Node.ID)
					rtt, valid, measured = 0, false, time.Time{}
				}
			}
			out = append(out, model.LinkStatus{RTTMillis: rtt, RTTValid: valid, RTTMeasuredAt: measured, NetworkID: n.id, EdgeID: p.Edge.ID, LinkID: "wg:" + string(n.id) + ":" + string(p.Node.ID), CandidateID: "wireguard", Transport: model.WireGuard, WireGuardPublicKey: n.publicKey, LastHandshake: handshake, Remote: f["endpoint"], Healthy: healthy, Active: healthy, RXBytes: rx, TXBytes: tx})
		}
	}
	return out
}
