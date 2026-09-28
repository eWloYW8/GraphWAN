# Linux operation and native verification

The Linux Agent currently supports TUN interfaces, IPv4/IPv6 packet parsing,
weighted forwarding, authenticated TCP/UDP/QUIC/WS/WSS/gRPC peer channels, multiple retained Links,
health probes and durable controller configuration. The process-level test verifies
an IPv4 overlay and underlay; native IPv6 verification remains to be added.

## Start and configure

Build with `go build -o bin/graphwan ./cmd/graphwan`. Start the controller as in the
README, distribute its public `ca.pem` to clients through a trusted channel, and
create one enrollment token per Agent via `POST /api/v1/enrollment-tokens`.

Run `graphwan agent --server https://host:8443 --ca ca.pem --name my-node
--data-dir /var/lib/graphwan-agent` with `GRAPHWAN_ENROLLMENT_TOKEN` in the
environment for initial enrollment. The token is not required on subsequent
starts. The Agent database contains its private key, registration certificate,
desired snapshot and last applied snapshot. Keep it private and do not copy an
Agent identity to a second concurrently running installation.

A new Agent waits for Network memberships. Use the management API to create a
Network, assign each enrolled Agent a fixed virtual address and add explicit Edges.
TCP, UDP, QUIC, WS, WSS and gRPC direct connectivity are operational. Configure WS/WSS
using [manual endpoints](websocket-operation.md); gRPC has its own
[endpoint guide](grpc-operation.md). See [QUIC setup](quic-operation.md) for
QUIC manual endpoints and datagram MTU behavior. [STUN and UDP hole punching](nat-operation.md)
are operational for the verified NAT scenarios; TCP punching and wider platform/NAT
coverage remain in progress. See the acceptance tracker.

## Runtime behavior

- Every Network gets an owned, nonpersistent TUN interface with a random `gw...`
  name, configured address, MTU and kernel route. Interfaces are created
  exclusively: the process cannot attach to an existing interface by accident.
- Configuration changes prepare required resources before replacing the current
  routing state. Failed preparation closes new resources and retains existing
  interfaces and connections. Changing only graph weights preserves peer sessions.
- Linux underlay discovery runs every five seconds. It includes device and veth
  global-unicast addresses, excludes TUN/TAP/bridge devices, and advertises only
  TCP/UDP endpoints using the configured Agent listen port (24752 by default).
- Each Edge independently retries allowed candidates with jittered backoff.
  The Agent permits eight concurrent outgoing attempts and eight incoming
  handshakes. Every retained Link exchanges authenticated heartbeats once per
  second, with a five-second liveness timeout.
- An available manually preferred candidate wins immediately. Otherwise the
  lowest measured RTT wins, with a two-second hold time and a 15%/2 ms minimum
  improvement to avoid switches caused by timing noise. An unhealthy active Link
  triggers selection of a fallback. The lower Node ID coordinates a common Link
  through authenticated prepare/accept/commit/confirm messages; the follower
  briefly pauses sending during the switch. See [the peer protocol](peer-protocol.md).
- Healthy standby Links remain connected. Session replacement begins after
  50 minutes; old sessions retire after an authenticated replacement is healthy
  and both endpoints have completed the common selection.
  Encryption enforces a one-hour/`2^32`-message hard limit independently.
- Packet queues are bounded. Congested Links drop packets instead of allocating
  unbounded memory. Inner TCP can retransmit; the raw UDP data transport does not
  add packet retransmission.
- The controller is used only for management, configuration and telemetry.
  Its absence does not cancel the runtime, peer reconnection or cached startup.

Other operating systems currently return an explicit unsupported-TUN error when
configured with a Network. This is an implementation gap, not the final platform
support policy.

## Reproduce native verification

Native tests require Linux, root privileges and network namespace support. The
scripts enforce namespace isolation; their interface/route changes are confined
to disposable namespaces.

```sh
go test -race ./...
go vet ./...
go test -c -tags integration -o /tmp/graphwan-tunnel-test ./internal/tunnel
sudo unshare --net env GRAPHWAN_TEST_NETNS=1 /tmp/graphwan-tunnel-test -test.v

go build -o /tmp/graphwan-e2e ./cmd/graphwan
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport ws
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport wss
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport grpc
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport quic
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport quic --mtu 9000 --underlay-mtu 1280
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport udp --nat
```

The process test needs Python 3.8+, `ip`, `unshare`, `nsenter`, `ping`, and `sleep`.
It creates three child namespaces, connects their veth NICs to an isolated bridge,
runs the actual controller and Agent binaries, and transfers traffic through their
native TUNs. It then stops the controller, restarts the transit Agent offline,
repeats the traffic checks, and checks that TUNs disappear on shutdown. All child
processes and namespaces are cleaned up on success or failure.
