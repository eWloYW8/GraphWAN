import { sha256 } from '@noble/hashes/sha2.js'
import { bytesToHex, utf8ToBytes } from '@noble/hashes/utils.js'
import {
  edgeView,
  linkRTT,
  rate,
  type Network,
  type Edge,
  type AgentStatus,
  type Rates,
} from './model'
export type FullMeshGroup = {
  id: string
  name: string
  members: string[]
  transports: Edge['transports']
  methods: Edge['methods']
}
export type GroupLink = {
  id: string
  node: string
  group: string
  weight: number
  enabled: boolean
  transports: Edge['transports']
  methods: Edge['methods']
}
export const groupEdgeID = (group: string, a: string, b: string) =>
  bytesToHex(sha256(utf8ToBytes(`graphwan/full-mesh/${group}/${[a, b].sort().join('/')}`))).slice(
    0,
    32,
  )
export function internalEdges(group: FullMeshGroup): Edge[] {
  const members = [...group.members].sort()
  return members.flatMap((a, i) =>
    members.slice(i + 1).map((b) => ({
      id: groupEdgeID(group.id, a, b),
      a,
      b,
      weight: 1,
      enabled: true,
      transports: group.transports,
      methods: group.methods,
    })),
  )
}
export function childEdges(network: Network, link: GroupLink): Edge[] {
  return (network.groups?.find((g) => g.id === link.group)?.members ?? []).map((b) => ({
    id: groupEdgeID(link.id, link.node, b),
    a: link.node,
    b,
    weight: link.weight,
    enabled: link.enabled,
    transports: link.transports,
    methods: link.methods,
  }))
}
export function effectiveEdges(network: Network): Edge[] {
  return [
    ...network.edges,
    ...(network.groups ?? []).flatMap(internalEdges),
    ...(network.group_links ?? []).flatMap((l) => childEdges(network, l)),
  ]
}
export function aggregate(
  network: Network,
  edges: Edge[],
  statuses: AgentStatus[],
  rates: Rates,
  live: boolean,
) {
  const views = edges.map((e) => edgeView(network, e, statuses, rates, live))
  const connected = views.filter((v) => v.state === 'Connected')
  const rtts = connected
    .flatMap((v) => v.links.filter((l) => l.active && l.healthy).map(linkRTT))
    .filter((r): r is number => r !== undefined)
  const samples = views.filter((v) => v.rx !== undefined && v.tx !== undefined)
  const speed =
    live && samples.length ? samples.reduce((sum, v) => sum + v.rx! + v.tx!, 0) : undefined
  const maximum = live && rtts.length ? Math.max(...rtts) : undefined
  const state = !live
    ? 'Unknown'
    : edges.every((e) => !e.enabled)
      ? 'Disabled'
      : connected.length === edges.length
        ? 'Connected'
        : connected.length
          ? 'Partial'
          : 'Down'
  return {
    state,
    connected: connected.length,
    total: edges.length,
    speed,
    maximum,
    label: `${rate(speed)} · ${maximum === undefined ? 'RTT unknown' : `max ${maximum.toFixed(1)} ms`} · ${live ? connected.length : '?'}/${edges.length}`,
  }
}
export function pruneGroups(network: Network): Network {
  const ids = new Set(network.nodes.map((n) => n.id))
  const groups = (network.groups ?? [])
    .map((g) => ({ ...g, members: g.members.filter((id) => ids.has(id)) }))
    .filter((g) => g.members.length >= 2)
  return {
    ...network,
    groups,
    group_links: (network.group_links ?? []).filter(
      (l) => ids.has(l.node) && groups.some((g) => g.id === l.group),
    ),
  }
}
// Explicit edges covered by a group are replaced only after confirmation in the editor.
export function coveredEdges(
  network: Network,
  groups = network.groups ?? [],
  links = network.group_links ?? [],
) {
  const pairs = new Set(
    [
      ...groups.flatMap(internalEdges),
      ...links.flatMap((l) => childEdges({ ...network, groups }, l)),
    ].map((e) => [e.a, e.b].sort().join('/')),
  )
  return network.edges.filter((e) => pairs.has([e.a, e.b].sort().join('/')))
}
export function topologyError(network: Network): string | undefined {
  const seen = new Set<string>()
  for (const group of network.groups ?? []) {
    if (!group.name.trim() || group.members.length < 2)
      return 'Each group needs a name and at least two members.'
    for (const id of group.members) {
      if (seen.has(id)) return 'A node can belong to only one group.'
      seen.add(id)
    }
  }
  const count =
    network.edges.length +
    (network.groups ?? []).reduce(
      (n, g) => n + (g.members.length * (g.members.length - 1)) / 2,
      0,
    ) +
    (network.group_links ?? []).reduce(
      (n, l) => n + (network.groups?.find((g) => g.id === l.group)?.members.length ?? 0),
      0,
    )
  if (count > 100000) return 'Expanded topology exceeds 100,000 edges.'
  const pairs = new Set<string>()
  for (const link of network.group_links ?? []) {
    const group = network.groups?.find((g) => g.id === link.group)
    if (!group || group.members.includes(link.node))
      return 'A node cannot connect to its own group.'
  }
  for (const edge of effectiveEdges(network)) {
    const pair = [edge.a, edge.b].sort().join('/')
    if (pairs.has(pair))
      return 'These groups or aggregate links create duplicate connections. Remove the conflicting link first.'
    pairs.add(pair)
  }
}

// Switching an aggregate to individual edges removes the aggregate as a whole.
export function individualConnection(network: Network, a: string, b: string): Network | undefined {
  if (network.groups?.some((g) => g.members.includes(a) && g.members.includes(b))) return undefined
  const conflicts = (network.group_links ?? []).filter((l) =>
    childEdges(network, l).some((e) => (e.a === a && e.b === b) || (e.a === b && e.b === a)),
  )
  if (!conflicts.length) return network
  if (
    !window.confirm(
      'Replace the conflicting node-to-group link(s) and all their child edges with this individual edge?',
    )
  )
    return undefined
  return { ...network, group_links: network.group_links?.filter((l) => !conflicts.includes(l)) }
}
