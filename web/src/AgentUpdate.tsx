import { useState } from 'react'
import { request, errorText, APIError } from './api'
import { ErrorBox, Field, Modal } from './components'
import type { Agent, AgentStatus, State } from './model'

type Release = { version: string; os: string; arch: string; size: number }
export default function AgentUpdate({
  agent,
  status,
  csrf,
  updated,
  close,
}: {
  agent: Agent
  status?: AgentStatus
  csrf: string
  updated: (s: State) => void
  close: () => void
}) {
  const [source, setSource] = useState('github')
  const [release, setRelease] = useState<Release>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [queuedID, setQueuedID] = useState('')
  const current = status?.update
  const queued = !!queuedID && current?.request_id !== queuedID
  const working = current?.phase === 'downloading' || current?.phase === 'installing'
  const eligible = status?.connected && current?.managed
  return (
    <Modal title={`Update ${agent.name}`} close={close}>
      {error && <ErrorBox>{error}</ErrorBox>}
      <dl>
        <dt>Current version</dt>
        <dd>{status?.version || 'Unknown'}</dd>
        <dt>Latest release</dt>
        <dd>{release?.version || 'Not checked'}</dd>
        <dt>Platform</dt>
        <dd>{current ? `${current.os}/${current.arch}` : 'Unknown'}</dd>
        <dt>Status</dt>
        <dd>{queued && !working ? 'Queued' : current?.phase || 'Ready'}</dd>
      </dl>
      {current?.error && <ErrorBox>{current.error}</ErrorBox>}
      {!eligible && (
        <ErrorBox>Agent must be online and installed with graphwan agent service.</ErrorBox>
      )}
      <Field label="Download source">
        <select
          value={source}
          onChange={(e) => setSource(e.target.value)}
          disabled={busy || working || queued}
        >
          <option value="github">Agent downloads from GitHub</option>
          <option value="server">Server downloads and caches</option>
        </select>
      </Field>
      <div className="actions">
        <button
          disabled={!eligible || busy || working || queued}
          onClick={async () => {
            setBusy(true)
            setError('')
            try {
              setRelease(
                await request<Release>(
                  `/updates/latest?os=${encodeURIComponent(current!.os)}&arch=${encodeURIComponent(current!.arch)}`,
                ),
              )
            } catch (e) {
              setError(errorText(e))
            } finally {
              setBusy(false)
            }
          }}
        >
          Check latest release
        </button>
        <button
          className="primary"
          disabled={
            !eligible ||
            !release ||
            busy ||
            working ||
            queued ||
            release.version === status?.version
          }
          onClick={async () => {
            setBusy(true)
            setError('')
            try {
              // Fetch a fresh revision: endpoint discovery may have advanced it
              // while the administrator was reviewing the release.
              const latest = await request<State>('/state')
              let revision = latest.revision
              let next: State | undefined
              for (let attempt = 0; attempt < 3; attempt++) {
                try {
                  next = await request<State>(`/agents/${agent.id}/update`, {
                    method: 'POST',
                    csrf,
                    revision,
                    body: { source, version: release!.version },
                  })
                  break
                } catch (e) {
                  if (
                    !(e instanceof APIError) ||
                    e.status !== 409 ||
                    e.message !== 'configuration changed; reload and retry' ||
                    attempt === 2
                  )
                    throw e
                  revision = (await request<State>('/state')).revision
                }
              }
              if (next) {
                updated(next)
                setQueuedID(next.agents.find((a) => a.id === agent.id)?.update?.id || '')
              }
            } catch (e) {
              setError(errorText(e))
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Working…' : 'Update and restart'}
        </button>
      </div>
    </Modal>
  )
}
