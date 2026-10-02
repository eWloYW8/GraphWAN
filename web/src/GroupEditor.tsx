import { useState } from 'react'
import { Modal, Field, ErrorBox } from './components'
import { newID, transports, type Edge, type Network } from './model'
import { coveredEdges, topologyError, type FullMeshGroup, type GroupLink } from './groups'

export function ConnectionPolicy({
  value,
  change,
}: {
  value: Pick<Edge, 'transports' | 'methods'>
  change: (patch: Partial<Pick<Edge, 'transports' | 'methods'>>) => void
}) {
  return (
    <>
      <h4>Allowed transports</h4>
      <div className="checks">
        {transports.map((t) => (
          <label className="check" key={t}>
            <input
              type="checkbox"
              checked={value.transports.includes(t)}
              onChange={(e) =>
                change({
                  transports: e.target.checked
                    ? [...value.transports, t]
                    : value.transports.filter((v) => v !== t),
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
            checked={!!value.methods[key]}
            disabled={key === 'hole_punch_extension' && !value.methods.hole_punch}
            onChange={(e) =>
              change({
                methods: {
                  ...value.methods,
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
    </>
  )
}
export type GroupEditorTarget =
  { kind: 'group'; id?: string } | { kind: 'link'; id?: string; node?: string; group?: string }
export default function GroupEditor({
  network,
  target,
  save,
  close,
}: {
  network: Network
  target: GroupEditorTarget
  save: (n: Network) => void
  close: () => void
}) {
  const defaults = {
    transports: ['udp', 'tcp'] as Edge['transports'],
    methods: { ipv4_direct: true, ipv6_direct: true, hole_punch: false },
  }
  const [group, setGroup] = useState<FullMeshGroup>(
    () =>
      network.groups?.find((g) => g.id === target.id) ?? {
        id: newID(),
        name: 'Full mesh',
        weight: 1,
        members: [],
        ...defaults,
      },
  )
  const [link, setLink] = useState<GroupLink>(
    () =>
      network.group_links?.find((l) => l.id === target.id) ?? {
        id: newID(),
        node: target.kind === 'link' ? (target.node ?? '') : '',
        group: target.kind === 'link' ? (target.group ?? '') : '',
        weight: 10,
        enabled: true,
        ...defaults,
      },
  )
  const [error, setError] = useState('')
  const value = target.kind === 'group' ? group : link
  const submit = () => {
    if (
      !value.transports.length ||
      !Object.entries(value.methods).some(([k, v]) => k !== 'hole_punch_extension' && v)
    ) {
      setError('Select at least one transport and connection method.')
      return
    }
    if (
      target.kind === 'link' &&
      (!network.nodes.some((n) => n.id === link.node && !n.wireguard) ||
        !Number.isInteger(link.weight) ||
        link.weight < 1 ||
        link.weight > 4294967295)
    ) {
      setError('Choose a node and a valid positive routing weight.')
      return
    }
    const weight = value.weight ?? 1
    if (!Number.isInteger(weight) || weight < 1 || weight > 4294967295) {
      setError('Choose a valid positive routing weight.')
      return
    }
    let updated = { ...network }
    if (target.kind === 'group')
      updated.groups = [...(network.groups ?? []).filter((g) => g.id !== group.id), group]
    else
      updated.group_links = [...(network.group_links ?? []).filter((l) => l.id !== link.id), link]
    // Bound expansion before calculating conflicts for large drafts.
    const count = (updated.groups ?? []).reduce(
      (n, g) => n + (g.members.length * (g.members.length - 1)) / 2,
      0,
    )
    if (count > 100000) {
      setError('Expanded topology exceeds 100,000 edges.')
      return
    }
    const conflicts = coveredEdges(updated)
    const ids = new Set(conflicts.map((e) => e.id))
    updated = { ...updated, edges: updated.edges.filter((e) => !ids.has(e.id)) }
    const issue = topologyError(updated)
    if (issue) {
      setError(issue)
      return
    }
    if (
      conflicts.length &&
      !window.confirm(
        `Replace ${conflicts.length} existing individual edge(s) with these shared connection settings?`,
      )
    )
      return
    save(updated)
    close()
  }
  return (
    <Modal title={target.kind === 'group' ? 'Full mesh group' : 'Node-to-group link'} close={close}>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        {error && <ErrorBox>{error}</ErrorBox>}
        {target.kind === 'group' ? (
          <>
            <Field label="Group name">
              <input
                value={group.name}
                maxLength={128}
                required
                onChange={(e) => setGroup({ ...group, name: e.target.value })}
              />
            </Field>
            <Field label="Routing weight">
              <input
                type="number"
                min={1}
                max={4294967295}
                step={1}
                required
                value={group.weight ?? 1}
                onChange={(e) => setGroup({ ...group, weight: Number(e.target.value) })}
              />
            </Field>
            <h4>Members · {group.members.length}</h4>
            <div className="mesh-members">
              {network.nodes
                .filter((n) => !n.wireguard)
                .map((n) => {
                  const occupied = network.groups?.some(
                    (g) => g.id !== group.id && g.members.includes(n.id),
                  )
                  return (
                    <label className="check" key={n.id}>
                      <input
                        type="checkbox"
                        disabled={occupied}
                        checked={group.members.includes(n.id)}
                        onChange={(e) =>
                          setGroup({
                            ...group,
                            members: e.target.checked
                              ? [...group.members, n.id]
                              : group.members.filter((id) => id !== n.id),
                          })
                        }
                      />
                      {n.name}
                      {occupied ? ' (grouped)' : ''}
                    </label>
                  )
                })}
            </div>
            <div className="mesh-note">
              {(group.members.length * (group.members.length - 1)) / 2} internal edges · Weight{' '}
              {group.weight ?? 1}
            </div>
          </>
        ) : (
          <>
            <Field label="Node">
              <select
                required
                value={link.node}
                onChange={(e) => setLink({ ...link, node: e.target.value })}
              >
                <option value="">Choose node</option>
                {network.nodes
                  .filter((n) => !n.wireguard)
                  .map((n) => (
                    <option key={n.id} value={n.id}>
                      {n.name}
                    </option>
                  ))}
              </select>
            </Field>
            <Field label="Full mesh group">
              <select
                required
                value={link.group}
                onChange={(e) => setLink({ ...link, group: e.target.value })}
              >
                <option value="">Choose group</option>
                {network.groups?.map((g) => (
                  <option key={g.id} value={g.id} disabled={g.members.includes(link.node)}>
                    {g.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Routing weight">
              <input
                type="number"
                min={1}
                max={4294967295}
                required
                value={link.weight}
                onChange={(e) => setLink({ ...link, weight: Number(e.target.value) })}
              />
            </Field>
            <label className="check">
              <input
                type="checkbox"
                checked={link.enabled}
                onChange={(e) => setLink({ ...link, enabled: e.target.checked })}
              />
              Enable link
            </label>
          </>
        )}
        <ConnectionPolicy
          value={value}
          change={(patch) =>
            target.kind === 'group'
              ? setGroup({ ...group, ...patch })
              : setLink({ ...link, ...patch })
          }
        />
        <footer>
          <button type="button" onClick={close}>
            Cancel
          </button>
          <button className="primary" type="submit">
            Apply to draft
          </button>
        </footer>
      </form>
    </Modal>
  )
}
