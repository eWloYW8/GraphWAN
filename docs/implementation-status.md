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
- [x] TCP/UDP observed endpoint exchange and rendezvous through peer snapshots, never relay.
- [ ] Broader NAT/platform acceptance beyond the verified Linux scenarios.
- [x] Batched live telemetry, stale/offline state and bounded event streaming.
- [x] Agent process CPU/Go memory/runtime telemetry with table and Node-detail display.

## Agent and forwarding

- [x] Runnable agent CLI, durable private identity and cached configuration.
- [x] Validate → persist → reconcile → ACK; old runtime retained on failed updates.
- [ ] Per-network TUN, address/route setup and safe resource cleanup on all platforms.
- [x] FreeBSD native IPv4/IPv6 TUN, full-MTU kernel I/O and live MTU reconciliation.
- [x] FreeBSD live address/prefix/MTU edits, multi-Network rollback and applied-state recovery.
- [ ] FreeBSD multi-host/NAT acceptance and older kernels.
- [x] macOS utun adapter code and Intel/Apple Silicon cross-builds.
- [ ] Native macOS utun/route/reconfiguration, multi-host and NAT acceptance.
- [x] Windows Wintun adapter code, pinned DLL retrieval and amd64/arm64/386 cross-builds.
- [ ] Native Windows kernel/Agent, multi-host, discovery and NAT acceptance.
- [x] Local recovery from fatal TUN I/O, independent runtime health and Linux offline recovery.
- [x] Actual multi-hop IP forwarding and network/source admission enforcement.
- [x] Native Linux IPv4/IPv6 overlays and underlays, all six IPv6 transports and mixed families.
- [x] TCP framing, native UDP and WS/WSS binary-message transports.
- [x] gRPC bidirectional streams through manual endpoints.
- [x] QUIC datagrams, shared UDP listener and bounded message fragmentation.
- [x] Authenticated ephemeral peer keys, cipher policy, replay protection/rekey.
- [ ] Physical interface discovery and changes; automatic TCP/UDP endpoints only.
- [ ] Manual hostname/path endpoints and configurable listeners (default 24752), full platform acceptance.
- [x] Per-address DNS Candidates across all six transports, refresh, preference and healthy rekey on Linux.
- [ ] Native Windows UDP reply-source selection for wildcard/multiple-address listeners.
- [x] STUN from the data socket and UDP punching through verified restricted NATs.
- [x] Linux TCP hole punching, independent STUN mappings and pooled authenticated sessions.
- [ ] Remaining NAT cases and native TCP punching support on other platforms.
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
  connection bounds are implemented;
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
  scope mapping and other operating systems remain pending. STUN evidence is below.

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
- UDP and TCP punching are verified below. Broader NAT/platform behavior
  remains outstanding as part of the full acceptance scope.

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

- QUIC: real datagram tests verify IPv4/IPv6, TLS identity admission, legacy
  native UDP compatibility on the shared port, message sizes through 16 KiB,
  reorder/duplicate/loss handling, bounded fragment assembly and expiry,
  cancellation under congestion and socket shutdown. A UDP loss proxy verifies
  that user data is not retransmitted. All six transports remain healthy on one
  Edge and QUIC failure selects an already established standby. Five race-enabled
  rotation test repetitions replace QUIC/TCP sessions while forwarding continues.
  Fragment-parser fuzzing completed 162,829 executions without failure (5 seconds;
  bounded evidence, not a proof). Native QUIC-only and native UDP-only runs each
  pass with 9000-byte overlay MTU, 1280-byte underlay MTU, full-sized ICMP packets,
  TCP transfer, controller outage, offline transit restart and interface cleanup.
  QUIC also passes the default-MTU run and a wildcard listener receiving on a
  secondary IPv4 address. Core cross-builds with QUIC pass for Windows/amd64,
  macOS/arm64, FreeBSD/amd64 and Linux/arm64; these do not verify native TUN adapters.
  Native IPv6 overlay evidence follows below. Wider path-MTU and NAT cases
  remain part of the outstanding acceptance scope.

- STUN/UDP punching: real IPv4/IPv6 socket tests verify exact data-port reuse,
  source/transaction admission, retries, RFC 5769 decoding, malformed/fingerprint
  rejection, cancellation, closure and pending-transaction bounds. Parser fuzzing
  completed 57,864 executions without failure (5-second requested budget).
  Ten race-enabled Mesh repetitions verify that removing expired observations
  preserves healthy traffic and session renewal, while disabling punch revokes
  the sessions. The browser saves central STUN settings through the actual API.
  Native three-Agent tests use three separate restricted Linux SNAT/firewall
  routers, translated UDP ports and a local STUN fixture. They verify observed
  endpoint publication, punch-only ICMP/TCP forwarding, STUN/controller outage,
  offline transit restart and owned TUN cleanup. The NAT test also passes with
  9000-byte overlay MTU over a 1280-byte underlay. Core cross-builds with STUN pass
  for Windows/amd64, macOS/arm64, FreeBSD/amd64 and Linux/arm64.
  This is evidence for that NAT behavior,
  not arbitrary NATs, NAT64 or all platform adapters. TCP evidence follows below.

- TCP punching: actual TCP sockets verify fixed-source-port dialing while the
  peer listener stays open, persistent IPv4/IPv6 STUN observations, independent
  TCP/UDP endpoint identities, segmented replies, malformed/oversized/foreign
  transaction rejection, cancellation, shutdown and the 32-service connection
  bound. Physical punch connections authenticate both announced identities with
  pinned TLS before exposing yamux streams; forged certificates are rejected in
  both TLS roles. Tests cover bidirectional 16 KiB messages, stream cancellation
  isolation and the 32-stream limit. Two graph Networks share one physical TCP
  connection and renew their independent Noise sessions after observation expiry;
  disabling punching revokes the connection. Five race-enabled repetitions pass.
  Native three-Agent TUN tests behind separate restricted Linux NATs verify
  distinct TCP/UDP mapped ports, rejection of unsolicited TCP, peer SYNs from both
  sides, punch-only multi-hop ICMP/TCP, 9000-byte overlay MTU over a 1280-byte
  underlay, STUN/controller outages, offline transit restart and owned TUN cleanup.
  Native UDP NAT and ordinary TCP/UDP regression cases pass. Full Go race tests,
  vet, frontend tests/build/format checks and both browser tests pass; the real
  controller browser case now persists TCP and UDP STUN configuration together.
  Core cross-builds pass for Windows/amd64, macOS/arm64, FreeBSD/amd64 and
  Linux/arm64. Native non-Linux adapters, broader TCP NAT behavior and the other
  unchecked requirements above remain unverified or incomplete.

- TUN runtime recovery: a failed reader or unavailable-device write retires that
  device and publishes an independent runtime error. A bounded local retry loop
  recreates only the failed interface with its address/route/MTU; unchanged Mesh,
  router, healthy Networks and durable applied revision are preserved. Ten
  race-enabled Agent test repetitions cover factory failures/backoff, local packet
  delivery after recovery, unaffected Networks, stale-device errors, same-config
  reapplication, membership removal and shutdown. A regression test verifies that
  a fatal delivery callback does not wait on the configuration lock while a Mesh
  may be retiring. Real TLS controller/client integration verifies runtime-error
  publication and clearing without changing applied revision; browser tests verify
  the Node error indicator and recovery. Native direct TCP/UDP and TCP-punch-only
  tests delete a real endpoint TUN after controller shutdown, then verify a new
  interface, restored MTU, multi-hop ICMP/TCP and final cleanup. The TCP NAT case
  uses 9000-byte overlay MTU over a 1280-byte underlay. Full Go race tests and vet,
  frontend tests/build/format/browser checks, and the four existing core
  cross-build targets pass. Native non-Linux adapters and detection of arbitrary
  external route/address edits remain outside this evidence.

- Resource telemetry: interval tests distinguish unavailable CPU from measured
  zero, support multi-core values above 100%, reject invalid/reset counters and
  verify cached/concurrent sample ownership. Native Linux CPU work advances the
  OS process counter; the three-Agent TUN test requires actual resources from all
  Agents. Real TLS controller/client integration verifies validated CPU/memory
  reports reaching the management API. Browser tests verify table and Node
  details, missing versus zero CPU, offline Agents and a disconnected live stream;
  the resource detail screenshot is inspected. Full Go race tests, vet, frontend
  build/tests/format/browser checks pass. Core cross-builds pass for
  Windows/amd64, macOS/arm64, FreeBSD/amd64, Linux/arm64, OpenBSD/amd64 and
  NetBSD/amd64. Non-Linux native resource sampling and TUN support remain part
  of the open platform acceptance matrix. See [resource telemetry](resource-telemetry.md).

- Native IPv6 acceptance: `tests/e2e_linux.py` independently selects overlay and
  underlay address families, including IPv6 controller TLS and discovered/manual
  peer endpoints. All six transports pass individually with an IPv6 overlay,
  IPv6 underlay, 9000-byte TUN MTU and 1280-byte underlay MTU. Automatic TCP+UDP
  passes IPv4/IPv4, IPv6/IPv6, IPv4-over-IPv6 and IPv6-over-IPv4 at default MTUs.
  IPv6 overlay over each of UDP-only and TCP-only restricted IPv4 NAT punching
  also passes at MTU 9000/1280. Every case verifies full-MTU ICMP, TCP delivery,
  controller outage, deleted TUN recovery while offline, transit restart from
  durable cache and final interface cleanup. The race-enabled native TUN test
  now directly verifies both IP families, connected routes and kernel UDP
  delivery (including the mandatory IPv6 UDP checksum). Full Go race tests and
  vet, including vet for the integration-tagged adapter test, pass. Reproduction
  commands and precise remaining gaps are in the
  [Linux verification matrix](linux-operation.md#ipv4-and-ipv6-verification-matrix).

- FreeBSD adapter: native FreeBSD 15.1/amd64 tests in QEMU/KVM verify exclusive
  TUN allocation, both address families at MTUs 1280 and 9000, connected routes,
  full-MTU UDP between kernel and Agent, interruptible idle reads, idempotent
  close, explicit cleanup fallback and kernel cleanup after SIGKILL. IPv4/IPv6
  address replacement preserves the connected route after retiring the old TUN.
  An actual two-Network Agent updates MTUs in place without replacing its readers
  or Mesh, then removes its devices at shutdown. The full Agent test package also
  passes natively. Ten Linux race-enabled repetitions verify multi-Network MTU
  transactions, rollback, rollback failure and recovery from the applied snapshot.
  Full Linux Go race tests/vet, native TUN tests and the three-Agent IPv6/IPv6
  TCP+UDP process test pass. Core cross-builds pass for FreeBSD amd64/arm64/386/arm/
  riscv64, macOS/arm64, Windows/amd64 and Linux/arm64. Other platforms' native
  adapters, FreeBSD multi-host/NAT acceptance,
  earlier kernel versions remain open. Address migration evidence follows below.
  See [FreeBSD operation and reproduction](freebsd-operation.md).

- FreeBSD configuration migration: seven native IPv4/IPv6 cases cover same-IP
  prefix narrowing/widening, new addresses and both directions of address-family
  changes. Each retains its interface index, removes the old address/subnet route
  and passes full-MTU bidirectional kernel UDP traffic afterwards. The actual
  Agent retains its original readers and Mesh through two-Network updates. A
  second-Network kernel IPv6 address conflict rolls back the first Network's
  completed prefix/MTU edit and preserves the applied runtime. Ten Linux race
  repetitions cover full-configuration and MTU transactions, command failures
  before/after mutation, rollback failures, retiring unavailable devices and
  recovery from the applied snapshot. This closes the same-IP IPv6 prefix gap
  noted in the earlier FreeBSD evidence. Full Go race tests/vet, native Linux TUN
  checks, the Linux three-Agent IPv6/IPv6 TCP+UDP test and the complete native
  FreeBSD Agent/TUN suites pass. Core cross-builds pass for the five FreeBSD
  architectures listed above, macOS/arm64, Windows/amd64 and Linux/arm64.
  Broader platform acceptance stays open.

- macOS adapter implementation: AF_SYSTEM utun allocation, address-family packet
  framing, bounded command configuration, subnet-route ownership checks and
  address/prefix/MTU rollback are implemented. Linux race tests cover route
  conflicts, scoped/foreign/reject routes, failures before/after route mutation,
  old-route recovery and unavailable-device reporting when rollback fails.
  Production binaries and integration-test binaries cross-build for Darwin amd64
  and arm64. Full Linux Go race tests/vet and native Linux TUN tests pass, along
  with ten race-enabled repetitions of the tunnel/route tests. The shared native
  test refactor passes the FreeBSD TUN suite and actual Agent reconciliation in
  the FreeBSD VM. Native macOS test cases are written for both kernel I/O and actual Agent
  reconciliation, but have not been executed on macOS. This is implementation
  and compile-time evidence only; the native macOS acceptance item remains open.
  See [macOS operation and pending native checks](macos-operation.md).

- Windows adapter implementation: Wintun packet rings, restricted DLL loading and
  ABI validation, case-insensitive alias reservation, owned-LUID address/routes,
  per-family IP MTUs and live address/prefix/MTU rollback are implemented. Ten Linux
  race-enabled tunnel test repetitions cover interrupted reads, concurrent writes
  and Close, mapped-storage lifetime, congestion recovery and MTU rollback. Full
  Go race tests/vet and the native Linux IPv4/IPv6 TUN test pass. Production and
  native Agent/TUN test binaries cross-build for Windows amd64, arm64 and 386;
  integration-tagged vet passes for each Windows architecture. Shared kernel-test
  helpers also cross-compile for Darwin amd64/arm64 and FreeBSD amd64/arm64.
  The pinned Wintun downloader succeeds against the official download and preserves
  exact DLL/license bytes for all four archive architectures; PE machine checks
  and corrupted-archive rejection pass. Native Windows tests for kernel packets,
  routes, names, configuration migration, owned cleanup and actual Agent
  reconciliation are written but have not run on Windows. No Windows native or
  multi-host acceptance is claimed. See [Windows setup and pending native
  verification](windows-operation.md).

- Manual DNS endpoints: every permitted address now has a distinct stable
  Candidate, retry state and retained Link. Real Linux sockets verify all six
  transports with two live addresses, exact preference, DNS refresh adding a
  third address, rekey of a withdrawn healthy address, traffic and endpoint
  removal. Additional tests cover IPv6-only direct policy, TCP/UDP punch methods,
  an unreachable first UDP answer and rejection of inconsistent target
  introductions. DNS fixture tests verify deduplication, family filtering,
  bounded concurrency, cancellation, cache pruning, transient failures and
  name-not-found. Real CA-trusted WSS/gRPC frontends verify original SNI,
  certificate hostname, HTTP authority/port and path. Full Go race tests/vet and
  six production cross-build targets pass. Native Linux three-Agent regressions
  pass for IPv6 UDP/gRPC direct and IPv6 overlay over restricted IPv4 UDP NAT at
  MTU 9000/1280, including outage continuity, offline recovery/restart and cleanup.
  UDP wildcard replies now preserve the received destination via packet control
  messages; standalone/shared-listener secondary IPv4 and IPv6 tests pass on
  Linux. Windows ancillary support and native BSD/macOS behavior remain pending.
  Native process regressions use literal IPs; DNS answer sets are controlled
  component fixtures. See [endpoint resolution and evidence](endpoint-resolution.md).
