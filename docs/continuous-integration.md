# Release CI and local development checks

The only workflow is [Release](../.github/workflows/release.yml). Ordinary branch
pushes and pull requests do not run CI. Pushing a SemVer tag such as `v1.2.3` or
`v1.2.3-rc.1` starts a build and automatically publishes a GitHub Release. The `v`
prefix is optional; build metadata such as `v1.2.3+build.1` is supported. Tags
are limited to 80 characters. Prerelease identifiers produce a prerelease.

```sh
git tag v1.2.3
git push origin v1.2.3
```

## Release build

One Ubuntu job installs the pinned Go, Node.js and pnpm versions, builds the
frontend from the lockfile, and embeds it into every binary. It cross-compiles
with CGO disabled for every architecture advertised by the pinned Go toolchain
on the seven supported operating systems. Go 1.26.8 currently supplies 33 targets:

| System | Architectures |
| --- | --- |
| Linux | 386, amd64, arm, arm64, loong64, mips, mipsle, mips64, mips64le, ppc64, ppc64le, riscv64, s390x |
| Windows | 386, amd64, arm64 |
| macOS (`darwin`) | amd64, arm64 |
| FreeBSD | 386, amd64, arm, arm64 |
| OpenBSD | 386, amd64, arm, arm64, ppc64, riscv64 |
| NetBSD | 386, amd64, arm, arm64 |
| DragonFly | amd64 |

The binary's `version` command and archive names use the exact tag. CPU baselines
remain in `scripts/cross-build.py` (including amd64 v1, ARMv7 and soft-float MIPS).
Cross-compilation does not establish native runtime support on every target;
platform restrictions remain documented in the operation guides.
The legacy Bolt import used by Raft's migration helper is redirected through a
small bbolt adapter in `internal/boltcompat`, allowing MIPS, RISC-V and LoongArch
builds. GraphWAN's live Raft store continues to use the same bbolt implementation
and file format.

Windows packages use ZIP; Unix packages use tar.gz. Every archive contains the
binary, build manifest, documentation and deployment examples. Release assets
also include `release.json` and `SHA256SUMS`. Packaging verifies all binary
hashes, then CI verifies archive checksums before uploading. All targets must
build successfully. No tests, browser automation or live deployments run in CI.

Publication uses the repository's `GITHUB_TOKEN` with `contents: write` and
automatically generated release notes. Assets are uploaded to a draft before
publication. A failed upload can be retried by rerunning the workflow; an already
published release is left intact. No extra release secret is required. Toolchains
are pinned in `.go-version`, `.node-version` and `web/package.json`.

## Local core tests

Ten Go test files contain 20 test functions covering:

- Topology validation and deterministic weighted routing.
- Disabled edges, revoked Agents and loop-free forwarding tables.
- Packet framing, malformed input and hop limits.
- Four cipher suites, authenticated handshakes, tampering and replay rejection.
- Durable configuration, transactional rollback and conflicting edits.
- Bidirectional cluster channels, leader changes, forwarded writes and reconnects
  over in-memory connections with only one permitted dialing direction.
- Idle probe failure detection, report suppression and observed endpoint lease
  renewal, using in-memory state and explicit timestamps.

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
