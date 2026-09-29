# Data-plane performance

## Implementation

The coordinated Edge send path uses the active Link maintained by selection
events and the periodic tick. A health check no longer builds telemetry or scans
all candidates for every packet. Link queues are bounded at 512 packets and UDP
receive queues at 256; overload still drops packets instead of growing memory.

`internal/packetbuf` pools explicitly owned buffers in 2 KiB and 16 KiB classes.
Encapsulation, Link queues, TCP/UDP receive and peer decryption reuse that storage.
Ownership transfers through the receive queue to Mesh and ends after synchronous
forwarding/delivery. Drops, failed authentication, cancellation and close release
their buffers. A buffer or any slice into it must not be used after release.
Legacy byte-slice APIs keep their independently owned return values. Exceptional
larger allocations are not retained by the pool. Pool acquire/release alone
measured zero allocations per operation after warmup; this does **not** mean the
whole forwarding path is allocation-free.

Link and stream batches consume immediately available messages, up to 32 at a
time, without waiting to fill a batch. Stream writes combine length headers and
encrypted messages into one write. Linux UDP uses `sendmmsg`/`recvmmsg`; its shared
socket retains STUN, QUIC and GraphWAN demultiplexing and source-interface control
messages. Other platforms keep individual datagram I/O.

The Linux TUN adapter enables `IFF_VNET_HDR` and uses wireguard-go's TUN offload
implementation for GSO segmentation and GRO coalescing. This reuses the TUN
adapter, not the WireGuard tunnel protocol. A single exclusive TUN descriptor
retains interface/route ownership. Receive delivery batches reach the adapter
without retaining pooled packet storage. GRO needs mutable headroom and larger
capacity, so the adapter copies into reusable private write buffers at this
boundary. Kernel offload availability still controls the supported packet types.

Encrypted wire messages, per-message nonces and the replay window are unchanged.
Large receive batches authenticate in parallel with at most four workers; small
batches run synchronously. ARM64 uses a lower size threshold because its
ChaCha20-Poly1305 implementation has different costs from amd64. Replay commits
remain in original receive order after authentication, including duplicate and
forged-high-nonce checks. No cipher, MTU or topology change is required.

## Linux measurements, 2026-09-29

Local amd64 sender to Orange Pi 5 ARM64 receiver over the same gigabit LAN.
iperf3 carries a single TCP flow through GraphWAN's TCP transport over IPv4.
The existing virtual network uses MTU 1280 and ChaCha20-Poly1305. Other machine
workloads remain running; these results are not an isolated hardware maximum.

| Build/stage | Received throughput | Local Agent CPU | opi5 Agent CPU |
| --- | ---: | ---: | ---: |
| Original Agent | 226.7 Mbps | 111.5% | 187.8% |
| Active-Link fast path and stream write reuse | 314.6 Mbps | 69.9% | 196.6% |
| TUN offloads and batched send/delivery | 457.5 Mbps | 38.1% | 183.0% |
| Batched receive and in-place decryption | 558.2 Mbps | 32.6% | 134.7% |
| Parallel authentication and bounded queue tuning | 710.2 Mbps | 41.5% | 167.3% |
| Packet pools throughout forwarding/receive | 716.0 Mbps | 32.6% | 172.5% |

These are individual 10-second runs after a 2-second warmup. CPU uses a
15-second process sampling window including startup/warmup; 100% means one
logical CPU. It is not a steady-state whole-machine percentage. The original
bare-LAN result was 940.0 Mbps. The pooled build's 12-second run with CPU profiling
enabled delivered 710.3 Mbps. Encapsulation, MTU 1280, encryption, heterogeneous
cores and remaining kernel work still limit throughput below the bare link.

The original opi5 TCP profile spent 49.2% of sampled CPU in system calls; the
pooled build spent 25.0%. In the latter profile, ChaCha20's vector routine was
11.9%, Poly1305 generic update 10.5% inclusive, and packet parsing 7.4% inclusive.
These are shares of process CPU, not elapsed-time shares. Binary receive-ID
validation subsequently avoids rescanning canonical hex strings, while still
rejecting the reserved zero ID.

The deployed release (`5d16c3f3005e`, without the profiling hook) also passed
longer runs: 20 seconds after 2 seconds of warmup, CPU sampled over 25 seconds.

| Direction and outer transport | Received throughput | Local Agent CPU | opi5 Agent CPU |
| --- | ---: | ---: | ---: |
| Local → opi5, TCP/IPv4 | 722.4 Mbps | 35.5% | 186.9% |
| opi5 → local, TCP/IPv4 | 779.5 Mbps | 35.4% | 135.8% |
| Local → opi5, UDP/IPv4 | 639.7 Mbps | 88.8% | 204.8% |
| Local → opi5, restored automatic selection (UDP/IPv4) | 689.0 Mbps | 51.8% | 193.7% |

The UDP run had 1,550 inner-TCP retransmissions versus 91 in the forward TCP
run. UDP batching does not remove queue loss or kernel per-datagram work. These
single-run results establish a substantial improvement, not gigabit line rate
or a universal advantage for one transport. The temporary candidate preference
was restored to automatic selection after these comparisons.

## Further optimization, 2026-09-29

The second pass targets the remaining UDP kernel work, queue handoffs and ID
conversion. Linux UDP now uses `UDP_SEGMENT` with scatter/gather buffers and
splits `UDP_GRO` receives before native/STUN/QUIC dispatch. The wire still carries
independent datagrams with their original token, nonce and authentication tag.
Groups contain at most 64 segments and 65,507 bytes, retaining source-interface
control messages. Unsupported offload or a path-MTU rejection disables GSO for
that peer and retries only the unsent prefix remainder as ordinary datagrams.
Negative syscall counts are not treated as submitted messages. This preserves
the existing fragmentation behavior for configured large virtual MTUs. See the
[Linux UDP socket API](https://man7.org/linux/man-pages/man7/udp.7.html) and
[kernel segmentation documentation](https://docs.kernel.org/networking/segmentation-offloads.html).

Reply packet information is decoded only when accepting a new peer. UDP receive
and Link delivery publish batches to a shared bounded packet queue, preserving
batches through decryption and TUN delivery. Capacity remains 256 packets for a
UDP peer and 512 for a Link; it is not multiplied by the batch size. Queue close
releases only queued storage, leaving already delivered buffers with their
consumer. Overflow releases the unqueued tail, and readers never wait to fill a
batch.

The immutable forwarding table now indexes received binary IDs directly. It
caches the invariant outgoing header when configuration is validated, instead
of repeatedly validating and decoding three string IDs. IP admission uses the
same length/address checks without recomputing an unused receive-side flow hash.
Transit copies the canonical input frame and changes only the hop limit.

`BenchmarkForwarding` uses a small valid IP packet, synchronous no-op delivery,
and three runs per case. These are routing microbenchmarks, excluding encryption,
queues, kernel I/O and payload-sized copy cost:

| CPU and operation | Before, median | After, median | Allocations per operation |
| --- | ---: | ---: | ---: |
| Local amd64 receive | 163.0 ns | 71.8 ns | 3 → 0 (96 → 0 bytes) |
| Local amd64 send | 248.4 ns | 69.9 ns | 0 → 0 |
| opi5 ARM64 receive | 570.9 ns | 160.3 ns | 3 → 0 (96 → 0 bytes) |
| opi5 ARM64 send | 638.0 ns | 165.4 ns | 0 → 0 |

The ARM64 CPU has heterogeneous cores and other running workloads; individual
baseline receive runs ranged from 500.6 to 755.3 ns. The microbenchmark reduction
does not imply a corresponding multiplier for encrypted network throughput.

Pre-release single-flow UDP/IPv4 measurements progressed from 636.8 Mbps with
baseline CPU profiling to 762.3 Mbps with offloads, 770.5 Mbps with binary routing
and Link batch delivery, and 788.4 Mbps after preserving UDP receive batches.
The latter runs used the existing 20-second test/2-second warmup/25-second CPU
window. The final step used 38.2% local Agent CPU and 210.3% opi5 Agent CPU.
TCP/IPv4 measured 736.1 Mbps forward and 788.9 Mbps reverse in this build.
These single measurements are development evidence, not isolated maxima.

Regression coverage includes negative/partial `sendmmsg` results, wire-compatible
GSO boundaries, GRO truncation, source control preservation, mixed native/QUIC
traffic, queue ownership/overflow/cancellation, canonical cached-header encoding
and immutable transit frames. Linux three-Agent tests cover TCP, IPv6 QUIC over
an MTU-1280 underlay, and IPv6 UDP with MTU 9000 through three restricted IPv4
NATs over MTU 1280. The latter explicitly exercises offload rejection and legacy
fragmentation, plus STUN/controller outages and restart recovery.

The first release of this pass (`915348225c0e`) repeated the UDP test three
times at 785.7, 781.8 and 783.2 Mbps, with local process CPU between 37.1% and
37.4%. The receiver's socket remained capped at 425,984 bytes and recorded
517 kernel drops across these runs. A subsequent socket-buffer experiment
preserved quic-go's existing 7 MiB receive-buffer request through the shared
wrapper: it uses `SO_RCVBUFFORCE` when the Agent's existing TUN capability permits
it, with ordinary capped socket buffers as an unprivileged fallback. Standalone
UDP requests 4 MiB. This changes only the Agent's sockets, not host sysctls.
Linux reports double the requested amount for buffer accounting, and allocates
receive storage as traffic arrives. The first follow-up run measured 787.5 Mbps,
35.6% local CPU and zero kernel socket drops; it reduced retransmissions but did
not establish a significant additional throughput gain over the preceding runs.

Final release measurements (`2a11a0c09116`, 20 seconds plus a 2-second
warm-up) repeated UDP forward throughput at 784.3, 783.6 and 784.4 Mbps
(median 784.3 Mbps). Local CPU was 37.1–37.4% and Orange Pi CPU was
209.8–212.2%, where 100% means one logical core. The reverse UDP run measured
719.7 Mbps (145.6% local CPU and 177.0% Orange Pi CPU). Reverse performance
remains direction dependent; these results do not imply symmetric throughput.
A subsequent portability fix reads Linux UDP_GRO ancillary data as native
int32, matching the kernel ABI on both little- and big-endian machines.
The temporary transport preference was restored to automatic after testing.

For perspective, the measured inner TCP MSS is 1,228 bytes. With a full
1,280-byte inner packet, the UDP/IPv4 path sends 1,472 bytes on the wire after
including the 80-byte overlay header, two kind bytes, 24-byte encrypted-session
overhead, 20-byte datagram token header, outer IP/UDP headers and 38 bytes of
untagged Ethernet framing/preamble/inter-frame gap. At 1 Gbit/s this gives an
estimated ideal application-payload ceiling of `1000 * 1228 / 1472 = 834.2`
Mbps, before retransmissions and control traffic. Thus 783 Mbps is about 94%
of this configuration's estimated wire-efficiency ceiling, rather than 94%
of the bare-LAN iperf result. Reaching the bare 940 Mbps would require changing
the MTU or encapsulation assumptions as well as reducing processing overhead.

## Reproduce and validate

Record the active transport/address from both Agents' telemetry before and after
each run. Pin the same candidate temporarily for a controlled comparison, then
restore the prior preference. On the receiver and sender respectively:

```sh
iperf3 -s -1 -p 35201
iperf3 -c <virtual-receiver-IP> -p 35201 -t 10 -O 2 -J
```

Use the physical IP for the underlay baseline and `-R` for reverse traffic.
UDP as a GraphWAN transport still carries this same inner TCP test; `iperf3 -u`
would be a different workload. Measure process CPU from `/proc/PID/stat` deltas,
with the same sampling interval for each build. Profiles used a temporary build
overlay with a signal-triggered 20-second capture, not a public profiling port;
release binaries do not include that hook.

Regression coverage includes pooled-buffer ownership across reads and close,
queue drops, batch frame boundaries and cancellation, IPv4/IPv6 UDP batches,
authenticated source/neighbor admission, replay equivalence to sequential
decryption, concurrent duplicates, and real IPv4/IPv6 TCP through Linux TUN
offloads. Run the native tests in an isolated network namespace:

```sh
go test -race ./internal/packetbuf ./internal/packet ./internal/forwarding \
  ./internal/link ./internal/transport ./internal/peer ./internal/secure \
  ./internal/mesh ./internal/agent
go test -race -tags integration -c -o /tmp/graphwan-tunnel.test ./internal/tunnel
sudo unshare -n env GRAPHWAN_TEST_NETNS=1 /tmp/graphwan-tunnel.test \
  -test.v -test.run TestNativeLinux
```

Linux is the native validation target. Cross-compilation of other platforms is
not a claim of equivalent performance or native acceptance there.
