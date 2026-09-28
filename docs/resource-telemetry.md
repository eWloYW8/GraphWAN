# Agent resource telemetry

The Agents table shows process CPU and Go memory. Selecting a graph Node shows
CPU, logical CPU count, Go memory, heap memory, goroutines and Agent uptime.
These values describe the Agent process on that machine. They do not affect
Edge weights, route compilation or active-Link selection.

## Sampling and units

- **CPU** is the change in process user + kernel CPU time divided by elapsed
  monotonic time, multiplied by 100. One fully occupied logical core is 100%;
  parallel work can exceed 100%. This is not host-wide CPU load or a percentage
  of a container quota. Unix uses `getrusage(RUSAGE_SELF)`; Windows uses
  `GetProcessTimes`, interpreting its CPU counters as 100 ns durations.
- **Logical CPUs** is the count available to the Go runtime at process startup,
  not a promise of the CPU time available under resource limits.
- **Go memory** is Go's total managed read/write memory minus released heap pages.
  It includes runtime bookkeeping and stacks as well as heap objects. It excludes
  allocations/mappings outside the Go runtime and is not process RSS.
- **Heap** is memory occupied by live objects and objects not yet reclaimed by GC.
- **Goroutines** counts current goroutines. **Uptime** starts when the Agent's
  configuration reconciler is initialized; it resets after Agent restart.

Sampling uses a mutex and a one-second cache, without a polling goroutine or
per-Link requests. Resource fields ride in the existing batched Agent reports,
normally sent every two seconds. Reports own their samples, so concurrent callers
cannot mutate the sampler's cache. A CPU rate needs two valid, nondecreasing
counter samples. The first sample, an OS error or a counter reset omits the CPU
value; zero remains a valid measured result. Other fields remain available.
Platforms without a CPU provider still publish Go memory/runtime fields.

See the [Go runtime metric definitions](https://pkg.go.dev/runtime/metrics),
[Unix process accounting](https://man7.org/linux/man-pages/man2/getrusage.2.html)
and [Windows process timing](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getprocesstimes).

## API and freshness

`AgentReport.resources` is optional for older Agents. When present it contains:

| JSON field | Meaning |
| --- | --- |
| `cpu_percent` | Optional finite nonnegative process CPU percentage |
| `logical_cpus` | Positive logical CPU count |
| `go_memory_bytes` | Go-managed memory excluding released heap pages |
| `heap_bytes` | Occupied Go heap bytes |
| `goroutines` | Positive goroutine count |
| `uptime_seconds` | Whole seconds since Agent reconciler initialization |

The controller checks finite CPU values, bounded counts, heap ≤ Go memory, and
integer ranges that remain exact in JavaScript. It publishes immutable resource
snapshots through both `/api/v1/telemetry` and the authenticated live event stream.
It retains the last report while disconnected; API consumers must check
`connected` and `last_seen`. The UI shows Unavailable when the Agent is offline,
the browser's live stream is stale/disconnected, or a field has not been reported.
Unavailable CPU is never formatted as 0%.

## Verification

Unit tests cover interval normalization including >100% CPU, measured zero,
initial/error/reset samples, cached/concurrent reads, ownership and malformed
report rejection. A native process-counter test verifies that actual CPU work
advances the counter. TLS controller/client tests verify nonempty resources and
CPU intervals arriving in the actual management API. The Linux three-Agent TUN
scenario requires valid resource samples from every Agent before forwarding
acceptance; it also verifies offline TUN recovery and controller outage traffic.
Browser tests cover table/Node detail rendering, absent versus zero CPU, and
hiding historical data when either the Agent or live stream disconnects.

Native CPU sampling is verified on Linux. Windows, macOS, FreeBSD, OpenBSD and
NetBSD code cross-compiles; their native platform acceptance remains outstanding.
