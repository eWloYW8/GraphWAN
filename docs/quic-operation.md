# QUIC datagram endpoints

Add a manual endpoint such as `quic://node.example.com:24752` in **Agents → Manage**,
then enable QUIC and the required IPv4/IPv6 direct methods on the Edge. Addresses
and DNS names require explicit ports. QUIC endpoints do not accept URL paths.
Automatic discovery advertises TCP/UDP only; it does not infer QUIC endpoints.

QUIC and native UDP share the configured Agent UDP listen port. A manual URL
with another external port requires UDP forwarding to that listener. Each
connection carries the existing Noise-authenticated peer protocol, heartbeat,
selection and forwarding messages. Healthy standby Links remain connected;
the Edge selects one common active Link using the same preference/RTT policy
as the other transports. The controller is never a packet relay.

## TLS and packet transport

The wire uses QUIC v1, TLS 1.3 and ALPN `graphwan.quic.v1`. Direct connections
verify the configured Agent Ed25519 key through its automatically renewed
certificate. A CA-issued certificate must validate against the system trust
store and endpoint hostname. Noise independently verifies both Agent identities
and the configured Edge/Network/transport. No 0-RTT data is enabled.

QUIC Retry verifies a source address before accepting its connection. A total
of 512 incoming/outgoing QUIC connections is allowed per Agent socket, including
handshakes; failed and closed connections release their slots. TLS handshakes
have a five-second idle limit and a ten-second overall limit. Idle established
connections expire after fifteen seconds; normal authenticated Link heartbeats
run every second. Peer Noise admission shares the existing eight-slot limit.
A stable stateless-reset key is derived from the durable Agent identity using
HKDF-SHA256 with a protocol-specific label.

All peer messages use [RFC 9221 QUIC datagrams](https://quic-go.net/docs/quic/datagrams/).
Bidirectional and unidirectional QUIC streams are disabled. Lost business-data
datagrams are not retransmitted; the existing peer handshake retries and Link
selection retries still apply to their respective control messages. QUIC supplies
congestion control and protects its packets with TLS keys. The inner Noise
session adds graph authentication, per-session keys and replay checks.

## MTU and bounded reassembly

This adapter sends conservative 1200-byte QUIC packets. It splits peer messages
into fragments with at most 1024 payload bytes and a 13-byte header:

| Offset | Size | Field |
| --- | --- | --- |
| 0 | 1 | Fragment format version, currently 1 |
| 1 | 8 | Nonzero, increasing per-direction message ID |
| 9 | 2 | Complete peer-message length, 1–16,384 bytes |
| 11 | 2 | Fragment offset, a multiple of 1024 |
| 13 | 1–1024 | Fragment payload; the last fragment may be shorter |

Integer fields are unsigned big-endian. Fragments may arrive out of order;
duplicate offsets do not extend a message's lifetime or count toward completion.
The receiver emits a message only after all of its bytes arrive. A missing
fragment drops the entire message. Up to 32 incomplete messages are retained per
connection, each bounded to 16 KiB. Records expire after two seconds, with expired
records discarded when the next datagram is processed. Header/length/offset
errors are discarded before allocating an assembly.

QUIC does not use IP fragmentation for the tested IPv4/IPv6 path budgets. A native
test carries 9000-byte virtual IP packets across a 1280-byte underlay using these
datagram fragments. This does not claim to support underlays unable to carry
QUIC's minimum 1200-byte UDP payload. Large overlay packets require more fragments
and are more likely to be lost if any individual datagram drops. TCP inside the
overlay can retransmit its own lost data.

## Sharing the native UDP socket

The existing native UDP wire format (`GWD`, version 1) is preserved. GraphWAN
QUIC connection IDs are 16 bytes: a `Q` prefix plus 120 random bits. This separates
normal incoming QUIC short headers from native UDP's magic prefix; QUIC long
headers already have a distinct high bit. Native UDP and QUIC keep separate peer
admission and queue bounds on the shared socket.

The shared reader dispatches native packets before passing the remaining packets
to quic-go. A generic `net.PacketConn` adapter intentionally hides raw socket
access: batched reads must not bypass dispatch, and changing socket-wide DF
behavior would break native UDP fragmentation. This disables QUIC's raw-socket
GSO/ECN optimizations and dynamic path-MTU discovery. The fixed packet budget
preserves correctness; no optimized-throughput claim is made. See
[quic-go's transport documentation](https://quic-go.net/docs/quic/transport/) for
the underlying API and optimization tradeoffs.

## Verification

Real UDP/QUIC tests verify IPv4/IPv6, TLS identity rejection, same-port native
UDP compatibility through 16 KiB, bidirectional datagram fragmentation, receive
timeouts, actual packet loss without data retransmission, blocked-send
cancellation and shutdown. Parser tests cover reordered/duplicate fragments,
missing-fragment expiry, malformed input and bounded allocation. Mesh tests
retain all six transports, fail the QUIC route while preserving existing standby
Links, and repeatedly rotate QUIC peer sessions while forwarding traffic.

The Linux process test verifies real three-Agent TUN routing, full-MTU ICMP,
TCP transfer, controller outage, offline transit-Agent restart and cleanup:

```sh
go build -o /tmp/graphwan-e2e ./cmd/graphwan
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e --transport quic
sudo unshare --net python3 tests/e2e_linux.py --binary /tmp/graphwan-e2e \
  --transport quic --mtu 9000 --underlay-mtu 1280
```

[STUN and UDP punching](nat-operation.md) also use the shared socket. QUIC itself
currently uses direct manual endpoints; arbitrary NAT connectivity and TCP
punching are separate outstanding work.
