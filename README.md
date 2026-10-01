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

## Run a Linux agent

Create an enrollment invitation from **Agents → Enroll agent**, save it as
`agent-invitation.txt`, then register and run:

```sh
sudo ./bin/graphwan agent enroll --invitation-file ./agent-invitation.txt \
  --name laptop --data-dir /var/lib/graphwan-agent
sudo ./bin/graphwan agent run --data-dir /var/lib/graphwan-agent
```

The invitation includes Server addresses, transport types, the cluster CA public
certificate and a one-use token. No separate `--server`, `--server-transport` or
`--ca` is required. Treat the invitation as a credential; Base64 is not encryption.
Use `--invitation-file -` for stdin or `GRAPHWAN_AGENT_INVITATION` for automation.
Enrollment saves identity and exits without starting a service. Repeating it for
the same cluster preserves the existing identity; another cluster is rejected.
The legacy `graphwan agent --server ...` enrollment remains supported.

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

For built-in Linux, Windows and macOS service management, see
[service management](docs/service-management.md).

For release binaries, Linux systemd services, upgrades and backups, see
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

Version tags such as `v1.2.3` trigger cross-platform builds and a GitHub Release
with one standalone binary per target. See [release CI and local checks](docs/continuous-integration.md).
