# Peer data protocol v1

This documents the currently implemented packet/channel components. The runnable
Agent, operating-system TUN adapters and Link reconciliation are still being
integrated; these primitives alone do not constitute a deployed VPN.

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
transport implementations are TCP streams and native UDP. A kind in this table
does not imply that its transport adapter has been implemented yet.

The initiator requires an authenticated Ready before exposing its channel. The
whole handshake has a 10-second deadline. UDP retransmits the same serialized
handshake message at 250 ms intervals. Duplicate messages replay the previously
serialized response; they never generate a new handshake state or nonce. After
a responder finishes, its receive loop continues answering duplicate Finish
messages with the original Ready, handling loss of the final confirmation.
Data messages do not gain retransmission from this handshake mechanism.

## Encrypted messages

The wire ciphertext is an 8-byte big-endian sequence followed by ChaCha20-Poly1305
ciphertext and its 16-byte authentication tag. The transcript binding and sequence
are authenticated associated data. Sequence values start at one and are never
reused, including after a failed transport send. The Noise cipher defines nonce
encoding for ChaCha20-Poly1305. A 1024-message replay window permits reordering.
Unauthenticated messages never move the window. Keys/nonces are never persisted;
reconnection always requires a fresh handshake.

A session refuses encryption after one hour or before sequence `2^32` is reached.
The Link manager must replace it using a fresh handshake; automatic rotation is
not yet integrated. The data interface is bounded by the overlay maximum MTU,
header size and one application message-type byte.

## Transport framing

Streams use an unsigned 32-bit big-endian length followed by one complete message,
with a maximum of 16 KiB. Lengths are checked before allocation. Reads and writes
have context cancellation; any partial I/O error closes the stream because its
frame boundary cannot safely be recovered. Concurrent writes cannot interleave.

UDP datagrams use four magic/version bytes (`GWD`, `1`), a random 16-byte
connection token and one complete message. A data socket multiplexes independent
connections by remote address and token. This token is a demultiplexing hint,
not authentication. Handshakes still verify graph policy and peer identities.
The socket allows at most 512 peers, 64 pending accepts, and 64 queued messages
per peer. Unauthenticated peers expire after 10 seconds; authenticated peers
expire after two minutes without a validated keepalive/data message. Invalid
packets cannot keep a peer alive. A slow reader drops UDP messages rather than
allowing an unbounded queue. STUN and rendezvous integration remains pending.

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
