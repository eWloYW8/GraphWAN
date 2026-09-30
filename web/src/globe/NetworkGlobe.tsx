import { useEffect, useMemo, useState } from 'react'
import { LocateFixed, Minus, Plus } from 'lucide-react'
import Globe, { type ViewCommand } from './Globe'
import type { Appearance, GlobeEdge, GlobeNode, Selection as GlobeSelection } from './types'
import type { Selection } from '../Topology'
import { edgeView, nodeState, type Network, type State, type AgentStatus } from '../model'
import type { Locations } from './useAgentLocations'
import './globe.css'

export default function NetworkGlobe({
  network,
  state,
  statuses,
  live,
  locations,
  error,
  retry,
  selection,
  select,
  selectNode,
  focusedEdges,
  pathNodes,
  connecting,
  connect,
}: {
  network: Network
  state: State
  statuses: AgentStatus[]
  live: boolean
  locations?: Locations
  error: string
  retry: () => void
  selection: Selection
  select: (selection: Selection) => void
  selectNode: (id: string, multiple?: boolean) => void
  focusedEdges: string[]
  pathNodes: string[]
  connecting: boolean
  connect: (source: string, target: string) => void
}) {
  const [command, setCommand] = useState<ViewCommand>({ kind: 'reset', serial: 0 })
  const [source, setSource] = useState<string>()
  useEffect(() => {
    setSource(undefined)
  }, [connecting, network.id])
  const move = (kind: ViewCommand['kind']) =>
    setCommand((old) => ({ kind, serial: old.serial + 1 }))
  const nodes = useMemo<GlobeNode[]>(
    () =>
      network.nodes.flatMap((node) => {
        const location = locations?.agents[node.agent_id]?.location
        return location ? [{ id: node.id, name: node.name, ...location }] : []
      }),
    [network.nodes, locations],
  )
  const edges = useMemo<GlobeEdge[]>(
    () => network.edges.map(({ id, a, b }) => ({ id, a, b })),
    [network.edges],
  )
  const missing = network.nodes.filter((node) => !locations?.agents[node.agent_id]?.location)
  const appearance = useMemo<Appearance>(() => {
    const agents = new Map(state.agents.map((a) => [a.id, a]))
    const status = new Map(statuses.map((s) => [s.agent_id, s]))
    return {
      nodes: Object.fromEntries(
        network.nodes.map((node) => {
          const health = nodeState(agents.get(node.agent_id), status.get(node.agent_id), live)
          const location = locations?.agents[node.agent_id]?.location
          return [
            node.id,
            {
              color:
                health === 'Online'
                  ? '#69e8c0'
                  : health === 'Error' || health === 'Revoked'
                    ? '#ef8888'
                    : '#8b9da8',
              title: [node.name, health, location?.ip, location?.city, location?.country]
                .filter(Boolean)
                .join(' · '),
            },
          ]
        }),
      ),
      edges: Object.fromEntries(
        network.edges.map((edge) => {
          const health = edgeView(network, edge, statuses, {}, live).state
          return [
            edge.id,
            {
              color: health === 'Connected' ? '#69e8c0' : '#899daa',
              dashed: health !== 'Connected',
            },
          ]
        }),
      ),
      focusedNodes: source ? [source] : selection?.type === 'node' ? [selection.id] : pathNodes,
      focusedEdges,
      dimmed: selection !== null,
    }
  }, [network, state.agents, statuses, live, locations, source, selection, pathNodes, focusedEdges])
  const choose = (picked: GlobeSelection, multiple = false) => {
    if (picked?.type === 'node') {
      if (connecting) {
        if (source && source !== picked.id) {
          connect(source, picked.id)
          setSource(undefined)
        } else setSource(source === picked.id ? undefined : picked.id)
      } else selectNode(picked.id, multiple)
    } else {
      setSource(undefined)
      select(picked)
    }
  }
  const message =
    error ||
    locations?.error ||
    (locations?.pending ? 'Loading GeoIP database…' : !locations ? 'Locating agents…' : '')
  return (
    <div className="network-globe">
      <Globe
        nodes={nodes}
        edges={edges}
        selection={selection?.type === 'path' ? null : selection}
        select={choose}
        appearance={appearance}
        rotating={false}
        labels
        command={command}
        connect={
          connecting
            ? (a, b) => {
                connect(a, b)
                setSource(undefined)
              }
            : undefined
        }
      />
      {message && (
        <div className="network-globe-message" role="status">
          {message}
          {error && <button onClick={retry}>Retry</button>}
        </div>
      )}
      {connecting && (
        <div className="network-globe-connect" role="status">
          {source ? 'Select the second node' : 'Select two nodes or drag between markers'}
        </div>
      )}
      {missing.length > 0 && (
        <details className="network-globe-unlocated">
          <summary>Unlocated ({missing.length})</summary>
          {missing.map((node) => (
            <button
              key={node.id}
              title={locations?.agents[node.agent_id]?.reason || 'Location unavailable'}
              onClick={(event) => selectNode(node.id, event.ctrlKey || event.metaKey)}
            >
              {node.name}
            </button>
          ))}
        </details>
      )}
      <div className="network-globe-controls" role="group" aria-label="Globe controls">
        <button onClick={() => move('in')} title="Zoom in" aria-label="Zoom in">
          <Plus size={17} />
        </button>
        <button onClick={() => move('out')} title="Zoom out" aria-label="Zoom out">
          <Minus size={17} />
        </button>
        <button
          onClick={() => move('reset')}
          title="Reset globe view"
          aria-label="Reset globe view"
        >
          <LocateFixed size={17} />
        </button>
      </div>
      <div className="network-globe-attribution">
        <a href="https://db-ip.com" target="_blank" rel="noreferrer">
          IP Geolocation by DB-IP
        </a>
        <span> · </span>
        <a href="https://www.naturalearthdata.com/" target="_blank" rel="noreferrer">
          Natural Earth
        </a>
      </div>
    </div>
  )
}
