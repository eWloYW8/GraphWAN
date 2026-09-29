import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Network as NetworkIcon,
  Server,
  LogOut,
  Plus,
  Eye,
  Pencil,
  Check,
  ArrowUpRight,
  Activity,
  X,
  Trash2,
  RefreshCw,
} from 'lucide-react'
import { request, APIError, errorText } from './api'
import { Badge, Field, Modal, ErrorBox } from './components'
import {
  type State,
  type Network,
  type AgentStatus,
  type Snapshot,
  type Draft,
  type Rates,
  newID,
  equal,
  rebase,
  ratesBetween,
  createEdge,
  nodeState,
} from './model'
import Topology, { type Selection } from './Topology'
import Agents, { Enrollment } from './Agents'
function Login({ ready }: { ready: (csrf: string) => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <div className="login-page">
      <div className="login-story">
        <a className="brand" href="/">
          <span className="brand-icon">
            <NetworkIcon size={25} />
          </span>
          GraphWAN
        </a>
        <div>
          <div className="eyebrow">YOUR NETWORK. YOUR TOPOLOGY.</div>
          <h1>
            Every connection,
            <br />
            on your terms.
          </h1>
          <p>
            Define the graph. Choose the paths.
            <br />
            Keep your networks connected.
          </p>
          <div className="login-graph" aria-hidden="true">
            <span>01</span>
            <i />
            <span>02</span>
            <i />
            <span>03</span>
          </div>
        </div>
        <small>Central control · Peer-to-peer data</small>
      </div>
      <main className="login-form">
        <form
          onSubmit={async (e) => {
            e.preventDefault()
            setBusy(true)
            setError('')
            try {
              const session = await request<{ csrf_token: string }>('/login', {
                method: 'POST',
                body: { password },
              })
              setPassword('')
              ready(session.csrf_token)
            } catch (e) {
              setError(errorText(e))
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="eyebrow">CONTROLLER ACCESS</div>
          <h2>Welcome back</h2>
          <p className="muted">Sign in to manage your GraphWAN networks.</p>
          {error && <ErrorBox>{error}</ErrorBox>}
          <Field label="Administrator password">
            <input
              autoFocus
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          <button className="primary wide" disabled={busy}>
            {busy ? 'Signing in…' : 'Sign in'}
            <ArrowUpRight size={17} />
          </button>
          <small className="muted">
            Use the administrator password configured on this controller.
          </small>
        </form>
      </main>
    </div>
  )
}
export default function App() {
  const [csrf, setCSRF] = useState<string | null>()
  const [state, setState] = useState<State>()
  const [statuses, setStatuses] = useState<AgentStatus[]>([])
  const [rates, setRates] = useState<Rates>({})
  const previous = useRef<AgentStatus[]>([])
  const [live, setLive] = useState(false)
  const [streamEpoch, setStreamEpoch] = useState(0)
  const [view, setView] = useState('networks')
  const [networkID, setNetworkID] = useState('')
  const [draft, setDraft] = useState<Draft>()
  const [selection, setSelection] = useState<Selection>(null)
  const [modal, setModal] = useState<'network' | 'node' | 'edge' | 'enroll' | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const dirty = !!draft && !equal(draft.network, draft.base)
  const loadState = useCallback((incoming: State) => {
    setState((current) => (!current || incoming.revision >= current.revision ? incoming : current))
    setDraft((current) =>
      current && incoming.revision >= current.revision ? rebase(current, incoming) : current,
    )
  }, [])
  const clearSession = useCallback(() => {
    setCSRF(null)
    setState(undefined)
    setDraft(undefined)
    setStatuses([])
    previous.current = []
    setRates({})
    setModal(null)
    setLive(false)
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    request<{ csrf_token: string }>('/session', { signal: controller.signal })
      .then((s) => setCSRF(s.csrf_token))
      .catch((e) => {
        if (controller.signal.aborted) return
        if (!(e instanceof APIError && e.status === 401)) setError(errorText(e))
        setCSRF(null)
      })
    return () => controller.abort()
  }, [])
  useEffect(() => {
    if (!csrf) return
    const events = new EventSource('/api/v1/events')
    let active = true
    let received = performance.now()
    const watchdog = setInterval(() => {
      const age = performance.now() - received
      if (age > 5000) setLive(false)
      if (age > 15000) {
        events.close()
        setStreamEpoch((epoch) => epoch + 1)
      }
    }, 1000)
    events.addEventListener('snapshot', (event) => {
      if (!active) return
      try {
        const message: Snapshot = JSON.parse((event as MessageEvent).data)
        if (message.state) loadState(message.state)
        // Events can repeat one Agent sample. Preserve its last computed rate
        // until a newer report arrives; use each report's time, not UI refresh time.
        const before = previous.current
        const nextRates = ratesBetween(before, message.agents)
        setRates((old) => {
          const result: Rates = {}
          for (const s of message.agents)
            for (const l of s.links ?? []) {
              const key = `${s.agent_id}/${l.link_id}`
              const prior = before.find((p) => p.agent_id === s.agent_id)
              if (s.connected && prior?.connected && s.last_seen === prior.last_seen && old[key])
                result[key] = old[key]
              else if (nextRates[key]) result[key] = nextRates[key]
            }
          return result
        })
        received = performance.now()
        previous.current = message.agents
        setStatuses(message.agents)
        setLive(true)
      } catch {
        setError('Could not read the live update. Reload to reconnect.')
        setLive(false)
      }
    })
    events.onerror = () => {
      setLive(false)
      void request('/session').catch((e) => {
        if (active && e instanceof APIError && e.status === 401) clearSession()
      })
    }
    return () => {
      active = false
      clearInterval(watchdog)
      events.close()
    }
  }, [csrf, clearSession, loadState, streamEpoch])
  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])
  const navigate = (next: string, id = '') => {
    if (dirty && !confirm('Discard your unsaved topology changes?')) return
    setDraft(undefined)
    setSelection(null)
    setView(next)
    setNetworkID(id)
    setError('')
    setNotice('')
  }
  const beginNetwork = () => {
    if (dirty && !confirm('Discard your unsaved topology changes?')) return
    setDraft(undefined)
    setModal('network')
  }
  const current = state?.networks.find((n) => n.id === networkID)
  const network = draft?.network ?? current
  const conflict = !!draft && !equal(current, draft.base)
  const change = (next: Network) => setDraft((d) => (d ? { ...d, network: next } : d))
  const edit = () => {
    if (current && state)
      setDraft({ network: structuredClone(current), base: current, revision: state.revision })
    setNotice('')
  }
  const mutate = async (
    path: string,
    method: string,
    body?: unknown,
    revision = state?.revision,
  ) => {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const next = await request<State>(path, { method, body, csrf: csrf!, revision })
      loadState(next)
      return next
    } catch (e) {
      setError(errorText(e))
      if (e instanceof APIError && e.status === 401) clearSession()
      if (e instanceof APIError && e.status === 409)
        void request<State>('/state')
          .then(loadState)
          .catch(() => {})
      throw e
    } finally {
      setBusy(false)
    }
  }
  if (csrf === undefined)
    return (
      <div className="loading" role="status">
        Connecting to GraphWAN…
      </div>
    )
  if (csrf === null)
    return (
      <>
        {error && (
          <div className="login-error">
            <ErrorBox>{error}</ErrorBox>
          </div>
        )}
        <Login
          ready={(token) => {
            setError('')
            setCSRF(token)
          }}
        />
      </>
    )
  const online =
    state?.agents.filter(
      (a) =>
        nodeState(
          a,
          statuses.find((s) => s.agent_id === a.id),
          live,
        ) === 'Online',
    ).length ?? 0
  const networkDetail = view === 'networks' && !!network
  const sessionControls = (
    <>
      <Badge tone={live ? 'online' : 'offline'}>{live ? 'Live updates' : 'Reconnecting'}</Badge>
      <button
        className="icon"
        aria-label="Sign out"
        onClick={async () => {
          if (dirty && !confirm('Discard unsaved changes and sign out?')) return
          try {
            await request('/logout', { method: 'POST', csrf })
            clearSession()
          } catch (e) {
            setError(errorText(e))
          }
        }}
      >
        <LogOut size={18} />
      </button>
    </>
  )
  return (
    <div className={`app${networkDetail ? ' network-page' : ''}`}>
      <aside className="sidebar">
        <a
          className="brand"
          href="/"
          onClick={(e) => {
            e.preventDefault()
            navigate('networks')
          }}
        >
          <span className="brand-icon">
            <NetworkIcon size={23} />
          </span>
          GraphWAN
        </a>
        <div className="sidebar-caption">WORKSPACE</div>
        <nav>
          <button
            className={view === 'networks' ? 'selected' : ''}
            onClick={() => navigate('networks')}
          >
            <NetworkIcon size={18} />
            Networks<span>{state?.networks.length ?? 0}</span>
          </button>
          <button
            className={view === 'agents' ? 'selected' : ''}
            onClick={() => navigate('agents')}
          >
            <Server size={18} />
            Agents<span>{state?.agents.length ?? 0}</span>
          </button>
        </nav>
        <div className="sidebar-caption">YOUR NETWORKS</div>
        <nav className="network-nav">
          {state?.networks.map((n) => (
            <button
              key={n.id}
              className={networkID === n.id && view === 'networks' ? 'selected' : ''}
              onClick={() => navigate('networks', n.id)}
            >
              <i className="network-square" />
              {n.name}
            </button>
          ))}
        </nav>
        <button className="sidebar-create" onClick={beginNetwork}>
          <Plus size={16} />
          Create network
        </button>
        <div className="sidebar-bottom">
          <div>
            <span className={`dot ${live ? 'online' : 'offline'}`} />
            <strong>{live ? 'Controller connected' : 'Reconnecting…'}</strong>
          </div>
        </div>
      </aside>
      <div className="app-content">
        {!networkDetail && (
          <header className="topbar">
            <span>
              Workspace <span className="muted">/</span>{' '}
              <strong>{view === 'agents' ? 'Agents' : network?.name || 'Networks'}</strong>
            </span>
            <div>{sessionControls}</div>
          </header>
        )}
        <main className={`main${networkDetail ? ' network-main' : ''}`}>
          {error && (
            <ErrorBox>
              {error}
              <button className="icon" aria-label="Dismiss error" onClick={() => setError('')}>
                <X size={15} />
              </button>
            </ErrorBox>
          )}
          {notice && (
            <div role="status" className="notice">
              <Check size={17} />
              {notice}
            </div>
          )}
          {!state ? (
            <div className="empty-page" role="status">
              <Activity size={32} />
              <h2>{live ? 'Loading workspace…' : 'Waiting for the controller'}</h2>

              <button onClick={() => location.reload()}>
                <RefreshCw size={15} />
                Reload
              </button>
            </div>
          ) : view === 'agents' ? (
            <Agents
              state={state}
              statuses={statuses}
              live={live}
              csrf={csrf}
              updated={loadState}
              enroll={() => setModal('enroll')}
            />
          ) : network ? (
            <>
              <h1 className="sr-only">{network.name}</h1>
              {conflict && (
                <ErrorBox>
                  The network changed on the server. Your draft is preserved. Discard it to load the
                  latest configuration before editing again.
                </ErrorBox>
              )}
              <div className="network-toolbar">
                <div className="segmented" role="group" aria-label="Topology mode">
                  <button
                    className={!draft ? 'active' : ''}
                    onClick={() => {
                      if (!dirty || confirm('Discard your unsaved topology changes?'))
                        setDraft(undefined)
                    }}
                  >
                    <Eye size={15} />
                    Observe
                  </button>
                  <button className={draft ? 'active' : ''} onClick={() => !draft && edit()}>
                    <Pencil size={15} />
                    Edit
                  </button>
                </div>
                <div className="actions">
                  {draft ? (
                    <>
                      <button
                        disabled={busy}
                        onClick={() => {
                          if (!dirty || confirm('Discard your unsaved topology changes?')) {
                            setDraft(undefined)
                            setSelection(null)
                          }
                        }}
                      >
                        Discard
                      </button>
                      <button
                        className="primary"
                        disabled={!dirty || busy || conflict}
                        onClick={async () => {
                          try {
                            await mutate(
                              `/networks/${network.id}`,
                              'PUT',
                              draft.network,
                              draft.revision,
                            )
                            setDraft(undefined)
                            setNotice(
                              'Network configuration saved. Agents are applying the update.',
                            )
                          } catch {
                            /* Preserve the draft on failure. */
                          }
                        }}
                      >
                        <Check size={16} />
                        {busy ? 'Saving…' : 'Save changes'}
                      </button>
                    </>
                  ) : (
                    <button
                      onClick={() => {
                        if (
                          confirm(
                            `Delete network ${network.name}, including all its nodes and edges?`,
                          )
                        )
                          void mutate(`/networks/${network.id}`, 'DELETE')
                            .then(() => navigate('networks'))
                            .catch(() => {})
                      }}
                      className="icon danger"
                      aria-label="Delete network"
                    >
                      <Trash2 size={17} />
                    </button>
                  )}
                  {sessionControls}
                </div>
              </div>
              <Topology
                key={network.id}
                state={state}
                network={network}
                statuses={statuses}
                rates={rates}
                live={live}
                editing={!!draft && !busy}
                selection={selection}
                select={setSelection}
                change={change}
                addNode={() => setModal('node')}
                addEdge={() => setModal('edge')}
              />
            </>
          ) : (
            <>
              <div className="page-heading">
                <div>
                  <h1>Networks</h1>
                </div>
                <button className="primary" onClick={beginNetwork}>
                  <Plus size={17} />
                  Create network
                </button>
              </div>
              <div className="metrics">
                <div>
                  <NetworkIcon size={18} />
                  <span>
                    Networks<strong>{state.networks.length}</strong>
                  </span>
                </div>
                <div>
                  <Server size={18} />
                  <span>
                    Agents<strong>{state.agents.length}</strong>
                  </span>
                </div>
                <div>
                  <Activity size={18} />
                  <span>
                    Online agents<strong>{live ? online : '—'}</strong>
                  </span>
                </div>
              </div>
              {state.networks.length ? (
                <div className="network-cards">
                  {state.networks.map((n) => (
                    <button key={n.id} onClick={() => navigate('networks', n.id)}>
                      <div className="network-card-icon">
                        <NetworkIcon size={23} />
                      </div>
                      <ArrowUpRight className="card-arrow" size={19} />
                      <h2>{n.name}</h2>
                      <p className="mono">{n.cidr}</p>
                      <footer>
                        <span>{n.nodes.length} nodes</span>
                        <span>{n.edges.length} edges</span>
                        <span>MTU {n.mtu}</span>
                      </footer>
                    </button>
                  ))}
                </div>
              ) : (
                <div className="empty-page">
                  <NetworkIcon size={35} />
                  <h2>No networks</h2>

                  <button className="primary" onClick={beginNetwork}>
                    Create a network
                  </button>
                  <button className="subtle" onClick={() => setModal('enroll')}>
                    Enroll an agent first
                  </button>
                </div>
              )}
            </>
          )}
        </main>
        <footer className="app-footer">
          <span>
            GraphWAN ·{' '}
            <a href="/THIRD_PARTY_LICENSES.txt" target="_blank" rel="noreferrer">
              Licenses
            </a>
          </span>
        </footer>
      </div>
      {modal === 'enroll' && <Enrollment csrf={csrf} close={() => setModal(null)} />}
      {modal === 'network' && state && (
        <CreateNetwork
          close={() => setModal(null)}
          save={async (n) => {
            const next = await mutate('/networks', 'POST', n)
            if (next) {
              setModal(null)
              navigate('networks', n.id)
            }
          }}
        />
      )}
      {modal === 'node' && network && draft && state && (
        <AddNode
          network={network}
          state={state}
          close={() => setModal(null)}
          save={(n) => {
            change(n)
            setModal(null)
            setSelection({ type: 'node', id: n.nodes.at(-1)!.id })
          }}
        />
      )}
      {modal === 'edge' && network && draft && (
        <AddEdge
          network={network}
          close={() => setModal(null)}
          save={(n) => {
            change(n)
            setModal(null)
            setSelection({ type: 'edge', id: n.edges.at(-1)!.id })
          }}
        />
      )}
    </div>
  )
}
function CreateNetwork({
  close,
  save,
}: {
  close: () => void
  save: (n: Network) => Promise<void>
}) {
  const [name, setName] = useState('')
  const [cidr, setCIDR] = useState('10.42.0.0/24')
  const [mtu, setMTU] = useState(1280)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Modal title="Create network" close={close}>
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          setError('')
          try {
            await save({
              id: newID(),
              name,
              cidr,
              mtu,
              cipher: 'chacha20-poly1305',
              nodes: [],
              edges: [],
            })
          } catch (e) {
            setError(errorText(e))
          } finally {
            setBusy(false)
          }
        }}
      >
        {error && <ErrorBox>{error}</ErrorBox>}
        <Field label="Network name">
          <input
            autoFocus
            required
            maxLength={128}
            placeholder="Production mesh"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="Subnet (CIDR)">
          <input required value={cidr} onChange={(e) => setCIDR(e.target.value)} />
        </Field>
        <Field label="TUN MTU">
          <input
            type="number"
            required
            min={1280}
            max={9000}
            value={mtu}
            onChange={(e) => setMTU(Number(e.target.value))}
          />
        </Field>

        <button className="primary wide" disabled={busy}>
          {busy ? 'Creating…' : 'Create network'}
        </button>
      </form>
    </Modal>
  )
}
function AddNode({
  network,
  state,
  save,
  close,
}: {
  network: Network
  state: State
  save: (n: Network) => void
  close: () => void
}) {
  const available = state.agents.filter(
    (a) => !a.revoked && !network.nodes.some((n) => n.agent_id === a.id),
  )
  const [agent, setAgent] = useState(available[0]?.id ?? '')
  const [name, setName] = useState(available[0]?.name ?? '')
  const [address, setAddress] = useState('')
  return (
    <Modal title="Add node" close={close}>
      {!available.length ? (
        <p>No available agents.</p>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            save({
              ...network,
              nodes: [
                ...network.nodes,
                {
                  id: newID(),
                  agent_id: agent,
                  name,
                  address,
                  position: {
                    x: (network.nodes.length % 3) * 280,
                    y: Math.floor(network.nodes.length / 3) * 150,
                  },
                },
              ],
            })
          }}
        >
          <Field label="Agent">
            <select
              value={agent}
              onChange={(e) => {
                setAgent(e.target.value)
                setName(available.find((a) => a.id === e.target.value)!.name)
              }}
            >
              {available.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Node name">
            <input
              required
              maxLength={128}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field label="Virtual IP">
            <input
              required
              placeholder="10.42.0.1"
              value={address}
              onChange={(e) => setAddress(e.target.value)}
            />
          </Field>
          <button className="primary wide">Add to draft</button>
        </form>
      )}
    </Modal>
  )
}
function AddEdge({
  network,
  save,
  close,
}: {
  network: Network
  save: (n: Network) => void
  close: () => void
}) {
  const [a, setA] = useState(network.nodes[0]?.id ?? '')
  const [b, setB] = useState(network.nodes[1]?.id ?? '')
  const [error, setError] = useState('')
  return (
    <Modal title="Add edge" close={close}>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          const edge = createEdge(network, a, b)
          if (!edge) {
            setError('Choose two different nodes that do not already share an edge.')
            return
          }
          save({ ...network, edges: [...network.edges, edge] })
        }}
      >
        {error && <ErrorBox>{error}</ErrorBox>}
        <Field label="First node">
          <select value={a} onChange={(e) => setA(e.target.value)}>
            {network.nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Second node">
          <select value={b} onChange={(e) => setB(e.target.value)}>
            {network.nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name}
              </option>
            ))}
          </select>
        </Field>

        <button className="primary wide">Add to draft</button>
      </form>
    </Modal>
  )
}
