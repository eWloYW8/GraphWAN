# Full-scope implementation and acceptance tracker

`[ ]` is incomplete or not yet verified. A module's existence is not acceptance.
Every feature below derives from the accepted proposal, including its suggestions.

## Foundation

- [x] Validated Network / Agent / Node / Endpoint / Edge models, stable IDs.
- [x] Deterministic weighted routing, isolated Nodes, disabled Edges, equal costs.
- [x] Per-agent topology compilation with peer identities and address directory.
- [ ] Versioned bounded packet header, network isolation, hop limit, flow identity.
- [x] Atomic persistent revision transactions and restart recovery.

## Controller

- [ ] Runnable server CLI, durable database, graceful shutdown, TLS setup.
- [ ] Password login/logout, session expiration, CSRF and login throttling.
- [ ] Expiring single-use enrollment; agent authentication and revocation.
- [ ] Multi-network/node/edge/endpoint CRUD with concurrency conflict handling.
- [ ] Persistent control connection, config push, applied ACK and recovery.
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
