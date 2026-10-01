import { x25519 } from '@noble/curves/ed25519.js'
import ipaddr from 'ipaddr.js'
import type { Network, Node, State, AgentStatus } from './model'

// Client secrets never enter State, API requests, URLs, or browser storage.
const privateKeys = new Map<string, string>()
export const forgetWireGuardKeys = () => privateKeys.clear()
export const clientPrivateKey = (id: string) => privateKeys.get(id) ?? ''
const base64 = (raw: Uint8Array) => btoa(String.fromCharCode(...raw))
export function clientPublicKey(secret: string) {
  const raw = Uint8Array.from(atob(secret), (c) => c.charCodeAt(0))
  if (raw.length !== 32) throw new Error('Private key must encode 32 bytes.')
  return base64(x25519.getPublicKey(raw))
}
export function generateClientKey(id: string) {
  const raw = crypto.getRandomValues(new Uint8Array(32))
  raw[0] &= 248
  raw[31] &= 127
  raw[31] |= 64
  const secret = base64(raw)
  privateKeys.set(id, secret)
  return clientPublicKey(secret)
}
export function nextAddress(network: Network) {
  try {
    const [address, bits] = ipaddr.parseCIDR(network.cidr)
    const raw = address.toByteArray()
    const width = raw.length * 8
    const base =
      (raw.reduce((n, b) => (n << 8n) | BigInt(b), 0n) >> BigInt(width - bits)) <<
      BigInt(width - bits)
    const end = base + (1n << BigInt(width - bits))
    const used = new Set(network.nodes.map((n) => ipaddr.parse(n.address).toNormalizedString()))
    for (
      let value = base + (width === 32 && bits >= 31 ? 0n : 1n);
      value < end && value <= base + BigInt(network.nodes.length + 2);
      value++
    ) {
      if (width === 32 && bits <= 30 && value === end - 1n) break
      const bytes = raw.map((_, i) => Number((value >> BigInt((raw.length - i - 1) * 8)) & 255n))
      const candidate = ipaddr.fromByteArray(bytes)
      if (!used.has(candidate.toNormalizedString())) return candidate.toString()
    }
  } catch {
    /* Invalid drafts are validated when saved. */
  }
  return ''
}
export function wireGuardAccess(
  network: Network,
  node: Node,
  state: State,
  statuses: AgentStatus[],
) {
  const edge = network.edges.find((e) => e.a === node.id || e.b === node.id)
  const gateway = network.nodes.find((n) => n.id === (edge?.a === node.id ? edge.b : edge?.a))
  const agent = state.agents.find((a) => a.id === gateway?.agent_id)
  const status = statuses.find((s) => s.agent_id === agent?.id)
  const link = status?.links?.find(
    (l) => l.network_id === network.id && l.edge_id === edge?.id && l.transport === 'wireguard',
  )
  const endpoints = (agent?.endpoints ?? [])
    .filter(
      (e) => e.transport === 'udp' && (!e.expires_at || Date.parse(e.expires_at) > Date.now()),
    )
    .map((e) => {
      try {
        const url = new URL(e.url)
        const host = url.hostname.replace(/^\[|\]$/g, '')
        const ip = ipaddr.isValid(host) ? ipaddr.parse(host) : undefined
        const rank =
          (ip && ip.range() !== 'unicast' ? 10 : 0) +
          { observed: 0, interface: 2, manual: 4 }[e.source] +
          (ip?.kind() === 'ipv6' ? 1 : 0)
        return { address: url.host, rank }
      } catch {
        return { address: '', rank: 100 }
      }
    })
    .filter((e) => e.address)
    .sort((a, b) => a.rank - b.rank || a.address.localeCompare(b.address))
  return {
    edge,
    gateway,
    agent,
    status,
    link,
    endpoint: node.wireguard?.endpoint || endpoints[0]?.address || '',
  }
}
export function wireGuardNodeState(
  network: Network,
  node: Node,
  state: State,
  statuses: AgentStatus[],
  live: boolean,
) {
  if (!live) return 'Unknown'
  const { edge, agent, status, link } = wireGuardAccess(network, node, state, statuses)
  if (!edge?.enabled) return 'Disabled'
  if (agent?.revoked) return 'Revoked'
  if (!status?.connected) return 'Offline'
  if (status.config_error || status.runtime_error) return 'Error'
  return link?.healthy
    ? 'Online'
    : link?.last_handshake && !link.last_handshake.startsWith('0001')
      ? 'Idle'
      : 'Waiting'
}
