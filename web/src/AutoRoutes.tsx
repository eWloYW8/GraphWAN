import type { Network, Node } from './model'

export function AutoRoutes({
  network,
  node,
  change,
}: {
  network: Network
  node: Node
  change: (network: Network) => void
}) {
  const advertised = new Map(
    network.nodes
      .filter((n) => n.id !== node.id)
      .flatMap((n) => (n.advertised_subnets ?? []).map((s) => [s.prefix, n.name] as const)),
  )
  const selected = node.auto_routes ?? []
  const prefixes = [...new Set([...advertised.keys(), ...selected])]
  return (
    <div className="advertised-subnets">
      <strong>Automatic subnet routes</strong>
      <p className="muted">
        Choose the advertised subnets this Agent should route through GraphWAN.
      </p>
      {prefixes.length === 0 && <p className="muted">No other node advertises a subnet.</p>}
      {prefixes.map((prefix) => (
        <label className="check" key={prefix}>
          <input
            type="checkbox"
            checked={selected.includes(prefix)}
            onChange={(e) =>
              change({
                ...network,
                nodes: network.nodes.map((n) =>
                  n.id === node.id
                    ? {
                        ...n,
                        auto_routes: e.target.checked
                          ? [...selected, prefix]
                          : selected.filter((p) => p !== prefix),
                      }
                    : n,
                ),
              })
            }
          />{' '}
          {prefix} — {advertised.get(prefix) ?? 'No longer advertised'}
        </label>
      ))}
    </div>
  )
}
