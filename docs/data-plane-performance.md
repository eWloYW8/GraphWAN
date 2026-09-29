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
