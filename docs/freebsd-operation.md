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
The Go 1.26.8 `386` discovery binary and a minimal Go hello program both exit with
SIGSEGV under this FreeBSD 15.1-p3/amd64 VM's i386 compatibility mode, even though
the kernel advertises i386 support. Their cross-builds pass, but this is a known
native execution failure whose cause is not yet established. Use amd64 on this
host; native 32-bit acceptance remains open.
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

Exclusive cloning, interface-index lookup, opening the descriptor and enabling
`TUNSTRANSIENT` are separate operations. An interruption before the last step can
leave an unconfigured clone even on a kernel that supports transient TUNs. If the
initial lookup fails, GraphWAN reports the allocated name and preserves the
interface: deleting by name alone cannot establish ownership after a failed
lookup. Later setup failures use the verified index or the owned transient
descriptor. An administrator may remove an orphan only after confirming it is
unused. Coordinate manual interface changes with the Agent; this is not an
atomic creation-and-ownership API. Linux fault tests exercise the shared index
validation, including failed lookups, absent/wrong names and invalid indices.

The TUN uses broadcast/multicast mode for subnet routes. IPv6 duplicate-address
detection and automatic link-local configuration are disabled on this interface;
the controller assigns unique overlay addresses. Physical interfaces and global
IPv6 settings are not changed. Configuration commands use absolute executable
paths, separate validated arguments, a five-second timeout and bounded output.

Address, subnet-prefix and MTU edits reuse the owned interface, reader and peer
sessions. MTU-only edits use a kernel ioctl. A new host address is added before
removing the previous address; IPv4/IPv6 family changes follow the same sequence.
When the host address stays the same but its prefix changes, the adapter removes
and re-adds that address: FreeBSD's
[IPv6 address implementation](https://github.com/freebsd/freebsd-src/blob/releng/15.1/sys/netinet6/in6.c)
rejects direct prefix edits. This produces a short address/route transition during
the update; it does not close the TUN or replace its blocked reader.

On a failed operation the adapter reads the kernel addresses, restores the old
address/prefix and MTU, and retains the old reported configuration. This also
handles a command failing after it changed kernel state. An error restoring the
old configuration marks the device unavailable, so the Agent retires it and
recovers the last applied snapshot. If a later Network fails during a multi-Network
update, earlier successful updates are rolled back too. The applied revision and
Mesh stay unchanged until every device update succeeds. Interface names cannot
be edited through this operation; name-based address commands first verify the
name against the owned descriptor to reject an externally renamed device.

## Automatic interface discovery

Discovery reads kernel interface types from the routing information base and the
original driver/unit through `net.link.generic.ifdata` / `IFDATA_DRIVERNAME`.
It uses the same read-only source as FreeBSD's
[original-name lookup](https://github.com/freebsd/freebsd-src/blob/releng/15.1/lib/libifconfig/libifconfig.c),
whose [kernel implementation](https://github.com/freebsd/freebsd-src/blob/releng/15.1/sys/net/if_mib.c)
keeps driver identity separate from the editable alias. `SIOCIFGCLONERS` supplies
the registered software-interface classes. Hardware link types qualify; cloned
software interfaces are excluded except Wi-Fi VAPs (`wlan`) and jail underlay
pairs (`epair`). Core tunnel/bridge/VLAN/aggregation drivers and netgraph `ngeth`
are also excluded even without a visible cloner. No MAC-address heuristic or
editable interface group decides eligibility.

Up interfaces advertise global-unicast IPv4/IPv6 addresses, including private and
ULA addresses, as TCP/UDP endpoints. Aliasing a TAP to an Ethernet-looking name,
removing its group, or aliasing an epair to a TUN-looking name does not alter its
classification. Errors reading kernel metadata reject the scan, preserving the
last published snapshot. IPv6 link-local scope mapping is still incomplete.

The native discovery test creates owned epair/TUN/TAP/bridge devices and changes
their aliases and groups. It verifies exclusion, stable unaffected endpoints,
IPv4/IPv6 additions/removals and down/up transitions, then destroys its fixtures.
Ten consecutive runs pass on FreeBSD 15.1-p3/amd64. Portable decision tests run
with the Linux race detector; production, integration tests and discovery vet
pass for all five FreeBSD architectures listed above.

## Reproduce the native checks

Run these integration tests only as root in a disposable FreeBSD VM. They create
interfaces and routes for test prefixes. The environment flag is explicit opt-in;
it cannot prove VM isolation on behalf of the caller.

```sh
go test -c -tags integration -o tunnel.test ./internal/tunnel
go test -c -tags integration -o agent.test ./internal/agent
go test -c -tags integration -o discovery.test ./internal/discovery
env GRAPHWAN_TEST_VM=1 ./tunnel.test -test.v -test.timeout=30s
env GRAPHWAN_TEST_VM=1 ./agent.test -test.v -test.timeout=180s
env GRAPHWAN_TEST_VM=1 ./discovery.test -test.v -test.timeout=60s
```

The binaries can also be cross-compiled with `GOOS=freebsd GOARCH=amd64` and copied
to the VM. The adapter tests check both IP families at MTUs 1280 and 9000, exact
connected routes, full-MTU kernel UDP traffic in both directions, exclusive
ownership, cancellation of blocked reads, repeated close, MTU edits, replacement
addresses, explicit cleanup and SIGKILL cleanup on supported kernels. The Agent
test applies two Networks through the production TUN factory, increases and
decreases their MTUs, changes prefixes/addresses/address families, verifies retained
interface/runtime ownership and checks shutdown cleanup. Seven adapter migration
cases check removal of old addresses/routes and full-MTU bidirectional kernel UDP
traffic after each migration. A real duplicate-IPv6-address error on a second
Network verifies rollback of the first Network's completed prefix/MTU edit.
The complete Agent package also passes in the FreeBSD VM;
its other tests include controller/cache and encrypted TCP/UDP forwarding with
in-memory tunnel fixtures.

Linux race tests cover MTU and full configuration transactions, failures before
and after address operations mutate state, rollback failures, device retirement
and recovery of the last applied configuration. Native FreeBSD tests above are
not race-enabled.
Full FreeBSD multi-host forwarding, NAT behavior, older kernel
releases and the other platform adapters remain open in the
[acceptance tracker](implementation-status.md).

## Native transport checks

BSD requires explicit secondary loopback aliases for the multiple-address tests.
Ordinary Go tests skip those cases with an explanation when aliases are absent;
they never configure host interfaces. In a disposable VM where `127.0.0.2` and
`127.0.0.3` are reserved for these fixtures, run as root:

```sh
go test -c -o transport.test ./internal/transport
go test -c -o mesh.test ./internal/mesh
sh <<'SH'
set -eu
trap 'ifconfig lo0 inet 127.0.0.2 -alias; ifconfig lo0 inet 127.0.0.3 -alias' EXIT
ifconfig lo0 inet 127.0.0.2/32 alias
ifconfig lo0 inet 127.0.0.3/32 alias
./transport.test -test.run TestUDPWildcardReplySource -test.count=5 -test.v
./mesh.test -test.timeout=120s
SH
```

GraphWAN sets a 64 KiB send buffer on its owned BSD UDP socket. FreeBSD's default
9216-byte limit cannot send the protocol's maximum 16 KiB message. This local
socket option supports the message limit without changing global UDP sysctls.
Native tests pass for full-size IPv4/IPv6 request/reply traffic, exact wildcard
reply sources and rejecting oversized datagrams, both standalone and sharing a
QUIC listener. All six transports pass DNS address refresh/retention and policy
edit tests with real sockets. These loopback tests do not establish multi-host or
NAT acceptance. macOS and the other BSD kernels still require native execution.

The [CI build/check script](continuous-integration.md) now creates a self-contained
FreeBSD amd64 test bundle, including Go's `test2json` executable. Its generated
runner and host-side result verification pass on the disposable 15.1-p3 VM:
TUN, Agent, discovery, wildcard UDP and the complete Mesh package execute with
no skipped tests. Required roots and skipped subtests are checked explicitly.
The runner rejects existing loopback fixture addresses; successful execution
removes its aliases and the native tests remove their interfaces. GitHub-hosted
execution of the new FreeBSD job has not yet been observed.
