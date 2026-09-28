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
The native TCP punching platform matrix remains unfinished.
When punching cannot connect, configure another permitted reachable endpoint; no
controller relay is supplied.
