import type { Edge, EdgeView, Network } from './model'

type Report = EdgeView['links'][number]
export type ConnectionEnd = {
  name: string
  address?: string
  report?: Report
}

// A Session ID is shared by both ends; counters remain separate observations of
// the same traffic and must never be summed as two independent connections.
export function connectionViews(network: Network, edge: Edge, links: Report[]) {
  const a = network.nodes.find((n) => n.id === edge.a)
  const b = network.nodes.find((n) => n.id === edge.b)
  const sessions = new Map<string, { a?: Report; b?: Report }>()
  for (const report of links) {
    if (report.agent !== a?.agent_id && report.agent !== b?.agent_id) continue
    const session = sessions.get(report.link_id) ?? {}
    if (report.agent === a?.agent_id) session.a = report
    else session.b = report
    sessions.set(report.link_id, session)
  }
  const localAddress = (raw?: string) =>
    raw && !/^(?:0\.0\.0\.0|\[::\]):/.test(raw) ? raw : undefined
  return [...sessions]
    .map(([id, reports]) => {
      const first = reports.a ?? reports.b!
      const active = !!(reports.a?.active && reports.b?.active)
      const healthy = !!(reports.a?.healthy && reports.b?.healthy)
      return {
        id,
        candidate: first.candidate_id,
        transport: first.transport,
        state:
          !reports.a || !reports.b
            ? 'Awaiting peer'
            : active && healthy
              ? 'Active'
              : reports.a.active || reports.b.active
                ? 'Switching'
                : healthy
                  ? 'Standby'
                  : 'Down',
        ends: [
          {
            name: a?.name ?? 'A',
            address:
              reports.b?.remote || reports.a?.observed_local || localAddress(reports.a?.local),
            report: reports.a,
          },
          {
            name: b?.name ?? 'B',
            address:
              reports.a?.remote || reports.b?.observed_local || localAddress(reports.b?.local),
            report: reports.b,
          },
        ] as [ConnectionEnd, ConnectionEnd],
      }
    })
    .sort(
      (a, b) =>
        Number(b.state === 'Active') - Number(a.state === 'Active') || a.id.localeCompare(b.id),
    )
}

export function endpointParts(address?: string) {
  const match = address?.match(/^(?:\[([^\]]+)\]|([^:]+)):(\d+)$/)
  return match
    ? { ip: match[1] ?? match[2], port: match[3] }
    : { ip: address || 'Unknown', port: '—' }
}
