export type Transport = 'udp' | 'tcp' | 'quic' | 'ws' | 'wss' | 'grpc'
export const transports: Transport[] = ['udp', 'tcp', 'quic', 'ws', 'wss', 'grpc']
export type Endpoint = {
  id: string
  transport: Transport
  url: string
  source: 'manual' | 'interface' | 'observed'
  expires_at?: string
}
export type Agent = {
  id: string
  name: string
  public_key: string
  listen_port: number
  revoked: boolean
  endpoints: Endpoint[] | null
  stun_servers?: string[]
}
export type Node = {
  id: string
  agent_id: string
  name: string
  address: string
  position: { x: number; y: number }
}
export type Edge = {
  id: string
  a: string
  b: string
  weight: number
  enabled: boolean
  transports: Transport[]
  methods: { ipv4_direct: boolean; ipv6_direct: boolean; hole_punch: boolean }
  preferred_candidate?: string
}
export type Network = {
  id: string
  name: string
  cidr: string
  mtu: number
  cipher: string
  nodes: Node[]
  edges: Edge[]
}
export type State = { schema: number; revision: number; networks: Network[]; agents: Agent[] }
export type Link = {
  network_id: string
  edge_id: string
  link_id: string
  candidate_id: string
  transport: Transport
  remote: string
  healthy: boolean
  active: boolean
  rtt_ms: number
  loss: number
  rx_bytes: number
  tx_bytes: number
}
export type AgentStatus = {
  agent_id: string
  connected: boolean
  last_seen: string
  version: string
  applied_revision: number
  config_error?: string
  runtime_error?: string
  links: Link[] | null
}
export type Snapshot = { at: string; revision: number; state?: State; agents: AgentStatus[] }
export type Rates = Record<string, { rx: number; tx: number }>
export const linkKey = (agent: string, link: string) => `${agent}/${link}`
export function ratesBetween(previous: AgentStatus[], current: AgentStatus[]): Rates {
  const result: Rates = {}
  const before = new Map(previous.map((s) => [s.agent_id, s]))
  for (const s of current) {
    const old = before.get(s.agent_id)
    const elapsed = old ? (Date.parse(s.last_seen) - Date.parse(old.last_seen)) / 1000 : 0
    if (!s.connected || !old?.connected || elapsed <= 0 || elapsed > 45) continue
    const links = new Map(old.links?.map((l) => [l.link_id, l]))
    for (const l of s.links ?? []) {
      const prev = links.get(l.link_id)
      if (!prev || l.rx_bytes < prev.rx_bytes || l.tx_bytes < prev.tx_bytes) continue
      result[linkKey(s.agent_id, l.link_id)] = {
        rx: ((l.rx_bytes - prev.rx_bytes) * 8) / elapsed,
        tx: ((l.tx_bytes - prev.tx_bytes) * 8) / elapsed,
      }
    }
  }
  return result
}
export const newID = () => crypto.randomUUID().replaceAll('-', '')
export const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
export const rate = (n: number | undefined) =>
  n === undefined
    ? '—'
    : n >= 1e6
      ? `${(n / 1e6).toFixed(1)} Mbps`
      : n >= 1e3
        ? `${(n / 1e3).toFixed(1)} Kbps`
        : `${Math.round(n)} bps`
export const bytes = (n: number) =>
  n >= 1e9
    ? `${(n / 1e9).toFixed(2)} GB`
    : n >= 1e6
      ? `${(n / 1e6).toFixed(1)} MB`
      : n >= 1e3
        ? `${(n / 1e3).toFixed(1)} KB`
        : `${n} B`
export const shortID = (id: string) => id.slice(0, 8)
export function nodeState(
  agent: Agent | undefined,
  status: AgentStatus | undefined,
  live: boolean,
) {
  if (agent?.revoked) return 'Revoked'
  if (!live) return 'Unknown'
  if (!status?.connected) return 'Offline'
  if (status.config_error || status.runtime_error) return 'Error'
  return 'Online'
}
export type EdgeView = {
  state: string
  links: (Link & { agent: string })[]
  active?: Link & { agent: string }
  rx?: number
  tx?: number
}
export function edgeView(
  network: Network,
  edge: Edge,
  statuses: AgentStatus[],
  rates: Rates,
  live: boolean,
): EdgeView {
  const links = statuses.flatMap((s) =>
    (s.links ?? [])
      .filter((l) => l.network_id === network.id && l.edge_id === edge.id)
      .map((l) => ({
        ...l,
        healthy: l.healthy && s.connected,
        active: l.active && s.connected,
        agent: s.agent_id,
      })),
  )
  const active = links.filter((l) => l.active && l.healthy)
  const same = active.length === 2 && active[0].link_id === active[1].link_id
  const chosen = active[0]
  const speed = chosen ? rates[linkKey(chosen.agent, chosen.link_id)] : undefined
  return {
    state: !edge.enabled
      ? 'Disabled'
      : !live
        ? 'Unknown'
        : same
          ? 'Connected'
          : active.length
            ? 'Switching'
            : 'Down',
    links,
    active: chosen,
    ...speed,
  }
}
export function removeNode(network: Network, id: string): Network {
  return {
    ...network,
    nodes: network.nodes.filter((n) => n.id !== id),
    edges: network.edges.filter((e) => e.a !== id && e.b !== id),
  }
}
export function createEdge(network: Network, a: string, b: string): Edge | undefined {
  if (
    a === b ||
    !network.nodes.some((n) => n.id === a) ||
    !network.nodes.some((n) => n.id === b) ||
    network.edges.some((e) => (e.a === a && e.b === b) || (e.a === b && e.b === a))
  )
    return
  return {
    id: newID(),
    a,
    b,
    weight: 10,
    enabled: true,
    transports: ['udp', 'tcp'],
    methods: { ipv4_direct: true, ipv6_direct: true, hole_punch: false },
  }
}
export type Draft = { network: Network; base: Network; revision: number }
export function rebase(draft: Draft, state: State): Draft {
  const remote = state.networks.find((n) => n.id === draft.network.id)
  return equal(remote, draft.base) ? { ...draft, revision: state.revision } : draft
}
