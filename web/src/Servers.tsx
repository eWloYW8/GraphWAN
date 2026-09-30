import { useEffect, useState } from 'react'
import { Plus, Pencil, Trash2 } from 'lucide-react'
import { request, errorText } from './api'
import { Badge, ErrorBox, Field, Modal } from './components'
import { type State, type Controller, type ServerEndpoint, newID, equal } from './model'
import { saveConfiguration } from './saveConfiguration'

type Status = { id: string; leader: string; voters: number }
const settings = (server?: Controller) =>
  server && {
    name: server.name,
    stun_servers: server.stun_servers ?? [],
    manual_endpoints: server.endpoints.filter((e) => e.source === 'manual'),
  }
export default function Servers({
  state,
  csrf,
  updated,
}: {
  state: State
  csrf: string
  updated: (state: State) => void
}) {
  const [status, setStatus] = useState<Status>()
  const [editing, setEditing] = useState<Controller>()
  const [mode, setMode] = useState<'invite' | 'join'>()
  const [invitation, setInvitation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [restarting, setRestarting] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    const poll = () =>
      void request<Status>('/servers/status', { signal: controller.signal })
        .then(setStatus)
        .catch(() => {})
    poll()
    const timer = setInterval(poll, 3000)
    return () => {
      controller.abort()
      clearInterval(timer)
    }
  }, [])
  const servers = state.servers ?? []
  return (
    <>
      <div className="page-heading">
        <h1>Servers</h1>
        <div className="actions">
          {servers.length === 1 && state.agents.length === 0 && state.networks.length === 0 && (
            <button
              onClick={() => {
                setMode('join')
                setInvitation('')
                setError('')
              }}
            >
              Join cluster
            </button>
          )}
          <button
            className="primary"
            onClick={async () => {
              setMode('invite')
              setInvitation('')
              setError('')
              setBusy(true)
              try {
                const result = await request<{ invitation: string }>('/servers/invitation', {
                  method: 'POST',
                  csrf,
                })
                setInvitation(result.invitation)
              } catch (e) {
                setError(errorText(e))
              } finally {
                setBusy(false)
              }
            }}
          >
            <Plus size={16} />
            Add server
          </button>
        </div>
      </div>
      <div className="server-list">
        {servers.map((server) => (
          <section className="server-card" key={server.id}>
            <div className="server-card-header">
              <div className="server-identity">
                <h2>{server.name}</h2>
                {server.id === status?.id && <Badge>This server</Badge>}
                {server.id === status?.leader && <Badge tone="online">Coordinator</Badge>}
              </div>
              <button aria-label={`Edit ${server.name}`} onClick={() => setEditing(server)}>
                <Pencil size={16} />
                Edit
              </button>
            </div>
            <div className="server-endpoints">
              {server.endpoints.map((ep) => (
                <div key={ep.id}>
                  <Badge>{ep.transport}</Badge>
                  <code>{ep.url}</code>
                  <span className="muted">
                    {ep.source}
                    {ep.source === 'observed' &&
                    ep.expires_at &&
                    Date.parse(ep.expires_at) < Date.now()
                      ? ' · expired'
                      : ''}
                  </span>
                </div>
              ))}
            </div>
          </section>
        ))}
      </div>
      {editing && (
        <EditServer
          server={editing}
          state={state}
          csrf={csrf}
          updated={updated}
          close={() => setEditing(undefined)}
        />
      )}
      {mode && (
        <Modal
          title={mode === 'invite' ? 'Add server' : 'Join cluster'}
          close={() => {
            if (!busy && !restarting) setMode(undefined)
          }}
        >
          {error && <ErrorBox>{error}</ErrorBox>}
          {restarting ? (
            <div role="status">
              Server restarting. Sign in with the cluster password.
              <button className="primary wide" onClick={() => location.reload()}>
                Sign in
              </button>
            </div>
          ) : (
            <form
              onSubmit={async (e) => {
                e.preventDefault()
                if (mode !== 'join') return
                setError('')
                setBusy(true)
                try {
                  await request('/servers/join', {
                    method: 'POST',
                    csrf,
                    body: { invitation },
                    signal: AbortSignal.timeout(60000),
                  })
                  setRestarting(true)
                } catch (e) {
                  setError(errorText(e))
                } finally {
                  setBusy(false)
                }
              }}
            >
              <Field
                label={
                  mode === 'invite'
                    ? 'Invitation — paste on the new server’s “Join cluster” page'
                    : 'Cluster invitation'
                }
              >
                <textarea
                  aria-label="Cluster invitation"
                  rows={6}
                  readOnly={mode === 'invite'}
                  required
                  value={invitation}
                  onChange={(e) => setInvitation(e.target.value.trim())}
                />
              </Field>
              {mode === 'invite' ? (
                <button
                  type="button"
                  disabled={!invitation}
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(invitation)
                    } catch {
                      setError('Select and copy the invitation above.')
                    }
                  }}
                >
                  Copy invitation
                </button>
              ) : (
                <button className="primary wide" disabled={busy}>
                  {busy ? 'Joining…' : 'Join cluster'}
                </button>
              )}
            </form>
          )}
        </Modal>
      )}
    </>
  )
}
function EditServer({
  server,
  state,
  csrf,
  updated,
  close,
}: {
  server: Controller
  state: State
  csrf: string
  updated: (state: State) => void
  close: () => void
}) {
  const [name, setName] = useState(server.name)
  const [stun, setSTUN] = useState((server.stun_servers ?? []).join('\n'))
  const [endpoints, setEndpoints] = useState<ServerEndpoint[]>(
    server.endpoints.filter((e) => e.source === 'manual'),
  )
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <Modal title="Edit server" close={close}>
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          setError('')
          try {
            const next = await saveConfiguration(
              `/servers/${server.id}`,
              {
                method: 'PATCH',
                csrf,
                revision: state.revision,
                body: {
                  name,
                  stun_servers: stun
                    .split('\n')
                    .map((s) => s.trim())
                    .filter(Boolean),
                  manual_endpoints: endpoints,
                },
              },
              (latest) =>
                equal(settings(latest.servers?.find((s) => s.id === server.id)), settings(server)),
              updated,
            )
            updated(next)
            close()
          } catch (e) {
            setError(errorText(e))
          } finally {
            setBusy(false)
          }
        }}
      >
        {error && <ErrorBox>{error}</ErrorBox>}
        <Field label="Server name">
          <input required maxLength={128} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="TCP STUN servers">
          <textarea rows={3} value={stun} onChange={(e) => setSTUN(e.target.value)} />
        </Field>
        <h4>Manual entry points</h4>
        {endpoints.map((ep, index) => (
          <div className="endpoint-editor" key={ep.id}>
            <select
              aria-label={`Transport ${index + 1}`}
              value={ep.transport}
              onChange={(e) => {
                const transport = e.target.value as ServerEndpoint['transport']
                const scheme = transport === 'websocket' ? 'ws' : transport
                setEndpoints(
                  endpoints.map((p) =>
                    p.id === ep.id
                      ? { ...p, transport, url: p.url.replace(/^[a-z]+:\/\//, `${scheme}://`) }
                      : p,
                  ),
                )
              }}
            >
              {['tcp', 'websocket', 'grpc', 'wss'].map((p) => (
                <option key={p}>{p}</option>
              ))}
            </select>
            <input
              aria-label={`Endpoint URL ${index + 1}`}
              required
              value={ep.url}
              placeholder="ws://server.example:8443"
              onChange={(e) =>
                setEndpoints(
                  endpoints.map((p) => (p.id === ep.id ? { ...p, url: e.target.value } : p)),
                )
              }
            />
            <button
              type="button"
              className="icon"
              aria-label={`Remove endpoint ${index + 1}`}
              onClick={() => setEndpoints(endpoints.filter((p) => p.id !== ep.id))}
            >
              <Trash2 size={16} />
            </button>
          </div>
        ))}
        <button
          type="button"
          onClick={() =>
            setEndpoints([
              ...endpoints,
              { id: newID(), transport: 'websocket', url: 'ws://', source: 'manual' },
            ])
          }
        >
          <Plus size={16} />
          Add entry point
        </button>
        <button className="primary wide" disabled={busy}>
          {busy ? 'Saving…' : 'Save changes'}
        </button>
      </form>
    </Modal>
  )
}
