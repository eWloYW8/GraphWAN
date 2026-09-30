# GraphWAN architecture

GraphWAN is a centrally configured, peer-forwarded overlay. The controller never
forwards user packets. `proposal.md` is the accepted product scope; this document
records concrete implementation decisions, not a reduction of that scope.

## Domain and ownership

An Agent is an enrolled installation with an Ed25519 identity. A Node is that
Agent's membership in one Network, with a fixed virtual IP and a TUN interface.
A Network has a canonical CIDR, MTU, cipher suite, Nodes and undirected Edges.
An Edge authorizes connectivity between exactly two Nodes and has a positive
integer weight, transport/method allowlists and an optional preferred candidate.
Candidate identifiers are stable across reconnects; live Link identifiers are not.
Healthy Links on distinct endpoint-IP/transport paths remain open. When both
Agents support address exchange, sessions differing only in endpoint ports or
dialing direction are consolidated into one healthy Link per path; suppressed
candidates resume dialing if that Link fails. Automatic selection uses the
lowest measured RTT across retained candidates. Only the selected Link sends user traffic; heartbeats
and negotiation may use standby Links. A Session is the active Link of an Edge.

Automatic endpoints use TCP/UDP only. Manual endpoints can use TCP, UDP, QUIC,
WS, WSS and gRPC, including DNS names and URL paths. Observed NAT endpoints
include the mapped port and expire; they are not arbitrary public-IP guesses.
Discovery excludes virtual interfaces. Candidate generation is bounded and
retries with backoff; an unsuccessful candidate does not block other candidates.

## Configuration and routing

The controller validates and persists a complete desired-state transaction before
publishing a new monotonically increasing revision. Edits require the expected
revision, so concurrent browser sessions cannot silently overwrite each other.
Per-agent snapshots contain adjacent peer connection policy, a destination
address directory and deterministic shortest-path forwarding tables. Isolated
Nodes are valid; no connectivity is promised across disconnected components.
Equal-cost paths use stable Node-ID ordering. Edge latency never changes routing
weight. Disabled Edges do not participate in routing or dialing.

Agents validate, persist, reconcile, then acknowledge snapshots. Desired and
applied revisions are distinct. The last durable snapshot is usable after an
agent restart while the controller is absent. Controller disconnection does not
close TUNs, peer Links or timers. Existing endpoints permit autonomous reconnect;
changed NAT mappings can require renewed rendezvous. No system can guarantee
hole punching through every NAT without relays, and GraphWAN has no relay mode.

Route changes carry a revision/epoch. A bounded overlay hop limit contains loops
during asynchronous updates. Packet forwarding must reject unknown networks,
unknown destinations, invalid lengths and unsupported wire versions. A later
coordinated route activation can improve convergence without changing this wire
contract. Network reachability and an individual Edge's standby failover are
separate concerns; telemetry must expose both.

## Security

Enrollment is authorized by expiring, single-use tokens. Agent private identity
keys stay on the agent. The controller issues client identities; control uses TLS
and client authentication. Peer connections authenticate the identities permitted
by the compiled Edge configuration. Network-wide cipher selection does not mean
network-wide shared private keys. Transport-independent authenticated encryption
uses ephemeral session secrets, direction-specific keys, unique nonces and replay
protection. Intermediate forwarding Nodes are trusted to see hop-decrypted packets;
end-to-end confidentiality is not claimed by this hop-encrypted design.

Browser authentication uses a salted password hash and bounded sessions with
HttpOnly cookies. State-changing requests require same-origin/CSRF protection.
Authentication, enrollment and frame parsing are bounded against resource abuse.
Telemetry and desired state use separate models and storage lifetimes.

## Runtime boundaries

- `internal/model`: validated desired state and compiled snapshot contracts.
- `internal/routing`: deterministic weighted shortest-path compilation.
- `internal/store`: durable transactional state; no runtime objects are persisted.
- `internal/control`: authenticated HTTP/control API and snapshot distribution.
- `internal/packet`: versioned packet framing, validation and IP inspection.
- Agent modules: identity, control reconciliation, tunnel, candidate discovery,
  transport, link health/selection, NAT rendezvous, encryption and forwarding.
- React/pnpm frontend: observe/edit topology, inspector forms and live telemetry;
  production assets embedded into the Go server.

Stream transports frame complete overlay packets. UDP preserves datagram
boundaries. Queues, frame sizes, dial concurrency and reconnect frequency are
bounded. MTU defaults to 1280 and remains an explicit network setting. Platform
TUN adapters own interface addresses/routes and clean up only their own resources.

## Verification

The small core unit suite covers graph invariants, deterministic routing, packet
framing, authenticated encryption, replay handling and transactional rollback.
Version-tag CI builds the frontend and cross-platform binaries, then publishes a
Release; see [release CI and local checks](continuous-integration.md).

Standards consulted: [ICE (RFC 8445)](https://www.rfc-editor.org/rfc/rfc8445),
[STUN (RFC 8489)](https://www.rfc-editor.org/rfc/rfc8489), and
[Go TLS documentation](https://pkg.go.dev/crypto/tls). GraphWAN uses discovery and
connectivity checks without a TURN data relay; it does not claim full ICE support.
