export type Transport = 'udp' | 'tcp' | 'quic' | 'ws' | 'wss' | 'grpc' | 'wireguard'
export const transports: Transport[] = ['udp', 'tcp', 'quic', 'ws', 'wss', 'grpc']
export type Endpoint = {
  id: string
  transport: Transport
  url: string
  source: 'manual' | 'interface' | 'observed'
  expires_at?: string
}
export type Agent = {
  update?: { id: string; source: string; asset: { version: string }; created_at: string }
  id: string
  name: string
  public_key: string
  listen_port: number
  revoked: boolean
  endpoints: Endpoint[] | null
  stun_servers?: string[]
  exclude_container_ips?: boolean
}
export type AdvertisedSubnet = { prefix: string; gateway_mode: 'off' | 'route' | 'snat' }
export type Node = {
  wireguard?: { public_key: string; endpoint?: string }
  advertised_subnets?: AdvertisedSubnet[]
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
  methods: {
    ipv4_direct: boolean
    ipv6_direct: boolean
    hole_punch: boolean
    hole_punch_extension?: boolean
  }
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
export type ServerEndpoint = {
  id: string
  transport: 'tcp' | 'websocket' | 'grpc' | 'wss'
  url: string
  source: 'manual' | 'interface' | 'observed'
  expires_at?: string
}
export type Controller = {
  update?: { id: string; asset: { version: string }; created_at: string }
  id: string
  name: string
  public_key: string
  endpoints: ServerEndpoint[]
  stun_servers?: string[]
  revoked?: boolean
}
export type State = {
  servers?: Controller[]
  cluster_id?: string
  schema: number
  revision: number
  networks: Network[]
  agents: Agent[]
}
export type Link = {
  rtt_valid?: boolean
  rtt_measured_at?: string
  wireguard_public_key?: string
  last_handshake?: string
  network_id: string
  edge_id: string
  link_id: string
  candidate_id: string
  transport: Transport
  remote: string
  local?: string
  observed_local?: string
  healthy: boolean
  active: boolean
  rtt_ms: number
  loss: number
  rx_bytes: number
  tx_bytes: number
}
// WireGuard uses optional ICMP samples; missing replies never imply zero RTT.
export function linkRTT(link: Link | undefined): number | undefined {
  if (!link || !Number.isFinite(link.rtt_ms) || link.rtt_ms < 0) return undefined
  if (
    link.transport === 'wireguard' &&
    (!link.rtt_valid ||
      !link.healthy ||
      !link.rtt_measured_at ||
      !Number.isFinite(Date.parse(link.rtt_measured_at)) ||
      Date.now() - Date.parse(link.rtt_measured_at) > 40_000)
  )
    return undefined
  return link.rtt_ms
}
export function latencyLabel(link: Link | undefined) {
  const rtt = linkRTT(link)
  return rtt === undefined ? 'RTT unknown' : `${rtt.toFixed(1)} ms`
}
export type ResourceUsage = {
  cpu_percent?: number
  logical_cpus: number
  go_memory_bytes: number
  heap_bytes: number
  goroutines: number
  uptime_seconds: number
}
export type AgentUpdateStatus = {
  managed: boolean
  service?: string
  os: string
  arch: string
  request_id?: string
  version?: string
  phase?: string
  error?: string
  updated_at?: string
}
export type AgentStatus = {
  update?: AgentUpdateStatus
  resources?: ResourceUsage
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
// getRandomValues is also available on HTTP management frontends; randomUUID
// requires a secure context. IDs are 128 random bits encoded as lowercase hex.
export const newID = () =>
  Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, '0'),
  ).join('')
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
  const same = edge.transports.includes('wireguard')
    ? active.length === 1
    : active.length === 2 && active[0].link_id === active[1].link_id
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
  const removed = new Set([id])
  for (const edge of network.edges) {
    const other = edge.a === id ? edge.b : edge.b === id ? edge.a : ''
    if (network.nodes.find((n) => n.id === other)?.wireguard) removed.add(other)
  }
  return {
    ...network,
    nodes: network.nodes.filter((n) => !removed.has(n.id)),
    edges: network.edges.filter((e) => !removed.has(e.a) && !removed.has(e.b)),
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
  const left = network.nodes.find((n) => n.id === a)!,
    right = network.nodes.find((n) => n.id === b)!
  if (left.wireguard && right.wireguard) return
  const wg = left.wireguard ? left : right.wireguard ? right : undefined
  if (wg && network.edges.some((e) => e.a === wg.id || e.b === wg.id)) return
  return {
    id: newID(),
    a,
    b,
    weight: 10,
    enabled: true,
    transports: wg ? ['wireguard'] : ['udp', 'tcp'],
    methods: { ipv4_direct: true, ipv6_direct: true, hole_punch: false },
  }
}
export type Draft = { network: Network; base: Network; revision: number }
export function rebase(draft: Draft, state: State): Draft {
  const remote = state.networks.find((n) => n.id === draft.network.id)
  return equal(remote, draft.base) ? { ...draft, revision: state.revision } : draft
}
