import { type AgentStatus, bytes } from './model'

function current(status: AgentStatus | undefined, live: boolean) {
  return live && status?.connected ? status.resources : undefined
}
function cpu(percent: number | undefined) {
  return percent === undefined ? 'Unavailable' : `${percent.toFixed(1)}%`
}
function uptime(seconds: number) {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return days
    ? `${days}d ${hours}h`
    : hours
      ? `${hours}h ${minutes}m`
      : `${minutes}m ${seconds % 60}s`
}
export function ResourceSummary({ status, live }: { status?: AgentStatus; live: boolean }) {
  const usage = current(status, live)
  if (!usage) return <span className="muted">Unavailable</span>
  return (
    <>
      <span title="Agent process CPU; 100% is one logical core">CPU {cpu(usage.cpu_percent)}</span>
      <small>Go memory {bytes(usage.go_memory_bytes)}</small>
    </>
  )
}
export function ResourceDetails({ status, live }: { status?: AgentStatus; live: boolean }) {
  const usage = current(status, live)
  return (
    <>
      <h4>Agent resources</h4>
      <p className="muted">
        Process CPU: 100% is one logical core. Memory covers Go-managed allocations.
      </p>
      <dl aria-label="Agent resources">
        <dt>CPU</dt>
        <dd>{usage ? cpu(usage.cpu_percent) : 'Unavailable'}</dd>
        <dt>Logical CPUs</dt>
        <dd>{usage?.logical_cpus ?? '—'}</dd>
        <dt>Go memory</dt>
        <dd>{usage ? bytes(usage.go_memory_bytes) : '—'}</dd>
        <dt>Heap</dt>
        <dd>{usage ? bytes(usage.heap_bytes) : '—'}</dd>
        <dt>Goroutines</dt>
        <dd>{usage?.goroutines ?? '—'}</dd>
        <dt>Uptime</dt>
        <dd>{usage ? uptime(usage.uptime_seconds) : '—'}</dd>
      </dl>
    </>
  )
}
