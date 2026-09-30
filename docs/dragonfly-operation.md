# DragonFly BSD operation

The DragonFly/amd64 Agent implements an exclusive TUN, IPv4/IPv6 packet framing,
subnet routes and live address/prefix/MTU reconciliation. This adapter is reviewed
against kernel source and cross-compiled. Native DragonFly execution is not claimed.

## Requirements and lifecycle

Run the Agent as root with the `if_tun` module and `/dev/tun` available. If the
driver is not built into the kernel, load it with `kldload if_tun`. The adapter
uses the system `/sbin/ifconfig` and `/sbin/route` utilities with individual
arguments and bounded command deadlines. Use the common enrollment instructions
in [deployment](deployment.md); the controller can run without TUN privileges.

Opening `/dev/tun` allocates a fresh `tunN` device rather than adopting a numbered
interface. DragonFly's autoclone path destroys this interface and removes its
addresses/routes when the descriptor closes, including process termination.
The Agent preserves the kernel name because the driver's close path uses the
canonical driver/unit name for destruction. The internal TUN API requires an
empty `Config.Name` on initial creation; the Agent already uses that convention.
Live updates preserve the allocated name. Do not externally rename an owned
TUN: the kernel's automatic destruction path depends on its original name.

The descriptor is nonblocking and close-on-exec. Each packet carries a four-byte
network-order address-family header, allowing IPv4 and IPv6 on the same adapter.
Supported configured MTUs are 1280–9000. Before assigning overlay addresses, the
adapter disables DAD, acceptance of router advertisements and automatic link-local
address creation on its own interface through the ND6 flags ioctl. Host-wide
IPv6 settings are unchanged; address uniqueness comes from the controller.

Live updates first verify the name returned by the owned descriptor and the
interface index. MTU operations use the interface ioctl after checking ownership:
DragonFly's descriptor-only MTU ioctl would leave the IPv6 ND6 maximum stale.
The interface ioctl updates both values without changing driver type or baud rate.
Address and subnet-route changes use the shared transactional
reconciliation code: a failed update restores the previous address, MTU and route,
or reports the device unavailable if restoration fails. Exact-prefix routes on
other interfaces cause an error and are not deliberately removed. Subnet routes
are checked after configuration rather than assuming `ifconfig` installed them.

As with other native adapters, a privileged administrator can race name-based
network operations. Stop the Agent before manually changing its interfaces.
There is no startup ownership journal for autoclones: the descriptor controls
their normal lifetime. This does not claim recovery from kernel bugs or external
renaming that prevents the kernel's close path from finding the interface.

## Source and build evidence

The implementation uses DragonFly source revision
`d1f4fb943c73e2b61ddabff6aa48ce7551db72f4`:

- [TUN driver](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/tun/if_tun.c): autoclone, close/destroy, nonblocking I/O and framing.
- [TUN ioctl definitions](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/tun/if_tun.h): eight-byte `tuninfo`; `TUNGIFNAME` is command 98, unlike FreeBSD.
- [Interface ABI](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/if.h): 32-byte `ifreq` on amd64.
- [Interface ioctl handling](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/if.c): `SIOCSIFMTU` refreshes ND6 state after changing the device MTU.
- [ND6 structures](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/netinet6/nd6.h) and [IPv6 ioctl definitions](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/netinet6/in6_var.h): 72-byte `in6_ndireq`, ND flags and ioctl sizes.

Reproduce compile-time checks from Linux:

```sh
GOOS=dragonfly GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/tunnel ./internal/agent ./internal/discovery
python3 scripts/cross-build.py --target dragonfly/amd64 --output /tmp/graphwan-dragonfly-build
```

## Physical-interface discovery

Discovery uses the routing interface snapshot and read-only driver queries.
Interface names and groups do not determine whether an interface qualifies.
The snapshot must report a supported hardware link type and a six-byte link-layer
address. The driver must successfully answer `SIOCGHWADDR` with that same address,
and must reject the VLAN, bridge and aggregation queries described below.
Guest virtio/VMware/ENA NICs and Wi-Fi VAPs use the ordinary Ethernet contract and
qualify. An interface named `tap0` can qualify if it is an actual renamed NIC;
renaming a TAP to `em0` cannot make it qualify.

The distinction depends on ioctl support, not merely a nonempty MAC address:

| Kernel driver | Read-only behavior used for classification |
| --- | --- |
| Ordinary Ethernet NIC, Wi-Fi VAP, FireWire Ethernet, guest NIC | Supports `SIOCGHWADDR` through the generic Ethernet handler; rejects the three software-driver queries. |
| TAP, Netgraph `eiface`/`fec` | Rejects `SIOCGHWADDR`, including closed TAP devices whose status text is empty. |
| VLAN | Supports `SIOCGETVLAN` (`SIOCGIFGENERIC`), even without a parent. |
| Bridge | Supports `SIOCGDRVSPEC` with `BRDGGCACHE`; the query only reads its cache-size setting. |
| LAGG | Supports `SIOCGLAGGFLAGS`; the query only reads aggregation flags. |

The adapter verifies interface name/index against the routing snapshot, verifies
the queried MAC, then checks name/index/MAC again after the driver probes. Lookup,
permission or I/O errors abort the scan; they do not turn a software interface
into a physical one. Unsupported GET operations are recognized by their driver
error codes. Unknown drivers that lack the expected Ethernet contract are omitted;
an administrator can still advertise a manual TCP/UDP endpoint for such hardware.
Custom kernel drivers are outside this classification contract. As with the other
adapters, discovery is not a security boundary against a privileged administrator
concurrently replacing interfaces.

Review against the pinned revision covers the
[generic Ethernet handler](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/if_ethersubr.c),
[TAP handler](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/tap/if_tap.c),
[VLAN handler](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/vlan/if_vlan.c),
[bridge handler](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/bridge/if_bridge.c),
[LAGG handler](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/lagg/if_lagg.c),
[Netgraph Ethernet](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/netgraph/eiface/ng_eiface.c)
and [Wi-Fi ioctl dispatch](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/netproto/802_11/wlan/ieee80211_ioctl.c).
Compile-time assertions check the 32/40/20-byte request layouts.

The earlier MIB investigation remains relevant:
[`IFDATA_GENERAL`](https://github.com/DragonFlyBSD/DragonFlyBSD/blob/d1f4fb943c73e2b61ddabff6aa48ce7551db72f4/sys/net/if_mib.c)
returns the current interface name; the MIB has no FreeBSD-style
`IFDATA_DRIVERNAME` query. Clone creation adds a driver-named group, but the
interface ioctls also allow administrators to add and remove groups. Neither a
name prefix nor group membership proves the original driver after those changes.
The driver-query implementation therefore does not use either as its classifier.
