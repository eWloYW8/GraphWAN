# Administrative CLI

The same `graphwan` binary runs the controller, Agent and administrative commands.
Administrative commands use the controller's authenticated management API and do
not require local root privileges or direct access to its database.

Set `GRAPHWAN_ADMIN_PASSWORD` to the existing controller password. It is read from
the environment, never from a command-line flag. Each invocation logs in, keeps
its session cookie and CSRF token in memory, and attempts logout before exiting.
No session file is written. If logout cannot reach the controller, the session
expires under the normal 12-hour server policy.

```sh
export GRAPHWAN_ADMIN_PASSWORD='<your-existing-controller-password>'
graphwan network create --server https://127.0.0.1:8443 --ca ./graphwan-data/ca.pem \
  --name lab --cidr 10.80.0.0/24
graphwan network list --server https://127.0.0.1:8443 --ca ./graphwan-data/ca.pem
graphwan node list --server https://127.0.0.1:8443 --ca ./graphwan-data/ca.pem \
  --network '<network-id>'
graphwan edge add --server https://127.0.0.1:8443 --ca ./graphwan-data/ca.pem \
  --network '<network-id>' --a '<first-node-id>' --b '<second-node-id>' \
  --weight 10 --transports udp,tcp --ipv4-direct=true --ipv6-direct=true --hole-punch=true
```

Create memberships and assign their fixed virtual IPs through the
[management UI](management-ui.md) or [API](control-api.md) before adding an Edge.
`--a` and `--b` reference **Node IDs in that Network**, not Agent IDs.
The CLI commands in the proposal are supported; other administrative operations
remain available through the UI/API.

## Commands and output

| Command | Options and JSON result |
| --- | --- |
| `network create` | Required `--name`, `--cidr`; optional `--mtu` (1280–9000, default 1280), `--cipher` (currently `chacha20-poly1305`). Returns `{revision, network}` with a new ID and empty Nodes/Edges. |
| `network list` | Returns `{revision, networks}` with complete desired Network configurations and their IDs. |
| `node list` | Optional `--network`; returns `{revision, nodes}`. Each Node includes `network_id`, `network_name`, `id`, `agent_id`, `name`, `address` and `position`. Without a filter, lists all memberships. An unknown Network is an error. |
| `edge add` | Required `--network`, `--a`, `--b`; optional `--weight` (1–4294967295, default 1), `--transports` (default `udp,tcp`), `--enabled` (true), `--ipv4-direct` (true), `--ipv6-direct` (true), `--hole-punch` (true), `--preferred-candidate` (empty). Returns `{revision, edge}`. |

All flags follow the two command words. Use, for example, `graphwan edge add
--help` for help. Boolean flags use `--flag=false` to disable. Transport values
are `udp,tcp,quic,ws,wss,grpc`; duplicates and unknown values are rejected. At
least one connection method and one transport are required, even for a disabled
Edge. An existing Node pair cannot receive a second Edge. CIDRs must be canonical
IPv4 or IPv6 subnets; they are not silently masked.

Successful commands emit one JSON object to stdout and exit zero. Empty lists
are `[]`. Errors go to stderr with a nonzero exit status. Listings describe
desired configuration; use the UI or telemetry API for live Link status and
Candidate IDs. Selecting an unavailable preferred Candidate falls back to the
normal automatic Link selection.

## Connection and update behavior

Every command requires `--server` with an HTTPS origin, without a path, query,
fragment or credentials. TLS 1.3 and normal hostname/certificate verification
are required. `--ca` adds PEM certificates to system trust. Redirects are rejected,
including redirects to another HTTPS URL. There is no TLS verification bypass.
For a controller explicitly started in local HTTP development mode, pass both
`--http` and a literal loopback origin, for example `--server
http://127.0.0.1:8080`; DNS names such as `localhost` are not accepted for HTTP.

`--timeout` bounds login and the operation together (default `30s`). Interrupts
cancel the operation. Best-effort logout has a separate two-second timeout.
Requests obey the API's 8 MiB body bound; CLI responses are limited to 64 MiB.
The controller's per-address login rate limit also applies to CLI invocations.

Mutations first fetch current state and send its revision in `If-Match`.
`edge add` preserves the Network's existing Nodes, Edges and other settings.
A concurrent change produces HTTP 409 and leaves that change intact. The CLI
does not automatically retry a mutation. Review the current state before
rerunning after a conflict. If a mutation's response is lost, times out or cannot
be decoded, it may already have committed; inspect `network list` before retrying
to avoid creating a duplicate Network. Likewise, an output-write failure after
success does not roll back the controller transaction.

Linux integration coverage in [admin_test.go](../cmd/graphwan/admin_test.go) uses
a real TLS controller and durable store for creation, membership listing,
configuration preservation, stale-revision rejection, authentication and session
invalidation. HTTP fixtures exercise redirect refusal, deadlines and malformed
responses. Other platforms use the same Go HTTP client and are cross-built.
