# STUN discovery and TCP/UDP hole punching

In **Agents → Manage**, set **STUN servers** to one service per line (at most
four). Bare `host:port` and `udp://host:port` select UDP; `tcp://host:port` selects
TCP. Examples are `stun.example.com:3478`, `[2001:db8::1]:3478` and
`tcp://stun.example.com:3478`. For TCP punching, configure a TCP-capable service.
Use a reachable RFC 8489 Binding service; the controller does not currently host
one. New enrollments start with these public services:

- `stun.miwifi.com:3478` (UDP)
- `stun.cloudflare.com:3478` (UDP)
- `stun.nextcloud.com:443` (UDP)
- `tcp://stun.nextcloud.com:443` (TCP)

The defaults are saved as ordinary Agent settings, visible and editable in the
panel. Existing Agents retain their saved list, including an empty list. Clearing
the list explicitly disables discovery; restarts and unrelated edits do not
restore the defaults. Public service availability and reachability vary by network.
These settings are persisted by the controller, delivered in that Agent's
snapshot and cached locally for offline restart. They are not Agent CLI settings.

STUN discovery is independent of the Edge's connection method. A fresh observed
public endpoint is eligible for **IPv4 direct** or **IPv6 direct**, just like an
interface or manual endpoint. Ordinary dialing can reach public listeners and
accessible static/port mappings without enabling **Hole punch**. The observed
port need not equal the local listening port. Address/port equality alone does
not prove reachability; a successful authenticated connection establishes it.

Enable **UDP** and/or **TCP**, and **Hole punch** for additional punching
attempts. The direct-family switches are independent: a punch-only Edge can
establish an authenticated peer connection with both direct methods disabled.
The controller sends observed endpoints
to adjacent peers through the existing revisioned configuration channel. Both
Agents repeatedly send from their peer data sockets to the advertised endpoints,
using the bounded candidate scheduler. UDP retransmits its Noise handshake; TCP
attempts simultaneous open with the same local port as its listening socket.
The controller exchanges configuration and observations; it never relays packets.

## Optional symmetric-NAT extension

In an Edge's **Connection methods**, **NAT hole punching extension** is available
only when **NAT hole punching** is selected. It defaults to off; disabling the
parent option also clears the extension. API and Agent snapshot validation reject
an extension without its parent. Update both Agents and the Server to a version
that supports this option before enabling it.

The extension adds UDP and TCP attempts to alternative destination ports on fresh
STUN-observed public IPv4 addresses. It infers possible allocator strides from
different observed ports on the same IP and transport, tries up to four multiples
in both directions (strides up to 256), and scans the adjacent ±32 ports. This is a
heuristic: observations are not a definitive NAT classification or ordered record
of port allocations. UDP observations never predict TCP ports or vice versa.

Searches exclude manual/interface endpoints, DNS names, private/shared/reserved
addresses, IPv6, and destination ports below 1024. Each dialing direction has at
most 128 additional candidates across its addresses and transports. Duplicate
targets are eliminated. The extension starts after a five-second grace period,
has two simultaneous attempts per Edge and four per Agent, and starts at most
two attempts per second per Agent. An attempt lasts up to four seconds; handshake
retransmissions may send more than one packet per attempt. Failed ports have a
two-minute cooldown plus jitter, with least-recently-attempted ports tried first.
Ordinary dialing retains its separate concurrency budget.

Once any path is healthy, new port searches stop and pending searches are
cancelled. Established extended candidates can still renew their session keys;
disabling the extension revokes those candidates and cancels their pending dials.
Successful connections use the existing peer authentication, path deduplication
and lowest-RTT selection. Predicted ports do not become globally advertised
endpoints or bypass Edge authorization.

This improves chances with sequential or small-stride port allocation; it does
not guarantee NAT3–NAT4 or NAT4–NAT4 connectivity. Random port allocation, multiple
public source IPs, short mapping lifetimes and TCP SYN filtering can still prevent
a connection. There is no exhaustive 65,535-port sweep or automatic relay service;
configure another reachable Agent path when direct traversal fails.

## Observations and lifetimes

UDP STUN runs on the exact socket that carries native UDP and QUIC. TCP STUN
binds outgoing connections to the same local port as the shared peer listener
and TCP punch dials, and retains each observation connection between refreshes.
An observation includes its actual externally mapped port, which may differ from
24752. DNS resolves both IPv4 and IPv6; each hostname
is bounded to 32 resolved addresses, alternating families and rotating address
order. Each service has its own worker (at most four); one-second transaction
deadlines prevent a dead address from starving alternatives within the
3.5-second discovery round. Rounds run approximately every 20–25 seconds.
The client publishes protocol-specific observations: a UDP mapping says nothing
about a TCP mapping on the same numeric port. Both transports need their own
service configuration when both mappings are required.

Each successful observation receives a local two-minute validity window, not a
promise about the NAT's actual lease. Identical mappings from multiple services
are deduplicated within each transport. Stable endpoint IDs include the Agent
identity, transport and mapped address. Interface/manual URLs take precedence
over duplicate observed URLs.
Stable mappings keep their published expiry until one minute or less remains;
only then is the latest successfully observed expiry published. This applies to
both Agents and Servers and reduces configuration revisions without changing
STUN probe frequency. Address changes, removals and shortened validity windows
are not delayed. Unchanged interface endpoint lists are not resent every scan.
Failed probes retain previous observations until expiry; a changed listener or
STUN configuration discards the discovery cache. Automatic and manual endpoints
share the existing 64-entry Agent limit; overflow is logged and automatic entries
are bounded deterministically: observed mappings first, then public interface
addresses, private interface addresses and finally link-local addresses. Stable
IDs break ties; publication order remains stable.

Expired observations cannot authorize new dialing. A currently healthy,
authenticated Link remains usable after discovery expiry and can renew the same
candidate's cryptographic session while the controller or STUN service is down.
When all of that candidate's sessions fail, its retained observation is discarded.
Cold offline recovery can try cached observations while still valid; discovering
a changed remote mapping requires another exchange with the controller. Removing
an Edge, revoking a peer, or disabling the session's direct/punch method still
closes its sessions.

## STUN admission and limits

The implementation uses [Pion STUN](https://github.com/pion/stun) for RFC 8489
message encoding and decoding. Binding requests use random 96-bit transaction
IDs and fingerprints. A response must match both an outstanding ID and the exact
resolved service address. Replies are bounded to 2048 bytes and checked for valid
lengths, fingerprints when present, supported required attributes, response type,
and a unicast XOR-MAPPED-ADDRESS of the queried family. IPv4-mapped addresses are
normalized. Unsolicited/malformed messages create no peer and elicit no response.
UDP allows at most 32 pending transactions per socket, removed on completion,
cancellation or closure. Retransmissions start at 500 ms and back off; the Agent's
round deadline truncates the full RFC schedule. TCP allows at most 32 persistent
service connections, with one transaction per service at a time and a maximum
five-second transaction lifetime (further limited by the discovery round).
TCP frames use the STUN header's length; no extra length prefix is inserted.
Protocol errors or cancellation close that observation connection; connections
unused for one minute are swept every ten seconds. Redirects are not followed.

An unauthenticated Binding service supplies a connectivity hint, not peer
authentication. STUN does not carry keys, enrollment tokens or virtual-network
data. Noise still authenticates both peers and the Network/Edge/transport binding.
See [RFC 8489](https://www.rfc-editor.org/rfc/rfc8489) for STUN's role and limits.

## TCP physical connections

TCP port reuse permits outgoing SYNs while the Agent continues listening on the
same port. Both peers retry allowed candidates without waiting for the controller
to trigger every attempt. Because simultaneous open does not reliably distinguish
an application initiator from a responder, Agents exchange identity hints and
choose their TLS roles by public-key order. Mutual TLS proves those identities
before publishing a multiplexed physical connection. Each logical stream then
performs the ordinary Network/Edge-bound Noise handshake. This allows multiple Networks
and session-key rotation to share one TCP four-tuple; opening a new connection
for every Link would conflict with the existing NAT mapping. See the
[wire format and resource limits](peer-protocol.md).

The logical stream allowance grows from locally authorized configuration, keeping
space for each network's endpoint candidates and session-key replacement. Adding
networks updates an existing physical session without reconnecting.

Physical connections are limited separately for each authenticated adjacent Agent.
The allowance starts at 64 established or pending connections and grows from
authorized endpoint counts, DNS answers and interface scopes, with a dial and
its result counted once. The number of authorized neighbors does not share that
allowance.

The pool distinguishes both local and remote socket addresses. A single neighbor
using the same source port can therefore reach multiple local NIC addresses
without those sockets being mistaken for duplicates.

Unix adapters set `SO_REUSEADDR`/`SO_REUSEPORT`; Windows sets `SO_REUSEADDR`.
These options permit local port sharing and differ between operating systems.
Run one Agent per configured listen address/port; do not use the port as a local
process-ownership boundary. Other native platforms and NAT behavior remain
unverified, even where the code cross-compiles. TCP NAT traversal depends on the
NAT's mapping, filtering and simultaneous-open behavior; it cannot be guaranteed
for every router. [RFC 5382](https://www.rfc-editor.org/rfc/rfc5382) describes those
TCP NAT requirements.

## Cross-platform TCP socket review

The production call chain is `ListenTCP` → `reuseTCPPort` and
`STUNBinding`/`punchStream` → `DialTCPPort` → the same reuse hook. Listener and dial
hooks run before bind, and both bind the configured peer port. Wildcard local IPs
are cleared before selecting `tcp4`/`tcp6`, so an IPv6 wildcard listener can also
supply IPv4 punch dials. Scoped IPv6 destinations preserve the initiator's zone.
The active/passive kernel result does not choose the TLS role; public-key order
chooses that role and each multiplexed stream still authenticates Network/Edge.

The Go 1.26.8 `net` sources (`sock_posix.go`, `sockopt_windows.go`) were checked
for hook ordering and default options. GraphWAN uses platform-specific constants
through `x/sys`, propagates either socket-option error and never silently falls
back to a different source port.

| Platform | Binding implementation and reviewed contract |
| --- | --- |
| Windows | `SO_REUSEADDR` on listeners and dials, without `SO_EXCLUSIVEADDRUSE`. Go's listener default does not install an exclusive bind. [Winsock bind](https://learn.microsoft.com/en-us/windows/win32/api/winsock/nf-winsock-bind) and [reuse/exclusive rules](https://learn.microsoft.com/en-us/windows/win32/winsock/using-so-reuseaddr-and-so-exclusiveaddruse). |
| macOS | Both `SO_REUSEADDR` and `SO_REUSEPORT` before bind. [Apple socket options](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setsockopt.2.html). |
| FreeBSD, OpenBSD, NetBSD, DragonFly | Both reuse options before bind, as defined by the respective native socket API. [FreeBSD](https://man.freebsd.org/cgi/man.cgi?query=setsockopt&sektion=2), [OpenBSD](https://man.openbsd.org/setsockopt.2), [NetBSD](https://man.netbsd.org/setsockopt.2), [DragonFly](https://man.dragonflybsd.org/?command=setsockopt&section=2). |
| Linux | Both reuse options before bind. |

This review supports the implementation's port-sharing design; it does not claim
native execution or successful traversal of every OS/router combination. In
particular, port reuse is not an ownership boundary between local processes and
cannot force a NAT to preserve mappings or permit simultaneous open.

## Live NAT mapping changes

When a NAT mapping changes, STUN discovery publishes the new protocol-specific
endpoint and adjacent Agents retry it. Discovery runs approximately every 20–25
seconds. A failed persistent TCP observation is closed before a subsequent round
opens a fresh connection, so recovery can require another discovery interval.
Previously observed endpoints may remain advertised until their two-minute
lease expires.

Peers need a controller exchange of the new endpoint or another already reachable
configured path. An undiscovered remote mapping cannot be recovered solely from
an offline cache.
