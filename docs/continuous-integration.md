# Build and verification

The [CI workflow](../.github/workflows/ci.yml) runs on pull requests, pushes to
`main`/`master`, and manual dispatch. `Required checks` succeeds only when every
job and matrix entry succeeds; skipped or canceled dependencies fail that gate.
Configure that check in repository branch protection after enabling Actions.
The workflow has read-only repository permissions, does not retain checkout
credentials, pins actions by commit, and retains logs for seven days. It does
not publish releases or deploy services.

## Toolchains

CI uses the Go patch release in [`.go-version`](../.go-version), the Node.js patch
release in [`.node-version`](../.node-version), and the pnpm version declared by
[`web/package.json`](../web/package.json). Go dependencies and frontend dependencies
use their committed lock data. Install these versions to reproduce CI locally.
Python 3.10 or newer is used for the check and build scripts.

## Checks

| Job | Coverage |
| --- | --- |
| Backend | Ubuntu 24.04, Windows 2025, macOS 15 arm64 and Intel: Go formatting, module integrity, vet, uncached race tests and native kernel/Agent tests |
| Frontend | Locked install, formatting, TypeScript/Vite build, exact embedded-asset comparison, unit tests and Chromium integration/accessibility tests against a real controller |
| Linux network | Thirteen isolated three-Agent scenarios: automatic TCP/UDP, all six IPv6 transports, mixed address families, and TCP/UDP NAT with IPv4/IPv6 overlays |
| FreeBSD native | FreeBSD 15.1 amd64 VM: TUN lifecycle and reconfiguration, Agent reconciliation, interface discovery, maximum-size UDP/wildcard replies and the complete Mesh test package |
| Cross-build | Every advertised architecture for the seven operating systems below, with binary sizes and SHA-256 hashes |

Linux network scenarios include controller/STUN outages, offline TUN repair,
cached Agent restart, full-MTU traffic and resource cleanup. The underlay MTU is
1280; the automatic IPv4 baseline uses overlay MTU 1280 and the other scenarios
use 9000. See [Linux testing](linux-operation.md) for the fixtures and their limits.

Native checks parse `test2json` output and require named tests to finish with
`pass`, including a successful package result. Missing tests, failed tests,
truncated results and skipped required tests or their subtests fail the gate.
This prevents a missing administrator privilege, opt-in flag or loopback fixture
from being counted as native acceptance. Ordinary unprivileged Go tests may still
skip privileged integration cases.

## Run locally

From the repository root:

```sh
python3 -B -m unittest discover -s scripts -p 'test_*.py' -v
python3 scripts/check.py go --logs /tmp/graphwan-checks
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -color
pnpm --dir web install --frozen-lockfile
pnpm --dir web exec playwright install --with-deps chromium
python3 scripts/check.py frontend --logs /tmp/graphwan-checks
```

Frontend sources and their rebuilt embedded assets must be committed together.
The check compares both the file set and contents, so newly generated hashed
assets cannot silently escape verification through `.gitignore`.

On Linux, native tests require `sudo -n`, `unshare`, `ip` and the TUN driver. Each
test binary runs inside a new network namespace:

```sh
python3 scripts/check.py native --logs /tmp/graphwan-native
```

On disposable macOS hosts, configure the two loopback aliases documented in
[macOS operation](macos-operation.md#socket-test-fixtures), then set `GRAPHWAN_TEST_MACOS=1` before
running the same command. Windows requires an elevated terminal and
`GRAPHWAN_TEST_WINDOWS=1`; use `python` if that is the installed command name.
The script downloads the pinned, verified Wintun DLL into the test binary's
temporary directory. See [Windows operation](windows-operation.md).

The FreeBSD CI job cross-compiles with the pinned Go toolchain and executes using
[vmactions/freebsd-vm](https://github.com/vmactions/freebsd-vm/tree/a2f9a41fa97f6848b8c3b791087dfcdaa5b473ff).
The action is pinned; its `15.1` image selector permits updated 15.1 images.
The guest needs neither a Go installation nor Python. To reproduce with your
own disposable FreeBSD 15.1/amd64 VM:

```sh
python3 scripts/check.py prepare-freebsd \
  --binaries /tmp/graphwan-freebsd --logs /tmp/graphwan-freebsd/build-logs
# Copy that entire directory into the guest, then run there as root:
env GRAPHWAN_TEST_VM=1 sh /root/graphwan-freebsd/run.sh
# Copy its logs directory back to the host, then:
python3 scripts/check.py verify-freebsd --logs /tmp/graphwan-freebsd/logs
```

The generated runner refuses pre-existing `127.0.0.2`/`127.0.0.3` fixtures,
adds them for the socket tests, and removes only its own aliases on exit. Native
test packages independently clean up their interfaces and routes. Always use
a disposable guest; the opt-in flag does not itself provide isolation.

## Cross-builds

```sh
python3 scripts/cross-build.py --output /tmp/graphwan-builds
python3 scripts/cross-build.py --goos windows --output /tmp/windows-builds
python3 scripts/cross-build.py --target linux/amd64 --output /tmp/linux-build
```

With Go 1.26.8, `go tool dist list` advertises 33 targets in this selection:

| System | Architectures |
| --- | --- |
| Linux | 386, amd64, arm, arm64, loong64, mips, mipsle, mips64, mips64le, ppc64, ppc64le, riscv64, s390x |
| Windows | 386, amd64, arm64 |
| macOS | amd64, arm64 |
| FreeBSD | 386, amd64, arm, arm64 |
| OpenBSD | 386, amd64, arm, arm64, ppc64, riscv64 |
| NetBSD | 386, amd64, arm, arm64 |
| DragonFly BSD | amd64 |

The script uses `CGO_ENABLED=0`, `-trimpath`, explicit CPU baselines and a Git
revision version. A dirty checkout adds `-dirty`; build from a clean checkout
for reproducible revision artifacts. Keep the output outside that checkout.
`manifest.json` records the full revision, worktree state, Go version, baselines,
binary paths, sizes and SHA-256 hashes. A manifest is written only after all
selected builds succeed; failed builds remove any prior manifest at that output.

For a reproducibility comparison, build the same committed revision from two
clean checkouts in different absolute paths and compare the binaries and
manifests. Keep output directories outside both checkouts. For example:

```sh
work=$(mktemp -d)
git clone --no-hardlinks . "$work/first"
git clone --no-hardlinks . "$work/second"
python3 "$work/first/scripts/cross-build.py" --target linux/amd64 --output "$work/build-first"
python3 "$work/second/scripts/cross-build.py" --target linux/amd64 --output "$work/build-second"
cmp "$work/build-first/linux-amd64/graphwan" "$work/build-second/linux-amd64/graphwan"
cmp "$work/build-first/manifest.json" "$work/build-second/manifest.json"
```

This comparison passes for `linux/amd64` at revision
`29ed0f66cd2135fe2e684139dbc14a8586be76ea` with Go 1.26.8 on Linux amd64.
One checkout path contains spaces; its environment also sets conflicting
`GOAMD64`, `GOARM` and `GOFLAGS`, which the build script overrides. Both clean
checkouts produce a 17,371,298-byte binary with SHA-256
`4847a801d6fd16cd196f53e5679e0f8e6c11748cb228c3de45166e163415663c`
and identical manifests. This is evidence for that target/toolchain/revision;
the other 32 targets have successful build/hash verification, not repeated-build
or cross-host reproducibility evidence.

These are build-verification binaries, not complete distribution packages.
Windows Agents still need the [Wintun DLL](windows-operation.md). Successful
cross-compilation does not establish native execution or TUN support: the
remaining BSD adapters, native Windows/macOS acceptance and FreeBSD i386 runtime
failure are tracked in the [acceptance checklist](implementation-status.md).

## Evidence and remaining verification

Local runs with the pinned toolchains pass Go race tests/vet, Linux native tests,
frontend build/embedded-asset/browser checks, all thirteen Linux network
scenarios and all 33 cross-build targets. The generated FreeBSD runner passes
all five packages on a disposable 15.1-p3/amd64 VM, with no skipped tests and
verified interface/loopback cleanup. Its pre-existing-alias rejection also passes.
The workflow definition passes actionlint. Hosted Actions execution has not been
observed: this checkout has no
Git remote configured. Defining a job does not claim that it has passed on a
hosted Windows, macOS or FreeBSD runner.
