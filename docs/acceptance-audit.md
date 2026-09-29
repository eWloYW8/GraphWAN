# Proposal acceptance audit

This audit maps the accepted proposal to implementation and executable evidence.
The accepted implementation and required verification are complete under the
agreed Linux-only runtime scope. Other systems have source review and
cross-compilation evidence; complete native execution is optional. Historical
native BSD results are supplemental. The [tracker](implementation-status.md)
preserves earlier evidence in chronological order; its old open-item statements
are superseded by this audit and its current checklist.

## Original requirements

| Proposal requirement | Current implementation and evidence | Result |
| --- | --- | --- |
| 1. Central server, managed Agents, powerful embedded Web UI | [Controller](../internal/control), [Agent control client](../internal/agent/client.go), [React application](../web/src/App.tsx); real TLS enrollment/control/revocation tests and real-controller Chromium tests | Implemented and tested |
| 2. Multiple Networks, fixed Node IPs, per-Network TUN, uniform cipher policy | [Models](../internal/model), [runtime reconciliation](../internal/agent/dataplane.go), [TUN adapters](../internal/tunnel); Linux native two-Network configuration/rollback test, cipher-bound authenticated sessions | Implemented and tested within documented platform limits |
| 3. Explicit Edge graph, weights, transports, direct/punch methods, all successful Links, preferred/lowest-RTT active Link | [Candidate policy](../internal/link/candidate.go), [Mesh](../internal/mesh), [selection](../internal/link); all six transports, method combinations, common selection/failover, TCP multi-address/rekey and 540-candidate UDP/QUIC/gRPC tests | Implemented and tested |
| 4. Automatic physical/public endpoints, port 24752, manual protocol/host/path entries | [Discovery](../internal/discovery), [STUN and listener integration](../internal/mesh), [endpoint contract](endpoint-resolution.md); Linux native discovery, all-address DNS tests, listener failure/retry and NAT remapping E2E | Implemented and tested; non-Linux APIs reviewed/cross-built |
| 5. Weighted shortest-path multi-hop virtual IP forwarding | [Routing](../internal/routing), [forwarding](../internal/forwarding); deterministic equal-cost handling, actual encrypted three-node forwarding, native Linux ICMP/TCP and hop-limit loop containment | Implemented and tested; disconnected graph components remain unreachable |
| 6. Password authentication, multiple graphs, observe/edit, Node/Edge configuration and live status | [Management UI](management-ui.md), [browser tests](../web/tests/workspace.spec.ts), [telemetry tests](../internal/control/events_test.go); enrollment, graph CRUD/positions, conflict handling, responsive views, active paths, rates, preferences and connection loss | Implemented and tested |
| 7. Server-managed configuration and continued operation during server outage | [Durable cache](../internal/agent/cache.go), [reconciler](../internal/agent/reconcile.go), `TestClientControlAndOfflineRestart`, Linux process E2E with controller stopped and cached Agent restart | Implemented and tested; unknown changed remote mappings can still require rendezvous |
| 8. Go core, pnpm/React embedded frontend, common OS/CPU targets | [Build matrix](continuous-integration.md#cross-builds), [platform guides](#platform-support), [release packaging](deployment.md); 33 cross-builds plus Linux gates | Implemented; complete non-Linux native validation is optional |

## Accepted architecture recommendations

The numbers below match the proposal's 28 recommendations. Each row identifies
implementation and executable evidence; an architecture suggestion is not treated
as a promise of every possible future optimization.

| # | Recommendation | Implementation and evidence |
| --- | --- | --- |
| 1 | Separate Network, Agent, Node, Endpoint, Edge and Link identities | [Model validation](../internal/model), [candidate identity](../internal/link/candidate.go); model, candidate and scope tests, multi-Network Agent tests |
| 2 | Separate control, runtime and packet layers | [Architecture](architecture.md), [controller](../internal/control), [Agent](../internal/agent), [Mesh](../internal/mesh); real controller/client and encrypted forwarding tests |
| 3 | Compile per-Agent configuration centrally | [Routing compiler](../internal/routing/routing.go); weighted multi-hop, disabled/revoked, isolated and deterministic equal-cost tests |
| 4 | Versioned overlay header with bounded forwarding | [Packet codec](../internal/packet/packet.go); round-trip/malformed/framing tests, parser fuzz evidence, mixed-epoch loop test |
| 5 | Separate Edge weights from Link metrics | [Routing](../internal/routing) and [selection](../internal/link/selection.go); weighted-route tests, contradictory local RTT and common-selection tests |
| 6 | Candidate and Link lifecycle, liveness and failover | [Mesh reconciliation](../internal/mesh/mesh.go), [Edge state](../internal/link/edge.go); backoff/scheduling, heartbeat, standby recovery, preference, selection-loss and rekey tests |
| 7 | Typed automatic, observed and manual endpoints | [Discovery](../internal/discovery), [endpoint contract](endpoint-resolution.md); physical-interface, STUN lease, manual-host/path and source-validation tests |
| 8 | Discover actual public mappings, including ports | [UDP/TCP STUN](nat-operation.md); shared data-socket mapping tests and live restricted-NAT remapping E2E |
| 9 | Controller rendezvous without payload relay | [Control snapshots](../internal/control/stream.go), [observations](../internal/mesh/observed.go); independent TCP/UDP mappings, punch-only, restricted/mixed-NAT process tests |
| 10 | UDP as a packet transport, TCP framing | [Transport implementations](../internal/transport), [packet framing](../internal/packet); real-socket encrypted UDP/TCP, retransmitted handshake and bounded-message tests |
| 11 | Add QUIC | [QUIC datagrams](quic-operation.md); datagram fragmentation, IPv6, shared socket, link-local, rekey and 540-candidate tests. HTTP/3 and automatic connection migration are not additional implemented features |
| 12 | Identity separate from ephemeral session keys | [Noise/Ed25519 handshake](../internal/secure/handshake.go), [sessions](../internal/secure/session.go); topology/role/transport binding, forgery, replay, concurrent nonce and key-lifetime tests |
| 13 | Authenticate control-plane identities | [Enrollment](../internal/control/enrollment.go), [client](../internal/agent/client.go); real TLS, CSR/token reuse/expiry, lost-response retry, mutual authentication and revocation tests |
| 14 | Revisioned durable configuration and ACK | [Reconciler](../internal/agent/reconcile.go), [cache](../internal/agent/cache.go); persist-before-apply, failed application, applied-state recovery and concurrent-editor tests |
| 15 | Keep operating when the controller is offline | `TestClientControlAndOfflineRestart` and [Linux process E2E](../tests/e2e_linux.py); controller outage, offline TUN repair, cached transit-Agent restart and continued traffic |
| 16 | Per-Network TUN and subnet route | [Adapters](../internal/tunnel), [Agent reconciliation](../internal/agent/dataplane.go); native Linux IPv4/IPv6 kernel I/O, two-Network migration, rollback, ownership and cleanup tests |
| 17 | Stable flow hash | [IP inspection](../internal/packet/ip.go), `TestIPInspection`; payload-independent hashing and port separation. The proposal's conditional future ECMP is not enabled |
| 18 | Routing epochs and loop containment | [Forwarding](../internal/forwarding/router.go); `TestMixedRoutingEpochLoopExhaustsHopLimit`, deterministic terminating forwarding tables. Atomic local snapshots and hop limit are implemented; two-phase route activation remains the proposal's future option |
| 19 | Graph editor and detailed observation | [UI](management-ui.md), [real-controller browser tests](../web/tests/workspace.spec.ts); graph CRUD/positions, Node resources/version/endpoints, Edge RTT/loss/rates, all policy fields, preferred candidates, responsive/accessibility/error checks |
| 20 | Desired configuration separate from actual state | [Controller streams](../internal/control), [runtime reports](resource-telemetry.md); revision-conflict, applied ACK, telemetry admission/staleness and browser desired/observed tests |
| 21 | Modular Agent internals | [Architecture](architecture.md); separate control/cache/reconciliation, discovery, Mesh, Link, transport, secure session, packet/router and TUN packages with component and integrated tests |
| 22 | OS-specific TUN and route backends | [Platform matrix](#platform-support); Linux execution, source review and all 33 selected Go OS/architecture cross-builds |
| 23 | Explicit MTU and packet bounds | [Packet bounds](peer-protocol.md#packet-bounds-and-backpressure); minimum 1280, exact-MTU/+1 checks, bounded queues/fragments and Linux overlay 9000 over underlay 1280. Dynamic path-MTU adaptation remains a future suggestion |
| 24 | WS/WSS/gRPC only through manual endpoints | [Endpoint validation](endpoint-resolution.md), [WS](websocket-operation.md), [gRPC](grpc-operation.md); manual hostname/path tests, automatic discovery restricted to UDP/TCP, all six transports over IPv6 |
| 25 | Independent transport and connection-method policy | [Candidate policy](../internal/link/candidate.go), [Mesh policy tests](../internal/mesh/policy_test.go); all eight direct/punch combinations, source-family admission and live policy revocation |
| 26 | Keep successful Links and select a common active Link | [Selection protocol](peer-protocol.md), [capacity fixture](../internal/mesh/admission_linux_test.go); preferred/RTT/hysteresis/failover, TCP multiple-address pooling and UDP/QUIC/gRPC 540-candidate retention with overlapping key replacement |
| 27 | Batch telemetry and bound streaming cost | [Agent client](../internal/agent/client.go), [browser events](../internal/control/events.go); two-second batched reports, one-second UI snapshots, coalescing, stream limits, expiration and shutdown tests. Configuration count ceilings are not a 10,000-Node performance benchmark |
| 28 | Independent control, Link and forwarding loops | [Agent reconciliation](../internal/agent/reconcile.go), [Mesh](../internal/mesh), [forwarding](../internal/forwarding); offline operation, failed-update retention, ongoing key rotation and full process E2E |

The proposal's administrative command examples are implemented by the
[CLI](admin-cli.md): `network create`, `node list` and `edge add`, plus
`network list`. Nine test roots cover real-controller TLS/durable changes,
Node memberships, revision conflicts, authentication/logout, redirects, deadlines,
malformed responses and output failure after a committed mutation. Separate
Linux CLI/controller processes also passed trusted-TLS create/list and exit-code
smoke checks.

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

The final Linux runtime pass used production revision
`a4c0c37a54ae64133e1ef83389789aef04748fae` on 2026-09-29. The audit update changes
documentation only. Toolchain versions are Go 1.26.8, Node.js 24.21.0 and
pnpm 10.33.3. These are local runs of the CI gates; no hosted Actions execution
is claimed, and the development checkout has no Git remote.

| Gate | Actual result |
| --- | --- |
| `scripts/check.py go` | Uncached full race suite: 16 test-bearing packages; tracked Go formatting, module integrity and vet passed. Required dual-stack roots and all UDP/QUIC/gRPC 540-candidate subtests executed without skips |
| `scripts/check.py frontend` | Frozen install, formatting, TypeScript/Vite production build, exact embedded file/content comparison, unit tests and both real-controller Chromium scenarios passed; desktop/mobile screenshots inspected |
| `scripts/check.py native` | Race-enabled Linux TUN, discovery and two-Network Agent fixtures passed in disposable network namespaces, without skipped required tests |
| `scripts/check.py deployment` | Built executable and systemd unit syntax passed; no host service installed or started |
| Python verifier/packager regressions | All nine tests passed |
| Linux process network matrix | All 24 current CI cases executed and passed at the production revision above; details below |
| `scripts/cross-build.py` | All 33 selected Go-advertised targets built; full inventory, actual byte sizes and SHA-256 hashes verified. No non-Linux binary executed in the final acceptance pass |
| `scripts/package.py` | All 33 distribution archives generated and independently checked: archive digests, extracted binaries, build manifests and every packaged document. Archives remain local |
| Pinned actionlint 1.7.12 | Both workflow definitions accepted |

The unchanged production code's full Go/race, Python and 33-target build results
were obtained during the admission fix. Frontend, native Linux, deployment,
actionlint and the complete network matrix were run again at `a4c0c37`.
Packaging had already passed all 33 targets; final delivery uses the same build
and packaging commands after the documentation audit, with the exact revision
and document hashes recorded in its generated manifests. These distinctions
avoid attributing an earlier test run to a later commit.

The final network run started at `2026-09-29T08:01:32Z`. Its executable SHA-256 is
`bd8ad6518ef80be20b21118f0b18abd8e3707b55d80142203e777025a41a7edf`.
The runner read the matrix from the checked-in workflow and retained each case's
arguments, exit status, duration and log digest. Four cases ran concurrently,
each inside its own `sudo unshare --net` namespace. All used restricted Agent
capabilities and underlay MTU 1280. Overlay MTU was 9000 except `auto-v4` (1280).

| Scenarios | Result |
| --- | --- |
| `auto-v4`, `auto-v4-over-v6`, `auto-v6-over-v4` | 3/3 passed |
| `auto-listener-change`, `grpc-listener-change` | 2/2 passed |
| `auto-link-local-v4`, `auto-link-local-v6`, `grpc-link-local-v6`, `punch-link-local-v6` | 4/4 passed |
| `punch-global-v6` | 1/1 passed |
| `tcp-v6`, `udp-v6`, `quic-v6`, `ws-v6`, `wss-v6`, `grpc-v6` | 6/6 passed |
| `udp-nat-v4`, `tcp-nat-v4`, `udp-nat-v6`, `tcp-nat-v6` | 4/4 passed |
| `udp-mixed-nat`, `tcp-mixed-nat`, `udp-remapped-nat`, `tcp-remapped-nat` | 4/4 passed |

These process fixtures verify real TUN forwarding, ICMP and a 155,648-byte TCP
exchange, controller outage, offline TUN repair, cached transit-Agent restart and
owned-resource cleanup. NAT cases also cover STUN outage; the remapping cases
change live router mappings. Listener-change cases include failed-bind rollback
and automatic retry. IPv6 link-local cases use two independently scoped paths.
The exact checks and reproduction flags are in [the fixture](../tests/e2e_linux.py)
and [CI workflow](../.github/workflows/ci.yml).

## Operational boundaries

Subsequent deployment found a gap in the original browser evidence: those tests
used HTTP and missed Chromium rejecting the Ed25519 HTTPS server key. The
controller now serves an ECDSA P-256 key while retaining its existing CA and Agent
identities. Both browser scenarios now run over HTTPS and pass; PKI, controller,
Agent and CLI race suites pass, including a new persisted-CA/existing-Agent
regression. This corrects the earlier browser-compatibility assumption.

- The Edge graph must connect the intended Nodes. Disconnected components have
  no route; the controller does not manufacture Edges or relay payloads.
- NAT traversal depends on router mapping/filtering behavior. The tests establish
  the documented Linux scenarios, not universal traversal. If both peers acquire
  unknown remote mappings while the controller is unavailable, fresh rendezvous
  can be required; established paths and cached configurations remain usable.
- Identity-bound encryption is hop-by-hop. Administratively trusted transit
  Agents can inspect transit payloads; this is not end-to-end encryption between
  nonadjacent Nodes. See [the peer protocol](peer-protocol.md).
- [API limits](control-api.md) bound request size, topology and endpoint counts.
  Discovery shares the 64-entry Agent allowance with manual endpoints; overflow
  is logged and selected deterministically. Bounded queues may drop packets
  under load. No throughput or large-topology performance SLA is claimed.
- Windows requires the matching verified Wintun DLL. NetBSD limits native MTU to
  1500. Persistent BSD interface creation has documented interruption windows;
  cleanup does not delete a resource without ownership evidence. Other-platform
  native execution is optional and the platform guides preserve known failures.
- Peers must use a compatible protocol revision; mixed-version rolling upgrades
  across wire-format changes are not promised. Back up controller/Agent state
  and follow [deployment and upgrade instructions](deployment.md).

There are no remaining implementation or required verification items from the
accepted scope. Future ECMP, coordinated two-phase route activation and dynamic
path-MTU adaptation retain their future status from the proposal.
