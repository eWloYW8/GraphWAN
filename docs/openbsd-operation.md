# OpenBSD TUN adapter

The adapter supports IPv4/IPv6 packet I/O, live address/prefix/MTU changes,
route-conflict rollback and owned-interface cleanup. The creation-window
ownership boundary is documented below.

## Build and run

Build with Go 1.26 or newer on OpenBSD, or cross-build using the pinned toolchain:

```sh
CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 go build -o graphwan ./cmd/graphwan
```

Production binaries cross-build for OpenBSD amd64, 386,
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
routing table before changing host state. Wildcard peer listeners bind separate
IPv4 and IPv6 sockets on the same port; native UDP, STUN and QUIC share the data
socket for each family. This avoids relying on IPv4-mapped IPv6 socket support.

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

## Physical-interface discovery

Automatic endpoints use routing-interface metadata and the kernel cloner list,
queried read-only through `SIOCIFGCLONERS`. The driver portion of the kernel
interface name is checked against this list. Descriptions and interface groups
are editable and never determine physical eligibility. OpenBSD's
[kernel interface code](https://github.com/openbsd/src/blob/master/sys/net/if.c)
defines the cloner enumeration and driver/unit naming used by this check.
A changed interface identity rejects the scan instead of publishing a partial
snapshot. Global-unicast addresses, including private IPv4 and ULA IPv6, are
advertised only as TCP/UDP endpoints; down and loopback interfaces are excluded.

## Recovery after process termination

Before applying any cached configuration, the native Agent factory recovers
orphaned marked interfaces, even if no Networks remain in its new configuration.
Opening a TUN also runs recovery. Each device holds an exclusive advisory lock
on a root-owned, mode-0600 record under `/var/run/graphwan-tun` (mode 0700).
The record's filename is a random 128-bit token and has no mutable payload;
its file and parent directory are synced before kernel creation. A separate
registry lock serializes publication and recovery with bounded waiting.

The interface description contains the token and the original kernel interface
index. Recovery requires an unlocked record and an exact marker/index match.
A living Agent keeps its record locked; a same-name replacement with a different
index, an unmarked interface or an altered description is preserved. Changing
the description revokes GraphWAN ownership and prevents further reconfiguration
or automatic destruction. Do not edit it during normal operation.

Normal close destroys the owned interface and removes its record. Failed cleanup
retains the record for retry. Recovery also removes the token-derived private
`/dev/graphwan-TOKEN/tun` node/directory if interrupted during opening; it never
changes public `/dev/tunN` nodes. Registry opens reject symlinks, hard-linked or
non-private records and non-root ownership. The empty registry and `.lock` file
remain for reuse; do not delete records or the registry lock while Agents run.
Privileged administrators must coordinate with the Agent;
these checks do not lock out concurrent root changes to the kernel.

## Creation-window review and platform limits

- **Interruption before ownership marking:** interfaces created through
  `SIOCIFCREATE` survive descriptor close ([tun(4)](https://man.openbsd.org/tun.4)).
  GraphWAN recovers marked
  interfaces after SIGKILL, but interface creation and description assignment
  are separate kernel operations. A kill between those operations can leave an
  unmarked, unconfigured TUN. It has no GraphWAN address or subnet route, so it
  does not block cached-network restart, but its ownership cannot be proven.
  It is deliberately left untouched. An ordinary index-lookup failure now also
  leaves the interface untouched and reports its name; it never falls back to
  an unchecked name-only destroy. A marker-write failure can clean up using the
  known name/index, and all later failure paths require the matching marker too.
  Older GraphWAN versions also created
  unmarked TUNs. After independently confirming ownership and stopping the Agent,
  use `ifconfig tunN destroy` for the specific residual interface. Never delete
  other applications' TUNs.
- Direct device opening is not a substitute for exclusive creation. Although
  OpenBSD destroys interfaces that a device open created, opening an existing
  idle unit can adopt that interface. The driver's `tun_dev_open` path does not
  provide an exclusive-create flag for this distinction. GraphWAN retains
  `SIOCIFCREATE` to refuse existing interfaces, followed by ownership marking.
  Source review used [OpenBSD if_tun.c](https://github.com/openbsd/src/blob/master/sys/net/if_tun.c)
  revision 1.258, including `tun_create`, `tun_dev_open` and `tun_dev_close`.
- Nonzero OpenBSD routing domains are an explicitly rejected runtime configuration.
