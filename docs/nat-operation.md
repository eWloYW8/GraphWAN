# STUN discovery and TCP/UDP hole punching

In **Agents → Manage**, set **STUN servers** to one service per line (at most
four). Bare `host:port` and `udp://host:port` select UDP; `tcp://host:port` selects
TCP. Examples are `stun.example.com:3478`, `[2001:db8::1]:3478` and
`tcp://stun.example.com:3478`. For TCP punching, configure a TCP-capable service.
Use a reachable RFC 8489 Binding service; the controller does not currently host
one. An empty list disables STUN discovery. There are no implicit public servers.
These settings are persisted by the controller, delivered in that Agent's
snapshot and cached locally for offline restart. They are not Agent CLI settings.

Enable **UDP** and/or **TCP**, and **Hole punch** on each desired Edge. IPv4/IPv6
direct methods are independent: a punch-only Edge can establish an authenticated peer connection
without enabling either direct method. The controller sends observed endpoints
to adjacent peers through the existing revisioned configuration channel. Both
Agents repeatedly send from their peer data sockets to the advertised endpoints,
using the bounded candidate scheduler. UDP retransmits its Noise handshake; TCP
attempts simultaneous open with the same local port as its listening socket.
The controller exchanges configuration and observations; it never relays packets.

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
Failed probes retain previous observations until expiry; a changed listener or
STUN configuration discards the discovery cache. Automatic and manual endpoints
share the existing 64-entry Agent limit; overflow is logged and automatic entries
are bounded deterministically.

Expired observations cannot authorize new dialing. A currently healthy,
authenticated Link remains usable after discovery expiry and can renew the same
candidate's cryptographic session while the controller or STUN service is down.
When all of that candidate's sessions fail, its retained observation is discarded.
Cold offline recovery can try cached observations while still valid; discovering
a changed remote mapping requires another exchange with the controller. Removing
an Edge, revoking a peer, or disabling punching still closes its sessions.

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
performs the ordinary Network/Edge-bound Noise handshake. This allows two Networks
and session-key rotation to share one TCP four-tuple; opening a new connection
for every Link would conflict with the existing NAT mapping. See the
[wire format and resource limits](peer-protocol.md).

Linux native tests below verify source-port reuse and peer SYNs through restricted
NATs. Unix adapters set `SO_REUSEADDR`/`SO_REUSEPORT`; Windows sets `SO_REUSEADDR`.
These options permit local port sharing and differ between operating systems.
Run one Agent per configured listen address/port; do not use the port as a local
process-ownership boundary. Other native platforms and NAT behavior remain
unverified, even where the code cross-compiles. TCP NAT traversal depends on the
NAT's mapping, filtering and simultaneous-open behavior; it cannot be guaranteed
for every router. [RFC 5382](https://www.rfc-editor.org/rfc/rfc5382) describes those
TCP NAT requirements.

## Native verification and current coverage

```sh
go build -o /tmp/graphwan-e2e ./cmd/graphwan
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --transport udp --nat
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --transport tcp --nat --mtu 9000 --underlay-mtu 1280
```

The test requires Linux TUN/network namespaces and `/usr/sbin/iptables` in
addition to the ordinary [Linux test tools](linux-operation.md). Every routing
and firewall change is confined to disposable network namespaces. A local
test-only STUN responder avoids depending on an Internet service.

Three real Agents run behind three separate SNAT/firewall routers. Each router
translates 24752 to different external ports for TCP and UDP, and drops unsolicited
incoming packets. The TCP case verifies independent TCP/UDP observations and
outbound peer SYNs from both ends of each Edge. Tests verify those drops, the
observed mapped ports, punch-only multi-hop ICMP and TCP, traffic after
STUN/controller shutdown, offline transit-Agent restart
from cache, and TUN cleanup. Transport tests separately cover IPv4/IPv6 STUN,
spoofed-source/transaction rejection, retransmission, RFC 5769 response decoding,
fingerprint errors, bounded transactions and cancellation. TCP tests additionally
verify persistent observation connections, segmented/malformed responses, mutual
TLS identity rejection in both roles and logical stream limits. Mesh tests exercise
multiple Networks sharing one physical TCP connection, session renewal after
observations are removed, then revocation of punching.

This verifies the tested Linux NAT mapping/filtering behavior. It does not prove
arbitrary symmetric NAT pairs, port prediction, NAT64, or an ICE/TURN deployment.
Both IPv4 and IPv6 virtual networks pass over the verified IPv4 NAT fixture;
use `--overlay-family 6` to reproduce the latter. This does not require IPv6
support in the underlying NAT. The fixture does not model IPv6 NAT or NAT64.
Non-Linux native punching runs remain optional under the agreed Linux-only
runtime acceptance scope; the implementation review below covers their socket
setup. Dynamic NAT remapping/recovery acceptance remains open.
When punching cannot connect, configure another permitted reachable endpoint; no
controller relay is supplied.


## Mixed peers, IPv6 and scheduler acceptance

The Linux fixture supports `--nat-nodes 1`, `2` or `3` with `--nat` (default 3).
Two NAT nodes and one public node put both a NAT–NAT Edge and a NAT–public Edge in
the same forwarding path. The public node keeps its interface endpoint when STUN
returns that identical address; translated nodes advertise their independently
mapped ports. Both TCP and UDP pass this scenario with IPv6 overlay MTU 9000,
underlay MTU 1280 and the restricted Agent capability set:

```sh
for transport in udp tcp; do
  sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
    --transport "$transport" --nat --nat-nodes 2 --restricted-agent \
    --overlay-family 6 --mtu 9000 --underlay-mtu 1280
done
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --punch-only --underlay-family 6 --overlay-family 4 --restricted-agent \
  --mtu 9000 --underlay-mtu 1280
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --punch-only --peer-link-local-v6 --overlay-family 6 --restricted-agent \
  --mtu 9000 --underlay-mtu 1280
```

`--punch-only` disables both direct methods and permits only the TCP/UDP punching
method. It does not itself create NAT. The IPv6 global-address case establishes
both transports without IPv4 underlay addresses. The link-local case uses two
NICs per node, repeated IPv6 addresses and different NIC names; its firewall
blocks IPv4 peer traffic on the management link. This is necessary because the
punch switch is independent of the direct-family switches and enables attempts
over both address families. Both IPv6 cases pass multi-hop traffic, controller
outage, offline TUN repair, cached transit restart and cleanup. The scoped case
also removes/restores one scope without replacing the other scope's sessions.

The fixture compares exact healthy candidate IDs, including the punch method and
both dialing directions. It cannot pass through an unintended direct connection
or one-sided subset of the expected candidates. Mixed NAT cases additionally
verify unsolicited packet rejection, translated STUN ports, outbound TCP SYNs
and traffic after stopping STUN. These cases join the Linux CI matrix.

Portable policy tests cover all eight combinations of the three connection
switches, disabled Edges, transport exclusion, literal/manual DNS endpoints and
observed leases. A real-socket scheduler test holds sixteen configured endpoints
without answering their handshakes: only eight dials may be pending, failures
must back off while every other endpoint gets attempted, and removing the
endpoints cancels outstanding dials. The scheduler uses a 250 ms tick, twelve-second
attempt deadlines and exponential delay limits from one to thirty seconds with
half-to-full jitter. These timers continue without the controller.

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
back to a different source port. All seven selected OS implementations were
cross-compiled in the preceding scope change; this acceptance change does not
alter production code.

| Platform | Binding implementation and reviewed contract |
| --- | --- |
| Windows | `SO_REUSEADDR` on listeners and dials, without `SO_EXCLUSIVEADDRUSE`. Go's listener default does not install an exclusive bind. [Winsock bind](https://learn.microsoft.com/en-us/windows/win32/api/winsock/nf-winsock-bind) and [reuse/exclusive rules](https://learn.microsoft.com/en-us/windows/win32/winsock/using-so-reuseaddr-and-so-exclusiveaddruse). |
| macOS | Both `SO_REUSEADDR` and `SO_REUSEPORT` before bind. [Apple socket options](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setsockopt.2.html). |
| FreeBSD, OpenBSD, NetBSD, DragonFly | Both reuse options before bind, as defined by the respective native socket API. [FreeBSD](https://man.freebsd.org/cgi/man.cgi?query=setsockopt&sektion=2), [OpenBSD](https://man.openbsd.org/setsockopt.2), [NetBSD](https://man.netbsd.org/setsockopt.2), [DragonFly](https://man.dragonflybsd.org/?command=setsockopt&section=2). |
| Linux | Both reuse options, with the real-socket and namespace acceptance described above. |

This review supports the implementation's port-sharing design; it does not claim
native execution or successful traversal of every OS/router combination. In
particular, port reuse is not an ownership boundary between local processes and
cannot force a NAT to preserve mappings or permit simultaneous open.
