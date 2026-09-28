# Controller API (v1)

All routes are under `/api/v1`. Bodies and responses are JSON. Unknown request
fields, multiple JSON values and bodies larger than 8 MiB are rejected. Error
responses are `{ "error": "message" }`. HTTPS is the default; agent control
requires TLS 1.3 and a controller-issued client certificate.

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
| PATCH | `/agents/{id}` | Optional `name`, `listen_port`, `revoked`, `manual_endpoints` |
| DELETE | `/agents/{id}` | Removes agent, memberships and incident edges |

Network fields are `id`, `name`, `cidr`, `mtu`, `cipher`, `nodes`, `edges`.
IDs are 32 lowercase hexadecimal characters. Nodes have `id`, `agent_id`, `name`,
`address`, and `position: {x,y}`. Missing Node/Edge IDs are generated, but when
creating both Nodes and referencing Edges in one request, assign Node IDs first.
An Agent can be a member of many networks, once each, without overlapping CIDRs.

Edges have `id`, `a`, `b`, positive `weight`, `enabled`, `transports`,
`methods: {ipv4_direct, ipv6_direct, hole_punch}`, and optional
`preferred_candidate`. Transports are `udp`, `tcp`, `quic`, `ws`, `wss`, `grpc`.
The current cipher suite is `chacha20-poly1305`.

Manual endpoints have `id`, `transport`, `url` and `source: "manual"`. URLs require
an explicit port. WS/WSS/gRPC may include a path. Automatic endpoints cannot be
edited by a browser. `PATCH` replaces the complete manual endpoint list while
retaining agent-discovered entries.

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

`GET /agent/control` upgrades to a WebSocket after validating the client
certificate against the CA **and** the current Agent identity/revocation state.
The controller sends `ControlMessage` JSON envelopes:

- `{"type":"config","snapshot":{...}}`: initial and updated compiled snapshot.
- `{"type":"heartbeat"}`: liveness message every 15 seconds when unchanged.

The Agent replies at least once every 45 seconds:

- `{"type":"ack","report":{"version":"...","applied_revision":N,"links":[],"config_error":"..."}}`
- `{"type":"endpoints","endpoints":[...]}`: full discovered endpoint set.

`applied_revision` is the successfully reconciled revision, not simply receipt.
An Agent may acknowledge an older revision while applying an update. Its old Link
statistics are omitted until it catches up. Endpoint updates may only contain
`interface` and `observed` TCP/UDP entries; manual entries remain authoritative
on the controller. Observed endpoints include expiration and mapped port.
Snapshots contain only this Agent's memberships, adjacent peers, destination
addresses and compiled routes. Revocation closes live connections and removes
that Agent from other peers' compiled connectivity.

`GET /telemetry` returns the latest batched reports with `last_seen` and
`connected`; stale reports become offline after 45 seconds. These are observed
runtime values and do not mutate desired topology.
