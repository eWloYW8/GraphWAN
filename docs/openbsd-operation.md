# OpenBSD TUN adapter and verification

The OpenBSD TUN adapter has native kernel and Agent configuration evidence on
OpenBSD 7.9/amd64. It supports IPv4/IPv6 packet I/O, live address/prefix/MTU changes,
route-conflict rollback and owned-interface cleanup. **Complete OpenBSD networking
is not accepted yet:** native transport tests expose a default wildcard-listener
limitation that prevents IPv6 underlay connections. Multi-host/NAT and crash
recovery also remain open. See the [acceptance tracker](implementation-status.md).

## Build and run

Build with Go 1.26 or newer on OpenBSD, or cross-build using the pinned toolchain:

```sh
CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 go build -o graphwan ./cmd/graphwan
```

Production, TUN-test and Agent-test binaries cross-build for OpenBSD amd64, 386,
arm, arm64, ppc64 and riscv64; only amd64 has native execution evidence.
The controller's setup and enrollment flow are the same as in the
[README](../README.md). An Agent requires root, writable `/dev`, the standard
`/dev/tun0` character device, `/sbin/ifconfig` and `/sbin/route`:

```sh
export GRAPHWAN_ENROLLMENT_TOKEN='<one-time-token>'
./graphwan agent --server https://controller.example.com:8443 \
  --ca ./ca.pem --name openbsd-node --data-dir /var/db/graphwan-agent
```

Use routing table/domain 0. Creating interfaces and reading routes in a different
routing domain are not implemented; the adapter rejects a nonzero process
routing table before changing host state. The default Agent peer listener
currently supports IPv4 underlay on OpenBSD; IPv6 overlay packet I/O is a separate,
verified TUN capability and does not establish IPv6 underlay connectivity.

## Device ownership and configuration

Creation uses `SIOCIFCREATE`, which fails if the requested interface exists.
The Agent chooses an unused random `tunN`; explicit library names must be
canonical `tun0` through `tun65535`. OpenBSD does not support arbitrary interface
renaming. The adapter never opens or adopts an existing idle interface.

Only a small number of `/dev/tunN` nodes exist by default. The adapter reads the
driver major number from `/dev/tun0` and creates a mode-0600 character device in
a private mode-0700 directory under `/dev`. It opens that device with nonblocking,
close-on-exec and no-follow flags, then removes its private node and directory.
Existing public device files remain unchanged. This allows many Networks without
requiring the administrator to pre-create hundreds of device nodes.

The [OpenBSD TUN ABI](https://man.openbsd.org/tun.4) prefixes packets with a
four-byte network-order address family. The adapter presents plain IP packets to
the Agent, supports MTUs 1280–9000, serializes writes, and interrupts blocked reads
on close. MTU changes use the owned descriptor's `TUNGIFINFO`/`TUNSIFINFO` rather
than a mutable or reused interface name. Updates verify descriptor validity and
the recorded interface index before invoking address commands.

IPv6 addresses undergo OpenBSD duplicate-address detection. Configuration waits
up to five seconds for a local UDP bind to succeed before treating an address
as usable. The probe sends no packets; it does not disable host-wide DAD. Errors
remain part of the configuration transaction and cause restoration or device
retirement if restoration fails.

Point-to-point subnet routes are reconciled with the addresses. Route commands
explicitly use `-link -iface`, as described by [route(8)](https://man.openbsd.org/route.8),
so interface names are never interpreted as DNS hostnames. Existing unscoped
equal-prefix routes on another interface cause a conflict, including when an
owned route appears first in the kernel's route list. Failed updates restore the
previous addresses, MTU and routes. Multi-Network Agent updates roll back earlier
successful edits when a later Network fails.

Normal close and failed creation destroy only the interface whose name and index
still match the allocation. An externally destroyed interface or a same-name
replacement is not adopted or removed. Commands have bounded runtime and output.
Other privileged administrators can still race configuration operations;
GraphWAN does not lock the kernel's interface/routing configuration globally.

## Native tests

Run only as root in a disposable OpenBSD VM; these tests create addresses and
routes. With Python and Go installed in the guest:

```sh
env GRAPHWAN_TEST_VM=1 python3 scripts/check.py native --logs /tmp/graphwan-native
```

The script requires named TUN and Agent tests and rejects skipped subtests. A
cross-compiled test bundle avoids needing either toolchain inside the guest:

```sh
CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 go test -c -tags integration -o tunnel.test ./internal/tunnel
CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 go test -c -tags integration -o agent.test ./internal/agent
# Copy the binaries into the disposable guest and run there:
env GRAPHWAN_TEST_VM=1 ./tunnel.test -test.v -test.run NativeOpenBSD -test.timeout=180s
env GRAPHWAN_TEST_VM=1 ./agent.test -test.v -test.run NativeBSDConfigurationReconcile -test.timeout=180s
```

Tests cover both IP families at MTUs 1280 and 9000, exact subnet routes,
bidirectional full-MTU kernel UDP packets, duplicate-name rejection, blocked-read
cancellation, repeated close and cleanup. Seven migration cases cover narrowing
and widening prefixes, changing IPv6 addresses, and IPv4/IPv6 transitions while
retaining the interface index. Conflict tests verify preservation of another
interface's routes and continued traffic on the restored configuration.
A replacement test revokes an open descriptor, recreates its name and verifies
that update/close of the old device cannot alter the replacement.

Native TUN tests pass repeatedly on the disposable OpenBSD 7.9/amd64 VM. The
Agent's real factory passes multi-Network configuration migration, rollback and
shutdown cleanup. Linux race tests and vet pass; the shared routing tests reject
foreign equal-prefix routes independently of enumeration order. Both Darwin
architectures also cross-build after extracting the common BSD route reader.
These are kernel/component checks, not three-host forwarding acceptance.

## Known incomplete behavior

- **Default IPv6 underlay listening:** OpenBSD does not provide the IPv4-mapped
  dual-stack socket behavior assumed by the current wildcard listeners. Native
  `TestUDPWildcardReplySource` passes its IPv4 cases but fails its IPv6 cases;
  `TestDNSIPv6AndPunchMethods` and `TestPolicyEditsPreserveUnaffectedLinks` fail
  their IPv6 connection checks across all six transports. Those failures remain
  visible; tests have not been skipped or weakened. Production needs coordinated
  IPv4/IPv6 listeners on the same configured port, including STUN and QUIC sharing.
- **SIGKILL recovery:** an atomically cloned OpenBSD TUN remains allocated after
  its descriptor closes, unlike a TUN created implicitly by opening `/dev/tunN`.
  The latter can adopt an existing interface and is not used. Normal shutdown
  cleans up, but a killed process can leave its owned interface/routes behind;
  automatic verified cleanup on restart is not implemented. After confirming
  ownership and stopping the Agent, use `ifconfig tunN destroy` to remove that
  specific residual interface. Never delete other applications' TUNs.
- Native physical-interface classification, scoped link-local endpoints,
  multi-host forwarding/NAT, nonzero routing domains, other OpenBSD releases and
  other CPU architectures still need implementation or acceptance evidence.

To reproduce the socket failures, compile `./internal/transport` and
`./internal/mesh` with `go test -c` for OpenBSD, copy both test binaries to the
disposable VM, and add the reserved `127.0.0.2/32` and `127.0.0.3/32` aliases to
`lo0` as in the [BSD socket fixture procedure](freebsd-operation.md#native-transport-checks).
Run `transport.test -test.v -test.run TestUDPWildcardReplySource` and
`mesh.test -test.v -test.timeout=180s`, retaining their nonzero statuses and
removing the fixture aliases afterwards. The local run completed the full Mesh
package in about 141 seconds; its two failed roots were the DNS IPv6/punch and
live policy-update tests named above. No timeout or skip is counted as a pass.

The VM used the official OpenBSD 7.9 amd64 `install79.iso`, verified against the
release's [SHA-256 file](https://cdn.openbsd.org/pub/OpenBSD/7.9/amd64/SHA256):
`7a4a92e953618035097c796a90b54424a0f3ae775552e1e7d102cf8a5130449f`.
Its kernel reports `OpenBSD 7.9 GENERIC.MP#449 amd64`. No hosted OpenBSD CI
execution or complete platform acceptance is claimed.
