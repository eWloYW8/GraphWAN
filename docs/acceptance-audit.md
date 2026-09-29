# Proposal acceptance audit

This audit maps the accepted proposal to implementation and executable evidence.
It does not declare the project complete. Runtime acceptance is Linux-only;
other systems require source review and cross-compilation. Historical native BSD
results are supplemental. The [tracker](implementation-status.md) records the
remaining acceptance items and detailed earlier evidence.

## Original requirements

| Proposal requirement | Current implementation and evidence | Result |
| --- | --- | --- |
| 1. Central server, managed Agents, powerful embedded Web UI | [Controller](../internal/control), [Agent control client](../internal/agent/client.go), [React application](../web/src/App.tsx); real TLS enrollment/control/revocation tests and real-controller Chromium tests | Implemented and tested |
| 2. Multiple Networks, fixed Node IPs, per-Network TUN, uniform cipher policy | [Models](../internal/model), [runtime reconciliation](../internal/agent/dataplane.go), [TUN adapters](../internal/tunnel); Linux native two-Network configuration/rollback test, cipher-bound authenticated sessions | Implemented and tested within documented platform limits |
| 3. Explicit Edge graph, weights, transports, direct/punch methods, all successful Links, preferred/lowest-RTT active Link | [Candidate policy](../internal/link/candidate.go), [Mesh](../internal/mesh), [selection](../internal/link); all six transports, method combinations, common selection/failover and TCP multi-address/rekey tests | Retention capacity audit remains open; see below |
| 4. Automatic physical/public endpoints, port 24752, manual protocol/host/path entries | [Discovery](../internal/discovery), [STUN and listener integration](../internal/mesh), [endpoint contract](endpoint-resolution.md); Linux native discovery, all-address DNS tests, listener failure/retry and NAT remapping E2E | Implemented and tested; non-Linux APIs reviewed/cross-built |
| 5. Weighted shortest-path multi-hop virtual IP forwarding | [Routing](../internal/routing), [forwarding](../internal/forwarding); deterministic equal-cost handling, actual encrypted three-node forwarding, native Linux ICMP/TCP and hop-limit loop containment | Implemented and tested; disconnected graph components remain unreachable |
| 6. Password authentication, multiple graphs, observe/edit, Node/Edge configuration and live status | [Management UI](management-ui.md), [browser tests](../web/tests/workspace.spec.ts), [telemetry tests](../internal/control/events_test.go); enrollment, graph CRUD/positions, conflict handling, responsive views, active paths, rates, preferences and connection loss | Implemented and tested |
| 7. Server-managed configuration and continued operation during server outage | [Durable cache](../internal/agent/cache.go), [reconciler](../internal/agent/reconcile.go), `TestClientControlAndOfflineRestart`, Linux process E2E with controller stopped and cached Agent restart | Implemented and tested; unknown changed remote mappings can still require rendezvous |
| 8. Go core, pnpm/React embedded frontend, common OS/CPU targets | [Build matrix](continuous-integration.md#cross-builds), [platform guides](#platform-support), [release packaging](deployment.md); 33 cross-builds plus Linux gates | Implemented; complete non-Linux native validation is optional |

## Accepted architecture recommendations

- Network/Agent/Node/Endpoint/Edge/Candidate/Link identities are separate. The
  controller compiles per-Agent forwarding tables; Edge weight is independent of
  measured Link RTT. Equal-cost routing is deterministic rather than per-packet
  random. Flow identifiers are stable; optional future ECMP is not enabled.
- Packets carry bounded versioned overlay headers, source/destination/Network,
  flow identity, routing epoch and hop limit. Mixed-revision loops terminate;
  coordinated two-phase *route* activation remains the proposal's future option.
  Active-Link selection does have authenticated two-party agreement.
- UDP is a packet transport; TCP is framed; WS/WSS use binary messages; gRPC uses
  bidirectional protobuf streams; QUIC uses bounded datagram fragmentation.
  Network cipher policy, Ed25519 identity, ephemeral Noise keys, replay checks
  and rekey are covered by real-socket and cryptographic regression tests.
- Enrollment uses expiring single-use tokens and controller-issued certificates.
  Control uses authenticated TLS/WebSocket. The proposal allowed WebSocket or
  gRPC for control; gRPC is also implemented as a peer transport.
- Desired configuration is validated and persisted before runtime application
  and acknowledgement. Desired/applied revisions and actual telemetry remain
  distinct. Runtime reconciliation, Link reconciliation and forwarding run
  independently; the controller never relays overlay payloads.
- Endpoint discovery and STUN advertise mapped ports. Candidate dialing has
  concurrency limits and backoff. The minimum MTU is 1280; configured MTU is
  enforced. Automatic path-MTU adaptation was a future suggestion and is not
  claimed. The [packet contract](peer-protocol.md#packet-bounds-and-backpressure)
  states queue limits, checksum responsibilities and fragmentation behavior.

## Platform support

| Platform | TUN lifecycle and constraints | Discovery | Required evidence |
| --- | --- | --- | --- |
| [Linux](linux-operation.md) | Exclusive nonpersistent TUN; descriptor close removes resources; configuration prepares replacement interfaces transactionally | Kernel netlink link types, including container veth | Native TUN, discovery, two-Network Agent and three-Agent E2E |
| [Windows](windows-operation.md) | Wintun rings and IP Helper; matching verified DLL and administrator privileges required | Hardware/filter/endpoint metadata | Source review, portable ring/rollback tests, 386/amd64/arm64 builds |
| [macOS](macos-operation.md) | utun control socket, interface routes and transactional changes | Kernel family/subfamily/clone metadata | Source review, portable policy/config tests, Intel/ARM builds |
| [FreeBSD](freebsd-operation.md) | Exclusive clone; transient descriptor lifetime on supporting kernels, index-checked fallback; pre-transient interruption boundary | Original driver identity, type and cloners | Source review, cross-builds; earlier native evidence recorded separately |
| [OpenBSD](openbsd-operation.md) | Exclusive persistent clone, token/index recovery journal, routing table 0; pre-marker interruption boundary | Kernel-assigned driver name, type and cloners | Source review, cross-builds; earlier native evidence recorded separately |
| [NetBSD](netbsd-operation.md) | Persistent clone/journal, native 1500 MTU ceiling, pre-marker interruption boundary | Kernel-assigned driver name, type and cloners | Source review, cross-builds; earlier native evidence and transport caveats recorded separately |
| [DragonFly](dragonfly-operation.md) | Exclusive `/dev/tun` autoclone, descriptor lifetime, kernel-assigned name and transactional routes | Interface type plus read-only driver queries | Source review, Linux policy/config tests, amd64 build |

These ownership mechanisms do not promise atomic behavior against a privileged
administrator concurrently replacing interfaces. The platform guides describe
the precise recovery boundaries instead of silently deleting unknown resources.

## Current local validation

At production revision `3eedd7c`, the following local gates passed. The added
Linux Agent native fixture is defined in this audit's change. Cross-build manifests
truthfully report a dirty worktree when local/untracked files are present.

- `python3 scripts/check.py go`: tracked Go formatting, module integrity, vet and
  uncached full race suite; 15 test-bearing packages passed. Required dual-stack
  socket/Mesh test roots executed; the gate rejects missing/skipped roots.
- `python3 scripts/check.py frontend`: frozen install, formatting, production
  build, exact checked-in embedded assets, unit tests and both Chromium scenarios.
- `python3 scripts/check.py native`: race-enabled Linux TUN, discovery and
  two-Network Agent fixtures in separate disposable network namespaces, without
  skipped required tests. The Agent fixture also passes integration-tag vet.
- `python3 scripts/check.py deployment`: unit syntax checked against a built
  executable without installing or starting host services.
- `python3 -B -m unittest discover -s scripts -p 'test_*.py' -v`: nine verifier
  and packaging regressions passed.
- `python3 scripts/cross-build.py`: all 33 advertised selected targets built;
  actual byte lengths and SHA-256 digests matched every manifest entry.
- `python3 scripts/package.py`: 33 distribution archives generated locally;
  checksums, extracted binary identities, build manifests and every packaged
  document matched their recorded hashes. No archive was published.
- Pinned `actionlint` 1.7.12 accepted both workflow definitions.

The [Linux network workflow](../.github/workflows/ci.yml) defines 24 isolated E2E
scenarios. Existing scenario results and reproduction commands are linked from
the tracker and platform/transport guides. This audit does not claim all 24 were
rerun in this change or that hosted CI ran; no Git remote is configured locally.

## Work still required before completion

1. Implement the administrative command examples shown in the proposal:
   `graphwan network create`, `graphwan node list`, and `graphwan edge add`.
   The current CLI dispatch implements `server`, `agent` and `version`; the
   corresponding administrative operations already exist in the UI/API.
2. Finish the all-viable-Link capacity audit. TCP punch allowances now grow with
   authorized topology and DNS answers, but native UDP and QUIC admission retain
   fixed 512-entry limits, and gRPC has a fixed 512-stream Mesh allowance. Those
   bounds must not silently exclude candidates of accepted configurations.
   Preserve bounded unauthenticated admission while fixing/retesting this case.
3. After those implementation changes, complete the relevant Linux regression
   and delivery gates, reconcile user/API/protocol documentation, and repeat the
   requirement audit. Until then, the final project checkbox remains open.
