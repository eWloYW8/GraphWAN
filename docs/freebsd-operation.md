# FreeBSD operation

The FreeBSD Agent creates kernel TUN interfaces, configures IPv4 or IPv6 addresses,
MTUs and connected subnet routes, and carries IP packets through the same Agent
runtime as Linux.

## Run

Build `go build -o graphwan ./cmd/graphwan` on FreeBSD, or cross-build on another
system with `CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -o graphwan ./cmd/graphwan`.
Core cross-builds pass for FreeBSD amd64, arm64, 386 and arm; only amd64
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
atomic creation-and-ownership API.

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

Up interfaces advertise global-unicast and link-local IPv4/IPv6 addresses,
including private and ULA addresses, as TCP/UDP endpoints. Aliasing a TAP to an Ethernet-looking name,
removing its group, or aliasing an epair to a TUN-looking name does not alter its
classification. Errors reading kernel metadata reject the scan, preserving the
last published snapshot. IPv6 link-local discovery and scope mapping use the
shared Mesh implementation.
