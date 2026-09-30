# Controller API (v1)

All routes are under `/api/v1`. Bodies and responses are JSON, except the browser event stream. Unknown request
fields, multiple JSON values and bodies larger than 8 MiB are rejected. Error
responses are `{ "error": "message" }`. HTTPS is the default; agent control
requires TLS 1.3 and a controller-issued client certificate.

The [administrative CLI](admin-cli.md) uses these session and revision contracts
for Network creation/listing, Node listing and Edge creation.

## Browser session

`POST /login` accepts `{ "password": "..." }`. On success the server sets a
12-hour HttpOnly, SameSite=Strict cookie (Secure under HTTPS) and returns
`{ "csrf_token": "..." }`. Keep that token in browser memory and send it as
`X-CSRF-Token` on every authenticated mutation. `GET /session` retrieves it after
a page reload. `POST /logout` invalidates the session. Sessions are intentionally
invalidated when the server restarts. Cross-origin browser requests are rejected.
Login attempts are limited per remote address; password verification concurrency
and session counts are bounded. Forwarded IP headers are not trusted.

## Desired state

`GET /state` returns schema, revision, agents and networks. `ETag` contains the
quoted decimal revision. Every topology mutation requires `If-Match` with that
revision. Missing preconditions return 428; stale revisions return 409 without
modification. Validation failures return 422 without consuming a revision.
Successful mutations return the committed state and new ETag.

| Method | Path | Body / semantics |
| --- | --- | --- |
| POST | `/networks` | Network; omitted ID, MTU and cipher receive defaults |
| PUT | `/networks/{id}` | Complete Network, including Nodes and Edges |
| DELETE | `/networks/{id}` | Removes network and all memberships/edges |
| PATCH | `/agents/{id}` | Optional `name`, `listen_port`, `revoked`, `manual_endpoints`, `stun_servers`, `exclude_container_ips` |
| DELETE | `/agents/{id}` | Removes agent, memberships and incident edges |

Network fields are `id`, `name`, `cidr`, `mtu`, `cipher`, `nodes`, `edges`.
IDs are 32 lowercase hexadecimal characters. Nodes have `id`, `agent_id`, `name`,
`address`, and `position: {x,y}`. Missing Node/Edge IDs are generated, but when
creating both Nodes and referencing Edges in one request, assign Node IDs first.
An Agent can be a member of many networks, once each, without overlapping CIDRs.

Edges have `id`, `a`, `b`, positive `weight`, `enabled`, `transports`,
`methods: {ipv4_direct, ipv6_direct, hole_punch}`, and optional
`preferred_candidate`. Transports are `udp`, `tcp`, `quic`, `ws`, `wss`, `grpc`.
Supported cipher suites are `aes-128-gcm`, `aes-256-gcm`,
`chacha20-poly1305` (default), and `xchacha20-poly1305`. A cipher change
replaces the affected peer sessions; peers must support the configured suite.

Validation permits at most 10,000 Nodes and 100,000 Edges per Network, 64 total
endpoints per Agent (manual plus discovered), and four STUN services per Agent.
A complete Network write must also fit the 8 MiB request limit; the individual
count ceilings do not imply that every maximum-size combination fits, or promise
that workload's performance. Names are 1–128 bytes without control characters.
Network MTU is 1280–9000; platform adapters may impose a lower native ceiling,
as documented for NetBSD.

Manual endpoints have `id`, `transport`, `url` and `source: "manual"`. URLs require
an explicit port. WS/WSS/gRPC may include a path. Automatic endpoints cannot be
edited by a browser. `PATCH` replaces the complete manual endpoint list while
retaining agent-discovered entries. WS/WSS paths are literal upgrade paths;
gRPC paths are prefixes for `/graphwan.v1.Peer/Connect`. GraphWAN gRPC endpoints
always dial TLS. See [the gRPC endpoint guide](grpc-operation.md).

## Enrollment

An authenticated administrator calls `POST /enrollment-tokens` with
`{ "ttl_seconds": 3600 }` (60–86400 allowed). The response contains the one-time
`token` and `expires_at`. Only a hash of the token is stored.

An Agent generates its own Ed25519 key and DER CSR, then calls `POST /enroll`
with `Authorization: Bearer <token>` and `{ "name": "...", "csr": "<base64 DER>" }`.
The response contains `agent_id`, `certificate` (base64 PEM), `ca_certificate`
(base64 PEM), and committed `revision`. Identity registration and token consumption
are atomic. Invalid enrollment does not consume a valid token. Duplicate public
keys cannot enroll as separate Agents. Agents send a stable `Idempotency-Key` for
each enrollment attempt. If the response is lost, retrying the exact same CSR,
name, token and idempotency key returns the original identity and certificate
without creating a new revision. The recovery receipt expires with the token;
a different identity or request cannot reuse it.

The CA must already be trusted when making the enrollment HTTPS request; a CA
returned in a response is not a substitute for authenticating the server.

## Agent connection

### Control transport carriers

On first enrollment, the Agent selects its controller carrier with `--server-transport` or
`GRAPHWAN_SERVER_TRANSPORT`; an explicit flag overrides the environment.
The default is `tcp`. The controller automatically accepts all four carriers
on its one configured TCP listen port (8443 by default):

| Selection | Control protocol, from inside to outside |
| --- | --- |
| `tcp` | WebSocket → mTLS → TCP |
| `websocket` | WebSocket → mTLS → WebSocket → TCP |
| `grpc` | WebSocket → mTLS → gRPC → HTTP/2 → TCP |
| `wss` | WebSocket → mTLS → WebSocket → outer TLS → TCP |

For example:

```sh
graphwan agent --server https://controller.example.com:8443 \
  --server-transport grpc --ca ./ca.pem --data-dir ./agent-data
```

`--server` remains an HTTPS origin identifying the inner authenticated service,
even when the outer carrier is plaintext WebSocket or HTTP/2. Enrollment HTTPS
requests use that same carrier: the Agent verifies the server before sending a
token, then uses its issued certificate for subsequent mTLS control connections.
After enrollment, both `--server` and `--server-transport` are ignored. The
persistent Server directory determines subsequent carriers and failover targets.
Directory changes preserve the cached identity and network configuration. There is no
automatic transport downgrade. WSS verifies the outer server certificate using
the same trusted roots and hostname, independently of inner TLS verification.

The outer WebSocket endpoint is `/api/v1/agent/tunnel`, with required subprotocol
`graphwan.control.v1`. Binary message payloads are concatenated into a TLS byte
stream; each message contains 1–32,768 bytes. Text messages, empty/oversized
messages, URL queries and browser Origin headers are rejected. Compression is
disabled. WebSocket message boundaries have no meaning to the inner TLS protocol.

Outer gRPC uses plaintext HTTP/2 (h2c) with the bidirectional method
`/graphwan.control.v1.Tunnel/Connect`, defined in
[control_tunnel.proto](../api/control_tunnel.proto). Each `BytesValue` contains
1–32,768 TLS bytes; encoded protobuf messages are capped at 32,772 bytes.
Compression is not enabled. The response metadata must include
`graphwan-protocol: graphwan-control-v1`. This only acknowledges the carrier;
it never authenticates an Agent. A separate HTTP/2 connection carries each
underlying control connection. This schema differs from the Agent-to-Agent gRPC
peer protocol, whose values contain complete peer messages rather than TLS bytes.

The public listener classifies direct TLS and plaintext HTTP. Plaintext HTTP
exposes only the outer tunnels, never the panel, enrollment or management APIs.
The normal HTTPS panel remains available on the same port. Inner TLS is handled
by the original controller API and does not expose another tunnel endpoint.
Client-certificate headers from proxies are never used as Agent authentication.
Initial classification has a five-second deadline, inner TLS setup ten seconds,
and at most 128 classifications/inner TLS handshakes may be pending. Established
tunnels do not consume this pending allowance. Byte adapters use bounded chunks
and backpressure, honor deadlines and close with their inner connection.

An explicit reverse proxy must preserve the WebSocket path/subprotocol or the
gRPC method, trailers and response metadata. Its upstream can be plaintext
WS/h2c because the inner TLS remains intact. The configured controller hostname
must still validate against the inner server certificate (`--tls-hosts`). The
loopback-only `server --http` development mode does not provide these TLS tunnels.

### Control messages

`GET /agent/control` upgrades to a WebSocket after validating the client
certificate against the CA **and** the current Agent identity/revocation state.
The controller sends `ControlMessage` JSON envelopes:

- `{"type":"config","snapshot":{...}}`: initial and updated compiled snapshot.
- `{"type":"heartbeat"}`: liveness message every 15 seconds when unchanged.

The Agent checks telemetry every 2 seconds. User traffic, link/state changes and
errors trigger reports at that cadence; applied configuration is ACKed promptly.
When idle, a full report is sent approximately every 15 seconds, including in
response to controller heartbeats. One final unchanged sample clears displayed
traffic rates when transfers stop. CPU, memory, RTT and loss samples alone do not
trigger extra reports. A 45-second receive timeout detects a silent connection:

- `{"type":"ack","report":{"version":"...","applied_revision":N,"links":[],"config_error":"..."}}`
- `{"type":"endpoints","endpoints":[...]}`: full discovered endpoint set.

The inner control WebSocket negotiates per-message DEFLATE without context
takeover for messages above 512 bytes. Older peers can decline compression;
the JSON protocol remains unchanged. Small heartbeats remain uncompressed, and
neither encrypted outer carriers nor data-plane packets are compressed.
Identical discovered endpoint sets are not retransmitted; stable observed leases
are renewed when at most one minute remains. Reconnect always republishes the
current set. Actual endpoint changes are published immediately after discovery.

`applied_revision` is the successfully reconciled revision, not simply receipt.
An Agent may acknowledge an older revision while applying an update. Its old Link
statistics are omitted until it catches up. Endpoint updates may only contain
`interface` and `observed` TCP/UDP entries; manual entries remain authoritative
on the controller. Observed endpoints include expiration and mapped port.
`exclude_container_ips` is an optional boolean, default false. On Linux it filters
container veth endpoints while retaining an unbridged default-route uplink.
Omission preserves the saved setting. Manual endpoints are unaffected.

`stun_servers` is an array of at most four service addresses: bare `host:port` or
`udp://host:port` for UDP, and `tcp://host:port` for TCP. Hostnames and bracketed IPv6
literals are supported. Duplicate normalized addresses within a transport are
rejected; TCP and UDP may share a numeric address. Settings are centrally
configured and included only in that Agent's snapshot. An empty array disables
STUN discovery; omission preserves the setting. See [NAT operation](nat-operation.md)
for refresh, expiration and offline behavior.
Snapshots contain only this Agent's memberships, adjacent peers, destination
addresses and compiled routes. Revocation closes live connections and removes
that Agent from other peers' compiled connectivity.

`GET /telemetry` returns the latest batched reports with `last_seen` and
`connected`; stale reports become offline after 45 seconds. These are observed
runtime values and do not mutate desired topology. `config_error` reports a failed
configuration application. `runtime_error` independently reports current TUN
failure/recovery problems (at most 4096 bytes); it clears after local recovery.
A runtime error does not roll back `applied_revision` or imply that healthy peer
Links have disconnected.
Reports can also include optional process `resources`; see [resource telemetry](resource-telemetry.md)
for field definitions, CPU units, missing samples and stale-data handling.

## Browser live snapshots

`GET /events` returns `text/event-stream`, authenticated by the same browser
session cookie and same-origin rules. Each `snapshot` event has:

```json
{"at":"2026-09-28T12:00:00Z","revision":42,"state":{"schema":1,"revision":42,"agents":[],"networks":[]},"agents":[]}
```

`agents` contains the same current reports as `/telemetry`. `state` is present
on initial connection and whenever desired revision changes; otherwise it is
omitted. Events are replaceable snapshots at one-second intervals, not a replay
log. Reconnect always gets full state. Report `last_seen` timestamps, rather than
event timestamps, determine rates from byte-counter differences. Session changes,
counter resets and stale/offline samples must not produce spurious traffic rates.

There are at most 32 browser streams per controller and 4 per session; extra
connections receive 429 with `Retry-After: 5`. Each stream retains only its current
snapshot and has a five-second write deadline. HTTP writes occur outside shared
status locks. Session expiry, logout (checked at each tick), client disconnect
and controller shutdown terminate the stream. `X-Accel-Buffering: no` requests
that reverse proxies deliver events without buffering.

Offline/revoked Agents' historical Link counters may remain visible, but their
Links are not marked healthy or active. Removed/disabled Edges are filtered out
immediately against current desired state, even before a new Agent report arrives.

## Server clusters

HTTPS controllers advertise `cluster_id`, public `cluster_ca` (base64 PEM) and
`servers` in `/state`. Each Server has `id`, `name`, `public_key`, `endpoints`, and
`stun_servers`. Endpoints have `id`, `transport` (`tcp`, `websocket`, `grpc`, `wss`),
`url`, `source` and optional `expires_at`. URL schemes are respectively `tcp`, `ws`,
`grpc`, `wss`; use an explicit port and no path, credentials, query or fragment.
Automatic endpoints are TCP only and use Agent interface discovery with container
filtering enabled: virtual NICs and container attachment interfaces are excluded,
while a containerized Server keeps its default-route uplink. An explicitly bound
loopback listener remains available for local development. Bound non-loopback
addresses must also pass filtering. Existing manual endpoints are retained.
TCP STUN defaults to `tcp://stun.nextcloud.com:443`;
an empty STUN list disables public mapping discovery. Each Server supports 64
entrances and a cluster supports 64 Servers.

| Method | Path | Body / semantics |
| --- | --- | --- |
| GET | `/servers/status` | Local Server ID, coordinator ID, voting member count and Raft indices |
| PATCH | `/servers/{id}` | Optional `name`, `manual_endpoints`, `stun_servers`; requires `If-Match` |
| POST | `/servers/invitation` | Returns a one-hour, single-identity `invitation`; requires admin session and CSRF |
| POST | `/servers/join` | `{ "invitation": "..." }`; only on an empty standalone Server, requires admin session and CSRF |

Joining returns 202 and automatically restarts the local controller. Log in again
using the cluster's administrator password. The invitation contains the trusted
CA, source Server directory and bearer secret; transferring it authorizes sharing
cluster secrets with the joining Server. The join exchange verifies source TLS
before sending the token. Exact retries recover the same Server certificate.
Existing configured clusters cannot be merged through this endpoint.

Raft commits configuration, CA and security-record transactions together. Any
Server forwards writes to the elected coordinator and waits for local application
before acknowledging them. An unavailable majority produces HTTP 503. Two voters
require both online; three tolerate one failure. Losing control quorum does not
stop existing Agent data forwarding. Reads can reflect a follower's latest
committed state while it catches up. Telemetry aggregates recent reports from
all reachable Servers; browser login sessions are local.

Enrollment responses and Agent configuration snapshots include `servers` as a
Server directory `{cluster_id, revision, ca, servers}`. Agents persist it, reject
CA/cluster changes and revision rollback, and try alternative entrances when a
connection fails. Only first registration uses bootstrap flags; registered Agents
can start without `--server`, `--server-transport`, or `--ca` once trust has been
cached. Legacy registrations keep their saved origin until the first directory
is received (supply the old CA file for this migration). Directory TLS verifies a
CA-issued stable per-Server name, allowing discovered addresses to change without
re-enrollment. Outer WSS uses the same name and CA.

Internal `/cluster/raft`, `/cluster/commit` and `/cluster/status` routes require
Server membership plus an authenticated Server certificate. Agent credentials
cannot invoke replication RPCs. These routes share the existing HTTPS/carrier
port; no separate Raft listener is needed. Plaintext development mode has no
cluster management or Agent control.

Upgraded Servers negotiate the WebSocket subprotocol `graphwan.cluster.v1` on
`/cluster/raft`. The authenticated byte stream carries yamux streams, and both
ends can open streams on that same physical connection. A stream starts with
one type byte: `1` carries the existing Raft network-transport framing; `2`
carries a JSON request with `path` and an optional `command`, followed by one
JSON response. Only the cluster status and commit operations use type `2`.
These streams inherit the authenticated Server identity, with membership checks
before use; they do not expose the general administrative API.

Either Server can initiate the connection. Incoming channels are immediately
available for reverse Raft RPCs, forwarded writes and status queries. Simultaneous
dials converge on the connection initiated by the lower Server ID. Keepalives
detect dead channels, and membership polling reconnects using the available
entrances. Removing or revoking a member closes its channel. For each pair,
at least one direction must be reachable; there is no inter-Server relay.

A peer that does not negotiate this subprotocol uses the original Raft stream
and HTTPS commit/status requests. This permits rolling upgrades, but one-way
reachability requires both peers to run the upgraded version. The transport
change preserves Raft's existing majority-write and election rules.
