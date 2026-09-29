# NetBSD TUN adapter and verification

The adapter has native TUN evidence on NetBSD 11.0/amd64: IPv4/IPv6 full-MTU
packet I/O, live address/prefix/MTU changes, foreign-route conflict rollback,
blocked-read cancellation and marked-interface recovery after SIGKILL.
Multi-Network Agent migration, rollback and shutdown also pass.
Complete NetBSD acceptance remains open; see the
[acceptance tracker](implementation-status.md).

## Requirements and build

An Agent requires root, `/dev/tun0`, writable `/dev` and `/var/run`, and the base
`/sbin/ifconfig` and `/sbin/route` utilities. Build with the pinned Go toolchain:

```sh
CGO_ENABLED=0 GOOS=netbsd GOARCH=amd64 go build -o graphwan ./cmd/graphwan
```

The controller setup and enrollment flow are described in the [README](../README.md).
NetBSD amd64, 386, arm and arm64 build successfully; only amd64 has native evidence.

**Set Network MTU to 1280–1500 on NetBSD.** The
[NetBSD 11 TUN driver](https://github.com/NetBSD/src/blob/netbsd-11/sys/net/if_tun.c)
limits both the interface MTU and injected packets to 1500. Larger configured
values are rejected before changing kernel state; GraphWAN does not silently
clamp the controller's desired MTU. Other supported adapters retain their wider
MTU range.

## Configuration and lifetime

Creation reserves a fresh canonical `tunN` with `SIOCIFCREATE`. Existing idle
interfaces are never adopted. A root-private temporary character device uses the
major number from `/dev/tun0`; the Agent removes its private device path after
opening it. Existing public device nodes are unchanged. The
[TUN multi-family ABI](https://man.netbsd.org/tun.4) carries a four-byte,
network-order address-family header; GraphWAN exposes ordinary IP packets.
The adapter explicitly enables driver nonblocking mode with `FIONBIO` in addition
to `O_NONBLOCK`; otherwise a concurrent reader can block in the kernel and prevent
Agent shutdown even though the descriptor is registered with Go's poller.

IPv6 configuration checks `SIOCGIFAFLAG_IN6` until tentative/detached flags clear,
with a five-second bound and immediate failure on a duplicate address. A local
UDP bind alone is insufficient to establish readiness on NetBSD. The adapter
leaves host-wide duplicate-address detection enabled. Address/MTU changes and
explicit subnet routes are transactional; foreign equal-prefix routes cause
rollback. Route commands use `-link -iface` so interface names are never resolved
as hostnames.

NetBSD leaves cloned TUN interfaces behind when the descriptor closes. GraphWAN
uses the same root-private, flock-held immutable token registry as OpenBSD at
`/var/run/graphwan-tun`. Before creating interfaces, and when the Agent starts
with no Networks, recovery removes orphaned interfaces only when their kernel
description matches an unlocked record's token and interface index. Normal
shutdown closes the descriptor and removes the owned interface and record.
Recovery preserves live owners, unmarked replacements, and interfaces whose
ownership description has been changed. Invalid permissions, hardlinks and
symlinks in ownership records are rejected.

There are two NetBSD-specific ownership boundaries:

- NetBSD immediately reuses free interface indices. A privileged administrator
  who destroys an interface and copies its complete GraphWAN marker onto a
  same-name, same-index replacement makes that replacement indistinguishable
  to recovery. Do not copy GraphWAN ownership descriptions to unrelated devices.
- The kernel can recycle an externally destroyed, still-open TUN's storage when
  the same unit is recreated. Closing the old descriptor can then bring the
  replacement down. Stop the Agent before replacing its TUN externally; a
  userspace name/index check cannot revoke the kernel's descriptor association.

As on OpenBSD, interruption between atomic creation and publishing the ownership
marker can leave an unmarked, unconfigured TUN. Automatic recovery cannot prove
ownership of that interface. Privileged concurrent interface/route edits are not
serialized with GraphWAN's transactions.

## Native reproduction

Run only in a disposable root NetBSD guest with explicit opt-in. With Go and Python in the guest:

```sh
GRAPHWAN_TEST_VM=1 python3 scripts/check.py native --logs /tmp/graphwan-native
```

The gate requires named TUN and Agent tests and rejects skipped subtests.
Cross-compiling requires no Go installation in the guest:

```sh
CGO_ENABLED=0 GOOS=netbsd GOARCH=amd64 go test -c -tags integration -o tunnel.test ./internal/tunnel
CGO_ENABLED=0 GOOS=netbsd GOARCH=amd64 go test -c -tags integration -o agent.test ./internal/agent
# Copy the binaries to the guest, then run there:
env GRAPHWAN_TEST_VM=1 ./tunnel.test -test.v -test.timeout=300s
env GRAPHWAN_TEST_VM=1 ./agent.test -test.v -test.run NativeBSDConfigurationReconcile -test.timeout=180s
```

The native ABI probe compiled against NetBSD 11 headers confirms `ifreq=144`,
`in6_ifreq=288`, and description-pointer offset 24 on amd64. TUN tests exercise
both families at MTUs 1280 and 1500, seven address/prefix/family migrations,
route conflicts, rejected oversized MTU updates, descriptor revocation,
existing-device rejection, SIGKILL recovery, recovery without opening another
TUN, record protection and concurrent close/recovery. These are kernel/component
tests, not multi-host/NAT or other-architecture execution evidence.

The verification guest uses the official NetBSD 11.0 amd64 live image,
`NetBSD-11.0-amd64-live.img.gz`, checked against its published SHA-512:

```text
ad0bc2786b2cd8b7c140ae108fd780064465fe06337a3a2c3f3b694ed3a1e042b3d401c9a682be02c081f3eb3efeba1246cfd6a8803149468ebfbd9c0e7c77e8
```

A disposable qcow2 overlay was expanded to 8 GiB and booted with virtio networking
and a virtio random source. The guest entropy requirement was zero before SSH
host keys were regenerated. No host-network interfaces or routes were modified.
