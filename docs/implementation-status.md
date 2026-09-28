# Full-scope implementation and acceptance tracker

`[ ]` is incomplete or not yet verified. A module's existence is not acceptance.
Every feature below derives from the accepted proposal, including its suggestions.

## Foundation

- [x] Validated Network / Agent / Node / Endpoint / Edge models, stable IDs.
- [x] Deterministic weighted routing, isolated Nodes, disabled Edges, equal costs.
- [x] Per-agent topology compilation with peer identities and address directory.
- [x] Versioned bounded packet header, network isolation, hop limit, flow identity.
- [x] Atomic persistent revision transactions and restart recovery.

## Controller

- [x] Runnable server CLI, durable database, graceful shutdown, TLS setup.
- [x] Password login/logout, session expiration, CSRF and login throttling.
- [x] Expiring single-use enrollment; agent authentication and revocation.
- [x] Multi-network/node/edge/endpoint CRUD with concurrency conflict handling.
- [x] Persistent control connection, config push, applied ACK and recovery.
- [ ] Rendezvous signaling and observed endpoint exchange (never packet relay).
- [ ] Batched live telemetry, stale/offline state and bounded event streaming.

## Agent and forwarding

- [ ] Runnable agent CLI, durable private identity and cached configuration.
- [ ] Validate → persist → reconcile → ACK; old runtime retained on failed updates.
- [ ] Per-network TUN, address/route setup and safe resource cleanup.
- [ ] Actual multi-hop IP forwarding and network/source admission enforcement.
- [ ] TCP framing, UDP transport, QUIC datagrams, WS/WSS, gRPC bidirectional stream.
- [ ] Authenticated ephemeral peer keys, cipher policy, replay protection/rekey.
- [ ] Physical interface discovery and changes; automatic TCP/UDP endpoints only.
- [ ] Manual hostname/path endpoints and configurable listeners (default 24752).
- [ ] STUN from the data socket; UDP and supported TCP hole punching.
- [ ] IPv4/IPv6 direct and punch allowlists, bounded scheduling/backoff.
- [ ] All viable Links retained; one active Link, preferred/lowest RTT selection.
- [ ] Heartbeat, RTT/loss/traffic metrics, hysteresis and standby failover.
- [ ] Controller outage continuity and autonomous peer reconnection.
- [ ] MTU handling, bounded queues, malformed packet rejection and hop limit.

## Management UI

- [ ] React + pnpm production build embedded into server.
- [ ] Login, multiple networks, enrollment and membership management.
- [ ] Interactive topology graph with observe/edit modes and persisted positions.
- [ ] Node/Edge inspectors covering every configuration option.
- [ ] Live node status, Link details, latency, traffic and errors.
- [ ] Accessible responsive layout, loading/empty/error states, browser verification.

## Delivery

- [ ] Windows, Linux, macOS and BSD adapters and documented support matrix.
- [ ] Common architecture build matrix; native integration evidence where available.
- [ ] Unit/race/integration/E2E tests, formatting, vet and frontend checks in CI.
- [ ] Packaging, reproducible build commands, deployment/service examples.
- [ ] User/API/protocol documentation and realistic security/operational limitations.
- [ ] Final requirement-by-requirement audit with linked evidence.

## Verification evidence

- Foundation: `go test -race ./...` and `go vet ./...` pass. Tests cover
  weighted A–B–C forwarding, equal-cost order independence, disconnected Nodes,
  disabled/revoked peers, competing editors and persistence after database reopen.
- Packet codec/framing and IP inspection are implemented and tested. Runtime
  network admission remains pending, so the combined acceptance item stays open.

- Controller integration: real TLS 1.3 HTTP/WebSocket tests exercise cookie/CSRF
  admission, optimistic edits, CSR enrollment, atomic concurrent token consumption,
  config push, ACK telemetry and immediate revocation/reconnect rejection.
  The Agent runtime and its restart recovery are still pending; the control-connection
  acceptance item remains open until both sides are exercised together.

- CLI smoke: built binary, trusted generated CA, queried HTTPS health, logged in,
  checked Secure cookie and verified clean SIGTERM exit.
- Packet fuzzing: 595,356 frame-parser executions and 596,817 IP-parser executions
  completed without a failure (5 seconds per target; this is bounded evidence).
- Controller cross-builds pass for Windows/amd64, macOS/arm64, FreeBSD/amd64 and
  Linux/arm64. This does not yet verify native TUN support or the whole platform
  matrix, which remain open acceptance items.

- Agent control/cache: real controller/client integration verifies enrollment,
  mTLS, revision push, durable-before-apply ordering, applied ACKs, server outage
  retaining runtime configuration, and agent restart from cache with the server
  unavailable. A failed runtime update retains the previous applied snapshot and
  retries the desired revision. This uses a test runtime; actual TUN/data forwarding
  outage behavior remains unverified and its acceptance item stays open.
- Enrollment response loss: an exact idempotent retry recovers the original
  certificate and Agent ID without consuming a second revision or identity.

- Peer security: Noise XX and Ed25519 binding, separate directional keys,
  tamper rejection, topology/transport binding, out-of-order replay window,
  concurrent nonce uniqueness and session age/message limits are tested.
- Actual TCP/UDP sockets: authenticated channel round trips pass, including loss
  of each UDP handshake message and duplicate handshake/data delivery. Queue and
  connection bounds are implemented. Other transport adapters and automatic
  session replacement remain pending.
- Forwarding engine: IP packets traverse A→B→C and C→B→A over actual encrypted
  TCP and UDP channels. Tests reject unknown Networks, nonadjacent senders,
  mismatched IP/Node addresses, spoofed local sources and exhausted hop limits.
  Native TUN delivery and full Agent runtime integration are still pending.

- Handshake input fuzzing completed 33,365 executions without failure. This
  exercises bounded malformed-message parsing, not a cryptographic proof.
