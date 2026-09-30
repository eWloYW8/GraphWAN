# Linux operation

The Linux Agent currently supports TUN interfaces, IPv4/IPv6 packet parsing,
weighted forwarding, authenticated TCP/UDP/QUIC/WS/WSS/gRPC peer channels, multiple retained Links,
health probes and durable controller configuration.

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
are described with their traversal limits in that guide.

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
  second during traffic and once per ten seconds while idle. Missing replies
  restore one-second probes, with a five-second response timeout; detecting a
  silent idle failure can therefore take approximately fifteen seconds.
  Updated peers negotiate three-byte probe bodies (legacy: nine bytes) while
  retaining the full authentication tag and replay protection.
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
  Updated Agents/controllers exchange one-byte control heartbeats every fifteen
  seconds and full telemetry about once per minute while idle. Traffic and state
  changes still report every two seconds; configuration application ACKs promptly.

Other implemented adapters are documented for [FreeBSD](freebsd-operation.md),
[macOS](macos-operation.md), [Windows](windows-operation.md),
[OpenBSD](openbsd-operation.md), [NetBSD](netbsd-operation.md) and
[DragonFly](dragonfly-operation.md).

## Live listener reconfiguration

Change an Agent's listen port through its settings. It prepares replacement
listeners before switching the applied configuration. A bind failure retains the
old listeners and reports a configuration error; reconciliation retries the
requested configuration. Successful replacement reconnects peer sessions while
retaining Network interfaces. Discovery republishes automatic endpoints with the
new port. Update manual endpoint URLs explicitly.
