# macOS utun adapter

The macOS adapter is implemented and cross-builds for `darwin/amd64` and
`darwin/arm64`. Its route/configuration algorithms pass Linux race-enabled tests.
**Native macOS execution has not been verified in the current development
environment.** The native test binaries compile, but their checks must be run on
a Mac before treating macOS networking as accepted. The overall platform acceptance
item remains open in the [implementation tracker](implementation-status.md).

## Build and run

Build on a Mac with Go 1.26 or newer:

```sh
go build -o graphwan ./cmd/graphwan
```

For a cross-build, set `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64`; use `amd64` for
Intel Macs. The embedded UI does not need a frontend toolchain unless rebuilding
its assets. Use the normal controller and enrollment flow from the
[README](../README.md). Creating and configuring a utun requires root. In a root
shell on the Mac:

```sh
export GRAPHWAN_ENROLLMENT_TOKEN='<one-time-token>'
./graphwan agent --server https://controller.example.com:8443 \
  --ca ./ca.pem --name mac-node --data-dir /var/db/graphwan-agent
```

The token is needed only for initial enrollment. Add Network memberships and
Edges in the management UI. Peer traffic uses the configured port, default 24752.
The adapter uses the built-in kernel control socket and the system utilities
`/sbin/ifconfig` and `/sbin/route`; it does not require a third-party TUN extension.

## Interface, routes and configuration

An empty requested interface name asks the kernel to allocate an unused `utunN`.
The native factory also accepts explicit canonical names such as `utun12`; an
already occupied unit fails rather than attaching to an existing device. The
Agent always uses automatic allocation. The adapter sets close-on-exec and
nonblocking mode, handles the four-byte address-family packet header, and shares
the bounded packet buffering and serialized writes used by the FreeBSD adapter.

The adapter configures the assigned address and MTU on the owned utun and disables
IPv6 DAD only on that interface. Kernel link-local IPv6 addresses are independent
of the configured overlay address. A point-to-point IPv4 address uses the local
address as its peer. For multi-address subnets, the adapter ensures an unscoped
subnet route through that utun. Single-address networks use the kernel's local
delivery route.

Route handling reads the kernel routing table and matches the exact prefix,
interface index, scope and usable flags. An existing unscoped route on another
interface causes a configuration error; it is not replaced or deleted. Scoped
routes on other interfaces do not satisfy an unscoped subnet route. Route removal
rechecks interface ownership before issuing the delete command. GraphWAN does not
coordinate route edits with other privileged administrators making simultaneous
changes to the same routing-table entry.

Address, prefix and MTU edits retain the utun descriptor and packet reader. Address
changes and explicit subnet routes are reconciled together. If any operation
fails, the old address, MTU and route are restored; a failed restoration reports
an unavailable device, allowing the Agent to retire it and recover its last
applied snapshot. A command reporting failure after installing/removing a route
is resolved by reading the actual routing table. All command arguments are
separate validated values, and command duration/output are bounded.

Closing the owned control socket is the kernel mechanism for detaching utun and
its associated addresses/routes; cleanup does not issue global route deletes.
Apple's implementation detaches asynchronously. Native tests wait for interface
removal and need to verify this behavior on the target macOS release. The API and
lifetime design follow Apple's [utun control definitions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/net/if_utun.h),
[utun implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/net/if_utun.c)
and [route utility documentation](https://github.com/apple-oss-distributions/network_cmds/blob/main/route.tproj/route.8).

## Native verification procedure

Use a disposable Mac or macOS VM. These tests require root and install temporary
test addresses/routes on their owned interfaces. The environment flag is an
explicit opt-in; it does not prove that the host is isolated.

```sh
go test -c -tags integration -o tunnel.test ./internal/tunnel
go test -c -tags integration -o agent.test ./internal/agent
sudo env GRAPHWAN_TEST_MACOS=1 ./tunnel.test -test.v -test.timeout=60s
sudo env GRAPHWAN_TEST_MACOS=1 ./agent.test \
  -test.run=TestNativeBSDConfigurationReconcile -test.v -test.timeout=60s
```

The pending native tests cover IPv4/IPv6 at MTUs 1280 and 9000, connected routes,
full-MTU UDP packets between kernel and adapter, duplicate-name rejection, idle
read interruption and interface cleanup. Seven migration cases check prefix
narrowing/widening, host address changes and IPv4/IPv6 switches. Route-conflict
tests check failed creation/update, old-configuration restoration and preservation
of another utun's route. The actual Agent test checks multiple Networks,
configuration rollback and retention of readers and peer sessions.

Tests already run on Linux cover route ownership, scoped/foreign/reject routes,
command failures before and after mutation, failed route removal, restoration of
a missing old route and failed rollback. These simulations and cross-builds do
not prove macOS kernel behavior. Native socket/packet/route tests, multi-host
transport/NAT tests and process-crash cleanup remain unverified.

## Socket test fixtures

The multi-address DNS, policy-update and UDP reply-source tests need explicit
`127.0.0.2` and `127.0.0.3` loopback aliases on macOS. Unprivileged tests skip
those cases if the aliases are absent. On a disposable test host where both
addresses are reserved for these fixtures, configure them before the full suite:

```sh
sudo ifconfig lo0 inet 127.0.0.2/32 alias
sudo ifconfig lo0 inet 127.0.0.3/32 alias
python3 scripts/check.py go --logs /tmp/graphwan-checks
env GRAPHWAN_TEST_MACOS=1 python3 scripts/check.py native --logs /tmp/graphwan-checks
# Remove only the aliases added for this run, including after a failed test:
sudo ifconfig lo0 inet 127.0.0.2 -alias
sudo ifconfig lo0 inet 127.0.0.3 -alias
```

The [CI workflow](continuous-integration.md) includes these fixtures and cleanup
on both macOS architectures. Hosted execution is still pending; its definition
is not native test evidence.
