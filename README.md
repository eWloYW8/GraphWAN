# GraphWAN

A centrally managed overlay network with administrator-defined graph topology,
weighted multi-hop routing and peer-to-peer data transport.

GraphWAN implements the accepted proposal with a Go controller and Agent,
an embedded React management UI, and authenticated TCP/UDP/QUIC/WS/WSS/gRPC
transport. The UI supports graph editing, live status and Link preferences;
the administrative CLI supports Network creation, Node listing and Edge creation.

Runtime acceptance is on Linux, including 24 isolated three-Agent network
scenarios. Windows, macOS, FreeBSD, OpenBSD, NetBSD and DragonFly adapters have
source review and cross-build evidence; complete native validation on those
systems is optional. The [acceptance audit](docs/acceptance-audit.md) maps the
requirements to implementation, tests and operational limits. Deployment
instructions and distribution packaging are in [the deployment guide](docs/deployment.md).

The controller manages multiple networks and distributes durable configuration.
Agents own TUN interfaces, authenticate their neighbors and forward packets along
compiled routes. Each logical edge can retain several transport links with one
active link and automatic failover. The controller does not relay user traffic.

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

Create an enrollment token from **Agents → Enroll agent** or the
[management API](docs/control-api.md), then:

```sh
export GRAPHWAN_ENROLLMENT_TOKEN='<one-time-token>'
sudo --preserve-env=GRAPHWAN_ENROLLMENT_TOKEN ./bin/graphwan agent \
  --server https://controller.example.com:8443 \
  --ca ./ca.pem --name laptop --data-dir /var/lib/graphwan-agent
```

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

See [Linux operation and testing](docs/linux-operation.md) for behavior, current
limits, and reproducible native tests.
For the FreeBSD adapter, requirements and native checks, see
[FreeBSD operation](docs/freebsd-operation.md).
The macOS adapter and its unverified native test procedure are documented in
[macOS operation](docs/macos-operation.md).
The Windows Wintun adapter, DLL setup and pending native checks are documented in
[Windows operation](docs/windows-operation.md).
The OpenBSD adapter, native checks and known transport limitations are documented
in [OpenBSD operation](docs/openbsd-operation.md).
For NetBSD native TUN/configuration/recovery checks and its MTU and ownership
constraints, see [NetBSD operation](docs/netbsd-operation.md).
For DragonFly's autoclone lifecycle and source-reviewed support, see
[DragonFly operation](docs/dragonfly-operation.md).

## Development

Requires Go 1.26 or newer. CI pins Go 1.26.8 and Node.js 24.21.0 in
`.go-version` and `.node-version`; use those versions to reproduce its builds.
Frontend development uses pnpm 10.33.3. Production assets are checked in so a Go-only checkout
builds a complete binary. Rebuild assets whenever frontend sources change.

```sh
go test ./...
go test -race ./...
go vet ./...
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
pnpm --dir web test
pnpm --dir web exec playwright install chromium
pnpm --dir web test:e2e
```

See [architecture](docs/architecture.md) for domain boundaries and invariants.
See [build and CI verification](docs/continuous-integration.md) for the native
test gates, Linux network matrix and cross-build commands.
Changes use Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`, `build:`).
