import { StrictMode, useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import {
  ArrowLeft,
  ArrowUpRight,
  Check,
  ChevronRight,
  CircleHelp,
  Globe2,
  Layers,
  LocateFixed,
  Maximize2,
  Minus,
  Network,
  Pause,
  Play,
  Plus,
  RotateCcw,
  Search,
  Server,
  Spline,
  Waypoints,
  X,
} from 'lucide-react'
import Globe, { type ViewCommand } from './Globe'
import {
  distance,
  examples,
  exampleEdges,
  locate,
  type GlobeNode,
  type GlobeEdge,
  type Selection,
} from './data'
import './style.css'

function FlatGraph({
  nodes,
  edges,
  curved,
  selection,
  select,
}: {
  nodes: GlobeNode[]
  edges: GlobeEdge[]
  curved: boolean
  selection: Selection
  select: (selection: Selection) => void
}) {
  const points = new Map(
    nodes.map((node, i) => [
      node.id,
      {
        x: 460 + Math.cos((i / nodes.length) * Math.PI * 2 - Math.PI / 2) * 310,
        y: 320 + Math.sin((i / nodes.length) * Math.PI * 2 - Math.PI / 2) * 230,
      },
    ]),
  )
  return (
    <svg
      className="flat-graph"
      viewBox="0 0 920 640"
      aria-label={curved ? 'Bezier topology' : 'Straight line topology'}
    >
      <defs>
        <pattern id="dots" width="24" height="24" patternUnits="userSpaceOnUse">
          <circle cx="1" cy="1" r="0.7" fill="#344756" />
        </pattern>
      </defs>
      <rect width="920" height="640" fill="url(#dots)" onClick={() => select(null)} />
      {edges.map((edge) => {
        const a = points.get(edge.a),
          b = points.get(edge.b)
        if (!a || !b) return null
        const active =
          selection?.type === 'edge'
            ? selection.id === edge.id
            : selection?.type === 'node' && (selection.id === edge.a || selection.id === edge.b)
        const d = curved ? `M${a.x} ${a.y} Q460 320 ${b.x} ${b.y}` : `M${a.x} ${a.y} L${b.x} ${b.y}`
        return (
          <g
            key={edge.id}
            className="flat-edge"
            onClick={() => select({ type: 'edge', id: edge.id })}
          >
            <path d={d} fill="none" stroke="transparent" strokeWidth="16" />
            <path
              d={d}
              fill="none"
              stroke={active ? '#ffd184' : '#69e8c0'}
              strokeOpacity={selection && !active ? 0.18 : 0.7}
              strokeWidth={active ? 2 : 1.5}
            />
          </g>
        )
      })}
      {nodes.map((node) => {
        const p = points.get(node.id)!
        return (
          <g
            key={node.id}
            className="flat-node"
            transform={`translate(${p.x},${p.y})`}
            role="button"
            tabIndex={0}
            aria-label={`Select ${node.name}`}
            onClick={() => select({ type: 'node', id: node.id })}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault()
                select({ type: 'node', id: node.id })
              }
            }}
          >
            <rect
              x="-68"
              y="-26"
              width="136"
              height="52"
              rx="9"
              fill="#11242e"
              stroke={
                selection?.type === 'node' && selection.id === node.id ? '#ffd184' : '#315662'
              }
            />
            <circle cx="-51" cy="-5" r="3" fill="#69e8c0" />
            <text x="-40" y="-1" fill="#e1eceb" fontSize="12">
              {node.name}
            </text>
            <text x="-51" y="16" fill="#8a9fae" fontSize="10">
              {node.city}
            </text>
          </g>
        )
      })}
    </svg>
  )
}

function App() {
  const [nodes, setNodes] = useState<GlobeNode[]>([])
  const [edges, setEdges] = useState<GlobeEdge[]>([])
  const [view, setView] = useState<'line' | 'bezier' | 'globe'>('globe')
  const [selection, select] = useState<Selection>(null)
  const [rotating, setRotating] = useState(false)
  const [labels, setLabels] = useState(true)
  const [command, setCommand] = useState<ViewCommand>({ kind: 'reset', serial: 0 })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [ip, setIP] = useState('')
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState('')
  const [help, setHelp] = useState(false)
  const stage = useRef<HTMLDivElement>(null)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    const abort = new AbortController()
    void Promise.allSettled(
      examples.map(async (node) => ({ ...(await locate(node.ip, abort.signal)), ...node })),
    ).then((results) => {
      if (abort.signal.aborted) return
      const resolved = results.flatMap((r) => (r.status === 'fulfilled' ? [r.value] : []))
      const ids = new Set(resolved.map((n) => n.id))
      setNodes(resolved)
      setEdges(exampleEdges.filter((e) => ids.has(e.a) && ids.has(e.b)))
      if (resolved.length < examples.length)
        setError(
          `${examples.length - resolved.length} example IPs could not be located. ${results.find((r) => r.status === 'rejected')?.reason?.message || ''}`,
        )
      setLoading(false)
    })
    return () => {
      mounted.current = false
      abort.abort()
    }
  }, [])
  const move = (kind: ViewCommand['kind'], id?: string) =>
    setCommand((c) => ({ kind, id, serial: c.serial + 1 }))
  const focusNode = (id: string) => {
    select({ type: 'node', id })
    move('focus', id)
  }
  const selectedNode =
    selection?.type === 'node' ? nodes.find((n) => n.id === selection.id) : undefined
  const selectedEdge =
    selection?.type === 'edge' ? edges.find((e) => e.id === selection.id) : undefined
  const countries = new Set(nodes.map((n) => n.countryCode)).size
  const neighbours = selectedNode
    ? edges.filter((e) => e.a === selectedNode.id || e.b === selectedNode.id)
    : []
  const coLocated = selectedNode
    ? nodes.filter((n) => n.id !== selectedNode.id && distance(n, selectedNode) < 50).length
    : 0
  const filtered = nodes.filter((n) =>
    `${n.name} ${n.ip} ${n.city} ${n.country}`.toLowerCase().includes(filter.toLowerCase()),
  )

  return (
    <div className="demo-app">
      <header className="app-header">
        <a className="brand" href="/" aria-label="GraphWAN globe demo">
          <span className="brand-mark">
            <Network size={21} />
          </span>
          GraphWAN
        </a>
        <div className="breadcrumb">
          <span>Networks</span>
          <ChevronRight size={14} />
          <strong>earth</strong>
          <span className="demo-badge">DEMO</span>
        </div>
        <a
          className="source-link"
          href="https://github.com/eWloYW8/GraphWAN"
          target="_blank"
          rel="noreferrer"
        >
          GitHub <ArrowUpRight size={14} />
        </a>
      </header>
      <main className="demo-main">
        <nav className="side-rail" aria-label="Demo navigation">
          <span title="Servers">
            <Server size={19} />
          </span>
          <span title="Agents">
            <Waypoints size={20} />
          </span>
          <span className="rail-active" title="Networks">
            <Network size={20} />
          </span>
          <div className="rail-bottom">
            <Globe2 size={19} />
          </div>
        </nav>
        <div className="demo-workspace">
          <section className="visual-panel" ref={stage} aria-label="Network visualization">
            <div className="visual-toolbar">
              <div className="network-name">
                <span className="status-dot" />
                earth <span className="sample-pill">Sample topology</span>
              </div>
              <div className="view-switch" role="group" aria-label="Drawing style">
                <button aria-pressed={view === 'line'} onClick={() => setView('line')}>
                  <span className="line-icon">╱</span>Line
                </button>
                <button aria-pressed={view === 'bezier'} onClick={() => setView('bezier')}>
                  <Spline size={15} />
                  Bezier
                </button>
                <button aria-pressed={view === 'globe'} onClick={() => setView('globe')}>
                  <Globe2 size={15} />
                  3D globe
                </button>
              </div>
            </div>
            <div className="scene-area">
              {view === 'globe' ? (
                <Globe
                  nodes={nodes}
                  edges={edges}
                  selection={selection}
                  select={select}
                  rotating={rotating}
                  labels={labels}
                  command={command}
                />
              ) : (
                <FlatGraph
                  nodes={nodes}
                  edges={edges}
                  curved={view === 'bezier'}
                  selection={selection}
                  select={select}
                />
              )}
              <div className="scene-caption">
                <Globe2 size={14} />
                {view === 'globe' ? 'GEOGRAPHIC VIEW' : 'TOPOLOGY VIEW'}
                <span>
                  {nodes.length} nodes<span className="dot-separator">·</span>
                  {edges.length} links
                </span>
              </div>
              {loading && (
                <div className="scene-loading">
                  <span className="spinner" />
                  Locating public IPs…
                </div>
              )}
              {view === 'globe' && (
                <>
                  <div className="globe-controls" role="group" aria-label="Globe controls">
                    <button onClick={() => move('in')} title="Zoom in" aria-label="Zoom in">
                      <Plus size={18} />
                    </button>
                    <button onClick={() => move('out')} title="Zoom out" aria-label="Zoom out">
                      <Minus size={18} />
                    </button>
                    <i />
                    <button
                      onClick={() => {
                        setRotating(false)
                        move('reset')
                      }}
                      title="Reset view"
                      aria-label="Reset view"
                    >
                      <LocateFixed size={18} />
                    </button>
                    <button
                      onClick={() => setRotating(!rotating)}
                      aria-pressed={rotating}
                      title="Auto rotate"
                      aria-label="Auto rotate"
                    >
                      {rotating ? <Pause size={17} /> : <Play size={17} />}
                    </button>
                    <button
                      onClick={() => setLabels(!labels)}
                      aria-pressed={labels}
                      title="Node labels"
                      aria-label="Node labels"
                    >
                      <Layers size={17} />
                    </button>
                  </div>
                  <div className="scene-bottom">
                    <span className="legend">
                      <i />
                      Node <b />
                      Connection
                    </span>
                    <div>
                      <button
                        className="icon-button"
                        aria-label="Interaction help"
                        aria-pressed={help}
                        onClick={() => setHelp(!help)}
                      >
                        <CircleHelp size={16} />
                      </button>
                      <button
                        className="icon-button"
                        aria-label="Toggle fullscreen"
                        onClick={() => {
                          void (
                            document.fullscreenElement
                              ? document.exitFullscreen()
                              : stage.current?.requestFullscreen()
                          )?.catch(() => setError('Fullscreen is unavailable in this browser.'))
                        }}
                      >
                        <Maximize2 size={16} />
                      </button>
                    </div>
                  </div>
                  {help && (
                    <div className="help-popover">
                      <strong>Explore the globe</strong>
                      <span>Drag to rotate · Scroll / pinch to zoom</span>
                      <span>Select a marker or a link to inspect it.</span>
                      <span>Choose a node in the list to fly to its location.</span>
                      <span>Nearby markers are offset; their coordinates stay unchanged.</span>
                    </div>
                  )}
                </>
              )}
            </div>
            <footer className="map-footer">
              <span>
                <i />
                {countries} countries<span className="dot-separator">/</span>GeoIP coordinates
              </span>
              <div>
                <a href="https://db-ip.com" target="_blank" rel="noreferrer">
                  IP Geolocation by DB-IP
                </a>
                <span>·</span>
                <a href="https://www.naturalearthdata.com/" target="_blank" rel="noreferrer">
                  Natural Earth
                </a>
              </div>
            </footer>
          </section>
          <aside className="inspector">
            <div className="inspector-heading">
              <h1>{selection ? (selectedNode ? 'Node' : 'Connection') : 'Nodes'}</h1>
              {selection ? (
                <button
                  className="icon-button"
                  aria-label="Close details"
                  onClick={() => select(null)}
                >
                  <X size={17} />
                </button>
              ) : (
                <span className="count-badge">{nodes.length}</span>
              )}
            </div>
            {error && (
              <div className="error-message" role="alert">
                {error}
                <button
                  className="icon-button"
                  onClick={() => setError('')}
                  aria-label="Dismiss error"
                >
                  <X size={14} />
                </button>
              </div>
            )}
            <div className="inspector-scroll">
              {selectedNode ? (
                <>
                  <button className="back-button" onClick={() => select(null)}>
                    <ArrowLeft size={13} />
                    All nodes
                  </button>
                  <div className="node-detail-heading">
                    <span className="detail-icon">
                      <Server size={23} />
                    </span>
                    <h2>{selectedNode.name}</h2>
                    <span className="small-label">EXAMPLE NODE</span>
                  </div>
                  <dl className="detail-fields">
                    <div>
                      <dt>Public IP</dt>
                      <dd className="mono">{selectedNode.ip}</dd>
                    </div>
                    <div>
                      <dt>City</dt>
                      <dd>{selectedNode.city}</dd>
                    </div>
                    <div>
                      <dt>Country</dt>
                      <dd>{selectedNode.country}</dd>
                    </div>
                    <div>
                      <dt>Coordinates</dt>
                      <dd className="mono">
                        {selectedNode.latitude.toFixed(4)}, {selectedNode.longitude.toFixed(4)}
                      </dd>
                    </div>
                    <div>
                      <dt>Location source</dt>
                      <dd>DB-IP City Lite</dd>
                    </div>
                  </dl>
                  {coLocated > 0 && (
                    <div className="location-note">
                      <Layers size={15} />
                      {coLocated + 1} nearby nodes · markers separated
                    </div>
                  )}
                  <button
                    className="focus-button"
                    onClick={() => {
                      setView('globe')
                      move('focus', selectedNode.id)
                    }}
                  >
                    <LocateFixed size={15} />
                    Locate on globe
                  </button>
                  <div className="section-label">
                    CONNECTIONS<span>{neighbours.length}</span>
                  </div>
                  {neighbours.map((edge) => {
                    const peer = nodes.find(
                      (n) => n.id === (edge.a === selectedNode.id ? edge.b : edge.a),
                    )!
                    return (
                      <button
                        className="peer-row"
                        key={edge.id}
                        onClick={() => select({ type: 'edge', id: edge.id })}
                      >
                        <span className="peer-dot" />
                        <div>
                          <strong>{peer.name}</strong>
                          <span>{distance(selectedNode, peer).toLocaleString()} km</span>
                        </div>
                        <ChevronRight size={14} />
                      </button>
                    )
                  })}
                </>
              ) : selectedEdge ? (
                (() => {
                  const a = nodes.find((n) => n.id === selectedEdge.a)!,
                    b = nodes.find((n) => n.id === selectedEdge.b)!
                  return (
                    <>
                      <button className="back-button" onClick={() => select(null)}>
                        <ArrowLeft size={13} />
                        All nodes
                      </button>
                      <div className="edge-endpoints">
                        {[a, b].map((node, i) => (
                          <div key={node.id}>
                            {i > 0 && <div className="endpoint-connector" />}
                            <button onClick={() => focusNode(node.id)}>
                              <span className="status-dot" />
                              <strong>{node.name}</strong>
                              <ChevronRight size={14} />
                              <small>
                                {node.city} · {node.countryCode}
                              </small>
                            </button>
                          </div>
                        ))}
                      </div>
                      <dl className="detail-fields">
                        <div>
                          <dt>Surface distance</dt>
                          <dd>{distance(a, b).toLocaleString()} km</dd>
                        </div>
                        <div>
                          <dt>Geometry</dt>
                          <dd>Elevated great-circle arc</dd>
                        </div>
                        <div>
                          <dt>Traffic</dt>
                          <dd>Not measured in this demo</dd>
                        </div>
                      </dl>
                    </>
                  )
                })()
              ) : (
                <>
                  <label className="search-box">
                    <Search size={15} />
                    <input
                      aria-label="Search nodes"
                      placeholder="Search nodes or locations"
                      value={filter}
                      onChange={(event) => setFilter(event.target.value)}
                    />
                    {filter && (
                      <button
                        className="icon-button"
                        aria-label="Clear search"
                        onClick={() => setFilter('')}
                      >
                        <X size={13} />
                      </button>
                    )}
                  </label>
                  <div className="node-list">
                    {filtered.map((node, i) => (
                      <button className="node-row" key={node.id} onClick={() => focusNode(node.id)}>
                        <span className="node-row-icon">
                          <Server size={16} />
                        </span>
                        <span className="node-row-text">
                          <strong>{node.name}</strong>
                          <span>
                            {node.city}
                            <span className="country-code">{node.countryCode}</span>
                          </span>
                        </span>
                        <ChevronRight size={14} />
                        <span className="node-number">{String(i + 1).padStart(2, '0')}</span>
                      </button>
                    ))}
                    {!loading && !filtered.length && (
                      <p className="empty-state">No matching nodes.</p>
                    )}
                  </div>
                </>
              )}
            </div>
            <form
              className="lookup-form"
              onSubmit={async (event) => {
                event.preventDefault()
                if (busy || !ip.trim()) return
                setBusy(true)
                setError('')
                try {
                  const location = await locate(ip.trim())
                  if (!mounted.current) return
                  const existing = nodes.find((n) => n.ip === location.ip)
                  if (existing) {
                    setView('globe')
                    focusNode(existing.id)
                  } else {
                    const id = `ip-${location.ip}`
                    const node = {
                      ...location,
                      id,
                      name: `custom-${nodes.filter((n) => n.id.startsWith('ip-')).length + 1}`,
                    }
                    setNodes((current) => [...current, node])
                    if (nodes.length)
                      setEdges((current) => [
                        ...current,
                        { id: `${nodes[0].id}:${id}`, a: nodes[0].id, b: id },
                      ])
                    setView('globe')
                    focusNode(id)
                  }
                  setIP('')
                } catch (e) {
                  if (mounted.current) setError(e instanceof Error ? e.message : 'Lookup failed.')
                } finally {
                  if (mounted.current) setBusy(false)
                }
              }}
            >
              <label htmlFor="public-ip">LOCATE A PUBLIC IP</label>
              <div>
                <input
                  id="public-ip"
                  placeholder="IPv4 or IPv6 address"
                  autoComplete="off"
                  spellCheck={false}
                  value={ip}
                  onChange={(event) => setIP(event.target.value)}
                />
                <button
                  disabled={busy || !ip.trim()}
                  title="Locate public IP"
                  aria-label="Locate public IP"
                >
                  {busy ? <RotateCcw size={16} className="spin" /> : <Plus size={18} />}
                </button>
              </div>
              <span>
                <Check size={12} />
                Local GeoIP lookup<span>Approximate location</span>
              </span>
            </form>
          </aside>
        </div>
      </main>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
