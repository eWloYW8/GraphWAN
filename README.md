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

## Development

Requires Go 1.26 or newer. The management frontend will use Node.js and pnpm.

```sh
go test ./...
go test -race ./...
go vet ./...
```

See [architecture](docs/architecture.md) for domain boundaries and invariants.
Changes use Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`, `build:`).
