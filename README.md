# GraphWAN

A centrally managed overlay network with administrator-defined graph topology,
weighted multi-hop routing and peer-to-peer data transport.

**Under development.** The repository is implementing the full design in
`proposal.md`; it is not yet a usable VPN. Progress and verification gaps are
tracked in [the acceptance checklist](docs/implementation-status.md).

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
`--http --listen 127.0.0.1:8080` enables a loopback-only development API.

The [management API](docs/control-api.md) is available now. The React UI, Agent
runtime and forwarding transports are still being implemented. Setting a different
password environment variable on restart does not replace the stored password.

## Development

Requires Go 1.26 or newer. The management frontend will use Node.js and pnpm.

```sh
go test ./...
go test -race ./...
go vet ./...
```

See [architecture](docs/architecture.md) for domain boundaries and invariants.
Changes use Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`, `build:`).
