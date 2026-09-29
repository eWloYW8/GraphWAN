# FreeBSD operation and native verification

The FreeBSD Agent creates kernel TUN interfaces, configures IPv4 or IPv6 addresses,
MTUs and connected subnet routes, and carries IP packets through the same Agent
runtime as Linux. FreeBSD 15.1/amd64 has been tested in a disposable QEMU/KVM VM.
This is native kernel and Agent reconciliation evidence; the Linux three-host
transport/NAT acceptance matrix has not yet been repeated on FreeBSD.

## Run

Build `go build -o graphwan ./cmd/graphwan` on FreeBSD, or cross-build on another
system with `CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -o graphwan ./cmd/graphwan`.
Core cross-builds pass for FreeBSD amd64, arm64, 386, arm and riscv64; only amd64
has native execution evidence. Use the architecture of the target host.
The controller CLI and enrollment flow
are the same as in the [README](../README.md). Run the Agent as root:

```sh
export GRAPHWAN_ENROLLMENT_TOKEN='<one-time-token>'
./graphwan agent --server https://controller.example.com:8443 \
  --ca ./ca.pem --name freebsd-node --data-dir /var/db/graphwan-agent
```

Enrollment needs the token only on the first run. Protect the data directory and
trust the controller CA through a separate trusted channel. Add Network membership
and Edges in the management UI. Allow the configured TCP/UDP peer port through the
host firewall; the default is 24752. `/sbin/ifconfig`, the `tun` kernel driver and
access to the corresponding `/dev/tun*` devices are required. Restricted jails
need additional host configuration and are not covered by this verification.

## Device ownership and updates

The adapter uses `SIOCIFCREATE` to allocate a new TUN clone, opens its nonblocking
descriptor and assigns a random `gw...` name. A requested name that already exists
fails; no existing TUN is adopted. The descriptor uses the kernel's four-byte
address-family prefix internally, while the Agent receives plain IP packets.
Writes are serialized and packet buffers are bounded by the supported 9000-byte
MTU. The Go network poller interrupts idle reads when the device closes.

FreeBSD 15.1 supports `TUNSTRANSIENT`: closing the last descriptor destroys the
owned interface and its routes, including after a process crash. Earlier kernels
that return `ENOTTY` use explicit interface destruction after close, guarded by
the original interface index. That fallback path is exercised on 15.1; older
releases themselves have not been natively verified. They cannot provide the
same automatic interface destruction after SIGKILL. The driver APIs are defined
in FreeBSD's [TUN header](https://github.com/freebsd/freebsd-src/blob/releng/15.1/sys/net/if_tun.h)
and [driver implementation](https://github.com/freebsd/freebsd-src/blob/releng/15.1/sys/net/if_tuntap.c).

The TUN uses broadcast/multicast mode for subnet routes. IPv6 duplicate-address
detection and automatic link-local configuration are disabled on this interface;
the controller assigns unique overlay addresses. Physical interfaces and global
IPv6 settings are not changed. Configuration commands use absolute executable
paths, separate validated arguments, a five-second timeout and bounded output.

An MTU-only update changes the owned descriptor through a kernel ioctl and keeps
its interface, reader, routes and peer sessions. This avoids creating a second
interface with an IPv6 address already assigned to the first. If another Network
fails during the update, previously changed MTUs are restored. If restoration
itself fails, the runtime reports the device failure and recreates it from the
last applied snapshot. Address changes to a new IP prepare a replacement device;
IPv4 and IPv6 route handoff have native test coverage. Subnet-prefix changes that
retain the exact IPv6 host address still need an in-place address migration and
are an open acceptance item.

## Reproduce the native checks

Run these integration tests only as root in a disposable FreeBSD VM. They create
interfaces and routes for test prefixes. The environment flag is explicit opt-in;
it cannot prove VM isolation on behalf of the caller.

```sh
go test -c -tags integration -o tunnel.test ./internal/tunnel
go test -c -tags integration -o agent.test ./internal/agent
env GRAPHWAN_TEST_VM=1 ./tunnel.test -test.v -test.timeout=30s
env GRAPHWAN_TEST_VM=1 ./agent.test -test.v -test.timeout=180s
```

The binaries can also be cross-compiled with `GOOS=freebsd GOARCH=amd64` and copied
to the VM. The adapter tests check both IP families at MTUs 1280 and 9000, exact
connected routes, full-MTU kernel UDP traffic in both directions, exclusive
ownership, cancellation of blocked reads, repeated close, MTU edits, replacement
addresses, explicit cleanup and SIGKILL cleanup on supported kernels. The Agent
test applies two Networks through the production TUN factory, increases and
decreases their MTUs, verifies retained interface/runtime ownership and checks
shutdown cleanup. The complete Agent package also passes in the FreeBSD VM;
its other tests include controller/cache and encrypted TCP/UDP forwarding with
in-memory tunnel fixtures.

Linux race tests cover multi-Network MTU success, rollback, rollback failure and
recovery of the last applied MTU. Native FreeBSD tests above are not race-enabled.
Full FreeBSD multi-host forwarding, NAT behavior, discovery changes, older kernel
releases and remaining address-reconfiguration cases remain open in the
[acceptance tracker](implementation-status.md).
