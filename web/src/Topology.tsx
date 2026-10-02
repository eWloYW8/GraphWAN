import GroupEditor, { type GroupEditorTarget } from './GroupEditor'
import {
  effectiveEdges,
  internalEdges,
  childEdges,
  aggregate,
  individualConnection,
} from './groups'
import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
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
import { Server, Plus, Settings2, Cable, MousePointer2, Globe2 } from 'lucide-react'
import { useAgentLocations } from './globe/useAgentLocations'
import { FloatingEdge, FloatingConnection, LineConnection } from './FloatingEdge'
import { planAnchors, groupFrameDimensions } from './edgeGeometry'
import { planRoutes, planLabels, type LineStyle } from './edgeRouting'
import { activePath } from './activePath'
import WireGuardNode from './WireGuardNode'
import { wireGuardNodeState } from './wireguard'
import { ResourceDetails } from './Resources'
import { Badge, Field } from './components'
import { connectionViews, endpointParts } from './connections'
import {
  type State,
  type AdvertisedSubnet,
  type Network,
  type AgentStatus,
  type Rates,
  type Edge,
  nodeState,
  latencyLabel,
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
  {
    name: string
    address: string
    state: string
    connecting: boolean
    path: boolean
    wireguard: boolean
    summary?: string
  },
  'agent' | 'meshGroup'
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
          {data.wireguard ? <Cable size={18} /> : <Server size={18} />}
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
function MeshGroupNode({ data, isConnectable }: NodeProps<GraphNode>) {
  return (
    <div className={`mesh-group-boundary ${data.path ? 'focused' : ''}`}>
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
      <div className="mesh-group-caption">
        <strong>{data.name}</strong>
        <span>{data.summary}</span>
      </div>
    </div>
  )
}
const nodeTypes = { agent: AgentNode, meshGroup: MeshGroupNode }
const edgeTypes = { floating: FloatingEdge }
const NetworkGlobe = lazy(() => import('./globe/NetworkGlobe'))
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
  const [scope, setScope] = useState<{ kind: 'group' | 'link'; id: string } | null>(null)
  const [groupEditor, setGroupEditor] = useState<GroupEditorTarget | null>(null)
  const allEdges = useMemo(() => effectiveEdges(network), [network])
  const scopeGroup =
    scope?.kind === 'group' ? network.groups?.find((g) => g.id === scope.id) : undefined
  const scopeLink =
    scope?.kind === 'link' ? network.group_links?.find((l) => l.id === scope.id) : undefined
  const scopedEdges = scopeGroup
    ? internalEdges(scopeGroup)
    : scopeLink
      ? childEdges(network, scopeLink)
      : undefined
  const scopeMembers = new Set(
    scopeGroup?.members ??
      (scopeLink
        ? [
            scopeLink.node,
            ...(network.groups?.find((g) => g.id === scopeLink.group)?.members ?? []),
          ]
        : network.nodes.map((n) => n.id)),
  )
  const displayEdges = scopedEdges ?? [
    ...network.edges,
    ...(network.group_links ?? []).map((l) => ({ ...l, a: l.node, b: l.group })),
  ]
  const displayNetwork = {
    ...network,
    nodes: network.nodes.filter((n) => scopeMembers.has(n.id)),
    edges: displayEdges,
  }
  const enter = (kind: 'group' | 'link', id: string) => {
    setScope({ kind, id })
    select(null)
  }
  useEffect(() => {
    setScope(null)
    setGroupEditor(null)
  }, [network.id])
  useEffect(() => {
    if (scope && !scopeGroup && !scopeLink) setScope(null)
  }, [scope, scopeGroup, scopeLink])
  const [viewStyle, setViewStyle] = useState<LineStyle | 'globe'>(() => {
    try {
      const saved = localStorage.getItem('graphwan:line-style')
      return saved === 'globe' || saved === 'bezier' ? saved : 'line'
    } catch {
      return 'line'
    }
  })
  const lineStyle = viewStyle === 'globe' ? 'line' : viewStyle
  const chooseLineStyle = (style: LineStyle | 'globe') => {
    setViewStyle(style)
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
    const group = network.groups?.find((g) => g.id === source || g.id === target)
    if (group) {
      const node = source === group.id ? target : source
      if (!group.members.includes(node) && network.nodes.some((n) => n.id === node && !n.wireguard))
        setGroupEditor({ kind: 'link', node, group: group.id })
      return
    }
    const updated = individualConnection(network, source, target)
    if (!updated) return
    const edge = createEdge(updated, source, target)
    if (edge) {
      change({ ...updated, edges: [...updated.edges, edge] })
      select({ type: 'edge', id: edge.id })
    }
  }
  const validConnection = ({ source, target }: { source: string | null; target: string | null }) =>
    !!source &&
    !!target &&
    (network.groups?.some(
      (g) =>
        (g.id === source || g.id === target) &&
        !g.members.includes(g.id === source ? target : source) &&
        network.nodes.some((n) => n.id === (g.id === source ? target : source) && !n.wireguard),
    ) ||
      !!createEdge({ ...network, group_links: [] }, source, target))

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
      network.nodes
        .filter((n) => scopeMembers.has(n.id))
        .map((n) => ({
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
            wireguard: !!n.wireguard,
            name: n.name,
            address: n.address,
            state: n.wireguard
              ? wireGuardNodeState(network, n, state, statuses, live)
              : nodeState(
                  state.agents.find((a) => a.id === n.agent_id),
                  status.get(n.agent_id),
                  live,
                ),
          },
          ariaLabel: `${n.name}, ${n.address}`,
        })),
    [network, selection, state, statuses, status, live, connecting, path, scope],
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
  const groupNodes: GraphNode[] = scope
    ? []
    : (network.groups ?? []).flatMap((g) => {
        const members = nodes.filter((n) => g.members.includes(n.id))
        if (!members.length) return []
        const x = Math.min(...members.map((n) => n.position.x)) - 30,
          y = Math.min(...members.map((n) => n.position.y)) - 70
        const width =
          Math.max(...members.map((n) => n.position.x + (n.measured?.width ?? 198))) - x + 30
        const height =
          Math.max(...members.map((n) => n.position.y + (n.measured?.height ?? 64))) - y + 30
        return [
          {
            id: g.id,
            type: 'meshGroup' as const,
            position: { x, y },
            ...groupFrameDimensions(width, height),
            style: { width, height },
            zIndex: -10,
            draggable: false,
            selectable: false,
            data: {
              name: g.name,
              summary: aggregate(network, internalEdges(g), statuses, rates, live).label,
              address: '',
              state: '',
              connecting,
              wireguard: false,
              path: internalEdges(g).some((e) => path?.edges.includes(e.id)),
            },
          },
        ]
      })
  const flowNodes = [...groupNodes, ...nodes]
  const geometryKey = JSON.stringify(
    flowNodes.map((n) => [
      n.id,
      n.position.x,
      n.position.y,
      n.width ?? n.measured?.width,
      n.height ?? n.measured?.height,
    ]),
  )
  const edgeKey = JSON.stringify(displayEdges.map((e) => [e.id, e.a, e.b]))
  const geometry = useMemo(() => {
    const rectangles = new Map(
      flowNodes.map((n) => [
        n.id,
        {
          ...n.position,
          width: n.width ?? n.measured?.width ?? 198,
          height: n.height ?? n.measured?.height ?? 64,
        },
      ]),
    )
    const anchors = planAnchors(rectangles, displayEdges)
    return {
      rectangles,
      anchors,
      routes: planRoutes(
        new Map([...rectangles].filter(([id]) => !network.groups?.some((g) => g.id === id))),
        displayEdges,
        anchors,
        dragging,
        lineStyle,
      ),
    }
  }, [geometryKey, edgeKey, dragging, lineStyle])
  const focus = new Set(
    displayEdges
      .filter((e) =>
        selection?.type === 'node'
          ? e.a === selection.id || e.b === selection.id
          : selection?.type === 'path'
            ? path?.edges.includes(e.id) ||
              (!scope &&
                network.group_links?.some(
                  (l) =>
                    l.id === e.id &&
                    childEdges(network, l).some((child) => path?.edges.includes(child.id)),
                ))
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
        : planLabels(
            geometry.routes,
            new Map(
              [...geometry.rectangles].filter(([id]) => !network.groups?.some((g) => g.id === id)),
            ),
            focus,
          ),
    [geometry, focusKey, dragging],
  )
  const edges = displayEdges.map((e) => {
    const link = !scope ? network.group_links?.find((l) => l.id === e.id) : undefined
    const summary = link
      ? aggregate(network, childEdges(network, link), statuses, rates, live)
      : undefined
    const view = edgeView(network, e, statuses, rates, live)
    if (summary) view.state = summary.state
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
        open: () => (link ? enter('link', link.id) : select({ type: 'edge', id: e.id })),
        anchors: geometry.anchors.get(e.id)!,
        route: geometry.routes.get(e.id)!,
        labelPoint: labels.get(e.id)!,
        focused,
        dimmed,
      },
      zIndex: focused ? 100 : dimmed ? -1 : 0,
      label: summary
        ? summary.label
        : editing
          ? `Weight ${e.weight}`
          : view.state === 'Connected'
            ? `${view.active!.transport.toUpperCase()} · ${latencyLabel(view.active)} · ↑ ${rate(view.tx)} ↓ ${rate(view.rx)}`
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
  const edge = selection?.type === 'edge' ? allEdges.find((e) => e.id === selection.id) : undefined
  const derivedOwner = edge
    ? (network.groups?.find((g) => internalEdges(g).some((e) => e.id === edge.id)) ??
      network.group_links?.find((l) => childEdges(network, l).some((e) => e.id === edge.id)))
    : undefined
  const scopeSummary = scopedEdges
    ? aggregate(network, scopedEdges, statuses, rates, live)
    : undefined
  const agent = node ? state.agents.find((a) => a.id === node.agent_id) : undefined
  const agentStatus = node ? status.get(node.agent_id) : undefined
  const connections = edge
    ? connectionViews(network, edge, edgeView(network, edge, statuses, rates, live).links)
    : []
  const geographyAgents = useMemo(() => {
    const ids = new Set(network.nodes.map((node) => node.agent_id))
    return state.agents.filter((agent) => ids.has(agent.id))
  }, [state.agents, network.nodes])
  const geography = useAgentLocations(
    geographyAgents,
    viewStyle === 'globe' || selection?.type === 'node',
  )
  const nodeGeography = node ? geography.data?.agents[node.agent_id] : undefined
  const updateEdge = (patch: Partial<Edge>) =>
    edge &&
    change({
      ...network,
      edges: network.edges.map((e) => (e.id === edge.id ? { ...e, ...patch } : e)),
    })
  return (
    <div className="workspace">
      {groupEditor && (
        <GroupEditor
          key={JSON.stringify(groupEditor)}
          network={network}
          target={groupEditor}
          save={change}
          close={() => setGroupEditor(null)}
        />
      )}
      <section className="canvas-card" aria-label="Network topology">
        <div className="canvas-toolbar">
          {scope && (
            <button
              onClick={() => {
                setScope(null)
                select(null)
              }}
            >
              ← Network
            </button>
          )}
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
              <button onClick={() => setGroupEditor({ kind: 'group' })}>Add group</button>
              <button
                disabled={!network.groups?.length}
                onClick={() => setGroupEditor({ kind: 'link' })}
              >
                Node → group
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
              <button aria-pressed={viewStyle === 'line'} onClick={() => chooseLineStyle('line')}>
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
                aria-pressed={viewStyle === 'bezier'}
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
              <button aria-pressed={viewStyle === 'globe'} onClick={() => chooseLineStyle('globe')}>
                <Globe2 size={16} />
                3D
              </button>
            </div>
          </div>
        </div>
        <div className="canvas">
          {viewStyle === 'globe' ? (
            <Suspense fallback={<div className="canvas-empty">Loading 3D view…</div>}>
              <NetworkGlobe
                key={network.id}
                network={displayNetwork}
                rates={rates}
                showMetrics={!!scope}
                groups={
                  scope
                    ? []
                    : (network.groups ?? []).map((g) => ({
                        ...g,
                        summary: aggregate(network, internalEdges(g), statuses, rates, live).label,
                      }))
                }
                groupLinks={
                  scope
                    ? []
                    : (network.group_links ?? []).map((l) => {
                        const summary = aggregate(
                          network,
                          childEdges(network, l),
                          statuses,
                          rates,
                          live,
                        )
                        return { ...l, summary: summary.label, state: summary.state }
                      })
                }
                enterGroup={enter}
                state={state}
                statuses={statuses}
                live={live}
                locations={geography.data}
                error={geography.error}
                retry={geography.retry}
                selection={selection}
                select={select}
                selectNode={selectNode}
                focusedEdges={[...focus]}
                pathNodes={path?.nodes ?? []}
                connecting={editing && connecting}
                connect={connect}
              />
            </Suspense>
          ) : (
            <ReactFlow
              nodes={flowNodes}
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
              onNodeClick={(event, n) =>
                n.type === 'meshGroup'
                  ? enter('group', n.id)
                  : selectNode(n.id, event.ctrlKey || event.metaKey)
              }
              onNodeContextMenu={(event, n) => {
                if (event.ctrlKey && n.type !== 'meshGroup') {
                  event.preventDefault()
                  selectNode(n.id, true)
                }
              }}
              onEdgeClick={(_, e) =>
                !scope && network.group_links?.some((l) => l.id === e.id)
                  ? enter('link', e.id)
                  : select({ type: 'edge', id: e.id })
              }
              onPaneClick={() => select(null)}
              onNodesChange={(changes) => {
                applyNodeChanges(
                  changes.filter(
                    (c) => !('id' in c) || !network.groups?.some((g) => g.id === c.id),
                  ),
                )
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
              <FitLayout key={scope?.id ?? 'network'} count={flowNodes.length} />
              <Background color="#c9d9d4" gap={22} size={1} />
              <Controls showInteractive={false} />
              <MiniMap style={{ width: 115, height: 75 }} pannable zoomable />
            </ReactFlow>
          )}
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
                  : scope
                    ? scope.kind === 'group'
                      ? 'Full mesh group'
                      : 'Aggregate link'
                    : 'Network details'}
          </h2>
        </div>
        {!selection && scope && scopeSummary ? (
          <>
            <div className="inspector-intro">
              <h3>
                {scopeGroup?.name ??
                  `${network.nodes.find((n) => n.id === scopeLink?.node)?.name} → ${network.groups?.find((g) => g.id === scopeLink?.group)?.name}`}
              </h3>
              <Badge>{scopeSummary.state}</Badge>
            </div>
            <dl className="inspector-summary">
              <dt>Total traffic</dt>
              <dd>{rate(scopeSummary.speed)}</dd>
              <dt>Maximum RTT</dt>
              <dd>
                {scopeSummary.maximum === undefined
                  ? 'Unknown'
                  : `${scopeSummary.maximum.toFixed(1)} ms`}
              </dd>
              <dt>Connected edges</dt>
              <dd>
                {live ? scopeSummary.connected : '—'} / {scopeSummary.total}
              </dd>
            </dl>
            {editing && (
              <div className="actions">
                <button onClick={() => setGroupEditor(scope)}>
                  Edit {scope.kind === 'group' ? 'group' : 'link'}
                </button>
                <button
                  className="danger"
                  onClick={() => {
                    if (
                      !window.confirm(
                        scope.kind === 'group'
                          ? 'Dissolve this group and remove its internal edges and aggregate links?'
                          : 'Remove this aggregate link and all its child edges?',
                      )
                    )
                      return
                    change(
                      scope.kind === 'group'
                        ? {
                            ...network,
                            groups: network.groups?.filter((g) => g.id !== scope.id),
                            group_links: network.group_links?.filter((l) => l.group !== scope.id),
                          }
                        : {
                            ...network,
                            group_links: network.group_links?.filter((l) => l.id !== scope.id),
                          },
                    )
                    setScope(null)
                    select(null)
                  }}
                >
                  Remove
                </button>
              </div>
            )}
            <div className="object-list">
              {scopedEdges?.map((e) => (
                <button key={e.id} onClick={() => select({ type: 'edge', id: e.id })}>
                  <Cable size={15} />
                  <span>
                    {network.nodes.find((n) => n.id === e.a)?.name} ↔{' '}
                    {network.nodes.find((n) => n.id === e.b)?.name}
                    <small>
                      {edgeView(network, e, statuses, rates, live).state} ·{' '}
                      {aggregate(network, [e], statuses, rates, live).label}
                    </small>
                  </span>
                </button>
              ))}
            </div>
          </>
        ) : path ? (
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
                const hop = allEdges.find((e) => e.id === path.edges[index])
                const view = hop ? edgeView(network, hop, statuses, rates, live) : undefined
                return (
                  <li key={id}>
                    <strong>{network.nodes.find((n) => n.id === id)?.name}</strong>
                    {view && (
                      <small>
                        {view.state === 'Connected'
                          ? `${view.active!.transport.toUpperCase()} · ${latencyLabel(view.active)}`
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
        ) : node?.wireguard ? (
          <WireGuardNode
            key={node.id}
            node={node}
            network={network}
            state={state}
            statuses={statuses}
            editing={editing}
            live={live}
            change={change}
            removed={() => select(null)}
          />
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
              <div className="advertised-subnets">
                <strong>Advertised subnets</strong>
                {(node.advertised_subnets || []).map((subnet, index) => (
                  <div className="advertised-subnet" key={index}>
                    <Field label="Subnet">
                      <input
                        aria-label={`Advertised subnet ${index + 1}`}
                        placeholder="192.168.10.0/24"
                        value={subnet.prefix}
                        onChange={(e) =>
                          change({
                            ...network,
                            nodes: network.nodes.map((n) =>
                              n.id === node.id
                                ? {
                                    ...n,
                                    advertised_subnets: (n.advertised_subnets || []).map((s, i) =>
                                      i === index ? { ...s, prefix: e.target.value } : s,
                                    ),
                                  }
                                : n,
                            ),
                          })
                        }
                      />
                    </Field>
                    <Field label="Automatic gateway">
                      <select
                        value={subnet.gateway_mode}
                        onChange={(e) =>
                          change({
                            ...network,
                            nodes: network.nodes.map((n) =>
                              n.id === node.id
                                ? {
                                    ...n,
                                    advertised_subnets: (n.advertised_subnets || []).map((s, i) =>
                                      i === index
                                        ? {
                                            ...s,
                                            gateway_mode: e.target
                                              .value as AdvertisedSubnet['gateway_mode'],
                                          }
                                        : s,
                                    ),
                                  }
                                : n,
                            ),
                          })
                        }
                      >
                        <option value="off">Off</option>
                        <option value="route">Routing</option>
                        <option value="snat">SNAT (Linux)</option>
                      </select>
                    </Field>
                    {editing && (
                      <button
                        type="button"
                        className="subtle"
                        onClick={() =>
                          change({
                            ...network,
                            nodes: network.nodes.map((n) =>
                              n.id === node.id
                                ? {
                                    ...n,
                                    advertised_subnets: (n.advertised_subnets || []).filter(
                                      (_, i) => i !== index,
                                    ),
                                  }
                                : n,
                            ),
                          })
                        }
                      >
                        Remove subnet
                      </button>
                    )}
                  </div>
                ))}
                {editing && (
                  <button
                    type="button"
                    className="subtle wide"
                    disabled={(node.advertised_subnets || []).length >= 64}
                    onClick={() =>
                      change({
                        ...network,
                        nodes: network.nodes.map((n) =>
                          n.id === node.id
                            ? {
                                ...n,
                                advertised_subnets: [
                                  ...(n.advertised_subnets || []),
                                  { prefix: '', gateway_mode: 'off' },
                                ],
                              }
                            : n,
                        ),
                      })
                    }
                  >
                    Add subnet
                  </button>
                )}
              </div>
            </fieldset>
            <dl>
              <dt>Public IP</dt>
              <dd>
                {nodeGeography?.public_ips.length
                  ? nodeGeography.public_ips.map((ip) => (
                      <div className="mono" key={ip}>
                        {ip}
                      </div>
                    ))
                  : geography.error
                    ? 'Unavailable'
                    : !geography.data
                      ? 'Loading…'
                      : 'Not reported'}
              </dd>
              <dt>GeoIP location</dt>
              <dd>
                {nodeGeography?.location
                  ? [nodeGeography.location.city, nodeGeography.location.country]
                      .filter(Boolean)
                      .join(', ') || 'Unknown city'
                  : geography.error ||
                    (geography.data?.pending
                      ? 'Locating agents…'
                      : geography.data?.error || nodeGeography?.reason || 'Not located')}
              </dd>
              {nodeGeography?.location && (
                <>
                  <dt>Coordinates</dt>
                  <dd className="mono">
                    {nodeGeography.location.latitude.toFixed(4)},{' '}
                    {nodeGeography.location.longitude.toFixed(4)}
                  </dd>
                  <dt>GeoIP address</dt>
                  <dd className="mono">{nodeGeography.location.ip}</dd>
                  <dt>GeoIP source</dt>
                  <dd>
                    {geography.data?.database === 'IP.SB' ? (
                      <a href="https://ip.sb/api/" target="_blank" rel="noreferrer">
                        IP Geolocation by IP.SB
                      </a>
                    ) : (
                      geography.data?.database
                    )}
                  </dd>
                </>
              )}
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
            {derivedOwner && (
              <button
                onClick={() => {
                  const kind = 'members' in derivedOwner ? 'group' : 'link'
                  enter(kind, derivedOwner.id)
                  if (editing) setGroupEditor({ kind, id: derivedOwner.id })
                }}
              >
                Shared {'members' in derivedOwner ? 'group' : 'link'} settings
              </button>
            )}
            <fieldset disabled={!editing || !!derivedOwner}>
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
              {!edge.transports.includes('wireguard') && (
                <>
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
                      ['hole_punch_extension', 'NAT hole punching extension'],
                    ] as const
                  ).map(([key, label]) => (
                    <label className="check" key={key}>
                      <input
                        type="checkbox"
                        checked={!!edge.methods[key]}
                        disabled={key === 'hole_punch_extension' && !edge.methods.hole_punch}
                        onChange={(e) =>
                          updateEdge({
                            methods: {
                              ...edge.methods,
                              [key]: e.target.checked,
                              ...(key === 'hole_punch' && !e.target.checked
                                ? { hole_punch_extension: false }
                                : {}),
                            },
                          })
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
                      {[...new Map(connections.map((c) => [c.candidate, c])).values()].map((c) => (
                        <option key={c.candidate} value={c.candidate}>
                          {c.transport.toUpperCase()} · {c.ends[0].address ?? 'Unknown'} ↔{' '}
                          {c.ends[1].address ?? 'Unknown'}
                        </option>
                      ))}
                      {edge.preferred_candidate &&
                        !edgeView(network, edge, statuses, rates, live).links.some(
                          (l) => l.candidate_id === edge.preferred_candidate,
                        ) && (
                          <option value={edge.preferred_candidate}>
                            Saved path · {shortID(edge.preferred_candidate)}
                          </option>
                        )}
                    </select>
                  </Field>
                </>
              )}
            </fieldset>
            <h4>Connections · {connections.length}</h4>
            {connections.map((c) => (
              <div className="link-detail" key={c.id}>
                <div>
                  <strong title={c.id}>Session {shortID(c.id)}</strong>
                  <Badge>{!live ? 'Unknown' : c.state}</Badge>
                </div>
                {c.ends.map((end, index) => {
                  const address = endpointParts(end.address)
                  return (
                    <div className="connection-end" key={index}>
                      <div className="connection-end-heading">
                        <strong>{end.name}</strong>
                        <span className="tag">
                          {(end.report?.transport ?? c.transport).toUpperCase()}
                        </span>
                      </div>
                      <div className="connection-address">
                        <code>{address.ip}</code>
                        <span>Port {address.port}</span>
                      </div>
                      {end.report ? (
                        <div className="link-metrics">
                          <span>
                            {latencyLabel(end.report)}
                            {end.report.transport === 'wireguard' ? ' · ICMP' : ' RTT'}
                          </span>
                          {end.report.transport !== 'wireguard' && (
                            <span>{(end.report.loss * 100).toFixed(1)}% loss</span>
                          )}
                          <span>↓ {bytes(end.report.rx_bytes)}</span>
                          <span>↑ {bytes(end.report.tx_bytes)}</span>
                        </div>
                      ) : (
                        <small>Awaiting report</small>
                      )}
                    </div>
                  )
                })}
              </div>
            ))}
            {!connections.length && <p className="muted">No links reported.</p>}
            {editing && !derivedOwner && (
              <button
                className="danger wide"
                onClick={() => {
                  change({
                    ...network,
                    edges: network.edges.filter((e) => e.id !== edge.id),
                    nodes: network.nodes.filter(
                      (n) => !(n.wireguard && (n.id === edge.a || n.id === edge.b)),
                    ),
                  })
                  select(null)
                }}
              >
                {edge.transports.includes('wireguard')
                  ? 'Remove edge and WireGuard node'
                  : 'Remove edge'}
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
            {!!network.groups?.length && (
              <>
                <h4>Full mesh groups</h4>
                <div className="object-list">
                  {network.groups.map((g) => (
                    <button key={g.id} onClick={() => enter('group', g.id)}>
                      <span>
                        {g.name}
                        <small>
                          {aggregate(network, internalEdges(g), statuses, rates, live).label}
                        </small>
                      </span>
                    </button>
                  ))}
                </div>
              </>
            )}
            {!!network.group_links?.length && (
              <>
                <h4>Node-to-group links</h4>
                <div className="object-list">
                  {network.group_links.map((l) => (
                    <button key={l.id} onClick={() => enter('link', l.id)}>
                      <span>
                        {network.nodes.find((n) => n.id === l.node)?.name} →{' '}
                        {network.groups?.find((g) => g.id === l.group)?.name}
                        <small>
                          {aggregate(network, childEdges(network, l), statuses, rates, live).label}
                        </small>
                      </span>
                    </button>
                  ))}
                </div>
              </>
            )}
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
            {scope ? 'Back to overview' : 'Back to network details'}
          </button>
        )}
      </aside>
    </div>
  )
}
