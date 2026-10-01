import { useEffect, useState } from 'react'
import QRCode from 'qrcode'
import { Field, Badge } from './components'
import {
  clientPrivateKey,
  clientPublicKey,
  generateClientKey,
  wireGuardAccess,
  wireGuardNodeState,
} from './wireguard'
import {
  linkRTT,
  bytes,
  removeNode,
  type Node,
  type Network,
  type State,
  type AgentStatus,
} from './model'

export default function WireGuardNode({
  node,
  network,
  state,
  statuses,
  editing,
  live,
  change,
  removed,
}: {
  node: Node
  network: Network
  state: State
  statuses: AgentStatus[]
  editing: boolean
  live: boolean
  change: (n: Network) => void
  removed: () => void
}) {
  const access = wireGuardAccess(network, node, state, statuses)
  const [secret, setSecret] = useState(clientPrivateKey(node.id))
  const [external, setExternal] = useState(false)
  const [keepalive, setKeepalive] = useState(25)
  const [show, setShow] = useState(false)
  const [qr, setQR] = useState('')
  const [qrError, setQRError] = useState('')
  const update = (patch: Partial<Node>) =>
    change({
      ...network,
      nodes: network.nodes.map((n) => (n.id === node.id ? { ...n, ...patch } : n)),
    })
  const wg = node.wireguard!
  const publicKey = access.link?.wireguard_public_key
  let error = editing
    ? 'Save the network before exporting.'
    : !access.edge?.enabled
      ? 'Enable the connection before exporting.'
      : !publicKey
        ? 'Waiting for the access Agent to report its WireGuard public key.'
        : !access.endpoint
          ? 'Set a reachable UDP endpoint.'
          : ''
  if (secret) {
    try {
      if (clientPublicKey(secret) !== wg.public_key)
        error = 'Private key does not match this node’s public key.'
    } catch {
      error = 'Invalid private key.'
    }
  }
  const prefixes = [
    network.cidr,
    ...(external
      ? network.nodes.flatMap((n) => (n.advertised_subnets ?? []).map((s) => s.prefix))
      : []),
  ]
  const config = `[Interface]\n${secret ? `PrivateKey = ${secret}` : '# PrivateKey = <your client private key>'}\nAddress = ${node.address}/${node.address.includes(':') ? 128 : 32}\nMTU = ${Math.min(network.mtu, 1280)}\n\n[Peer]\nPublicKey = ${publicKey ?? ''}\nEndpoint = ${access.endpoint}\nAllowedIPs = ${[...new Set(prefixes)].join(', ')}\nPersistentKeepalive = ${keepalive}\n`
  useEffect(() => {
    setShow(false)
    setQR('')
  }, [config, error])
  useEffect(() => {
    if (!show || error || !secret) return
    let cancelled = false
    setQRError('')
    QRCode.toDataURL(config, { width: 320, margin: 2, errorCorrectionLevel: 'M' })
      .then((data) => {
        if (!cancelled) setQR(data)
      })
      .catch(() => {
        if (!cancelled) setQRError('Configuration is too large for a QR code; download it instead.')
      })
    return () => {
      cancelled = true
    }
  }, [show, config, error, secret])
  const download = () => {
    const blob = new Blob([config], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = (node.name.replace(/[^A-Za-z0-9_-]/g, '_').slice(0, 15) || 'wireguard') + '.conf'
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  return (
    <>
      <div className="inspector-intro">
        <h3>{node.name}</h3>
        <Badge>{wireGuardNodeState(network, node, state, statuses, live)}</Badge>
      </div>
      <fieldset disabled={!editing}>
        <Field label="Node name">
          <input
            value={node.name}
            maxLength={128}
            onChange={(e) => update({ name: e.target.value })}
          />
        </Field>
        <Field label="Virtual IP">
          <input value={node.address} onChange={(e) => update({ address: e.target.value })} />
        </Field>
        <Field label="Access node">
          <select
            value={access.gateway?.id ?? ''}
            onChange={(e) => {
              change({
                ...network,
                edges: network.edges.map((edge) =>
                  edge.id === access.edge?.id ? { ...edge, a: node.id, b: e.target.value } : edge,
                ),
              })
            }}
          >
            {network.nodes
              .filter((n) => !n.wireguard)
              .map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
          </select>
        </Field>
        <Field label="Endpoint override">
          <input
            placeholder={access.endpoint || 'vpn.example.com:24752'}
            value={wg.endpoint ?? ''}
            onChange={(e) => update({ wireguard: { ...wg, endpoint: e.target.value.trim() } })}
          />
        </Field>
        <Field label="Client public key">
          <input
            className="mono"
            value={wg.public_key}
            onChange={(e) => update({ wireguard: { ...wg, public_key: e.target.value.trim() } })}
          />
        </Field>
        <button
          type="button"
          className="wide"
          onClick={() => {
            const key = generateClientKey(node.id)
            setSecret(clientPrivateKey(node.id))
            update({ wireguard: { ...wg, public_key: key } })
          }}
        >
          Generate new client key
        </button>
      </fieldset>
      <dl>
        <dt>Protocol</dt>
        <dd>WireGuard / UDP</dd>
        <dt>Endpoint</dt>
        <dd className="mono">{access.endpoint || 'Not configured'}</dd>
        <dt>Access public key</dt>
        <dd className="mono">{publicKey || 'Not reported'}</dd>
        <dt>Latency (ICMP RTT)</dt>
        <dd>
          {live && access.status?.connected && linkRTT(access.link) !== undefined
            ? `${linkRTT(access.link)!.toFixed(1)} ms`
            : 'Unknown'}
        </dd>
        <dt>Last handshake</dt>
        <dd>
          {access.link?.last_handshake && !access.link.last_handshake.startsWith('0001')
            ? new Date(access.link.last_handshake).toLocaleString()
            : 'Never'}
        </dd>
        <dt>Client endpoint</dt>
        <dd className="mono">{access.link?.remote || 'Not reported'}</dd>
        <dt>Received / sent by Agent</dt>
        <dd>
          {bytes(access.link?.rx_bytes ?? 0)} / {bytes(access.link?.tx_bytes ?? 0)}
        </dd>
      </dl>
      <Field label="Client private key (local only)">
        <input
          type="password"
          autoComplete="off"
          spellCheck={false}
          value={secret}
          placeholder="Optional: paste to export a complete configuration"
          onChange={(e) => setSecret(e.target.value.trim())}
        />
      </Field>
      {secret && (
        <p className="muted">
          Download before closing this page. Private keys are not stored on the Server.
        </p>
      )}
      <label className="check">
        <input type="checkbox" checked={external} onChange={(e) => setExternal(e.target.checked)} />
        Include advertised subnets
      </label>
      <Field label="Persistent keepalive (seconds)">
        <input
          type="number"
          min={0}
          max={65535}
          value={keepalive}
          onChange={(e) =>
            setKeepalive(Math.max(0, Math.min(65535, Math.floor(Number(e.target.value)))))
          }
        />
      </Field>
      {error && (
        <p role="status" className="muted">
          {error}
        </p>
      )}
      <div className="actions">
        <button disabled={!!error} onClick={download}>
          {secret ? 'Download configuration' : 'Download template'}
        </button>
        <button disabled={!!error || !secret} onClick={() => setShow(!show)}>
          {show ? 'Hide QR code' : 'Show QR code'}
        </button>
      </div>
      {show && qr && (
        <img
          src={qr}
          alt="WireGuard configuration QR code"
          style={{ maxWidth: '100%', display: 'block', margin: '12px auto' }}
        />
      )}
      {show && qrError && <p className="error">{qrError}</p>}
      {editing && (
        <button
          className="danger wide"
          onClick={() => {
            change(removeNode(network, node.id))
            removed()
          }}
        >
          Remove WireGuard node
        </button>
      )}
    </>
  )
}
