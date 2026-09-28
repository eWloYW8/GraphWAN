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
- [x] Batched live telemetry, stale/offline state and bounded event streaming.
- [ ] Agent CPU/resource telemetry and its UI display from the accepted suggestions.

## Agent and forwarding

- [x] Runnable agent CLI, durable private identity and cached configuration.
- [x] Validate → persist → reconcile → ACK; old runtime retained on failed updates.
- [ ] Per-network TUN, address/route setup and safe resource cleanup.
- [x] Actual multi-hop IP forwarding and network/source admission enforcement.
- [x] TCP framing, native UDP and WS/WSS binary-message transports.
- [x] gRPC bidirectional streams through manual endpoints.
- [ ] QUIC datagrams.
- [x] Authenticated ephemeral peer keys, cipher policy, replay protection/rekey.
- [ ] Physical interface discovery and changes; automatic TCP/UDP endpoints only.
- [ ] Manual hostname/path endpoints and configurable listeners (default 24752).
- [ ] STUN from the data socket; UDP and supported TCP hole punching.
- [ ] IPv4/IPv6 direct and punch allowlists, bounded scheduling/backoff.
- [ ] All viable Links retained; one active Link, preferred/lowest RTT selection.
- [x] Heartbeat, RTT/loss/traffic metrics, hysteresis and standby failover.
- [x] Common active-Link agreement for implemented transports, including rekey.
- [x] Controller outage continuity and autonomous peer reconnection.
- [ ] MTU handling, bounded queues, malformed packet rejection and hop limit.

## Management UI

- [x] React + pnpm production build embedded into server.
- [x] Login, multiple networks, enrollment and membership management.
- [x] Interactive topology graph with observe/edit modes and persisted positions.
- [x] Node/Edge inspectors covering every configuration option.
- [x] Live node status, Link details, latency, traffic and errors.
- [x] Accessible responsive layout, loading/empty/error states, browser verification.

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
- Packet codec/framing, IP inspection and runtime network admission are verified
  by parser, forwarding-engine and real encrypted channel tests.

- Controller integration: real TLS 1.3 HTTP/WebSocket tests exercise cookie/CSRF
  admission, optimistic edits, CSR enrollment, atomic concurrent token consumption,
  config push, ACK telemetry and immediate revocation/reconnect rejection.
  The real Agent client and native Linux runtime are now exercised together by
  the isolated process-level test described below.

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
  retries the desired revision. These component tests use a test runtime; native
  TUN outage behavior is additionally verified by the process-level test below.
- Enrollment response loss: an exact idempotent retry recovers the original
  certificate and Agent ID without consuming a second revision or identity.

- Peer security: Noise XX and Ed25519 binding, separate directional keys,
  tamper rejection, topology/transport binding, out-of-order replay window,
  concurrent nonce uniqueness and session age/message limits are tested.
- Actual TCP/UDP sockets: authenticated channel round trips pass, including loss
  of each UDP handshake message and duplicate handshake/data delivery. Queue and
  connection bounds are implemented. The QUIC adapter remains pending;
  automatic session replacement is covered by a real-socket test below.
- Forwarding engine: IP packets traverse A→B→C and C→B→A over actual encrypted
  TCP and UDP channels. Tests reject unknown Networks, nonadjacent senders,
  mismatched IP/Node addresses, spoofed local sources and exhausted hop limits.
  Native TUN delivery and full Linux Agent integration are now verified below.

- Handshake input fuzzing completed 33,365 executions without failure. This
  exercises bounded malformed-message parsing, not a cryptographic proof.

- Linux native adapter: `go test -c -tags integration ... ./internal/tunnel`, run
  with `sudo unshare --net`, verifies address/route/MTU setup, exclusive ownership,
  kernel-to-Agent and Agent-to-kernel UDP packets, interruptible idle reads, and
  interface deletion. The test caught and now guards against registering the TUN
  descriptor with Go's poller before `TUNSETIFF`.
- Native process E2E: `tests/e2e_linux.py` starts a real controller and three real
  Agent CLI processes in separate network namespaces with veth underlay and TUN
  overlay interfaces. It verifies multi-hop ICMP, a 155,648-byte TCP echo exchange,
  continued traffic after controller shutdown, transit-Agent restart from durable
  cache while offline, and owned-interface cleanup at process shutdown.
- Mesh/Link: real UDP socket failure falls back to already established TCP Links;
  an accelerated rotation test repeatedly replaces authenticated sessions while
  traffic continues. Unit tests cover preferred-path recovery, RTT hysteresis,
  heartbeat failure, packet-queue bounds and candidate method/family policy.
- Common active Link: authenticated prepare/accept/commit/confirm negotiation
  selects the same session at both endpoints. Tests drop each control phase,
  inject stale decisions, use contradictory local RTTs, restart the selector,
  switch preferences, fail and recover Links, and invalidate queued data across
  reactivation. Real UDP-to-TCP fallback checks matching Link IDs at both ends.
  Ten race-enabled Link/Mesh test repetitions passed. Native process E2E also
  passed with common selection, including controller outage and offline restart.
  Candidate discovery and other transports still need their separate acceptance.
- Linux discovery covers global-unicast IPv4/IPv6 addresses on devices and
  container veth interfaces, excluding TUN/TAP and bridges by link type. Link-local
  scope mapping, other operating systems and STUN-discovered addresses are pending.

- Browser management: the pnpm/React production build is embedded and served by
  the Go binary. Playwright against an actual temporary Go controller verifies
  password login, token creation, real CSR enrollment, network/membership/edge
  editing, persisted graph drag positions, concurrent-editor conflict protection,
  manual endpoints, deletion and logout. Desktop and 390px mobile layouts are
  rendered and inspected; axe checks the tested topology pages against WCAG A/AA
  rules. This is bounded accessibility evidence, not a complete manual audit.
- Telemetry UI: a separate browser fixture exercises active Link agreement, RTT,
  report-time traffic rates, duplicate samples, path preference and connection
  loss. Go TLS integration independently verifies actual mTLS Agent reports and
  revocation reaching browser events, authentication/origin rejection, connection
  limits, session expiry/logout and controller shutdown. Slow writes have deadlines
  and cannot hold status locks; no unbounded event queue is retained.
- The QUIC adapter and full NAT punching are still pending, even
  though their desired policies can now be configured in the management UI.

- WS/WSS: real binary-message, TLS and mesh tests verify message limits,
  cancellation, shared TCP/WS/WSS listener, configured-path admission, preferred
  Link changes, endpoint removal, bounded slow HTTP admission and shutdown.
  Proxy tests exercise CA-validated TLS termination with an independent proxy key
  and authenticated mesh connections through IPv6-to-IPv4 WS/WSS proxies.
  Native three-Agent TUN tests pass separately for WS-only and WSS-only Edges,
  including multi-hop ICMP/TCP, controller outage, offline transit-Agent restart
  and owned-interface cleanup. The original automatic TCP/UDP case also passes.

- gRPC: standard protobuf bidirectional streams now carry authenticated peer
  messages through manual endpoints on the shared listener. Real TLS tests cover
  the 16 KiB message boundary, remote protobuf-size rejection, empty messages,
  independent CA-trusted frontends, per-operation and dial cancellation, and
  cancellation under HTTP/2 flow control. Mesh tests cover shared-port coexistence,
  configured-prefix and encrypted candidate admission, endpoint removal, fallback
  to existing standby Links, and IPv6 TLS proxies with IPv4 HTTP/2 backends.
  Shutdown cancels incomplete prefaces before waiting for grpc-go's Serve loop;
  the regression tests pass in five race-enabled repetitions. Native three-Agent
  TUN tests pass with gRPC-only Edges, controller outage and offline transit restart;
  TCP/UDP, WS-only and WSS-only regression cases also pass. Windows/amd64,
  macOS/arm64 and FreeBSD/amd64 core cross-builds pass with the new dependencies;
  their native TUN adapters remain outstanding.
