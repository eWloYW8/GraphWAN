# Peer data protocol v1

This documents the packet/channel components shared by the Agent adapters.
See [NAT limits](nat-operation.md) for traversal constraints.

## Admission and handshake

A logical Edge authorizes a Network, local Node, remote Node, transport and pair
of Ed25519 identities. Incoming Hello metadata selects an existing policy. It
cannot create an Edge, register an identity or choose a weaker cipher. The
listener checks that the declared transport matches the listener's transport.

Each connection performs `Noise_XX_25519_<cipher>_BLAKE2s` using `flynn/noise`.
The controller's Network policy selects exactly one suite; there is no peer-led
fallback or cipher downgrade. Noise binds its cipher name into the handshake
transcript. The default ChaCha20-Poly1305 handshake remains wire-compatible.

| Network cipher | Noise cipher name | Key bytes | Nonce encoding |
| --- | --- | ---: | --- |
| `aes-128-gcm` | `AES128GCM` | First 16 of the 32 derived bytes | 4 zero bytes + big-endian uint64 |
| `aes-256-gcm` | `AESGCM` | 32 | 4 zero bytes + big-endian uint64 |
| `chacha20-poly1305` | `ChaChaPoly` | 32 | 4 zero bytes + little-endian uint64 |
| `xchacha20-poly1305` | `XChaChaPoly` | 32 | 16 zero bytes + little-endian uint64 |

`AES128GCM` and `XChaChaPoly` are GraphWAN CipherFunc extensions, not cipher
names defined by the base Noise specification. Their distinct names separate
handshake transcripts and key derivation. Implementations use Go's `crypto/aes`,
`crypto/cipher`, and `golang.org/x/crypto/chacha20poly1305`; the extended nonce
uses a unique per-key sequence rather than random nonces. Upgrade all Agents
in a Network before selecting a newly supported suite.

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
The optional `path_exchange: true` capability enables the address exchange and
duplicate retirement messages below. Older peers ignore this field; no new
message types are sent until the remote advertises or demonstrates support.

## Encrypted messages

The wire ciphertext is an 8-byte big-endian sequence followed by the configured AEAD
ciphertext and its 16-byte authentication tag. The transcript binding and sequence
are authenticated associated data. Sequence values start at one and are never
reused, including after a failed transport send. The table above defines nonce
encoding for each suite. A 1024-message replay window permits reordering.
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

The TLS connection carries yamux streams. A physical session is pooled by local
and remote IP/port plus authenticated Agent identity, allowing multiple Networks and fresh
Noise sessions to share the same TCP four-tuple. Every stream uses the 32-bit
length framing and ordinary Network/Edge Noise admission above. Admission also
checks that a punch candidate arrived over this transport, not ordinary TCP.
Each authenticated adjacent Agent identity has a physical-session allowance of
at least 64, growing with its locally authorized TCP endpoint candidates and
DNS answers. Local endpoint hostnames use the bounded DNS cache for this sizing
as well, without requiring a matching answer to authenticate an incoming target.
The bound includes headroom for replacement sessions and IPv6 interface scopes;
remote connection attempts cannot increase it. Pending outgoing connections count
toward this allowance. A pending connection that becomes
installed consumes one slot; a closed connection can be replaced in that slot.
The allowance is separate for each identity, so one peer cannot consume the
physical-connection capacity of other configured peers. Logical stream capacity starts
at 32 per session and grows with the locally authorized TCP-punch topology for
that peer. Its bound includes endpoint aliases, IPv6 egress scopes and headroom
for concurrent handshakes, replacement keys and retiring streams. Peers cannot
negotiate this allowance. Existing physical sessions retain their highest
authorized stream allowance when policy shrinks, so retiring streams and healthy Links
whose observed endpoints have expired can finish or renew. The accept backlog
remains eight streams, with a 256 KiB window per stream.
Writes time out after two seconds; unacknowledged stream opens and graceful stream
closes expire after three seconds. Mux keepalives run every fifteen seconds. Canceling a
stream does not close unrelated streams. Disabling the last TCP punch Edge to an
identity closes its physical sessions. See [NAT operation](nat-operation.md) for
source-port reuse and platform limits.

UDP datagrams use four magic/version bytes (`GWD`, `1`), a random 16-byte
connection token and one complete message. A data socket multiplexes independent
connections by remote address and token. This token is a demultiplexing hint,
not authentication. Handshakes still verify graph policy and peer identities.
The socket allows at most 512 pending unauthenticated peers, 64 pending accepts,
and 64 queued messages per peer. Both address families share these limits.
A successfully verified configured Noise handshake releases its pending slot;
existing authenticated Links do not consume new-handshake capacity. Authentication,
failure and close release each reservation only once. Unauthenticated peers
expire after 10 seconds; authenticated peers
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

### Packet bounds and backpressure

The configured Network MTU applies to the **inner IP packet**, at both TUN and
peer ingress, before either local delivery or transit. It defaults to 1280 and
can be set from 1280 through 9000. The overlay adds an 80-byte header; peer crypto
and transport framing add their own overhead. This setting does not estimate or
automatically track path MTU. Stream transports segment their byte stream, native
UDP relies on the host IP stack's fragmentation behavior, and QUIC splits peer
messages into its bounded application fragments. Networks that discard outer
fragments can require a smaller configured MTU or another transport.

Packet parsing checks the wire version, reserved fields, nonzero IDs and hop
limit, declared payload length and maximum frame length. Inner-IP inspection
checks the version, base header size and exact IP length; the forwarding layer
also enforces configured Node/address admission. Upper-layer checksums and full
IPv6 extension-header semantics remain the receiving host stack's responsibility.
New overlay packets start with hop limit 32. Transit decrements it; a packet with
one remaining hop may be delivered at its destination but cannot transit again.
Forwarding preserves the original routing epoch instead of resetting the hop
budget when a packet crosses Agents with different applied revisions.

Each Link has 128 queued outbound frames and 128 queued received frames, with
separate eight-message probe and selection queues. Sending copies the caller's
frame before enqueueing; a full outbound queue returns `ErrQueueFull`, and a full
receive queue drops the arriving frame. Probes retain write priority and incoming
control processing does not wait for TUN delivery. Stream writes have deadlines;
transport failure closes that Link so the Edge can use a healthy standby. No
unbounded application packet backlog or overlay data retransmission is added.

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

Link probes run every second during user traffic and every ten seconds while
idle (including standby Links). A missing response resumes one-second probes;
five seconds without a valid response after the first unanswered probe closes
the Link. Thus silent failure detection can take approximately fifteen seconds
while idle, versus approximately five to six seconds during traffic. Initial
admission still requires a valid pong within five seconds. User data restores
fast probes on the next tick; after ten seconds without user data the Link
returns to idle cadence. Ping/pong traffic itself does not count as user activity.
Wire framing is unchanged, and peers with the old probe cadence interoperate.
Probes have priority over queued user frames. Standby Links run the same probes;
only the selected sending Link receives user frames from the routing engine.

## Address exchange and duplicate sessions

An updated responder starts an authenticated type-8 message: one type byte, one
acknowledgment byte (0 or 1), then the ASCII `IP:port` of its remote endpoint
(IPv6 uses brackets). The whole message is at most 130 bytes. The acknowledgment
bit means the sender has received the other end's address observation. The dialer
enables this exchange after receiving type 8. Until acknowledged, an endpoint
retries about once per second; it answers a first observation or an unacknowledged
message immediately. The exchange then stops. Addresses are immutable within a
session, and unspecified/multicast addresses and zero ports are rejected.

Each end now knows both IPs as observed by its peer, including NAT mappings and
the actual source of a connection from a wildcard UDP listener. Within one Edge,
sessions are duplicates only if both observed endpoint IPs and the GraphWAN
transport match. Ports, dialing direction and direct/punch method do not
distinguish duplicates. IPv4-mapped addresses are normalized; IPv6 zones remain
part of scoped address identity. Different IPs or transports remain independent
candidates. Unknown addresses or peers without this capability are not guessed
or deduplicated.

The common-Link selector retains an established healthy session per IP/protocol
path. It excludes redundant sessions from data selection, waits for the common
selection to finish, and requests retirement on the **retained** session. Type 9
is a retirement request and type 10 its acknowledgment; both carry the redundant
session's 32-byte decoded ID after the type byte. The follower verifies the same
IP/protocol path, a healthy retained session, and that the redundant session is
safe to retire. It records the retired candidate's replacement before closing
and acknowledging. Requests retry once per second and acknowledgments are
idempotent even after the redundant session closes. Thus a lost UDP acknowledgment
cannot cause a permanent retirement retry loop.

Both sides suppress redialing candidates already served by a healthy retained
session. Failure releases suppression; renewal may temporarily overlap sessions
until a fresh healthy replacement is ready. Saved manual candidate preferences
follow their retained session on the same path. Probe sizes/cadences are unchanged.

## Common active Link negotiation

The endpoint with the lexicographically smaller Node ID selects the active Link
for both directions of an Edge. Its measured RTT drives automatic selection;
standby probes and telemetry continue at both endpoints. Explicit candidate
preference overrides RTT. Automatic selection chooses the lowest measured RTT
among the retained healthy candidates, with Link ID as a deterministic tie break.
When the current healthy Link has carried user data in either direction within
the last five seconds, automatic switching requires at least a 10% RTT reduction.
Activity is sampled from user-byte counters during maintenance/selection; control
messages do not extend the window. Idle Links use the lowest RTT without this
margin. Explicit preferences, failure recovery and required session replacement
bypass the margin. The coordinated
switch below still completes before the follower starts using the selected Link.

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
