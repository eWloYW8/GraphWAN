# Endpoint discovery, updates and DNS

An Agent advertises automatic TCP/UDP IP endpoints and administrator-provided
manual URLs. All six transports accept manual hostnames with an explicit port:

```text
udp://edge.example.com:24752
tcp://edge.example.com:24752
quic://edge.example.com:24752
ws://edge.example.com:24752/overlay
wss://edge.example.com:443/overlay
grpc://edge.example.com:443/overlay
```

Configure these on the destination Agent in the management UI. The edge must
enable the chosen transport and connection method. WS/WSS and gRPC are only
generated from manual endpoints; their paths identify the configured HTTP entry.
QUIC also requires a manual endpoint. URLs do not create additional listeners:
the Agent's configured listen port (24752 by default) is shared by its transports.
An external hostname/port can instead route through an administrator-managed
proxy or port forward. See [WebSocket](websocket-operation.md),
[gRPC](grpc-operation.md), [QUIC](quic-operation.md) and [NAT](nat-operation.md).

## Automatic interface discovery

The Agent scans interfaces every five seconds and publishes TCP/UDP endpoints
for global-unicast and link-local IPv4/IPv6 addresses, including private and ULA
addresses. IPv6 link-local URLs carry the owner’s interface scope, as described
[below](#ipv6-link-local-scopes).
Down and loopback interfaces are excluded. Duplicate address URLs collapse to
one endpoint; enumeration order does not change its identity. An address-read
failure rejects the entire scan, leaving the last published snapshot in place
until a complete scan succeeds. Removing an address or taking its interface down
withdraws its endpoints on the next successful scan and controller update.

An Agent has a total allowance of 64 endpoints, shared by manual and discovered
entries. Manual entries reserve their slots first. If discovery exceeds the
remaining allowance, the Agent logs a warning and prioritizes STUN observations,
public interface addresses, private addresses and then link-local addresses.
Stable IDs break ties and determine publication order. On hosts with large address inventories, reserve needed
addresses as manual endpoints within that allowance. Duplicate URLs do not use
extra slots, and expired STUN observations are removed.

Linux reads kernel link types in one netlink dump and accepts physical devices
and container veth interfaces. TUN, TAP, dummy devices and bridges are excluded
regardless of their names. **Agents → Manage → Exclude container IPs** additionally
excludes veth interfaces attached to a bridge or without a unicast default route.
A containerized Agent's unbridged default-route veth remains eligible, preserving
its own uplink. This opt-in setting (`exclude_container_ips`) defaults to false
and is delivered in the cached Agent snapshot. Filtering uses kernel link and
route metadata, not interface-name prefixes or blanket private-IP exclusions;
manual endpoints and STUN observations are preserved. A veth used only with
specific routes is excluded when the option is enabled. Other platforms retain
their existing interface classifiers; this additional filter is Linux-specific.

Windows uses `GetIfTable2Ex` hardware, filter, endpoint
and interface-type metadata instead of adapter aliases or MAC-address presence.
The hardware flag permits Ethernet, Wi-Fi and cellular devices, including guest
NICs reported as hardware; software loopback, virtual, tunnel and bridge types
are excluded. See [Windows operation](windows-operation.md).

FreeBSD combines kernel interface types, original driver identities and registered
cloners. Wi-Fi VAPs and jail epairs qualify; software overlays are excluded even
after alias/group edits. See
[FreeBSD operation](freebsd-operation.md).

OpenBSD reads interface types from the routing interface snapshot and excludes
all drivers listed by `SIOCIFGCLONERS`. It uses kernel driver-assigned names,
not descriptions, editable groups or MAC-address presence. This rejects virtual
Ethernet clones as well as TUN/TAP and bridges; Ethernet, Wi-Fi and OpenBSD's
MBIM cellular type qualify. See
[OpenBSD operation](openbsd-operation.md#physical-interface-discovery).

NetBSD shares the kernel type/cloner classifier with OpenBSD.
Descriptions do not influence classification or endpoint IDs. See
[NetBSD operation](netbsd-operation.md#physical-interface-discovery).

macOS queries kernel interface type, family, subfamily and clone flags. See
[macOS operation](macos-operation.md#physical-interface-discovery).
DragonFly combines routing-interface types with read-only driver queries. TAP and
Netgraph Ethernet devices reject its hardware-address query; separate VLAN,
bridge and LAGG queries exclude those software Ethernet drivers regardless of
name/group edits. See
[DragonFly operation](dragonfly-operation.md#physical-interface-discovery).

## Live listener changes

Set the Agent's `listen_port` through the management UI or
[`PATCH /api/v1/agents/{id}`](control-api.md). The default is 24752; TCP/WS/WSS/gRPC
share its TCP listener and UDP/QUIC share its UDP socket. Wildcard binding covers
both IPv4 and IPv6 where available. A manual URL's port describes the advertised
entry point; it does not create an extra listener or change `listen_port`.

The Agent prepares the new TCP/UDP listeners before replacing its current Mesh.
A bind failure releases every partial reservation and keeps the existing TUN,
Links and forwarding configuration. The desired revision stays cached while the
applied revision stays unchanged, and the Agent retries automatically. The
controller reports the configuration error; Links from an Agent behind the
current desired revision are omitted from its telemetry until it catches up.

After a successful change, automatic TCP/UDP endpoints advertise the new port;
old listener sockets and sessions are closed. The TUN and Agent process remain
in place. Manual URLs and external proxies are administrator-managed: update
their destination/advertised ports when appropriate. Peers reconnect under the
new endpoint policy, so a successful port change may briefly interrupt traffic.
See [Linux listener reconfiguration](linux-operation.md#live-listener-reconfiguration).

## Live policy changes

Adding an endpoint, enabling another transport or enabling another direct-address
family retains existing permitted sessions. Removing an endpoint or disabling a
transport/method closes only affected Links and cancels their pending outgoing
handshakes. Changing a URL under the same endpoint ID replaces that endpoint's
sessions while keeping its stable preference identity. Other healthy Links,
including the reverse dialing direction, retain their session IDs. Network,
Node, Edge, cipher or peer-identity changes still replace the affected group.

Admission checks the current policy again when a handshake completes. Both peers
bind the introduction to the configured endpoint as described below, preventing
a late old handshake from claiming a replacement URL under the same endpoint ID.
Healthy observed/STUN sessions can still renew after their lease disappears;
disabling punching or their transport revokes them. Configuration changes require
the controller; cached policy and healthy links continue during its outage.

## Multiple addresses

Each allowed DNS answer becomes a separate Candidate. Its identity includes the
edge, dialing Node, endpoint, family, method and normalized target address.
Duplicates and IPv4-mapped equivalents collapse to one address; answer ordering
does not change identity. Unspecified, multicast and zone-qualified DNS answers
are discarded. IPv4/IPv6 direct policy is checked before a target can dial;
TCP/UDP punching uses separate candidates under the edge's punch policy.

Every candidate has independent backoff and renewal. Eight Mesh-wide dial slots
bound simultaneous attempts; an unreachable address cannot prevent another
address from connecting. Successful Links are retained. UDP, QUIC and gRPC
release pending admission reservations after the configured Noise handshake;
TCP pool allowances grow with authorized topology and resolved candidates.
One common active Link is selected by both Agents.
Use its Candidate in the edge preference selector to prefer that particular
address. Literal-IP endpoints keep their existing Candidate IDs. Preferences for
older hostname/family-only IDs fall back to automatic selection; select a new
address-specific Candidate to restore an explicit preference.

The Mesh shares hostname lookups across its Networks and Edges. It uses the Go
system resolver, at most four concurrent lookups, a five-second lookup deadline
and a thirty-second refresh interval. This is periodic refresh, not DNS TTL
tracking. Temporary lookup failures retain the last successful answers. Successful
empty answers or name-not-found responses withdraw targets from new probes.
Already healthy authenticated Links remain up and can renew their keys even when
their address is withdrawn. Once such a Link fails, its withdrawn target is no
longer retried. Removing the endpoint or disabling its method closes its Links.
Changing the endpoint URL also replaces that endpoint's runtime policy. Unused
cache entries are removed, and Mesh shutdown cancels and joins pending lookups.

## TLS and peer admission

Only the socket destination changes when selecting a DNS answer. WSS and gRPC
retain the original hostname for SNI and certificate validation, the URL's host
and port for HTTP authority, and its configured path. QUIC retains its original
TLS hostname as well. A TLS frontend with an independent certificate must pass
normal CA/hostname validation. The encrypted peer handshake still authenticates
the configured Agent behind it.

The authenticated Link introduction includes an endpoint fingerprint and, for
DNS candidates, the concrete DNS target. The
receiving Agent reconstructs its Candidate ID against an advertised manual
hostname endpoint, permitted family/method and actual ingress transport/path.
It rejects an inconsistent hash, disabled family or attempt to override a literal
endpoint. Target validation does not require a second DNS lookup: split-horizon DNS
and reverse proxies can make its answer set different from the initiator's.
TCP punch physical-session capacity may independently resolve a local endpoint
hostname to size its per-peer resource allowance; that answer set does not
replace the authenticated introduction's target or serve as an address allowlist.
The target is a path identifier, not evidence of peer identity; identity comes
from the authenticated handshake. The fingerprint is the first 16 bytes of
SHA-256 over endpoint source, NUL, transport, NUL and exact URL, encoded as 32
lowercase hexadecimal characters. Lease expiry is excluded so STUN refresh does
not change admission identity. This field is required for every transport and
literal or DNS endpoint. Agents predating this field cannot connect to the new
implementation; upgrade both ends together. Older hostname/family-only Candidate
IDs are also incompatible with address-specific introductions.

## Wildcard listeners and UDP source addresses

Wildcard TCP and UDP binds (`:port`, `0.0.0.0:port`, `[::]:port`) reserve separate
IPv4 and IPv6 sockets on the same port. Only a kernel-unavailable address family
may be omitted. Other errors close all partial reservations; a port-zero
collision retries at most eight times. Explicit non-wildcard addresses retain
normal single-address binding behavior. Native UDP, STUN and QUIC share the
selected family's socket, peer/admission budgets remain shared across families,
and close interrupts accepts/readers on both sockets.

A wildcard native UDP listener records the destination IP of the first incoming
datagram and uses it as the reply source. This prevents a request to a secondary
local address from receiving its response from the host's default address. The
same behavior applies with or without a shared QUIC listener; socket sharing,
STUN data-port reuse and IP fragmentation are preserved. The implementation uses
`x/net` packet control messages on Unix and native Winsock packet information on
Windows. Truncated datagrams/control data are discarded without stopping the
listener, including Windows `WSAEMSGSIZE`. Owned BSD sockets reserve a 64 KiB
send buffer because the default may be below the protocol's 16 KiB message
limit. See [Windows UDP packet information](windows-operation.md#udp-packet-information).

## IPv6 link-local scopes

Automatic discovery includes IPv6 link-local unicast addresses on eligible
underlay NICs. Each URL records the **owner's** interface, for example
`udp://[fe80::1%25uplink0]:24752`. The percent sign is URL-escaped; the stored
scope belongs to the advertising Agent. It is never used directly as a dialing
node's interface name. The same address on two NICs produces separate endpoints.
As specified by [RFC 4007 §6](https://www.rfc-editor.org/rfc/rfc4007.html#section-6),
zone identifiers are local to their node.

Mesh expands a link-local candidate over the initiator's configured automatic
IPv6 link-local UDP endpoints. These endpoints identify eligible local interfaces
for **all** transports; this does not require enabling UDP on the Edge. A socket
target uses the remote IP with that local interface's zone. The endpoint URL,
HTTP authority and TLS identity remain tied to the advertised endpoint. A DNS
answer containing an unscoped IPv6 link-local address follows the same expansion.
No local scope means no dial; there is no guessed/default interface fallback.

Each scoped candidate has its own bounded retry and Link lifecycle. Define `H`
as the lowercase hex encoding of the first 16 SHA-256 bytes:

```
scope = H(endpointID + "/" + source + "/" + transport + "/" + URL)
candidate = H(baseCandidateID + "/scope/" + scope)
```

The scope endpoint belongs to the **initiator**, whereas the base endpoint belongs
to the recipient. DNS expansion happens before scoping. The authenticated
introduction carries `scope`; optional `target` remains an unscoped DNS answer.
The receiver reconstructs the scope from the initiator's current endpoint list,
rejects unknown or missing scopes, and checks a scoped literal's receiving
interface against the actual socket's remote-address zone. Numeric owner zones
and interface names are matched through the receiving OS's interface index.
Scope withdrawal/replacement revokes pending and established candidates on both
nodes, including healthy retained DNS sessions. Unaffected interfaces keep their
existing sessions. Interface renaming changes the advertised scope and therefore
its candidate identity.

Link-local addresses cannot traverse routers or NAT.
