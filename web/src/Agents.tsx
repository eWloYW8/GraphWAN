import { useState } from 'react'
import { Plus, Copy, Trash2, Server } from 'lucide-react'
import { ResourceSummary } from './Resources'
import { request, errorText } from './api'
import { Badge, Field, Modal, ErrorBox } from './components'
import {
  type Agent,
  type AgentStatus,
  type State,
  type Endpoint,
  type Transport,
  transports,
  newID,
  nodeState,
  shortID,
} from './model'
export function Enrollment({ csrf, close }: { csrf: string; close: () => void }) {
  const [ttl, setTTL] = useState(3600)
  const [result, setResult] = useState<{ token: string; expires_at: string }>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  return (
    <Modal title="Enroll an agent" close={close}>
      <p className="muted">Create a single-use token to register an agent with this controller.</p>
      {error && <ErrorBox>{error}</ErrorBox>}
      {result ? (
        <>
          <Field label="Enrollment token">
            <textarea readOnly rows={3} value={result.token} />
          </Field>
          <button
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(result.token)
                setCopied(true)
              } catch {
                setError('Copy is unavailable. Select and copy the token above.')
              }
            }}
          >
            <Copy size={15} />
            {copied ? 'Copied' : 'Copy token'}
          </button>
          <p className="muted">
            Expires {new Date(result.expires_at).toLocaleString()}. This token is shown only here.
          </p>
          <h4>On the agent machine</h4>
          <p>
            Copy the controller’s <code>ca.pem</code> securely, set{' '}
            <code>GRAPHWAN_ENROLLMENT_TOKEN</code> to this token, then run:
          </p>
          <pre>
            graphwan agent --server {location.origin}
            {' \\\n'} --ca ./ca.pem --name my-agent
          </pre>
          <p className="muted">
            The agent needs permission to create TUN devices. After enrollment, add it to a network.
          </p>
          <button className="primary wide" onClick={close}>
            Done
          </button>
        </>
      ) : (
        <form
          onSubmit={async (e) => {
            e.preventDefault()
            setBusy(true)
            setError('')
            try {
              setResult(
                await request('/enrollment-tokens', {
                  method: 'POST',
                  csrf,
                  body: { ttl_seconds: ttl },
                }),
              )
            } catch (e) {
              setError(errorText(e))
            } finally {
              setBusy(false)
            }
          }}
        >
          <Field label="Token lifetime">
            <select value={ttl} onChange={(e) => setTTL(Number(e.target.value))}>
              <option value={600}>10 minutes</option>
              <option value={3600}>1 hour</option>
              <option value={86400}>24 hours</option>
            </select>
          </Field>
          <button className="primary wide" disabled={busy}>
            {busy ? 'Creating…' : 'Create enrollment token'}
          </button>
        </form>
      )}
    </Modal>
  )
}
function AgentEditor({
  agent,
  csrf,
  state,
  close,
  updated,
}: {
  agent: Agent
  csrf: string
  state: State
  close: () => void
  updated: (s: State) => void
}) {
  const [name, setName] = useState(agent.name)
  const [port, setPort] = useState(agent.listen_port)
  const [stunServers, setSTUNServers] = useState((agent.stun_servers ?? []).join('\n'))
  const [revoked, setRevoked] = useState(agent.revoked)
  const [endpoints, setEndpoints] = useState<Endpoint[]>(
    agent.endpoints?.filter((e) => e.source === 'manual') ?? [],
  )
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const editable = (a: Agent | undefined) =>
    a && {
      name: a.name,
      listen_port: a.listen_port,
      revoked: a.revoked,
      stun_servers: a.stun_servers ?? [],
      endpoints: a.endpoints?.filter((e) => e.source === 'manual') ?? [],
    }
  const originalChanged =
    JSON.stringify(editable(state.agents.find((a) => a.id === agent.id))) !==
    JSON.stringify(editable(agent))
  const change = (id: string, patch: Partial<Endpoint>) =>
    setEndpoints(endpoints.map((e) => (e.id === id ? { ...e, ...patch } : e)))
  const mutate = async (method: string, body?: unknown) => {
    setBusy(true)
    setError('')
    try {
      updated(
        await request<State>(`/agents/${agent.id}`, {
          method,
          body,
          csrf,
          revision: state.revision,
        }),
      )
      close()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal title={`Manage ${agent.name}`} close={close}>
      {error && <ErrorBox>{error}</ErrorBox>}
      {originalChanged && (
        <ErrorBox>
          This agent changed while you were editing. Close and reopen to load the latest settings.
        </ErrorBox>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void mutate('PATCH', {
            name,
            listen_port: port,
            revoked,
            manual_endpoints: endpoints,
            stun_servers: stunServers
              .split('\n')
              .map((s) => s.trim())
              .filter(Boolean),
          })
        }}
      >
        <fieldset disabled={busy}>
          <Field label="Agent name">
            <input
              required
              maxLength={128}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field label="Listen port">
            <input
              required
              type="number"
              min={1}
              max={65535}
              value={port}
              onChange={(e) => setPort(Number(e.target.value))}
            />
          </Field>
          <Field
            label="STUN servers"
            hint="One service per line, up to four: host:port for UDP, tcp://host:port for TCP. Each discovers its own mapping for hole punching. Leave empty to disable discovery."
          >
            <textarea
              value={stunServers}
              rows={3}
              maxLength={1204}
              placeholder={'stun.example.com:3478\ntcp://stun.example.com:3478'}
              onChange={(e) => setSTUNServers(e.target.value)}
            />
          </Field>
          <label className="check">
            <input
              type="checkbox"
              checked={revoked}
              onChange={(e) => setRevoked(e.target.checked)}
            />
            Revoke this agent’s access
          </label>
          <h4>Manual endpoints</h4>
          <p className="muted">
            WS/WSS and gRPC require an explicit endpoint. Automatic addresses use TCP and UDP.
          </p>
          {endpoints.map((ep, i) => (
            <div className="endpoint-editor" key={ep.id}>
              <Field label={`Endpoint ${i + 1} transport`}>
                <select
                  value={ep.transport}
                  onChange={(e) =>
                    change(ep.id, {
                      transport: e.target.value as Transport,
                      url: ep.url.replace(/^[a-z]+:/, `${e.target.value}:`),
                    })
                  }
                >
                  {transports.map((t) => (
                    <option key={t} value={t}>
                      {t.toUpperCase()}
                    </option>
                  ))}
                </select>
              </Field>
              <Field
                label={`Endpoint ${i + 1} URL`}
                hint={
                  ep.transport === 'grpc'
                    ? 'Uses TLS. An optional path is a service prefix, such as /overlay.'
                    : undefined
                }
              >
                <input
                  required
                  value={ep.url}
                  placeholder={`${ep.transport}://host:24752`}
                  onChange={(e) => change(ep.id, { url: e.target.value })}
                />
              </Field>
              <button
                type="button"
                className="icon danger"
                aria-label={`Remove endpoint ${i + 1}`}
                onClick={() => setEndpoints(endpoints.filter((e) => e.id !== ep.id))}
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
                { id: newID(), transport: 'udp', url: 'udp://', source: 'manual' },
              ])
            }
            disabled={endpoints.length >= 64}
          >
            <Plus size={15} />
            Add endpoint
          </button>
          <h4>Discovered endpoints</h4>
          {agent.endpoints
            ?.filter((e) => e.source !== 'manual')
            .map((e) => (
              <div className="endpoint" key={e.id}>
                <span className="tag">{e.source}</span>
                <code>{e.url}</code>
              </div>
            ))}
          <div className="modal-actions">
            <button
              type="button"
              className="danger"
              onClick={() => {
                if (
                  confirm(
                    `Delete ${agent.name}? This also removes its nodes and incident edges from all networks.`,
                  )
                )
                  void mutate('DELETE')
              }}
            >
              Delete agent
            </button>
            <button type="submit" className="primary" disabled={originalChanged}>
              {busy ? 'Saving…' : 'Save agent'}
            </button>
          </div>
        </fieldset>
      </form>
    </Modal>
  )
}
export default function Agents({
  state,
  statuses,
  live,
  csrf,
  updated,
  enroll,
}: {
  state: State
  statuses: AgentStatus[]
  live: boolean
  csrf: string
  updated: (s: State) => void
  enroll: () => void
}) {
  const [editing, setEditing] = useState<Agent>()
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">INFRASTRUCTURE</div>
          <h1>Agents</h1>
          <p>Registered machines and their connection endpoints.</p>
        </div>
        <button className="primary" onClick={enroll}>
          <Plus size={17} />
          Enroll agent
        </button>
      </div>
      <div className="table-card">
        <table>
          <thead>
            <tr>
              <th>Agent</th>
              <th>Status</th>
              <th>Memberships</th>
              <th>Endpoints</th>
              <th>Resources</th>
              <th>Version</th>
              <th>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {state.agents.map((a) => {
              const status = statuses.find((s) => s.agent_id === a.id)
              return (
                <tr key={a.id}>
                  <td>
                    <strong>{a.name}</strong>
                    <small className="mono">{shortID(a.id)}</small>
                  </td>
                  <td>
                    <Badge>{nodeState(a, status, live)}</Badge>
                    {status?.config_error && <small className="red">{status.config_error}</small>}
                    {status?.runtime_error && <small className="red">{status.runtime_error}</small>}
                  </td>
                  <td>
                    {state.networks
                      .filter((n) => n.nodes.some((node) => node.agent_id === a.id))
                      .map((n) => n.name)
                      .join(', ') || 'No networks'}
                  </td>
                  <td>
                    {a.endpoints?.length ?? 0}
                    <small>Port {a.listen_port}</small>
                  </td>
                  <td>
                    <ResourceSummary status={status} live={live} />
                  </td>
                  <td>{status?.version || '—'}</td>
                  <td>
                    <button onClick={() => setEditing(a)} aria-label={`Manage ${a.name}`}>
                      Manage
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
        {!state.agents.length && (
          <div className="empty-page">
            <Server size={32} />
            <h2>No agents enrolled</h2>
            <p>Generate a token and start an agent to register your first machine.</p>
            <button className="primary" onClick={enroll}>
              Enroll first agent
            </button>
          </div>
        )}
      </div>
      {editing && (
        <AgentEditor
          key={editing.id}
          agent={editing}
          state={state}
          csrf={csrf}
          updated={updated}
          close={() => setEditing(undefined)}
        />
      )}
    </>
  )
}
