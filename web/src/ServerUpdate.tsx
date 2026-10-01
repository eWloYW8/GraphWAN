import { useState } from 'react'
import { APIError, errorText, request } from './api'
import { ErrorBox, Modal } from './components'
import type { AgentUpdateStatus, Controller, State } from './model'

export type ServerSoftware = { id: string; version: string; update?: AgentUpdateStatus }
export default function ServerUpdate({
  server,
  software,
  local,
  csrf,
  updated,
  close,
}: {
  server: Controller
  software?: ServerSoftware
  local: boolean
  csrf: string
  updated: (s: State) => void
  close: () => void
}) {
  const [release, setRelease] = useState<{ version: string }>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [queuedID, setQueuedID] = useState('')
  const current = software?.update
  const queued = !!queuedID && current?.request_id !== queuedID
  const working = current?.phase === 'downloading' || current?.phase === 'installing'
  return (
    <Modal title={`Update ${server.name}`} close={close}>
      {error && <ErrorBox>{error}</ErrorBox>}
      <dl>
        <dt>Current version</dt>
        <dd>{software?.version || 'Unknown'}</dd>
        <dt>Latest release</dt>
        <dd>{release?.version || 'Not checked'}</dd>
        <dt>Platform</dt>
        <dd>{current ? `${current.os}/${current.arch}` : 'Unknown'}</dd>
        <dt>Status</dt>
        <dd>{queued ? 'Queued / reconnecting' : current?.phase || 'Ready'}</dd>
      </dl>
      {current?.error && <ErrorBox>{current.error}</ErrorBox>}
      {!current?.managed && !queued && (
        <ErrorBox>Requires an online Server installed with graphwan server service.</ErrorBox>
      )}
      <div className="actions">
        <button
          disabled={busy || working || queued || !current?.managed}
          onClick={async () => {
            setBusy(true)
            setError('')
            try {
              setRelease(
                await request(
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
            busy ||
            working ||
            queued ||
            !current?.managed ||
            !release ||
            release.version === software?.version
          }
          onClick={async () => {
            setBusy(true)
            setError('')
            try {
              let revision = (await request<State>('/state')).revision
              for (let attempt = 0; attempt < 3; attempt++) {
                try {
                  const next = await request<State>(`/servers/${server.id}/update`, {
                    method: 'POST',
                    csrf,
                    revision,
                    body: { version: release!.version },
                  })
                  updated(next)
                  setQueuedID(next.servers?.find((s) => s.id === server.id)?.update?.id || '')
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
            } catch (e) {
              setError(errorText(e))
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Working…' : 'Download and restart'}
        </button>
        {queuedID && local && <button onClick={() => location.reload()}>Reconnect to panel</button>}
      </div>
    </Modal>
  )
}
