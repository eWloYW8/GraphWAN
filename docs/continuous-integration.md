# Development checks

CI runs two small jobs on Linux: the core Go tests and binary build, and the
frontend production build. It checks that the embedded frontend assets match
the source. Toolchains are pinned in `.go-version`, `.node-version` and
`web/package.json`.

## Core tests

Seven Go test files contain 17 test functions covering:

- Topology validation and deterministic weighted routing.
- Disabled edges, revoked Agents and loop-free forwarding tables.
- Packet framing, malformed input and hop limits.
- Four cipher suites, authenticated handshakes, tampering and replay rejection.
- Durable configuration, transactional rollback and conflicting edits.
- Bidirectional cluster channels, leader changes, forwarded writes and reconnects
  over in-memory connections with only one permitted dialing direction.

They run locally without starting a controller or Agent, opening network ports,
creating TUN devices or requiring administrator privileges. Store tests use
private temporary files.

```sh
go test ./...
go build -o bin/graphwan ./cmd/graphwan
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
```

Commit rebuilt files in `internal/webui/dist` when frontend sources change.
Cross-platform binaries can be built on demand with `scripts/cross-build.py`;
packaging instructions are in [deployment](deployment.md).
