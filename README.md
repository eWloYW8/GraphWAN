# GraphWAN

A centrally managed overlay network with administrator-defined graph topology,
weighted multi-hop routing and peer-to-peer data transport.

## Run the controller

```sh
go build -o bin/graphwan ./cmd/graphwan
# Required only for the first initialization. Choose your own strong password.
export GRAPHWAN_ADMIN_PASSWORD='replace-with-your-own-password'
./bin/graphwan server --data-dir ./graphwan-data
```

The default listener is `https://127.0.0.1:8443`. The controller creates a private
CA and exports its public certificate to `graphwan-data/ca.pem`; trust this file
on administrative clients. Supply `--tls-hosts` with your deployment's DNS names
and IPs, and `--listen` to expose it on the intended interface. Keep the data
directory private and back it up: it contains the CA key and configuration.
`--http --listen 127.0.0.1:8080` enables a loopback-only development server.

Open the controller URL in a browser and sign in to the management UI. See the
[UI guide](docs/management-ui.md) and [management API](docs/control-api.md). Setting
a different password environment variable on restart does not replace the stored password.

The [administrative CLI](docs/admin-cli.md) supports `graphwan network create`,
`graphwan network list`, `graphwan node list` and `graphwan edge add`, with JSON
output, verified HTTPS and revision-checked updates.

## Multiple controllers

Each HTTPS controller starts as a one-member Raft cluster. In **Servers → Add
server**, create an invitation. Start an empty controller on another machine,
open **Servers → Join cluster**, and paste the invitation. It restarts
with the shared configuration, CA and administrator password. All Servers expose
the same management APIs and can enroll Agents; the coordinator is elected
without a permanent primary. Invitations expire after one hour and authorize one
Server identity. Adding a Server first synchronizes it as a nonvoter, then promotes it.

Configuration and enrollment changes require a majority of voting Servers.
Use at least three for one-failure write availability. With two Servers, losing
either pauses writes; Agent connections and existing data forwarding continue.
A minority returns HTTP 503 for writes. Reads show the local committed state and
may briefly lag another replica. Browser sessions remain local to each Server.

Servers persist their entrances and discover TCP addresses using the same physical
interface rules as Agents, with container filtering enabled. TUN/TAP, WireGuard,
bridges and container attachment interfaces are excluded; a containerized Server
retains its default-route uplink. Explicit loopback listeners remain usable for
local development. TCP STUN uses the actual listening port. Add manual
`tcp://`, `ws://`, `grpc://` or `wss://` entrances in **Servers → Edit**; all require
an explicit port. Automatic discovery only declares TCP. Inter-server replication
uses the same listen port and authenticated control carriers. Each data directory
contains that Server's private identity and Raft log; keep it unique per replica.

Each pair of Servers shares a bidirectional connection: either side may establish
it, and Raft replication, elections, write forwarding and status queries reuse it
in both directions. The other Server does not need to dial back. Both Servers
must support this channel protocol; older peers retain the original connection
behavior during rolling upgrades.
See [Server management API](docs/control-api.md#server-clusters).

## Run a Linux agent

Create an enrollment token from **Agents → Enroll agent** or the
[management API](docs/control-api.md), then:

```sh
export GRAPHWAN_ENROLLMENT_TOKEN='<one-time-token>'
sudo --preserve-env=GRAPHWAN_ENROLLMENT_TOKEN ./bin/graphwan agent \
  --server https://controller.example.com:8443 \
  --ca ./ca.pem --name laptop --data-dir /var/lib/graphwan-agent
```

Agent control connections accept `--server-transport tcp|websocket|grpc|wss`
(default `tcp`, also configurable with `GRAPHWAN_SERVER_TRANSPORT`). All four
carriers share the controller's existing listen port and preserve the inner
TLS 1.3 authentication and WebSocket control protocol. `--server` and
`--server-transport` apply only to first enrollment. After registration, the Agent
uses its persistent Server directory and cached CA, updates that directory from
controller snapshots, and automatically switches entrances or Servers on failure.
Restarting with different bootstrap flags does not override the cached directory.
See [control transport configuration](docs/control-api.md#control-transport-carriers).

TUN setup requires root or `CAP_NET_ADMIN`. Add the enrolled Agent to a Network
with a fixed virtual address, then create the desired Edges. Linux automatically
advertises TCP/UDP endpoints from underlay interfaces; manual endpoints are also
supported. QUIC, WS/WSS and gRPC use explicit manual URLs; see
[QUIC setup](docs/quic-operation.md), [WebSocket setup](docs/websocket-operation.md)
and [gRPC setup](docs/grpc-operation.md).
New Agents automatically discover public mappings using configurable STUN services.
For STUN discovery and TCP/UDP hole punching, see [NAT setup](docs/nat-operation.md).
Manual hostname behavior, multiple DNS addresses and TLS identity are documented
in [endpoint resolution](docs/endpoint-resolution.md).
The default peer listen port is 24752. No controller packet relay is
used. Agents keep forwarding and can restart from cached configuration while the
controller is unavailable.

For distribution archives, Linux systemd services, upgrades and backups, see
[deployment](docs/deployment.md).

Platform setup and limitations: [Linux](docs/linux-operation.md),
[FreeBSD](docs/freebsd-operation.md), [macOS](docs/macos-operation.md),
[Windows](docs/windows-operation.md), [OpenBSD](docs/openbsd-operation.md),
[NetBSD](docs/netbsd-operation.md) and [DragonFly](docs/dragonfly-operation.md).

## Development

Requires Go 1.26 or newer. CI pins Go 1.26.8 and Node.js 24.21.0 in
`.go-version` and `.node-version`; use those versions to reproduce its builds.
Frontend development uses pnpm 10.33.3. Production assets are checked in so a Go-only checkout
builds a complete binary. Rebuild assets whenever frontend sources change.

```sh
go test ./...
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
```

See [development checks](docs/continuous-integration.md) for the small core test suite and CI.
