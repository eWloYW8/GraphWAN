# macOS utun adapter

The macOS adapter is implemented and cross-builds for `darwin/amd64` and
`darwin/arm64`. Native macOS execution has not been verified in the current
development environment.

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

For startup at boot and background operation, use the built-in
[service commands](service-management.md), which install a system LaunchDaemon.

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
Apple's implementation detaches asynchronously. The API and lifetime design
follow Apple's [utun control definitions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/net/if_utun.h),
[utun implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/net/if_utun.c)
and [route utility documentation](https://github.com/apple-oss-distributions/network_cmds/blob/main/route.tproj/route.8).

## Physical-interface discovery

Automatic discovery reads XNU's `SIOCGIFTYPE` and `SIOCGIFEFLAGS` metadata through
a close-on-exec socket. It accepts Ethernet/Wi-Fi, FireWire and cellular families
with hardware transport subfamilies, without requiring an Ethernet MAC address.
It excludes kernel clones (including fake Ethernet), AWDL, VMNET, simulated
cellular, VLAN, bonds, utun and other tunnel families. It also excludes the
canonical legacy `tapN`/`tunN` driver names because older third-party TAP drivers
register as ordinary Ethernet without using XNU's clone framework. System
Settings' editable network-service labels do not determine classification.

The concrete family is used instead of functional type: the latter can report a
tunnel's delegated physical transport. Discovery checks that the interface name
still maps to the original index after querying metadata and rejects incomplete
snapshots on errors. It does not fall back to advertising every MAC-bearing
interface if an ioctl is unavailable. Unknown families/subfamilies are excluded;
an arbitrary third-party kernel driver that presents itself as standard hardware
is not distinguishable from hardware through these ioctls.

The source reference is XNU revision
`f6217f891ac0bb64f3d375211650a4c1ff8ca1ea`:
[interface metadata and flags](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/net/if_private.h),
[ioctl definitions](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/sys/sockio_private.h) and
[kernel query/clone handling](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/net/if.c).
IPv6 link-local scope mapping is implemented in the shared Mesh; see
[scope identities and policy](endpoint-resolution.md#ipv6-link-local-scopes).
