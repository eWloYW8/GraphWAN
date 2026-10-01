import { linkRTT, edgeView, type AgentStatus, type Network, type State } from './model'

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
  const saved = state.networks.find((n) => n.id === network.id)
  const routingKey = (n: Network) =>
    JSON.stringify([
      n.nodes.map((node) => [node.id, node.agent_id, node.address]).sort(),
      n.edges.map((e) => [e.id, e.a, e.b, e.enabled, e.weight, e.transports]).sort(),
    ])
  const useLive = live && !!saved && routingKey(saved) === routingKey(network)
  const available = (a: string, b: string, edgeID: string) => {
    const endpoint = (id: string) =>
      statuses.find((s) => s.agent_id === network.nodes.find((n) => n.id === id)?.agent_id)
    const healthy = (s: AgentStatus | undefined) =>
      !!s?.connected &&
      Date.now() - Date.parse(s.last_seen) <= 45000 &&
      !s.config_error &&
      !s.runtime_error
    const left = endpoint(a),
      right = endpoint(b)
    if (
      network.nodes.find((n) => n.id === a)?.wireguard ||
      network.nodes.find((n) => n.id === b)?.wireguard
    ) {
      const gateway = network.nodes.find((n) => n.id === a)?.wireguard ? right : left
      return (
        healthy(gateway) &&
        !!gateway?.links?.some(
          (l) =>
            l.network_id === network.id &&
            l.edge_id === edgeID &&
            l.transport === 'wireguard' &&
            l.healthy,
        )
      )
    }
    return (
      healthy(left) &&
      healthy(right) &&
      (left?.links ?? []).some(
        (l) =>
          l.network_id === network.id &&
          l.edge_id === edgeID &&
          l.healthy &&
          (right?.links ?? []).some(
            (r) =>
              r.network_id === network.id &&
              r.edge_id === edgeID &&
              r.healthy &&
              r.link_id === l.link_id &&
              r.transport === l.transport,
          ),
      )
    )
  }
  const edges = network.edges.filter(
    (e) =>
      e.enabled &&
      !excluded.has(e.a) &&
      !excluded.has(e.b) &&
      (!useLive || available(e.a, e.b, e.id)),
  )
  const result: PathView = { nodes: [source], edges: [], weight: 0, state: 'Unknown' }
  // Drafts have not passed the controller's validation. A negative undirected
  // edge would make Dijkstra revisit the same nodes indefinitely.
  if (edges.some((e) => !Number.isInteger(e.weight) || e.weight < 1 || e.weight > 4294967295))
    return { ...result, reason: 'Routing weights must be integers between 1 and 4294967295.' }
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
      return {
        ...result,
        state: 'Unavailable',
        reason: useLive ? 'No available route.' : 'No configured route.',
      }
    result.edges.push(edge.id)
    result.nodes.push(next)
    result.weight += edge.weight
    current = next
  }
  if (!live) return { ...result, reason: 'Live telemetry is unavailable.' }
  if (!useLive)
    return { ...result, reason: 'Unsaved routing changes; showing the configured draft path.' }
  for (const id of result.nodes) {
    const node = network.nodes.find((n) => n.id === id)!
    if (node.wireguard) continue
    const status = statuses.find((s) => s.agent_id === node.agent_id)
    if (!status?.connected)
      return { ...result, state: 'Unavailable', reason: `${node.name} is offline.` }
    if (status.applied_revision !== state.revision || status.config_error || status.runtime_error)
      return { ...result, reason: `${node.name} has not confirmed the current configuration.` }
  }
  let rtt = 0
  let measured = true
  for (const id of result.edges) {
    const edge = network.edges.find((e) => e.id === id)!
    const view = edgeView(network, edge, statuses, {}, live)
    if (view.state !== 'Connected')
      return {
        ...result,
        state: 'Unavailable',
        reason: 'A configured hop has no healthy active link.',
      }
    const sample = linkRTT(view.active)
    if (sample === undefined) measured = false
    else rtt += sample
  }
  return {
    ...result,
    state: 'Active',
    rtt_ms: measured ? rtt : undefined,
  }
}
