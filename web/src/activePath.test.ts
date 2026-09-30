import { describe, it, expect } from 'vitest'
import { activePath } from './activePath'
import type { Network, State, AgentStatus } from './model'

function fixture() {
  const network: Network = {
    id: 'net',
    name: 'test',
    cidr: '7.7.7.0/24',
    mtu: 1280,
    cipher: 'aes-128-gcm',
    nodes: ['a', 'b', 'c'].map((id) => ({
      id,
      agent_id: id,
      name: id,
      address: '',
      position: { x: 0, y: 0 },
    })),
    edges: [
      { id: 'ab', a: 'a', b: 'b', weight: 20 },
      { id: 'ac', a: 'a', b: 'c', weight: 5 },
      { id: 'cb', a: 'c', b: 'b', weight: 5 },
    ].map((e) => ({
      ...e,
      enabled: true,
      transports: ['tcp'],
      methods: { ipv4_direct: true, ipv6_direct: false, hole_punch: false },
    })),
  }
  const state: State = {
    schema: 1,
    revision: 7,
    networks: [network],
    agents: network.nodes.map((n) => ({
      id: n.id,
      name: n.name,
      public_key: '',
      listen_port: 1,
      revoked: false,
      endpoints: [],
    })),
  }
  const statuses: AgentStatus[] = network.nodes.map((n) => ({
    agent_id: n.id,
    connected: true,
    last_seen: '',
    version: '',
    applied_revision: 7,
    links: network.edges
      .filter((e) => e.a === n.id || e.b === n.id)
      .map((e) => ({
        network_id: network.id,
        edge_id: e.id,
        link_id: e.id,
        candidate_id: e.id,
        transport: 'tcp',
        remote: '',
        healthy: true,
        active: true,
        rtt_ms: e.id === 'ab' ? 1 : 100,
        loss: 0,
        rx_bytes: 0,
        tx_bytes: 0,
      })),
  }))
  return { network, state, statuses }
}
describe('forwarding path', () => {
  it('uses configured weight rather than RTT and confirms every active hop', () => {
    const f = fixture()
    expect(activePath(f.network, 'a', 'b', f.state, f.statuses, true)).toEqual({
      nodes: ['a', 'c', 'b'],
      edges: ['ac', 'cb'],
      weight: 10,
      state: 'Active',
      rtt_ms: 200,
    })
  })
  it('does not invent an alternate forwarding route when an active hop fails', () => {
    const f = fixture()
    f.statuses[0].links!.find((l) => l.edge_id === 'ac')!.healthy = false
    const path = activePath(f.network, 'a', 'b', f.state, f.statuses, true)
    expect(path.edges).toEqual(['ac', 'cb'])
    expect(path.state).toBe('Unavailable')
    expect(path.rtt_ms).toBeUndefined()
  })
  it('marks stale configuration and disconnected telemetry as unconfirmed', () => {
    const f = fixture()
    f.statuses[2].applied_revision = 6
    expect(activePath(f.network, 'a', 'b', f.state, f.statuses, true).state).toBe('Unknown')
    expect(activePath(f.network, 'a', 'b', f.state, f.statuses, false).state).toBe('Unknown')
  })
  it('excludes revoked nodes and disabled edges', () => {
    const f = fixture()
    f.state.agents[2].revoked = true
    expect(activePath(f.network, 'a', 'b', f.state, f.statuses, true).edges).toEqual(['ab'])
    f.network.edges[0].enabled = false
    expect(activePath(f.network, 'a', 'b', f.state, f.statuses, true).state).toBe('Unavailable')
  })
  it('breaks equal-weight ties by the next-hop ID, as the controller does', () => {
    const f = fixture()
    f.network.edges[0].weight = 10
    expect(activePath(f.network, 'a', 'b', f.state, f.statuses, true).edges).toEqual(['ab'])
  })
  it('does not claim an unsaved weight change is an active forwarding path', () => {
    const f = fixture()
    const draft = {
      ...f.network,
      edges: f.network.edges.map((e) => ({ ...e, weight: e.id === 'ab' ? 1 : e.weight })),
    }
    const path = activePath(draft, 'a', 'b', f.state, f.statuses, true)
    expect(path.edges).toEqual(['ab'])
    expect(path.state).toBe('Unknown')
  })
})
