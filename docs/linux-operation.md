# Linux operation and native verification

The Linux Agent currently supports TUN interfaces, IPv4/IPv6 packet parsing,
weighted forwarding, authenticated TCP/UDP/QUIC/WS/WSS/gRPC peer channels, multiple retained Links,
health probes and durable controller configuration. Native tests verify IPv4 and
IPv6 overlays and underlays independently, including all six transports over IPv6.
The address-family verification matrix below records the tested combinations.

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
QUIC manual endpoints and datagram MTU behavior. [STUN and TCP/UDP hole punching](nat-operation.md)
are operational for the verified Linux NAT scenarios; wider platform/NAT coverage
remains in progress. See the acceptance tracker.

## Runtime behavior

- Every Network gets an owned, nonpersistent TUN interface with a random `gw...`
  name, configured address, MTU and kernel route. Interfaces are created
  exclusively: the process cannot attach to an existing interface by accident.
- Fatal TUN read errors or unavailable-device writes retire only that device.
  A local recovery loop recreates its interface, address, route and MTU even while
  the controller is offline. Retry delays start at one second and double to 30
  seconds, with a one-second scheduling granularity; a device that runs for a
  minute resets the delay. Other Networks, routing tables and peer sessions stay
  intact. Packet rejection or congestion alone does not trigger device recovery.
  The Agent reports `runtime_error` separately from configuration errors; the UI
  shows the fault until recovery succeeds. This handles detected I/O failures;
  it does not yet monitor arbitrary external address/route edits that leave I/O
  operational.
- Configuration changes prepare required resources before replacing the current
  routing state. Failed preparation closes new resources and retains existing
  interfaces and connections. Changing only graph weights preserves peer sessions.
- Linux underlay discovery runs every five seconds. It includes device and veth
  global-unicast and IPv4/IPv6 link-local addresses, excludes TUN/TAP/bridge devices, and advertises only
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

Other implemented adapters are documented for [FreeBSD](freebsd-operation.md),
[macOS](macos-operation.md), [Windows](windows-operation.md),
[OpenBSD](openbsd-operation.md), [NetBSD](netbsd-operation.md) and
[DragonFly](dragonfly-operation.md). Complete runtime acceptance is required on
Linux; other platforms use source review and cross-builds, with any native test
evidence recorded separately.

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
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport tcp --nat --mtu 9000 --underlay-mtu 1280
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --link-local --restricted-agent --overlay-family 6 --mtu 9000 --underlay-mtu 1280
```

The process test needs Python 3.8+, `ip`, `unshare`, `nsenter`, `ping`, and `sleep`.
It creates three child namespaces, connects their veth NICs to an isolated bridge,
runs the actual controller and Agent binaries, and transfers traffic through their
native TUNs. It then stops the controller, restarts the transit Agent offline,
deletes an endpoint TUN while offline and verifies automatic interface/route/MTU
recovery and traffic, then checks that TUNs disappear on shutdown. All child
processes and namespaces are cleaned up on success or failure.

## IPv4 and IPv6 verification matrix

`--overlay-family 4|6` chooses the virtual subnet and inner ICMP/TCP traffic.
`--underlay-family 4|6` chooses peer addresses, the controller's HTTPS address and
its certificate IP SAN. Both default to 4. Automatic TCP/UDP scenarios use the
Agent's discovered addresses; QUIC/WS/WSS/gRPC scenarios configure explicit URLs.
Edges enable only the chosen direct address family, preventing a silent fallback.
The IPv6 fixture uses `2001:db8:42::/64` for the isolated underlay and
`fd42:6777::/64` for the overlay. Test addresses disable DAD to avoid setup delays.

`--link-local` replaces every underlay/controller address with an address from
`169.254.42.0/16`. It requires IPv4 underlay and cannot be combined with NAT. The
three Agents share one Ethernet link and have no other unicast underlay addresses.
The test requires exactly the discovered TCP/UDP link-local endpoints before
creating the topology; automatic mode then requires both transports to become
healthy. It verifies full-MTU IPv6 overlay traffic, controller outage, offline
TUN repair, cached transit-Agent restart and shutdown cleanup. IPv4 link-local
connectivity is intended for peers on the same link, not as a public/NAT endpoint.

`--peer-link-local-v6` keeps IPv4 for controller access and enables only IPv6 peer
traffic. Each Agent has two NICs on separate links with identical link-local IPs,
but different interface names across Agents. Mesh maps owner scopes to each
initiator's local NICs and checks the receiving scope during admission. The test
requires every viable candidate in both directions, rejects mismatched scopes,
and verifies address removal/restoration without replacing unaffected sessions.
It works with automatic TCP/UDP and manual QUIC/WS/WSS/gRPC endpoints. See
[scope identities and policy](endpoint-resolution.md#ipv6-link-local-scopes).
Unspecified, loopback, multicast and broadcast interface addresses remain excluded.

```sh
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --peer-link-local-v6 --restricted-agent --overlay-family 6 \
  --mtu 9000 --underlay-mtu 1280
# Repeat with --transport ws, wss, grpc or quic for manual ingress.
```

| Overlay | Underlay | Transports | Overlay/underlay MTU | Result |
| --- | --- | --- | --- | --- |
| IPv4 | IPv4 | TCP + UDP | 1280 / 1500 | Passed |
| IPv6 | IPv6 | TCP + UDP | 1280 / 1500 | Passed |
| IPv6 | IPv6 | Each of TCP, UDP, QUIC, WS, WSS, gRPC alone | 9000 / 1280 | Passed |
| IPv4 | IPv6 | TCP + UDP | 1280 / 1500 | Passed |
| IPv6 | IPv6 link-local, two NICs | TCP + UDP; WS, WSS, gRPC, QUIC separately | 9000 / 1280 | Passed |
| IPv6 | IPv4 | TCP + UDP | 1280 / 1500 | Passed |
| IPv6 | IPv4 restricted SNAT | UDP alone, TCP alone; punch-only | 9000 / 1280 | Passed |

Every row uses three actual Agents and TUN interfaces. Checks include a full-MTU
ICMP/ICMPv6 packet, a 155,648-byte TCP echo, resource reports, controller outage,
offline replacement of a deleted endpoint TUN, offline transit-Agent restart and
owned-interface cleanup. NAT cases additionally stop STUN. The native adapter
integration test independently checks IPv4/IPv6 connected routes, exclusive
ownership, bidirectional kernel UDP delivery and interrupting idle reads on close.

```sh
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --overlay-family 6 --underlay-family 6
for transport in tcp udp quic ws wss grpc; do
  sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
    --overlay-family 6 --underlay-family 6 --transport "$transport" \
    --mtu 9000 --underlay-mtu 1280
done
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --overlay-family 4 --underlay-family 6
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --overlay-family 6 --underlay-family 4
for transport in udp tcp; do
  sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
    --overlay-family 6 --transport "$transport" --nat \
    --mtu 9000 --underlay-mtu 1280
done
```

These are Linux scenarios with fixed underlay MTUs. They do not establish native
support on other systems, changing path MTUs,
NAT64 or arbitrary NAT behavior. The NAT fixture models IPv4 SNAT and explicitly
rejects `--nat --underlay-family 6`; either virtual address family can use it.
