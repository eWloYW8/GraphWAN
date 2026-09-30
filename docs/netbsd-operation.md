# NetBSD TUN adapter

The adapter supports IPv4/IPv6 packet I/O, live address/prefix/MTU changes,
foreign-route conflict rollback, blocked-read cancellation and marked-interface
recovery after process termination.

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
ownership of that interface. The creation-window review keeps this fail-closed
boundary: a failed index lookup reports the interface name without attempting a
name-only destroy. A marker-write failure cleans up only with the observed index;
after marking, cleanup requires the matching token/index description as well.
NetBSD's empty-description `ENOMSG` result is accepted only when
reading, never as a successful marker write.

The [NetBSD 11 driver source](https://github.com/NetBSD/src/blob/netbsd-11/sys/net/if_tun.c)
(revision 1.177) confirms that device opening can adopt an existing idle unit;
closing a descriptor does not destroy a normally cloned interface. Switching to
a name-existence check followed by `open` would remove the exclusive-create
protection without solving ownership recovery. The adapter retains `SIOCIFCREATE`
and the marker registry. Privileged concurrent interface/route edits are not
serialized with GraphWAN's transactions. This review and cross-compilation do not
claim new native execution or automatic recovery of unmarked interfaces.

## Physical-interface discovery

Automatic endpoints use the routing interface snapshot's kernel type/name and
`SIOCIFGCLONERS`, shared with OpenBSD. The classifier accepts supported hardware
link types and rejects every registered clone driver, including Ethernet-shaped
software interfaces. It does not infer hardware from a MAC address or editable
description. Interface names and indices must match the collected snapshot;
a stale identity fails discovery rather than publishing a partial result.
Automatic endpoints remain TCP/UDP only.
