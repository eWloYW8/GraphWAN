# Peer data protocol v1

This documents the packet/channel components integrated into the Linux Agent.
Other platform adapters and broader NAT acceptance are still
being implemented. See the full-scope acceptance tracker for verification status.

## Admission and handshake

A logical Edge authorizes a Network, local Node, remote Node, transport and pair
of Ed25519 identities. Incoming Hello metadata selects an existing policy. It
cannot create an Edge, register an identity or choose a weaker cipher. The
listener checks that the declared transport matches the listener's transport.

Each connection performs `Noise_XX_25519_ChaChaPoly_BLAKE2s` using `flynn/noise`.
The Noise static key and ephemeral key are newly generated for that connection.
The encrypted second and third handshake payloads contain Ed25519 signatures
binding the temporary static key to the configured identity, initiator/responder
role, protocol version, Network ID, Edge ID, both Node IDs and transport. The
same fields form the Noise prologue. No shared network secret is used. The
handshake generates independent keys for each direction and a transcript binding.

The [Noise specification](https://noiseprotocol.org/noise.html) describes the
handshake/key schedule and explicit nonces for out-of-order transport messages.
[Library API documentation](https://pkg.go.dev/github.com/flynn/noise) describes
the low-level cipher interface used by the session layer. This implementation
has tests; it has not undergone an independent cryptographic audit.

Wire handshake messages start with one kind byte:

| Kind | Contents |
| --- | --- |
| 1 / Hello | version byte, Network/Edge/initiator/responder IDs (16 bytes each), transport byte, Noise message 1 |
| 2 / Reply | Noise message 2 |
| 3 / Finish | Noise message 3 |
| 4 / Ready | session-encrypted one-byte confirmation (`0`) |
| 5 / Data | session-encrypted application message |

Transport codes are UDP=0, TCP=1, QUIC=2, WS=3, WSS=4, gRPC=5. The current
transport implementations cover all six codes: TCP streams, native UDP, QUIC
datagrams, WS/WSS binary messages and gRPC bidirectional streams.

The initiator requires an authenticated Ready before exposing its channel. The
whole handshake has a 10-second deadline. UDP retransmits the same serialized
handshake message at 250 ms intervals. Duplicate messages replay the previously
serialized response; they never generate a new handshake state or nonce. After
a responder finishes, its receive loop continues answering duplicate Finish
messages with the original Ready, handling loss of the final confirmation.
Data messages do not gain retransmission from this handshake mechanism.

Before Link heartbeats/data, the initiator sends an encrypted introduction:
one zero byte followed by JSON, at most 256 bytes including that prefix. Required
`candidate` and `endpoint` fields contain 32-character identifiers. `candidate`
is the stable Candidate ID; `endpoint` is lowercase hexadecimal encoding of the
first 16 bytes of SHA-256 over `source + NUL + transport + NUL + URL` using the
exact advertised endpoint strings. Optional `target` identifies a concrete DNS
answer. The receiver checks the fingerprint, candidate family/method/transport,
DNS target and actual ingress path against current policy. IPv6 link-local
candidates additionally require a 32-character `scope`, binding the initiator’s
configured interface endpoint; see [scope mapping](endpoint-resolution.md#ipv6-link-local-scopes). Registration rechecks
that policy under its update lock, so revoked pending handshakes cannot restore
Links. See [endpoint admission](endpoint-resolution.md#tls-and-peer-admission).
This development protocol now requires the endpoint fingerprint for all Links;
Agents predating that requirement must be upgraded together with their peers.
Link-local scope introductions also require updated peers; existing unscoped
global-address candidate identities and introductions are unchanged.

## Encrypted messages

The wire ciphertext is an 8-byte big-endian sequence followed by ChaCha20-Poly1305
ciphertext and its 16-byte authentication tag. The transcript binding and sequence
are authenticated associated data. Sequence values start at one and are never
reused, including after a failed transport send. The Noise cipher defines nonce
encoding for ChaCha20-Poly1305. A 1024-message replay window permits reordering.
Unauthenticated messages never move the window. Keys/nonces are never persisted;
reconnection always requires a fresh handshake.

A session refuses encryption after one hour or before sequence `2^32` is reached.
The Link manager starts a fresh handshake after 50 minutes and retires an old
session only after its replacement is healthy. The data interface is bounded by the overlay maximum MTU,
header size and one application message-type byte.

## Transport framing

Streams use an unsigned 32-bit big-endian length followed by one complete message,
with a maximum of 16 KiB. Lengths are checked before allocation. Reads and writes
have context cancellation; any partial I/O error closes the stream because its
frame boundary cannot safely be recovered. Concurrent writes cannot interleave.

TCP punch connections use a separate physical-session preface: bytes `H G W 01`
followed by the Agent's 32-byte Ed25519 public identity. Both sides send the same
format. This cleartext hint must match an adjacent peer with TCP punching enabled;
it is authenticated by a subsequent mutually pinned TLS 1.3 exchange with ALPN
`graphwan.punch.v1`. The lexicographically smaller public key acts as TLS client,
independently of which socket was dialed or accepted. Both certificates must prove
the announced identity, have valid lifetimes and signing/role usage, and contain
no unhandled critical extensions. No CA/proxy fallback is allowed here. Each side
then sends the four magic bytes inside TLS, confirming that both verifiers passed.

The TLS connection carries yamux streams. A physical session is pooled by remote
IP/port and authenticated Agent identity, allowing multiple Networks and fresh
Noise sessions to share the same TCP four-tuple. Every stream uses the 32-bit
length framing and ordinary Network/Edge Noise admission above. Admission also
checks that a punch candidate arrived over this transport, not ordinary TCP.
There are at most 64 physical sessions per Agent and 32 retained logical streams
per session, an eight-stream accept backlog, and a 256 KiB window per stream.
Writes time out after two seconds; unacknowledged stream opens and graceful stream
closes expire after three seconds. Keepalives run every five seconds. Canceling a
stream does not close unrelated streams. Disabling the last TCP punch Edge to an
identity closes its physical sessions. See [NAT operation](nat-operation.md) for
source-port reuse, platform limits and native verification.

UDP datagrams use four magic/version bytes (`GWD`, `1`), a random 16-byte
connection token and one complete message. A data socket multiplexes independent
connections by remote address and token. This token is a demultiplexing hint,
not authentication. Handshakes still verify graph policy and peer identities.
The socket allows at most 512 peers, 64 pending accepts, and 64 queued messages
per peer. Unauthenticated peers expire after 10 seconds; authenticated peers
expire after two minutes without a validated keepalive/data message. Invalid
packets cannot keep a peer alive. A slow reader drops UDP messages rather than
allowing an unbounded queue. RFC 8489 Binding replies are dispatched separately
on this data socket. STUN mappings are exchanged through controller snapshots;
UDP punching uses the same retransmitted peer handshake and identity checks.
See [NAT operation](nat-operation.md) for limits, expiry and verified coverage.

QUIC shares this UDP socket while preserving native UDP wire compatibility. It
uses QUIC v1 / TLS 1.3 / ALPN `graphwan.quic.v1`, then the same Noise admission.
Peer messages use unreliable RFC 9221 DATAGRAM frames, with a versioned 13-byte
fragment header and 1024-byte fragment payloads. Missing fragments cause message
loss; they do not add data retransmission. Fragment assembly is bounded by size,
count and expiry. See [QUIC operation](quic-operation.md) for the exact fragment
format, connection-ID namespace, socket dispatch and MTU constraints.

WS/WSS use one nonempty binary message per peer message, bounded to 16 KiB.
Text messages are rejected and compression is disabled. They negotiate
`graphwan.ws.v1` / `graphwan.wss.v1`, then run the same Noise handshake above.
Only explicit manual endpoint paths accept upgrades. TLS verifies either the
configured Agent identity or a trusted proxy certificate and hostname; the inner
handshake always verifies the Agent. See [WS/WSS operation](websocket-operation.md)
for listener sharing, reverse proxy setup and address-family policy.

gRPC uses the `graphwan.v1.Peer/Connect` bidirectional protobuf service. The
manual URL path prefixes this service method. A `google.protobuf.BytesValue`
carries each peer message, with a 16 KiB value limit and a 16,388-byte encoded
message limit. TLS admission follows the same Agent-pin-or-proxy-PKI policy as
WSS. The client requires `h2` ALPN and the `graphwan-protocol: graphwan-peer-v1`
response before starting Noise. See [the schema](../api/peer.proto) and
[gRPC operation](grpc-operation.md) for paths, proxying and resource limits.

## Overlay forwarding

The 80-byte `GW` v1 header contains payload length, hop limit, Network/Source/
Destination IDs, routing epoch, flow hash and sequence field. The peer session
layer supplies cryptographic replay protection. The complete overlay header and
IP packet are encrypted together on each hop.

TUN-originated packets must use that Node's configured virtual source address.
Peer-originated packets must arrive from an authenticated, configured neighbor;
header IDs and inner IP addresses must agree with the Network's address directory.
Unknown Networks, unknown destinations, oversized/invalid IP packets, reflected
local sources and exhausted hop limits are rejected. Transit forwarding follows
the compiled weighted route and decrements the overlay hop limit. Packets arriving
at their destination are delivered unchanged to the TUN callback.

Intermediate Nodes are trusted hop forwarders and see plaintext. Source/address
checks enforce admission but do not provide cryptographic end-to-end origin
attestation against a compromised transit Node. Configuration replacement swaps
an immutable routing table; packets already in flight may belong to an older
epoch and are bounded by hop limits during convergence.

## Link control inside the authenticated channel

After Ready, the dialer sends a type-0 plaintext message containing a JSON
`candidate` identifier. The responder reconstructs allowed candidates from its
own advertised endpoints, the configured Edge methods/transports, and the remote
Node identity. The identifier and actual socket address family must match for
TCP/UDP/QUIC. For WS/WSS and gRPC, the HTTP/RPC path must match the identified manual
endpoint (gRPC appends the service method to its URL prefix). The dialer enforces IPv4/IPv6 on its socket; the responder cannot infer
that family from a reverse proxy's backend connection. This metadata is encrypted
by the established peer session.

The Link then uses plaintext type 1 followed by an overlay frame for user data,
and type 2/type 3 followed by an eight-byte big-endian probe sequence for ping/pong.
Probes have priority over queued user frames. Standby Links run the same probes;
only the selected sending Link receives user frames from the routing engine.

## Common active Link negotiation

The endpoint with the lexicographically smaller Node ID selects the active Link
for both directions of an Edge. Its measured RTT drives automatic selection;
standby probes and telemetry continue at both endpoints. Explicit candidate
preference overrides RTT. Automatic changes retain the 2-second hold time and
require an improvement of at least 2 ms or 15%, whichever is larger. A failed
Link and a retiring session with a healthy newer replacement bypass the hold.

Selection messages are encrypted application messages on the **proposed Link**.
They contain a one-byte type, a random 16-byte selector incarnation and an
unsigned 64-bit big-endian decision sequence. The channel supplies the Link ID;
announcements cannot name an unrelated connection. Types are:

| Type | Phase | Sender and action |
| --- | --- | --- |
| 4 | Prepare | Selector proposes a healthy Link with a new sequence. |
| 5 | Accept | Follower stops sending on its previous Link, then grants the proposal. |
| 6 | Commit | Selector switches its sender after Accept, then commits. |
| 7 | Confirm | Follower enables its sender on the committed Link and acknowledges. |

The follower never independently picks a different Link. This keeps the two
senders on the same Link when both are enabled; the follower briefly pauses
between Accept and Commit. Packets already in flight may still arrive on the
previous connection. Queued packets carry a local activation generation and are
dropped if their Link is deactivated, including if that Link is later reactivated.
Overlay forwarding remains best effort: negotiation does not guarantee zero loss
or ordering across a switch, and upper-layer reliable transports recover loss.

Unconfirmed phases retry no more often than every 100 ms; the idle mesh scheduler
runs every 250 ms. Each retry is newly encrypted with a fresh session nonce.
Duplicate phases are idempotent, old sequences are ignored, and a sequence is
bound to one proposed Link. Only Prepare can begin a newer decision. Channels
from another Edge, unhealthy channels, and messages from the wrong selection
role cannot change the sender. Selection processing is independent of potentially
blocked TUN delivery and uses bounded control queues.

A Link binds to exactly one selector incarnation. A follower accepts a restarted
selector's new incarnation only after every known healthy channel belonging to
the previous incarnation has disappeared. TCP closure or the normal UDP heartbeat
timeout supplies this evidence. Incarnation bookkeeping is bounded by retained
Links. It is not stored across restarts because peer sessions and keys are not
stored either. A retiring session is closed only after a newer healthy session
for its candidate exists and the common selection has finished on another Link.

This negotiation requires both peers to implement these application message types;
there is no compatibility fallback to independent sender selection.
