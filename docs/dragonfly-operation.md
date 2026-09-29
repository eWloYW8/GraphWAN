# DragonFly BSD operation

The DragonFly/amd64 Agent implements an exclusive TUN, IPv4/IPv6 packet framing,
subnet routes and live address/prefix/MTU reconciliation. This adapter is reviewed
against kernel source and cross-compiled. Native DragonFly execution is not claimed
or required by the Linux-only runtime acceptance scope.

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
GOOS=dragonfly GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/tunnel ./internal/agent
GOOS=dragonfly GOARCH=amd64 CGO_ENABLED=0 go test -c \
  -o /tmp/graphwan-dragonfly-tunnel.test ./internal/tunnel
python3 scripts/cross-build.py --target dragonfly/amd64 --output /tmp/graphwan-dragonfly-build
```

The platform-independent configuration/route rollback tests run on Linux, as do
the existing Agent and discovery regression tests. The DragonFly test binary is
only compiled, not executed on Linux. Physical-interface discovery still uses
the fallback name/MAC classification; authoritative classification of renamed
software Ethernet interfaces remains an implementation item. This TUN adapter
does not establish completion of that separate discovery feature.
