import { describe, it, expect } from 'vitest'
import {
  createEdge,
  removeNode,
  edgeView,
  ratesBetween,
  rebase,
  type Network,
  type AgentStatus,
  type State,
} from './model'
const network: Network = {
  id: 'net',
  name: 'Lab',
  cidr: '10.0.0.0/24',
  mtu: 1280,
  cipher: 'chacha20-poly1305',
  nodes: [
    { id: 'a', agent_id: 'aa', name: 'A', address: '10.0.0.1', position: { x: 0, y: 0 } },
    { id: 'b', agent_id: 'ab', name: 'B', address: '10.0.0.2', position: { x: 200, y: 0 } },
  ],
  edges: [
    {
      id: 'edge',
      a: 'a',
      b: 'b',
      weight: 10,
      enabled: true,
      transports: ['udp'],
      methods: { ipv4_direct: true, ipv6_direct: true, hole_punch: false },
    },
  ],
}
function sample(id: string, time: string, rx = 100, tx = 200): AgentStatus {
  return {
    agent_id: id,
    connected: true,
    last_seen: time,
    applied_revision: 1,
    version: 'test',
    links: [
      {
        network_id: 'net',
        edge_id: 'edge',
        link_id: 'session',
        candidate_id: 'candidate',
        transport: 'udp',
        remote: '127.0.0.1:24752',
        healthy: true,
        active: true,
        rtt_ms: 15,
        loss: 0,
        rx_bytes: rx,
        tx_bytes: tx,
      },
    ],
  }
}
describe('topology integrity and concurrency', () => {
  it('rejects self-links, reverse duplicates, and unknown nodes', () => {
    expect(createEdge(network, 'a', 'a')).toBeUndefined()
    expect(createEdge(network, 'b', 'a')).toBeUndefined()
    expect(createEdge(network, 'a', 'missing')).toBeUndefined()
  })
  it('removes incident edges with a node without mutating the original', () => {
    const changed = removeNode(network, 'a')
    expect(changed.edges).toEqual([])
    expect(changed.nodes.map((n) => n.id)).toEqual(['b'])
    expect(network.edges).toHaveLength(1)
  })
  it('rebases unrelated revisions but preserves conflicting drafts', () => {
    const draft = { network: { ...network, name: 'Draft' }, base: network, revision: 1 }
    const state: State = { schema: 1, revision: 2, networks: [network], agents: [] }
    expect(rebase(draft, state).revision).toBe(2)
    expect(rebase(draft, { ...state, networks: [{ ...network, name: 'Other editor' }] })).toEqual(
      draft,
    )
  })
})
describe('telemetry semantics', () => {
  it('uses report times and individual sessions; ignores reset, stale and offline counters', () => {
    const before = sample('aa', '2026-01-01T00:00:00Z')
    const after = sample('aa', '2026-01-01T00:00:02Z', 300, 600)
    expect(ratesBetween([before], [after])['aa/session']).toEqual({ rx: 800, tx: 1600 })
    expect(ratesBetween([before], [before])).toEqual({})
    expect(ratesBetween([after], [sample('aa', '2026-01-01T00:00:03Z', 0, 0)])).toEqual({})
    expect(ratesBetween([before], [{ ...after, connected: false }])).toEqual({})
    expect(ratesBetween([before], [{ ...after, last_seen: '2026-01-01T00:01:00Z' }])).toEqual({})
  })
  it('requires both endpoints to agree, and never marks stale views connected', () => {
    const a = sample('aa', '2026-01-01T00:00:00Z'),
      b = sample('ab', '2026-01-01T00:00:00Z')
    expect(edgeView(network, network.edges[0], [a, b], {}, true).state).toBe('Connected')
    expect(edgeView(network, network.edges[0], [a, b], {}, false).state).toBe('Unknown')
    expect(
      edgeView(network, network.edges[0], [a, { ...b, connected: false }], {}, true).state,
    ).toBe('Switching')
    expect(edgeView(network, { ...network.edges[0], enabled: false }, [a, b], {}, true).state).toBe(
      'Disabled',
    )
    b.links![0].link_id = 'other'
    expect(edgeView(network, network.edges[0], [a, b], {}, true).state).toBe('Switching')
  })
})
