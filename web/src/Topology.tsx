import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ReactFlow,
  useReactFlow,
  useNodesInitialized,
  useStore,
  useNodesState,
  Background,
  Controls,
  MiniMap,
  Handle,
  Position,
  ConnectionMode,
  type NodeProps,
  type Node as FlowNode,
} from '@xyflow/react'
import { Server, Plus, Settings2, Cable, MousePointer2 } from 'lucide-react'
import { FloatingEdge, FloatingConnection, LineConnection } from './FloatingEdge'
import { planAnchors } from './edgeGeometry'
import { planRoutes, planLabels, type LineStyle } from './edgeRouting'
import { activePath } from './activePath'
import { ResourceDetails } from './Resources'
import { Badge, Field } from './components'
import {
  type State,
  type Network,
  type AgentStatus,
  type Rates,
  type Edge,
  nodeState,
  edgeView,
  createEdge,
  removeNode,
  bytes,
  rate,
  shortID,
} from './model'
export type Selection =
  { type: 'node' | 'edge'; id: string } | { type: 'path'; id: string; target: string } | null

// Keep the entire graph visible when the canvas changes size or membership.
// Metrics and ordinary edits do not reset a user's pan/zoom position.
function FitLayout({ count }: { count: number }) {
  const { fitView } = useReactFlow()
  const initialized = useNodesInitialized()
  const width = useStore((s) => s.width)
  const height = useStore((s) => s.height)
  useEffect(() => {
    if (!initialized || !count) return
    const task = requestAnimationFrame(() => {
      void fitView({ padding: 0.3, maxZoom: 1 })
    })
    return () => cancelAnimationFrame(task)
  }, [fitView, initialized, width, height, count])
  return null
}

type GraphNode = FlowNode<
  { name: string; address: string; state: string; connecting: boolean; path: boolean },
  'agent'
>
function AgentNode({ data, isConnectable }: NodeProps<GraphNode>) {
  return (
    <div
      className={`graph-node ${data.state.toLowerCase()} ${data.connecting ? 'connect-mode' : ''} ${data.path ? 'active-path-node' : ''}`}
    >
      <Handle
        id="surface"
        type="source"
        position={Position.Top}
        className="node-connect-surface"
        isConnectable={isConnectable}
      />
      <Handle
        id="target"
        type="target"
        position={Position.Top}
        className="node-hidden-target"
        isConnectable={isConnectable}
      />
      <div className="node-content">
        <div className="node-icon">
          <Server size={18} />
        </div>
        <div>
          <strong>{data.name}</strong>
          <span>{data.address}</span>
        </div>
        <i className="node-dot" title={data.state} />
      </div>
    </div>
  )
}
const nodeTypes = { agent: AgentNode }
const edgeTypes = { floating: FloatingEdge }
export default function Topology({
  state,
  network,
  statuses,
  rates,
  live,
  editing,
  selection,
  select,
  change,
  addNode,
  addEdge,
}: {
  state: State
  network: Network
  statuses: AgentStatus[]
  rates: Rates
  live: boolean
  editing: boolean
  selection: Selection
  select: (s: Selection) => void
  change: (network: Network) => void
  addNode: () => void
  addEdge: () => void
}) {
  const [lineStyle, setLineStyle] = useState<LineStyle>(() => {
    try {
      return localStorage.getItem('graphwan:line-style') === 'bezier' ? 'bezier' : 'line'
    } catch {
      return 'line'
    }
  })
  const chooseLineStyle = (style: LineStyle) => {
    setLineStyle(style)
    try {
      localStorage.setItem('graphwan:line-style', style)
    } catch {
      /* Browsing without storage remains supported. */
    }
  }
  const [connecting, setConnecting] = useState(false)
  const [dragging, setDragging] = useState(false)
  const connected = useRef(false)
  useEffect(() => {
    setConnecting(false)
  }, [editing, network.id])
  const connect = (source: string, target: string) => {
    if (!editing) return
    const edge = createEdge(network, source, target)
    if (edge) {
      change({ ...network, edges: [...network.edges, edge] })
      select({ type: 'edge', id: edge.id })
    }
  }
  const validConnection = ({ source, target }: { source: string | null; target: string | null }) =>
    !!source &&
    !!target &&
    source !== target &&
    !network.edges.some(
      (edge) =>
        (edge.a === source && edge.b === target) || (edge.a === target && edge.b === source),
    )
  const path = useMemo(
    () =>
      selection?.type === 'path'
        ? activePath(network, selection.id, selection.target, state, statuses, live)
        : undefined,
    [network, selection, state, statuses, live],
  )
  const selectNode = (id: string, multiple = false) => {
    if (selection?.type === 'path') {
      if (id === selection.id) select({ type: 'node', id: selection.target })
      else if (id === selection.target) select({ type: 'node', id: selection.id })
      else select(multiple ? { ...selection, target: id } : { type: 'node', id })
    } else if (selection?.type === 'node') {
      select(
        selection.id === id
          ? null
          : multiple
            ? { type: 'path', id: selection.id, target: id }
            : { type: 'node', id },
      )
    } else select({ type: 'node', id })
  }
  const status = useMemo(() => new Map(statuses.map((s) => [s.agent_id, s])), [statuses])
  const desiredNodes = useMemo<GraphNode[]>(
    () =>
      network.nodes.map((n) => ({
        id: n.id,
        type: 'agent',
        position: n.position,
        selected:
          selection?.type === 'path'
            ? selection.id === n.id || selection.target === n.id
            : selection?.type === 'node' && selection.id === n.id,
        data: {
          connecting,
          path: path?.nodes.includes(n.id) ?? false,
          name: n.name,
          address: n.address,
          state: nodeState(
            state.agents.find((a) => a.id === n.agent_id),
            status.get(n.agent_id),
            live,
          ),
        },
        ariaLabel: `${n.name}, ${n.address}`,
      })),
    [network.nodes, selection, state.agents, status, live, connecting, path],
  )
  const [nodes, setNodes, applyNodeChanges] = useNodesState<GraphNode>([])
  useEffect(() => {
    setNodes((previous) => {
      const byID = new Map(previous.map((node) => [node.id, node]))
      // Retain React Flow's measured dimensions and interaction metadata while
      // reconciling the domain model. Only positions belong to desired state.
      return desiredNodes.map((node) => ({ ...byID.get(node.id), ...node }))
    })
  }, [desiredNodes, setNodes])
  const geometryKey = JSON.stringify(
    nodes.map((n) => [n.id, n.position.x, n.position.y, n.measured?.width, n.measured?.height]),
  )
  const edgeKey = JSON.stringify(network.edges.map((e) => [e.id, e.a, e.b]))
  const geometry = useMemo(() => {
    const rectangles = new Map(
      nodes.map((n) => [
        n.id,
        { ...n.position, width: n.measured?.width ?? 198, height: n.measured?.height ?? 64 },
      ]),
    )
    const anchors = planAnchors(rectangles, network.edges)
    return {
      rectangles,
      anchors,
      routes: planRoutes(rectangles, network.edges, anchors, dragging, lineStyle),
    }
  }, [geometryKey, edgeKey, dragging, lineStyle])
  const focus = new Set(
    network.edges
      .filter((e) =>
        selection?.type === 'node'
          ? e.a === selection.id || e.b === selection.id
          : selection?.type === 'path'
            ? path?.edges.includes(e.id)
            : selection?.type === 'edge' && e.id === selection.id,
      )
      .map((e) => e.id),
  )
  const focusKey = [...focus].sort().join('/')
  const labels = useMemo(
    () =>
      dragging
        ? new Map(
            [...geometry.routes].map(([id, route]) => [id, { x: route.labelX, y: route.labelY }]),
          )
        : planLabels(geometry.routes, geometry.rectangles, focus),
    [geometry, focusKey, dragging],
  )
  const edges = network.edges.map((e) => {
    const view = edgeView(network, e, statuses, rates, live)
    const focused = focus.has(e.id)
    const dimmed = selection !== null && !focused
    return {
      id: e.id,
      source: e.a,
      target: e.b,
      selected: selection?.type === 'edge' && selection.id === e.id,
      type: 'floating',
      sourceHandle: 'surface',
      targetHandle: 'target',
      data: {
        anchors: geometry.anchors.get(e.id)!,
        route: geometry.routes.get(e.id)!,
        labelPoint: labels.get(e.id)!,
        focused,
        dimmed,
      },
      zIndex: focused ? 100 : dimmed ? -1 : 0,
      label: editing
        ? `Weight ${e.weight}`
        : view.state === 'Connected'
          ? `${view.active!.transport.toUpperCase()} · ${view.active!.rtt_ms.toFixed(1)} ms${(view.tx ?? 0) > 0 ? ` · ${rate(view.tx)}` : ''}`
          : view.state,
      style: {
        stroke: focused ? '#087b66' : view.state === 'Connected' ? '#278f79' : '#91a3a0',
        strokeWidth: focused ? 3.5 : 1.8,
        opacity: dimmed ? 0.12 : 1,
        strokeDasharray: view.state === 'Connected' ? undefined : '5 5',
      },
      labelStyle: { fill: '#35514b', fontSize: 11 },
      labelBgStyle: { fill: '#fff' },
      labelBgPadding: [8, 5] as [number, number],
      labelBgBorderRadius: 5,
      ariaLabel: `Edge ${network.nodes.find((n) => n.id === e.a)?.name} to ${network.nodes.find((n) => n.id === e.b)?.name}`,
    }
  })
  const node =
    selection?.type === 'node' ? network.nodes.find((n) => n.id === selection.id) : undefined
  const edge =
    selection?.type === 'edge' ? network.edges.find((e) => e.id === selection.id) : undefined
  const agent = node ? state.agents.find((a) => a.id === node.agent_id) : undefined
  const agentStatus = node ? status.get(node.agent_id) : undefined
  const updateEdge = (patch: Partial<Edge>) =>
    edge &&
    change({
      ...network,
      edges: network.edges.map((e) => (e.id === edge.id ? { ...e, ...patch } : e)),
    })
  return (
    <div className="workspace">
      <section className="canvas-card" aria-label="Network topology">
        <div className="canvas-toolbar">
          {editing && (
            <div className="actions" role="group" aria-label="Editing tools">
              <button
                aria-label="Move nodes"
                aria-pressed={!connecting}
                onClick={() => setConnecting(false)}
              >
                <MousePointer2 size={14} />
                Move
              </button>
              <button
                aria-label="Connect nodes"
                aria-pressed={connecting}
                onClick={() => setConnecting(true)}
              >
                <Cable size={14} />
                Connect
              </button>
              <button onClick={addNode}>
                <Plus size={14} />
                Add node
              </button>
              <button onClick={addEdge} disabled={network.nodes.length < 2}>
                <Cable size={14} />
                Add edge
              </button>
            </div>
          )}
          <div className="drawing-control">
            <span>Drawing</span>
            <div className="drawing-switch" role="group" aria-label="Line drawing">
              <button aria-pressed={lineStyle === 'line'} onClick={() => chooseLineStyle('line')}>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                  <path
                    d="M3 13 13 3"
                    stroke="currentColor"
                    strokeWidth="1.5"
                    strokeLinecap="round"
                  />
                </svg>
                Line
              </button>
              <button
                aria-pressed={lineStyle === 'bezier'}
                onClick={() => chooseLineStyle('bezier')}
              >
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                  <path
                    d="M3 13C3 3 13 13 13 3"
                    stroke="currentColor"
                    strokeWidth="1.5"
                    strokeLinecap="round"
                  />
                </svg>
                Bezier
              </button>
            </div>
          </div>
        </div>
        <div className="canvas">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            connectionLineComponent={lineStyle === 'line' ? LineConnection : FloatingConnection}
            connectionMode={ConnectionMode.Loose}
            connectOnClick={false}
            isValidConnection={validConnection}
            nodesDraggable={editing && !connecting}
            nodesConnectable={editing}
            edgesReconnectable={false}
            deleteKeyCode={null}
            onNodeDragStart={() => setDragging(true)}
            onNodeDragStop={() => setDragging(false)}
            onNodeClick={(event, n) => selectNode(n.id, event.ctrlKey || event.metaKey)}
            onNodeContextMenu={(event, n) => {
              if (event.ctrlKey) {
                event.preventDefault()
                selectNode(n.id, true)
              }
            }}
            onEdgeClick={(_, e) => select({ type: 'edge', id: e.id })}
            onPaneClick={() => select(null)}
            onNodesChange={(changes) => {
              applyNodeChanges(changes)
              if (!editing) return
              const positions = new Map(
                changes.flatMap((c) =>
                  c.type === 'position' && c.position ? [[c.id, c.position] as const] : [],
                ),
              )
              if (positions.size)
                change({
                  ...network,
                  nodes: network.nodes.map((n) =>
                    positions.has(n.id) ? { ...n, position: positions.get(n.id)! } : n,
                  ),
                })
            }}
            onConnectStart={() => {
              connected.current = false
            }}
            onConnect={(connection) => {
              connected.current = true
              connect(connection.source, connection.target)
            }}
            onConnectEnd={(event, connection) => {
              if (connected.current || !connection.fromNode) return
              const pointer = 'changedTouches' in event ? event.changedTouches[0] : event
              if (!pointer) return
              const target = document
                .elementFromPoint(pointer.clientX, pointer.clientY)
                ?.closest('.react-flow__node')
                ?.getAttribute('data-id')
              if (target) connect(connection.fromNode.id, target)
            }}
            fitView
            fitViewOptions={{ padding: 0.3, maxZoom: 1 }}
            minZoom={0.15}
            maxZoom={2}
            connectionRadius={48}
          >
            <FitLayout count={nodes.length} />
            <Background color="#c9d9d4" gap={22} size={1} />
            <Controls showInteractive={false} />
            <MiniMap
              style={{ width: 115, height: 75 }}
              pannable
              zoomable
              nodeColor="#7ebbaa"
              maskColor="rgba(239,245,242,.7)"
            />
          </ReactFlow>
          {network.nodes.length === 0 && (
            <div className="canvas-empty">
              <div className="empty-symbol">
                <Server size={29} />
              </div>
              <h3>No nodes</h3>

              <button className="primary" onClick={addNode} disabled={!editing}>
                <Plus size={16} />
                {editing ? 'Add first node' : 'Switch to Edit to add nodes'}
              </button>
            </div>
          )}
        </div>
        <div className="canvas-footer">
          {path ? (
            <span className="active-path-summary" role="status">
              {path.nodes.map((id) => network.nodes.find((n) => n.id === id)?.name).join(' → ')}
              {' · '}
              {path.rtt_ms !== undefined && `${path.rtt_ms.toFixed(1)} ms · `}
              {path.state === 'Active'
                ? 'Active path'
                : path.state === 'Unknown'
                  ? 'Unconfirmed'
                  : 'Unavailable'}
            </span>
          ) : (
            <span className="muted">Ctrl-click two nodes to show their active path</span>
          )}

          <span>
            <i className="dot online" />
            Connected
          </span>
          <span>
            <i className="dot offline" />
            Down / unknown
          </span>
        </div>
      </section>
      <aside className="inspector" aria-label="Inspector">
        <div className="inspector-title">
          <Settings2 size={17} />
          <h2>
            {path
              ? 'Active path'
              : node
                ? 'Node details'
                : edge
                  ? 'Edge details'
                  : 'Network details'}
          </h2>
        </div>
        {path ? (
          <>
            <div className="inspector-intro">
              <h3>
                {network.nodes.find((n) => n.id === selection?.id)?.name} →{' '}
                {selection?.type === 'path' &&
                  network.nodes.find((n) => n.id === selection.target)?.name}
              </h3>
              <Badge>
                {path.state === 'Active'
                  ? 'Active'
                  : path.state === 'Unknown'
                    ? 'Unknown'
                    : 'Unavailable'}
              </Badge>
            </div>
            <div className="path-latency">
              <span>Total latency</span>
              <strong>{path.rtt_ms === undefined ? '—' : `${path.rtt_ms.toFixed(1)} ms`}</strong>
              <small>Hop RTT sum</small>
            </div>
            {path.reason && <p className="muted">{path.reason}</p>}
            <p className="muted">
              {path.edges.length} hops · Weight {path.weight}
            </p>
            <ol className="active-path-hops">
              {path.nodes.map((id, index) => {
                const hop = network.edges.find((e) => e.id === path.edges[index])
                const view = hop ? edgeView(network, hop, statuses, rates, live) : undefined
                return (
                  <li key={id}>
                    <strong>{network.nodes.find((n) => n.id === id)?.name}</strong>
                    {view && (
                      <small>
                        {view.state === 'Connected'
                          ? `${view.active!.transport.toUpperCase()} · ${view.active!.rtt_ms.toFixed(1)} ms`
                          : view.state}
                      </small>
                    )}
                  </li>
                )
              })}
            </ol>
            <button
              className="wide subtle"
              onClick={() =>
                selection?.type === 'path' &&
                select({ type: 'path', id: selection.target, target: selection.id })
              }
            >
              Reverse direction
            </button>
          </>
        ) : node ? (
          <>
            <div className="inspector-intro">
              <h3>{node.name}</h3>
              <Badge>{nodeState(agent, agentStatus, live)}</Badge>
            </div>
            <fieldset disabled={!editing}>
              <Field label="Node name">
                <input
                  value={node.name}
                  maxLength={128}
                  onChange={(e) =>
                    change({
                      ...network,
                      nodes: network.nodes.map((n) =>
                        n.id === node.id ? { ...n, name: e.target.value } : n,
                      ),
                    })
                  }
                />
              </Field>
              <Field label="Agent">
                <select
                  value={node.agent_id}
                  onChange={(e) =>
                    change({
                      ...network,
                      nodes: network.nodes.map((n) =>
                        n.id === node.id ? { ...n, agent_id: e.target.value } : n,
                      ),
                    })
                  }
                >
                  {state.agents
                    .filter(
                      (a) =>
                        a.id === node.agent_id ||
                        (!a.revoked && !network.nodes.some((n) => n.agent_id === a.id)),
                    )
                    .map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                </select>
              </Field>
              <Field label="Virtual IP">
                <input
                  value={node.address}
                  onChange={(e) =>
                    change({
                      ...network,
                      nodes: network.nodes.map((n) =>
                        n.id === node.id ? { ...n, address: e.target.value } : n,
                      ),
                    })
                  }
                />
              </Field>
            </fieldset>
            <dl>
              <dt>Agent version</dt>
              <dd>{agentStatus?.version || 'Not reported'}</dd>
              <dt>Applied revision</dt>
              <dd>{agentStatus?.applied_revision ?? '—'}</dd>
              <dt>Last seen</dt>
              <dd>
                {agentStatus?.last_seen && !agentStatus.last_seen.startsWith('0001')
                  ? new Date(agentStatus.last_seen).toLocaleString()
                  : 'Never'}
              </dd>
              <dt>Node ID</dt>
              <dd className="mono">{shortID(node.id)}</dd>
            </dl>
            <ResourceDetails status={agentStatus} live={live} />
            {agentStatus?.config_error && (
              <p role="alert" className="error">
                {agentStatus.config_error}
              </p>
            )}
            {agentStatus?.runtime_error && (
              <p role="alert" className="error">
                {agentStatus.runtime_error}
              </p>
            )}
            <h4>Advertised endpoints</h4>
            {agent?.endpoints?.length ? (
              agent.endpoints.map((ep) => (
                <div className="endpoint" key={ep.id}>
                  <span className="tag">{ep.source}</span>
                  <code>{ep.url}</code>
                </div>
              ))
            ) : (
              <p className="muted">No endpoints reported yet.</p>
            )}
            {editing && (
              <button
                className="danger wide"
                onClick={() => {
                  change(removeNode(network, node.id))
                  select(null)
                }}
              >
                Remove node and its edges
              </button>
            )}
          </>
        ) : edge ? (
          <>
            <div className="inspector-intro">
              <h3>
                {network.nodes.find((n) => n.id === edge.a)?.name}
                <span className="muted"> ↔ </span>
                {network.nodes.find((n) => n.id === edge.b)?.name}
              </h3>
              <Badge>{edgeView(network, edge, statuses, rates, live).state}</Badge>
            </div>
            <fieldset disabled={!editing}>
              <label className="check">
                <input
                  type="checkbox"
                  checked={edge.enabled}
                  onChange={(e) => updateEdge({ enabled: e.target.checked })}
                />
                Enable edge
              </label>
              <Field label="Routing weight">
                <input
                  type="number"
                  min={1}
                  max={4294967295}
                  value={edge.weight}
                  onChange={(e) => updateEdge({ weight: Number(e.target.value) })}
                />
              </Field>
              <h4>Allowed transports</h4>
              <div className="checks">
                {(['udp', 'tcp', 'quic', 'ws', 'wss', 'grpc'] as const).map((t) => (
                  <label className="check" key={t}>
                    <input
                      type="checkbox"
                      checked={edge.transports.includes(t)}
                      onChange={(e) =>
                        updateEdge({
                          transports: e.target.checked
                            ? [...edge.transports, t]
                            : edge.transports.filter((v) => v !== t),
                        })
                      }
                    />
                    {t.toUpperCase()}
                  </label>
                ))}
              </div>
              <h4>Connection methods</h4>
              {(
                [
                  ['ipv4_direct', 'IPv4 direct'],
                  ['ipv6_direct', 'IPv6 direct'],
                  ['hole_punch', 'NAT hole punching'],
                ] as const
              ).map(([key, label]) => (
                <label className="check" key={key}>
                  <input
                    type="checkbox"
                    checked={edge.methods[key]}
                    onChange={(e) =>
                      updateEdge({ methods: { ...edge.methods, [key]: e.target.checked } })
                    }
                  />
                  {label}
                </label>
              ))}
              <Field label="Preferred path">
                <select
                  value={edge.preferred_candidate ?? ''}
                  onChange={(e) => updateEdge({ preferred_candidate: e.target.value })}
                >
                  <option value="">Automatic · Lowest RTT</option>
                  {[
                    ...new Map(
                      edgeView(network, edge, statuses, rates, live).links.map((l) => [
                        l.candidate_id,
                        l,
                      ]),
                    ).values(),
                  ].map((l) => (
                    <option key={l.candidate_id} value={l.candidate_id}>
                      {l.transport.toUpperCase()} · {l.remote} · {shortID(l.candidate_id)}
                    </option>
                  ))}
                  {edge.preferred_candidate &&
                    !edgeView(network, edge, statuses, rates, live).links.some(
                      (l) => l.candidate_id === edge.preferred_candidate,
                    ) && (
                      <option value={edge.preferred_candidate}>
                        Saved path · {shortID(edge.preferred_candidate)} (unavailable)
                      </option>
                    )}
                </select>
              </Field>
            </fieldset>
            <h4>Live links · both endpoints</h4>
            {edgeView(network, edge, statuses, rates, live).links.map((l) => (
              <div className="link-detail" key={`${l.agent}/${l.link_id}`}>
                <div>
                  <strong>{l.transport.toUpperCase()}</strong>
                  <Badge>
                    {!live ? 'Unknown' : l.active ? 'Active' : l.healthy ? 'Standby' : 'Down'}
                  </Badge>
                </div>
                <small>
                  {state.agents.find((a) => a.id === l.agent)?.name} → {l.remote}
                </small>
                <div className="link-metrics">
                  <span>{l.rtt_ms.toFixed(1)} ms RTT</span>
                  <span>{(l.loss * 100).toFixed(1)}% loss</span>
                  <span>↓ {bytes(l.rx_bytes)}</span>
                  <span>↑ {bytes(l.tx_bytes)}</span>
                </div>
                <small className="mono">Session {shortID(l.link_id)}</small>
              </div>
            ))}
            {!edgeView(network, edge, statuses, rates, live).links.length && (
              <p className="muted">No links reported.</p>
            )}
            {editing && (
              <button
                className="danger wide"
                onClick={() => {
                  change({ ...network, edges: network.edges.filter((e) => e.id !== edge.id) })
                  select(null)
                }}
              >
                Remove edge
              </button>
            )}
          </>
        ) : (
          <>
            <div className="inspector-intro">
              <h3>{network.name}</h3>
            </div>
            <fieldset disabled={!editing}>
              <Field label="Network name">
                <input
                  value={network.name}
                  maxLength={128}
                  onChange={(e) => change({ ...network, name: e.target.value })}
                />
              </Field>
              <Field label="Subnet (CIDR)">
                <input
                  value={network.cidr}
                  onChange={(e) => change({ ...network, cidr: e.target.value })}
                />
              </Field>
              <Field label="TUN MTU">
                <input
                  type="number"
                  min={1280}
                  max={9000}
                  value={network.mtu}
                  onChange={(e) => change({ ...network, mtu: Number(e.target.value) })}
                />
              </Field>
              <Field label="Encryption">
                <select
                  value={network.cipher}
                  onChange={(e) => change({ ...network, cipher: e.target.value })}
                >
                  <option value="aes-128-gcm">AES-128-GCM</option>
                  <option value="aes-256-gcm">AES-256-GCM</option>
                  <option value="chacha20-poly1305">ChaCha20-Poly1305</option>
                  <option value="xchacha20-poly1305">XChaCha20-Poly1305</option>
                </select>
              </Field>
            </fieldset>
            <h4>
              Nodes <span className="count">{network.nodes.length}</span>
            </h4>
            <div className="object-list">
              {network.nodes.map((n) => (
                <button
                  key={n.id}
                  onClick={(event) => selectNode(n.id, event.ctrlKey || event.metaKey)}
                >
                  <Server size={15} />
                  <span>
                    {n.name}
                    <small>{n.address}</small>
                  </span>
                  <i
                    className={`dot ${nodeState(
                      state.agents.find((a) => a.id === n.agent_id),
                      status.get(n.agent_id),
                      live,
                    ).toLowerCase()}`}
                  />
                </button>
              ))}
            </div>
            <h4>
              Edges <span className="count">{network.edges.length}</span>
            </h4>
            <div className="object-list">
              {network.edges.map((e) => (
                <button key={e.id} onClick={() => select({ type: 'edge', id: e.id })}>
                  <Cable size={15} />
                  <span>
                    {network.nodes.find((n) => n.id === e.a)?.name} ↔{' '}
                    {network.nodes.find((n) => n.id === e.b)?.name}
                    <small>Weight {e.weight}</small>
                  </span>
                </button>
              ))}
            </div>
          </>
        )}
        {selection && (
          <button className="wide subtle" onClick={() => select(null)}>
            Back to network details
          </button>
        )}
      </aside>
    </div>
  )
}
