# Full-scope implementation and acceptance tracker

`[ ]` is incomplete or not yet verified. A module's existence is not acceptance.
Every feature below derives from the accepted proposal, including its suggestions.

Acceptance scope (updated 2026-09-29): runtime, integration and end-to-end
verification is required on **Linux only**. Other platforms still require their
implementations, source review and cross-builds, but complete native execution
is optional. Historical evidence below records what actually ran; it does not
make other-platform native tests a completion requirement. Unverified native
checks are listed separately from outstanding implementation work.

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
- [x] Batched live telemetry, stale/offline state and bounded event streaming.
- [x] Agent process CPU/Go memory/runtime telemetry with table and Node-detail display.

## Agent and forwarding

- [x] Runnable agent CLI, durable private identity and cached configuration.
- [x] Validate → persist → reconcile → ACK; old runtime retained on failed updates.
- [ ] Per-network TUN, address/route setup and safe resource cleanup on all platforms.
- [x] FreeBSD native IPv4/IPv6 TUN, full-MTU kernel I/O and live MTU reconciliation.
- [x] FreeBSD live address/prefix/MTU edits, multi-Network rollback and applied-state recovery.
- [x] OpenBSD native IPv4/IPv6 TUN, live configuration, route rollback and ownership checks.
- [x] Coordinated IPv4/IPv6 listeners with native OpenBSD transport/Mesh evidence.
- [x] OpenBSD marked-TUN SIGKILL recovery, including startup with no Networks.
- [ ] OpenBSD pre-marker creation-window cleanup review.
- [x] NetBSD native dual-stack TUN, MTU validation, live edits and route rollback.
- [x] NetBSD marked-TUN SIGKILL recovery and multi-Network Agent configuration/shutdown.
- [x] NetBSD native transport/Mesh execution and reproducible cross-compiled test bundle.
- [ ] NetBSD pre-marker interruption cleanup review.
- [x] macOS utun adapter code and Intel/Apple Silicon cross-builds.
- [x] Windows Wintun adapter code, pinned DLL retrieval and amd64/arm64/386 cross-builds.
- [x] DragonFly autoclone TUN, dual-stack framing and live route/address/MTU reconciliation; source review and cross-build.
- [x] Local recovery from fatal TUN I/O, independent runtime health and Linux offline recovery.
- [x] Actual multi-hop IP forwarding and network/source admission enforcement.
- [x] Native Linux IPv4/IPv6 overlays and underlays, all six IPv6 transports and mixed families.
- [x] TCP framing, native UDP and WS/WSS binary-message transports.
- [x] gRPC bidirectional streams through manual endpoints.
- [x] QUIC datagrams, shared UDP listener and bounded message fragmentation.
- [x] Authenticated ephemeral peer keys, cipher policy, replay protection/rekey.
- [ ] Physical interface discovery and changes; automatic TCP/UDP endpoints only.
- [x] FreeBSD kernel driver/type discovery with native renamed-interface and address-change tests.
- [x] OpenBSD kernel type/cloner discovery with native clone exclusion and address-change tests.
- [x] NetBSD kernel type/cloner discovery with native clone exclusion and address-change tests.
- [x] macOS kernel family/subfamily/clone discovery with Linux policy tests and Intel/Apple Silicon cross-builds.
- [x] Manual hostname/path endpoints and configurable listeners (default 24752), Linux acceptance and cross-platform review.
- [x] Per-address DNS Candidates across all six transports, refresh, preference and healthy rekey on Linux.
- [x] Windows UDP packet-info implementation and portable 32/64-bit ABI validation.
- [x] STUN from the data socket and UDP punching through verified restricted NATs.
- [x] Linux TCP hole punching, independent STUN mappings and pooled authenticated sessions.
- [x] Linux restricted/mixed NAT and live mapping recovery; cross-platform TCP punching implementation review.
- [x] IPv4/IPv6 direct and punch allowlists, bounded scheduling/backoff.
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
- [ ] Common architecture build matrix and Linux integration evidence.
- [ ] Unit/race/integration/E2E tests, formatting, vet and frontend checks in CI.
- [x] Pinned-toolchain CI definitions, native-result gates and local Linux/FreeBSD execution.
- [x] All 33 Go-advertised targets for seven selected operating systems cross-build with hash manifests.
- [x] Verified distribution archives, reproducible build commands and Linux deployment/service examples.
- [ ] User/API/protocol documentation and realistic security/operational limitations.
- [ ] Final requirement-by-requirement audit with linked evidence.

## Optional supplemental native verification

These checks are not required for completion under the agreed Linux validation scope:

- Windows TUN/Agent, discovery, wildcard UDP reply source and multi-host/NAT runtime checks.
- macOS utun/routes/reconfiguration and multi-host/NAT runtime checks.
- FreeBSD/OpenBSD/NetBSD multi-host/NAT, older kernels, other architectures and physical hardware.
- Hosted execution of the manually dispatched native-platform workflow.

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
  Linux. Native Windows/BSD/macOS ancillary behavior remains pending; Windows
  implementation evidence follows below.
  Native process regressions use literal IPs; DNS answer sets are controlled
  component fixtures. See [endpoint resolution and evidence](endpoint-resolution.md).

- Windows UDP packet information: native Winsock socket options and a bounded
  WSACMSGHDR codec now preserve the received destination and interface for replies.
  IPv4, IPv6, dual-stack sockets and IPv4-mapped peers use their required control
  types; unexpected setup errors close the owned socket. Oversized/truncated
  datagrams and WSAEMSGSIZE no longer stop the listener. Independent 32/64-bit
  byte fixtures, malformed controls and output ownership pass ten Linux race
  repetitions; a five-second requested fuzz budget completed 81,043 executions.
  Native Linux standalone/shared UDP tests reject an oversized packet and then
  deliver IPv4/IPv6 replies. The full Go race suite and vet pass, as do production
  and transport-test cross-builds for Windows amd64/arm64/386, macOS arm64,
  FreeBSD amd64 and Linux arm64. The native Linux three-Agent restricted UDP NAT
  case passes with IPv6 overlay and MTU 9000/1280, including STUN/controller
  outages, offline TUN recovery/restart and cleanup. A DNS test's single-packet
  reliability assumption during rekey was corrected to bounded repeated probes;
  five race-enabled repetitions pass with its topology/rekey assertions retained.
  Actual Windows socket options, source selection and truncation handling still
  require execution of the [native Windows UDP checks](windows-operation.md#udp-packet-information).

- Interface discovery and live endpoint updates: Linux uses a single kernel link
  dump; Windows uses native hardware/type/filter/endpoint metadata instead of
  alias or MAC heuristics. Discovery deduplicates URLs, stabilizes enumeration
  order and rejects partial address snapshots. An isolated Linux native test
  verifies renamed veth/TUN/TAP, dummy/bridge exclusion, IPv6 address withdrawal
  and interface down/up transitions. Real-socket tests across all six transports
  retain exact unaffected Link IDs through endpoint addition/removal, same-ID URL
  replacement, IPv6 enablement, IPv4 revocation and transport changes, followed
  by packet delivery. Pending revoked TCP handshakes are canceled; completed
  stale handshakes cannot introduce a revoked or replaced endpoint. Five
  race-enabled policy/admission repetitions pass. Introductions now require an
  exact endpoint fingerprint; both peers must be upgraded together.
  Full Linux Go race tests and vet pass. Three-Agent process regressions pass
  for automatic TCP/UDP with IPv6 overlay/underlay and restricted UDP NAT with
  IPv6 overlay/IPv4 underlay, both at MTU 9000/1280, including outage continuity,
  offline TUN repair, cached transit restart and owned-interface cleanup.
  An ephemeral TCP/UDP port-allocation collision exposed during parallel tests
  is fixed by bounded pair retries; ten race repetitions verify recovery, fixed
  port failure, exhaustion, cancellation and release of failed TCP reservations.
  Production, discovery and Mesh tests cross-compile for Windows amd64/arm64/386,
  macOS arm64, FreeBSD amd64 and Linux arm64. Native Windows discovery, authoritative
  DragonFly/macOS classification and IPv6 link-local scope mapping remain open, so the
  overall interface-discovery acceptance item is still unchecked. See
  [endpoint discovery and updates](endpoint-resolution.md).

- FreeBSD discovery: native interface types, read-only driver/unit sysctls and
  registered cloners replace alias/MAC heuristics. Wi-Fi VAPs and jail epairs
  remain eligible; TUN/TAP, bridges and other software overlays are excluded.
  Ten FreeBSD 15.1-p3/amd64 native repetitions verify renamed epair/TUN/TAP/bridge
  fixtures after removing their default groups, stable unrelated guest-NIC
  endpoints, IPv4/IPv6 address changes and interface down/up discovery. The native
  test also checks the cloner ioctl ABI size and destroys its owned fixtures.
  The native Agent configuration-reconciliation test and full Mesh package pass.
  The latter includes all six transports' DNS address refresh/retention and live
  policy changes, with explicitly configured BSD loopback aliases. Ordinary tests
  skip multi-address cases when those required aliases are absent; native results
  here include actual execution, not skips.
  Testing exposed FreeBSD's 9216-byte default UDP send limit; owned BSD sockets
  now reserve 64 KiB without changing global sysctls. Five native repetitions
  verify maximum 16 KiB IPv4/IPv6 messages in both directions, exact reply sources
  and oversized-packet rejection, with standalone and QUIC-shared listeners.
  Ten corresponding Linux race repetitions, the full Linux race suite and vet
  pass. Production binaries, discovery/transport/Mesh test binaries and vet pass
  for FreeBSD amd64/arm64/386/arm/riscv64, macOS arm64, Windows amd64 and Linux arm64.
  The Go 1.26.8 i386 discovery binary and a minimal Go hello program both crash
  under this VM's i386 compatibility mode; cause remains unresolved. Therefore
  32-bit native acceptance is explicitly not met despite successful cross-builds.
  DragonFly/macOS discovery, broader hardware coverage, link-local scope mapping,
  multi-host and NAT acceptance remain open. Reproduction and limitations are in
  [FreeBSD operation](freebsd-operation.md).

- Build and CI: Go 1.26.8, Node.js 24.21.0 and pnpm 10.33.3 are pinned; the
  workflow defines Linux/Windows/macOS backend and native jobs, a FreeBSD 15.1
  VM job, frontend/browser checks, thirteen Linux network scenarios and a
  seven-system/33-architecture build matrix. Its aggregate check rejects any
  failed, canceled or skipped dependency. Native result parsing requires actual
  named test execution and package success, rejecting skipped required subtests
  and truncated logs; regression tests cover these failure cases. Full race-suite
  results also require the multi-address Mesh and UDP cases to execute.
  Local formatting, module integrity, vet, uncached race tests, Linux native
  tests, pinned frontend/embedded-assets/browser checks and all thirteen native
  Linux scenarios pass. All 33 binaries build and their manifest hashes/sizes
  verify. The generated FreeBSD runner passes TUN, Agent, discovery, UDP and
  complete Mesh tests on 15.1-p3/amd64 with no skips; interface/alias cleanup and
  rejection of existing fixture addresses are verified. Actionlint passes.
  No remote is configured, so hosted CI execution, especially actual Windows
  and macOS results, remains pending. Cross-build artifacts do not establish
  complete distribution packaging or native platform acceptance. Commands,
  matrix and limitations are in [build and CI verification](continuous-integration.md).
  Two independent clean checkouts of revision `29ed0f66cd21` also produce
  byte-identical Linux amd64 binaries and manifests with Go 1.26.8, despite
  different absolute paths (one with spaces) and conflicting local CPU/build
  flags. Other targets' repeat-build and cross-host reproducibility, complete
  packaging and deployment examples remain open.
  Additional same-worktree Linux amd64/riscv64 comparisons verify identical
  binaries/manifests despite conflicting persistent Go configuration, CPU/FIPS
  settings, module mode, target and compiler flags. Cross-builds now isolate
  these settings for toolchain probes as well as compilation and record them
  in the manifest; native RISC-V and cross-host execution remain unverified.

- OpenBSD TUN: production and integration binaries build for amd64, 386, arm,
  arm64, ppc64 and riscv64, with integration-tagged vet. A disposable OpenBSD
  7.9/amd64 VM verifies both IP families at MTUs 1280/9000, exact subnet routes,
  full-MTU bidirectional kernel UDP, exclusive allocation, blocked-read close,
  seven configuration migrations and route-conflict rollback. Replacement tests
  revoke the original descriptor and verify that its update/close cannot change
  a same-name replacement or adopt an idle foreign TUN. Repeated native runs and
  the real Agent multi-Network configuration/rollback/cleanup test pass; strict
  JSON gates reject skips. Private device nodes/directories and owned interfaces
  are removed on normal close and failed creation. IPv6 configuration waits for
  DAD completion; route commands use explicit link-layer interface addresses.
  The shared BSD route reader also cross-builds for both macOS architectures;
  portable race tests reject foreign equal-prefix routes regardless of list order.
  Full Linux race tests and vet pass.
  The IPv6 wildcard-listener defect found by native transport testing is fixed:
  TCP/UDP reserve separate IPv4/IPv6 sockets on the same port, retaining shared
  admission limits and STUN/QUIC data-socket reuse. Failed partial binds close
  owned sockets; ephemeral collisions retry, and unavailable families may be
  omitted. The entire OpenBSD transport suite passes three consecutive runs
  without skips; the complete Mesh suite passes 66 tests/subtests, including
  IPv6 DNS/punch and live policy edits across all six transports. Required socket
  gates now include dual-family TCP, QUIC, TCP/UDP STUN, cleanup and peer bounds.
  Pre-marker creation-window cleanup, broader physical hardware coverage and
  multi-host/NAT acceptance are still required. Other OpenBSD architectures have
  compile evidence only. See [OpenBSD operation](openbsd-operation.md).
  This listener change passes the uncached Linux full-repository race/vet gate
  and all thirteen isolated three-Agent scenarios with underlay MTU 1500,
  including IPv6 transports, mixed families and TCP/UDP NAT. All 33 production
  targets build with the pinned toolchain; manifest sizes and SHA-256 hashes
  match the artifacts. These cross-builds are not native Windows/macOS/BSD
  execution evidence.

- OpenBSD process recovery: root-private immutable token records are locked for
  each device's lifetime and published durably before interface creation. The
  kernel description binds the token to the original interface index; startup
  and per-Open recovery only destroy matching orphan interfaces. Native tests
  kill real subprocesses, recover IPv4/IPv6 subnets and verify full-MTU kernel
  I/O, preserve live owners and replaced/retagged interfaces, retire unpublished
  records, and recover during Agent startup with no new TUN. Recovery tests are
  required by the native JSON gate. A kill before the kernel ownership marker
  is assigned can still leave an unconfigured interface; it is preserved because
  its ownership cannot be proved. See [OpenBSD operation](openbsd-operation.md).
  The final OpenBSD native TUN suite passes 31 tests/subtests without skips,
  including 30 close/recovery races and rejection of symlinked, hard-linked,
  public or non-root records. Earlier repeated recovery runs also pass. The
  Agent multi-Network reconciliation/rollback test passes natively. Linux
  uncached full race tests and vet pass; all six OpenBSD integration targets
  build and pass vet. All 33 production targets cross-build with verified
  manifest sizes/hashes. Only OpenBSD amd64 has native recovery evidence.

- OpenBSD physical discovery now uses kernel interface types and the registered
  cloner list instead of name/MAC heuristics. Both OpenBSD 7.9/amd64 and FreeBSD
  15.1-p3/amd64 pass three full native discovery runs (93 tests/subtests each,
  zero skips) with the shared cloner-query implementation. OpenBSD fixtures
  verify two guest NICs, seven clone classes, edited groups/descriptions, stable
  IDs, IPv4/IPv6 address changes, down/up withdrawal and stale-identity rejection.
  The required native gate now includes OpenBSD discovery and requires a spare
  guest NIC. Wi-Fi/MBIM classification has unit coverage only; native hardware
  coverage and link-local scope remain open. Reproduction is documented in
  [OpenBSD operation](openbsd-operation.md#physical-interface-discovery).
  Linux uncached full-repository race tests and vet pass. Discovery integration
  binaries and vet pass for all ten Go-advertised FreeBSD/OpenBSD architecture
  targets; all 33 production targets build and match their manifest sizes and
  SHA-256 hashes. Native interface/address cleanup is verified on both VMs.


- NetBSD 11.0/amd64 TUN now passes native IPv4/IPv6 full-MTU kernel I/O,
  seven address/prefix/family migration cases, exact-route conflict rollback,
  oversized-MTU rejection and owned-device lifecycle checks. The kernel's
  1500-byte TUN limit is enforced before mutation. Native header compilation
  confirms ioctl layouts; IPv6 readiness uses address flags rather than a UDP
  bind, and explicit driver `FIONBIO` prevents kernel-blocked reads on shutdown.
  The final TUN package passes 99 tests/subtests with zero skips.
  The same real Agent test passes two consecutive multi-Network migration,
  rollback and shutdown runs. See [NetBSD operation](netbsd-operation.md).
- Persistent TUN ownership/recovery code is shared by OpenBSD and NetBSD, with
  platform-specific ioctl hooks. Both run SIGKILL recovery, live-owner protection,
  unmarked replacement/retagged interface preservation, startup without Networks,
  incomplete records, unsafe-record rejection and 30 concurrent close/recover
  iterations. OpenBSD retains its stronger copied-marker replacement test; its
  final native suite passes 31 tests/subtests with zero skips after the refactor.
  NetBSD reuses interface indices and cannot distinguish a privileged replacement
  carrying a complete copied marker. Its kernel also retains descriptor
  associations across external same-unit replacement. These ownership boundaries
  and the unresolved pre-marker creation window are documented explicitly.
- Linux uncached full race tests and vet pass after the shared changes. Production
  binaries for all 33 advertised targets have verified manifest sizes/hashes;
  the final NetBSD nonblocking fix is additionally built for all four NetBSD
  targets. Integration binaries and vet pass for all ten NetBSD/OpenBSD targets.
  Cross-builds do not establish native execution on those other architectures.
  NetBSD multi-host/NAT and broader platform acceptance remain open. Its new local native gate rejects skipped or missing required TUN/Agent
  tests; no hosted NetBSD job has run.


- NetBSD physical discovery now shares the kernel type/cloner implementation with
  OpenBSD, replacing the name/MAC fallback. NetBSD 11.0/amd64 and OpenBSD
  7.9/amd64 each pass three full discovery-package runs (102 tests/subtests,
  zero skips). NetBSD fixtures include TAP, TUN, bridge, vether, VLAN, agr and
  lagg plus a separate physical guest NIC; addresses, endpoint IDs, interface
  state and stale identities are checked. Fixture cleanup is verified to remove
  clones and restore the spare NIC. See [NetBSD operation](netbsd-operation.md#physical-interface-discovery)
  for the observed NetBSD VLAN-destruction wait and the explicit detach/wait
  cleanup sequence. The native gate now requires NetBSD discovery and an
  explicitly selected spare interface. Broader physical hardware coverage and
  IPv6 link-local scope mapping remain incomplete.

  Linux uncached full race tests and vet pass. Integration builds/vet pass for
  all 14 advertised NetBSD/OpenBSD/FreeBSD architecture targets, and all 33
  production targets have verified manifest sizes/hashes. Native evidence is
  limited to the two amd64 guests above; no hosted CI run is claimed.


- NetBSD 11.0/amd64 passes all transport tests three times (270 tests/subtests,
  zero skips) and the complete Mesh suite (66 tests/subtests, zero skips).
  This provides native socket evidence for both IP families, all six transports,
  secondary-address reply selection, STUN data-port reuse, truncation recovery,
  listener/resource cleanup, DNS refresh, policy-preserved Links, fallback and
  rekey. It does not establish multi-host/NAT acceptance. The new
  `prepare-netbsd`/`verify-netbsd` commands package and verify TUN, Agent,
  discovery, transport and Mesh tests with guarded loopback fixtures. FreeBSD
  retains its existing command names and now also runs the full transport suite.
  See [NetBSD native bundle](netbsd-operation.md#complete-native-test-bundle).

- Validation scope change: non-Linux native CI moved to a separate manual-only
  workflow. The default gate retains Linux backend/native checks, frontend/browser
  checks, thirteen Linux network scenarios and all 33 production cross-builds.
  The final local NetBSD bundle exposed an intermittent
  `TestGRPCSharesListenerAndFallsBackToEstablishedLinks` failure (`edge has no
  healthy link` after fallback); that bundle is not counted as a complete pass.
  A subsequent Linux race-enabled run of this test passed 100 repetitions. This
  does not establish the cause or prove that the intermittent failure is fixed.
  Workflow lint and all five script tests pass on Linux. The Linux native TUN
  and interface-discovery gates also pass in isolated network namespaces after
  the runner changes. No hosted workflow execution is claimed.

- Deployment and packaging: Linux controller/Agent systemd examples include
  persistent private state, initial enrollment settings, restart/termination
  behavior and restricted capabilities. `scripts/check.py deployment` builds a
  temporary executable and verifies both units without installing host services.
  `scripts/package.py` checks the cross-build manifest, normalizes tar/ZIP metadata,
  bundles operation guides, and atomically publishes archives and SHA-256 manifests.
  Nine script tests pass, including corrupt inputs, existing-output preservation,
  path validation and deterministic tar/ZIP generation. Real Linux/amd64 and
  Windows/amd64 builds produce byte-identical archives on repeated packaging;
  checksums, bundled files and the extracted Linux executable are verified.
  Windows execution is not claimed. Linux three-Agent E2E passes with the service
  capability policy for automatic TCP/UDP and IPv6-over-IPv4 UDP NAT, including
  MTU 9000/1280 traffic, controller/STUN outage, TUN repair, offline cached restart
  and cleanup. The NAT run verifies actual effective/bounding capabilities and
  `NoNewPrivs` through `/proc`. CI now uses this policy for its Linux network
  matrix and retains distribution archives alongside cross-build artifacts.
  Unit syntax validation does not claim a real systemd installation test.
  See [deployment and upgrade instructions](deployment.md).

- DragonFly/amd64 now has an implemented TUN adapter rather than an unsupported
  stub. Source review against the pinned DragonFly kernel revision establishes
  the autoclone ownership path, canonical-name close behavior, IPv4/IPv6 framing,
  nonblocking reads and ioctl layouts. Live configuration reuses the tested
  address/route rollback logic and checks descriptor name plus interface index.
  MTU writes use SIOCSIFMTU because the descriptor-only setter would leave ND6's
  maximum MTU stale. The final DragonFly binary and test binary cross-compile,
  target vet passes, and the binary manifest's size/hash are verified. Related
  FreeBSD/OpenBSD/NetBSD amd64 and Darwin arm64 binaries also cross-build with
  verified manifests after extending the shared BSD build tags. Linux race tests
  for TUN, Agent and discovery pass, along with repository vet. No DragonFly VM
  was started or native execution claimed. Authoritative DragonFly physical
  interface discovery remains separate outstanding work.
  See [DragonFly operation and kernel references](dragonfly-operation.md).

- macOS physical-interface discovery now queries concrete kernel interface type,
  family, subfamily and extended flags rather than requiring a MAC and using a
  broad prefix blacklist. Cloned Ethernet, AWDL, VMNET, simulated cellular,
  VLAN/bond/tunnel families and canonical legacy TAP/TUN drivers are excluded;
  Ethernet/Wi-Fi, FireWire and cellular hardware are accepted independently of
  an Ethernet MAC. Name/index mismatches and ioctl failures reject incomplete
  snapshots. XNU's delegated functional type is intentionally not used. Linux
  race tests pass for the classification rules, discovery and Agent; both Darwin
  production/test architectures cross-build, target vet passes and production
  manifest sizes/hashes are verified. No macOS native execution is claimed.
  This completes the macOS classification implementation item; DragonFly
  classification and link-local scope mapping remain open. Details and pinned
  kernel references are in [macOS operation](macos-operation.md#physical-interface-discovery).

- IPv4 link-local automatic endpoints are now included on eligible underlay
  interfaces. The snapshot retains only TCP/UDP and still excludes loopback,
  unspecified, multicast/broadcast and unsupported IPv6 link-local addresses.
  Linux native discovery verifies link-local address add/remove and interface
  down/up while rejecting TUN/TAP/bridge/dummy addresses. Race-enabled discovery,
  Link and model tests pass, along with the native Linux gates and workflow lint.
  A new isolated three-Agent scenario uses only 169.254.42.0/16 underlay addresses,
  checks the exact automatic endpoint sets and both healthy TCP/UDP transports,
  and passes IPv6 overlay MTU 9000 over underlay MTU 1280, controller outage,
  offline TUN repair, cached transit restart and interface cleanup. Agents run
  under the deployment capability policy. The default Linux CI matrix now has
  fourteen scenarios. See [Linux operation](linux-operation.md).
- DragonFly discovery investigation confirms the generic interface MIB exposes
  the mutable current name, not FreeBSD's original-driver query, and cloned
  interface groups can be edited. Group membership alone would not close the
  outstanding classification requirement. The finding is recorded in the
  [DragonFly guide](dragonfly-operation.md); no native platform validation was run.


- IPv6 link-local scope mapping is implemented across discovery, candidate
  generation, authenticated admission, all six transports and policy revocation.
  Owner interface names are advertised in escaped URLs; dialing uses only a
  configured initiator interface. Scope endpoint replacement changes the candidate
  identity and stale handshakes cannot restore it. Portable tests cover literal
  and DNS scopes, invalid introductions, both directions, replacement/removal,
  numeric zone aliases and disabled policies. Previous entries listing IPv6 scope
  mapping as incomplete are historical; DragonFly interface classification and
  the remaining top-level audit items are still open.
- Linux native discovery verifies IPv6 link-local inclusion/exclusion on real
  veth versus dummy/bridge/TUN/TAP devices, address withdrawal and down/up changes.
  Three-Agent Linux E2E now includes two independent links with repeated link-local
  IPs and different NIC names on every Agent. It checks exact healthy candidates
  for both dialing directions, rejects mismatched scopes, removes/restores one
  scope while retaining the other Link IDs, and exercises native TUN forwarding,
  controller outage, offline TUN repair, cached transit restart and cleanup.
  Automatic TCP/UDP and manual WS/WSS/gRPC/QUIC pass at MTU 9000/1280 with restricted
  Agent capabilities. This caught and fixed a gRPC resolver-URI escaping defect
  affecting zone names whose first two characters are not hexadecimal.
- Default CI adds automatic and gRPC IPv6 link-local cases (16 Linux network
  scenarios total). BSD native discovery fixtures account for OS-generated scoped
  addresses on their explicitly allowed test NICs; they are compile-checked only
  in this change. Other-platform native runs remain optional under the agreed
  Linux-only runtime acceptance scope. Reproduction and wire identity details are
  in [Linux operation](linux-operation.md) and
  [endpoint scopes](endpoint-resolution.md#ipv6-link-local-scopes).

- Validation for this scope change: Linux race suites for Link, Discovery, Mesh,
  Transport and Agent pass, along with `go vet ./...` and pinned actionlint.
  `scripts/cross-build.py` produces all 33 selected Go OS/architecture binaries;
  every manifest byte count and SHA-256 digest was checked. FreeBSD/OpenBSD/NetBSD
  integration Discovery test binaries also cross-compile. No non-Linux native
  test or hosted CI execution is claimed for this change.


- Connection-policy acceptance now covers every combination of IPv4 direct,
  IPv6 direct and punch switches, disabled Edges, disallowed transports, DNS
  templates and observed leases. A real-socket test saturates the eight outgoing
  handshake slots with nonresponding endpoints, checks backoff/eventual admission
  of all sixteen endpoints, and verifies slot release on configuration removal.
  Link/Mesh race suites and vet pass; the new scheduler test also passed three
  initial repetitions. Existing encrypted-introduction and endpoint-revocation
  tests remain part of the same Mesh suite.
- Linux three-Agent acceptance adds IPv6 global and dual-NIC link-local
  punch-only TCP/UDP, plus separate TCP/UDP mixed NAT/public-peer scenarios.
  Exact expected punch Candidate IDs must become healthy in both directions;
  traffic alone is insufficient. Mixed cases verify translated STUN ports,
  unsolicited ingress filtering, TCP source-port SYNs, multi-hop IPv6 traffic,
  STUN/controller outages, offline TUN repair, cached restart and cleanup.
  IPv6 scoped punching also verifies interface-scope withdrawal/restoration.
  All four cases pass with Agent capability restrictions and MTU 9000/1280.
- TCP port-reuse call paths, Go 1.26.8 hook ordering and documented Windows,
  macOS and four BSD socket-option contracts were reviewed. Sources, actual
  Linux evidence and optional non-Linux native checks are separated in
  [NAT operation](nat-operation.md#cross-platform-tcp-socket-review).
  The open NAT acceptance item still includes live mapped-port change/recovery;
  this review does not assert that every NAT can be traversed.
- Linux network CI now has twenty cases, adding scoped/global IPv6 punching
  and both mixed NAT transports. The pinned actionlint check passes locally;
  no hosted CI execution is claimed.


- Live NAT remapping acceptance now passes for UDP and TCP with either three
  NAT nodes or one NAT node plus two public nodes. The fixture changes both
  protocol mappings, flushes the routers' connection tracking, requires old
  affected sessions to disappear and newly observed candidate IDs to become
  healthy in both directions, then verifies full-MTU multi-hop traffic without
  Agent restart. The single-NAT case additionally preserves all unaffected
  public-peer Link IDs. STUN/controller outage, offline TUN repair, cached transit
  restart and cleanup also pass after the new mappings are learned.
- These four Linux namespace runs use overlay MTU 9000, underlay MTU 1280 and
  restricted Agent capabilities. No production fix was needed: observation
  renewal, revisioned endpoint publication and the existing reconnect loop
  handled the faults. Recorded single-NAT recovery was 23.9 s for UDP and 44.0 s
  for TCP; the documented discovery interval explains the extra TCP round and
  is not an availability guarantee. Offline discovery of unknown remote mappings
  remains a stated protocol limitation, not a tested capability.
- The Linux CI matrix adds full UDP and partial TCP remapping cases (22 cases
  total), installs `conntrack` as a fixture dependency and passes pinned actionlint.
  The test keeps router namespace holders alive through bounded recovery waits.
  [NAT recovery reproduction](nat-operation.md#live-nat-mapping-changes) records
  the fault and the assertions; prior entries listing this acceptance as open
  are historical. Other outstanding implementation/acceptance items remain open.

- TCP punch logical-stream capacity now grows with the authorized peer topology.
  A Linux regression first reproduced the fixed 32-stream limit dropping the
  shared physical session when expanding from one to twenty Networks. The fix
  preserves that same session through all forty bidirectional candidates becoming
  healthy, renewing their keys and agreeing on active Links, then removes the
  extra Networks. Capacity includes endpoint aliases and IPv6 scope combinations;
  the authenticated peer cannot raise its own allowance. Policy shrink retains
  the physical session's high-water allowance for retiring/retained Links.
- Transport coverage fills the default allowance, raises it while existing
  streams remain open, verifies a lower requested allowance does not shrink it,
  fills the raised allowance from the opposite direction, rejects further opens
  from either side and exchanges data on an original stream. Mesh, transport and
  Agent race suites and vet pass. The existing fixed physical-session limit is
  unchanged; the full all-viable-Links acceptance item remains open.
- Linux three-Agent TCP restricted-NAT/remapping and dual-NIC IPv6 link-local
  punch-only E2E both pass after this change, with overlay/underlay MTU 9000/1280
  and restricted Agent capabilities. NAT recovery took 43.9 s in this run;
  controller/STUN outage, offline TUN repair, cached restart and cleanup pass.
  Cross-builds and their SHA-256 manifests pass for Linux/386, Windows/amd64,
  macOS/arm64 and FreeBSD/OpenBSD/NetBSD/DragonFly amd64. This adds Linux runtime
  evidence and cross-compilation evidence only, with no new non-Linux native run.

- TCP punch physical-connection admission now allocates the 64-connection bound
  separately to each authenticated adjacent Agent identity. A real 66-Mesh
  Linux loopback star reproduced the previous global limit: the hub stopped at
  64 neighbors and the 65th Edge could not become active. With the fix, all 65
  Edges agree on their active Links, receive encrypted payloads, and retain the
  other 64 physical sessions when one Edge is disabled. The earlier global
  physical-limit description is historical; the per-peer resource bound remains.
- A real authenticated peer saturates its 64 physical connections; the 65th is
  rejected without closing the original sessions, and disabling its last Edge
  closes all of them. Reservation tests also cover pending/installed overlap,
  canceled dials, replacing a closed session at capacity and independent identity
  allowances. Mesh/transport/Agent race suites and vet pass. Seven production
  cross-builds and SHA-256 checks pass: Linux/386, Windows/amd64, macOS/arm64,
  FreeBSD/amd64, OpenBSD/amd64, NetBSD/amd64 and DragonFly/amd64.
- Linux mixed public/restricted TCP NAT acceptance passes after the admission
  change, including live remapping (44.0 s in this run), unaffected public-peer
  session preservation, MTU 9000/1280 traffic, restricted Agent capabilities,
  STUN/controller outage, offline TUN recovery, cached restart and cleanup.
  No non-Linux native execution was added. Full Link-retention/selection and
  final feature-by-feature acceptance remain separate outstanding audit items.


- Manual endpoint/listener acceptance is complete under the Linux execution scope.
  Current Linux race tests cover all six transports' per-address DNS candidates,
  hostname/SNI/authority/path preservation, configured-path admission, DNS refresh,
  retained healthy answers/rekey, endpoint edits/removal and wildcard/failed binds.
  The shared listener construction, separate family binding, partial-close paths,
  Unix/Windows bind-error classification and default-24752 enrollment were reviewed;
  prior seven-platform cross-build evidence still applies to unchanged production
  code. This does not claim new non-Linux runtime or system-DNS outage coverage.
- The new Linux `--listen-port-change` fixture uses the real controller API and
  three native-TUN Agents. It occupies the requested UDP port and verifies the
  configuration error, unchanged applied revision/TUN, preserved original Link
  IDs at adjacent Agents, full-MTU traffic and release of both partial TCP binds.
  Releasing that port must trigger automatic retry, endpoint republication and
  recovery without restarting the Agent or TUN. Exact expected Candidate IDs
  must be healthy in both dialing directions on every Edge. Both old TCP/UDP
  ports become available on both families. Manual URL IDs/paths are preserved across their
  explicit port update. Controller outage, offline TUN recovery, cached restart
  on the new port and final cleanup all pass.
- Local runs pass for automatic TCP/UDP on IPv4 underlay and manual WS, WSS, gRPC
  and QUIC on IPv6 underlay, each with IPv6 overlay, MTU 9000/1280 and restricted
  capabilities. Existing production code handled all five cases without a fix.
  CI adds automatic and gRPC listener-change cases (24 Linux network cases in
  total); Python syntax and pinned actionlint pass. Discovery/operation docs now
  reflect implemented macOS metadata and IPv4/IPv6 link-local support instead of
  stale pre-implementation descriptions. Hosted CI execution is not claimed.
