import { edgeView, type AgentStatus, type Network, type State } from './model'

export type PathView = {
  nodes: string[]
  edges: string[]
  weight: number
  rtt_ms?: number
  state: 'Active' | 'Unavailable' | 'Unknown'
  reason?: string
}

// Match internal/routing/routing.go: weight, then first-hop ID, then node ID.
// Recompute the next hop at each intermediate node, as the forwarding plane does.
export function activePath(
  network: Network,
  source: string,
  destination: string,
  state: State,
  statuses: AgentStatus[],
  live: boolean,
): PathView {
  const excluded = new Set(
    network.nodes
      .filter((n) => state.agents.find((a) => a.id === n.agent_id)?.revoked)
      .map((n) => n.id),
  )
  const edges = network.edges.filter((e) => e.enabled && !excluded.has(e.a) && !excluded.has(e.b))
  const result: PathView = { nodes: [source], edges: [], weight: 0, state: 'Unknown' }
  if (
    !network.nodes.some((n) => n.id === source) ||
    !network.nodes.some((n) => n.id === destination) ||
    excluded.has(source) ||
    excluded.has(destination)
  )
    return { ...result, state: 'Unavailable', reason: 'Endpoint is unavailable.' }
  let current = source
  while (current !== destination) {
    const best = new Map<string, { cost: number; first: string }>([
      [current, { cost: 0, first: '' }],
    ])
    const queue = [{ node: current, cost: 0, first: '' }]
    while (queue.length) {
      queue.sort(
        (a, b) => a.cost - b.cost || a.first.localeCompare(b.first) || a.node.localeCompare(b.node),
      )
      const entry = queue.shift()!
      const known = best.get(entry.node)!
      if (entry.cost !== known.cost || entry.first !== known.first) continue
      for (const edge of edges) {
        const next = edge.a === entry.node ? edge.b : edge.b === entry.node ? edge.a : undefined
        if (!next) continue
        const candidate = {
          cost: entry.cost + edge.weight,
          first: entry.node === current ? next : entry.first,
        }
        const old = best.get(next)
        if (
          !old ||
          candidate.cost < old.cost ||
          (candidate.cost === old.cost && candidate.first < old.first)
        ) {
          best.set(next, candidate)
          queue.push({ node: next, ...candidate })
        }
      }
    }
    const next = best.get(destination)?.first
    const edge = edges.find(
      (e) => (e.a === current && e.b === next) || (e.b === current && e.a === next),
    )
    if (!next || !edge || result.nodes.includes(next))
      return { ...result, state: 'Unavailable', reason: 'No configured route.' }
    result.edges.push(edge.id)
    result.nodes.push(next)
    result.weight += edge.weight
    current = next
  }
  if (!live) return { ...result, reason: 'Live telemetry is unavailable.' }
  const saved = state.networks.find((n) => n.id === network.id)
  const routingKey = (n: Network) =>
    JSON.stringify(n.edges.map((e) => [e.id, e.a, e.b, e.enabled, e.weight]).sort())
  if (!saved || routingKey(saved) !== routingKey(network))
    return { ...result, reason: 'Unsaved routing changes; showing the configured draft path.' }
  for (const id of result.nodes) {
    const node = network.nodes.find((n) => n.id === id)!
    const status = statuses.find((s) => s.agent_id === node.agent_id)
    if (!status?.connected)
      return { ...result, state: 'Unavailable', reason: `${node.name} is offline.` }
    if (status.applied_revision !== state.revision || status.config_error || status.runtime_error)
      return { ...result, reason: `${node.name} has not confirmed the current configuration.` }
  }
  let rtt = 0
  for (const id of result.edges) {
    const edge = network.edges.find((e) => e.id === id)!
    const view = edgeView(network, edge, statuses, {}, live)
    if (view.state !== 'Connected')
      return {
        ...result,
        state: 'Unavailable',
        reason: 'A configured hop has no healthy active link.',
      }
    if (!Number.isFinite(view.active?.rtt_ms) || view.active!.rtt_ms < 0)
      return { ...result, reason: 'Hop latency is unavailable.' }
    rtt += view.active!.rtt_ms
  }
  return { ...result, state: 'Active', rtt_ms: rtt }
}
