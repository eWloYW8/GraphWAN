# GraphWAN management UI

The controller serves its embedded management application at `/`. Sign in with
its administrator password. The UI and API share the controller's HTTPS origin;
no CDN, external font or separate frontend service is required.

## Enroll machines and create a topology

1. Open **Agents → Enroll agent**, select a lifetime, and create a single-use token.
   Copy it before closing the dialog. The controller retains only its hash.
2. On the machine, trust the controller's exported CA file and run the Agent with
   that token as described in [Linux operation](linux-operation.md). Refresh is
   automatic when the new registration reaches the controller.
3. Create a Network with a canonical CIDR and MTU. Start with 1280 unless the
   underlay supports your chosen larger encapsulated packet size.
4. Open the network and choose **Edit**. **Add node** selects an enrolled Agent,
   a Node name and a fixed host IP inside that CIDR. Revoked Agents and existing
   members are excluded from the add list.
5. Add Edges with **Add edge**, or drag from anywhere along a Node's border onto
   another Node. **Connect** mode lets you drag from anywhere on the whole card;
   **Move** mode lets you drag its center to arrange it. Connections are
   bidirectional. Edges attach automatically around each card's perimeter and
   adjust as Nodes move; neighboring attachments spread apart. There are no fixed
   visible ports.
   The inspector's Node/Edge lists provide an alternative to graph selection.
6. Select an Edge to change weight, enablement, allowed transports, IPv4/IPv6
   direct methods, punching policy, or preferred candidate. Automatic preference
   uses the selector's RTT; an unavailable saved preference remains visible.
7. **Save changes** applies the entire draft as one revision. Agents then reconcile
   that revision. Removing a Node also removes its incident Edges in the draft.

Network settings include name, subnet, MTU and cipher. Node settings include
name, Agent membership and virtual IP. Agent **Manage** changes the machine name,
listen port, revocation and manual endpoints, including protocol-specific URL
paths. **Exclude container IPs** is enabled for newly enrolled Agents and can be
disabled in **Manage**. It controls the [Linux container interface
filter](endpoint-resolution.md#automatic-interface-discovery). Discovered endpoints are read-only. Deleting an Agent removes its network
memberships; deleting a Network removes all its memberships and Edges.

TCP, UDP, QUIC, WS, WSS and gRPC data transports run in the Agent. WS/WSS require
[manual endpoints](websocket-operation.md); gRPC uses [manual service prefixes](grpc-operation.md).
[QUIC also requires a manual endpoint](quic-operation.md). Agent settings include
[STUN servers for TCP/UDP hole punching](nat-operation.md). Use `host:port` for UDP
and `tcp://host:port` for TCP; the two protocols can have different NAT mappings.
See [NAT operation](nat-operation.md) for traversal behavior and limits.

Agent rows show process CPU and Go-managed memory. Node details additionally show
heap memory, goroutines, logical CPUs and uptime. CPU can exceed 100% when multiple
cores are busy. Missing or stale resource data displays as Unavailable. See
[resource telemetry](resource-telemetry.md) for sampling and memory definitions.

## Observe actual connectivity

**Observe** prevents configuration edits while allowing pan, zoom and inspection.
Node state is independent of Edge connectivity: a connected control channel does
not prove an underlay Link is working. Config application errors and current TUN runtime errors appear in Node
and Agent details. A runtime fault marks the Agent as Error without changing its
applied revision; the error clears after automatic local recovery. Edge state is connected only when both reports select the same
healthy Link. Partial/asynchronous agreement appears as switching.

The graph displays active transport, RTT and transmit rate from one reporting
endpoint. The Edge inspector shows both endpoints' Link reports, including standby
Links, loss and cumulative receive/transmit counters. Rates use successive Agent
report times, normally two seconds apart; browser events arrive once a second.
Duplicate browser events retain the latest rate. New/rekeyed sessions require two
counter samples before a rate is available; resets never produce negative rates.

Loss of the browser event connection, or five seconds without a snapshot,
displays unknown status and reconnecting,
while retaining the latest configuration for reference. Fifteen seconds of silence
forces a fresh event connection. It does not assert that
Agent data forwarding stopped. The Agent continues independently of the controller.

## Topology drawing and paths

Use **Drawing** to choose **Line** (straight segments with short detours around
Node cards) or **Bezier** curves. This browser preference persists across reloads
and does not change the Network configuration. Clicking a selected Node again
clears its selection.

Ctrl-click two Nodes (Command-click also works on macOS) to highlight their
forwarding path, in selection order. The inspector lists the hops and their
active transports, and shows the sum of hop RTTs. This sum is not an end-to-end
latency probe. **Reverse direction** shows the opposite route. Clicking an
endpoint again removes it from the pair.

The displayed route follows the controller's configured weights and deterministic
next-hop tie breaking. It is marked active only when every Agent confirms the
current revision and both endpoints of each Edge report the same healthy active
Link. Offline hops, stale configuration, unsaved routing changes and disconnected
telemetry are shown as unavailable or unconfirmed; they do not invent alternate
forwarding paths.

## Concurrent edits and session behavior

Topology edits remain in browser memory until saved. Navigation or closing the
page warns about unsaved topology changes. Changes to another network can safely
advance the draft's revision. Changes to the same network preserve the draft and
block saving; discard it to reload current configuration. If a server-side 409
comes from an unrelated update (including automatic endpoint discovery), network
and Agent saves fetch current state and retry with its revision, up to five
attempts. The comparison always uses the original edit base; changes to the same
network or editable Agent settings preserve the draft and stop the save. Agent
discovered endpoints are excluded from that comparison because a settings save
does not replace them. Validation errors are displayed and do not consume a revision.

Sessions expire after 12 hours and are invalidated on controller restart. Logout
closes the browser view and revokes existing event streams. Passwords, tokens and
CSRF values are never persisted in localStorage. Enrollment tokens are held only
while their dialog is open. Closing that dialog cannot recover the plaintext token;
create another if needed.

## Build and verify

The application uses React, TypeScript, Vite and a controlled React Flow graph.
The [React Flow controlled-graph guide](https://reactflow.dev/learn/concepts/adding-interactivity)
and [Vite setup documentation](https://vite.dev/guide/) describe these integration
points. pnpm's committed lockfile fixes dependency versions.

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web run format:check
pnpm --dir web build
go build -o bin/graphwan ./cmd/graphwan
```

`internal/webui/dist` is generated, committed and embedded with `go:embed`. Always
rebuild it after changing frontend sources. The build also preserves dependency
license notices in `/THIRD_PARTY_LICENSES.txt`.

For hot reload, run a development controller on `127.0.0.1:8443` with `--http`,
then `pnpm --dir web dev`. Vite proxies `/api` while preserving the request Host
so the existing same-origin checks still apply. Open Vite's local URL.

## Servers

**Servers** lists cluster members and their automatic and manual entry points.
**Edit** changes a Server name, its TCP STUN list and manual `tcp`, `websocket`,
`grpc` or `wss` entrances. Automatic discoveries are retained when saving manual
changes. Edits retry unrelated revision changes while preserving the draft if
another administrator changed the same settings.

Use **Add server** to generate an invitation, then **Join cluster** on a new empty
Server to paste it. The new Server restarts automatically; sign in with the
existing cluster password. Coordinator election is automatic and every Server
provides the same controls. Three voting members are needed to keep configuration
writes available after one fails. Agent connections and data forwarding remain
available without write quorum.

## Full mesh groups and node-to-group links

In a Network's **Edit** mode, choose **Add group**, select at least two regular
nodes, and choose a shared routing weight, transports and connection methods.
Internal connections default to weight 1 and use the Network's encryption suite. A node belongs to
at most one group; WireGuard leaf nodes cannot join groups.

The main canvas hides internal edges behind a dashed, shaded group boundary.
Click the boundary or shaded area to open the group; **← Network** returns to the
main topology. The inspector also lists groups and aggregate links, including
those without geolocation. Line and Bezier views support group navigation. The 3D view always expands the
whole network into actual node-to-node edges, including internal mesh edges and
aggregate children. It shows no group regions or edge information labels; click
a node or edge to inspect its details.

Use **Node → group**, or drag a connection between a regular node and a group
boundary, to create an aggregate link. Its weight, enabled state, transports and
connection methods apply to every child connection. Click the aggregate edge to
view its children individually. The external node cannot belong to the target
group. Group-to-group links are not supported.

Aggregate links and individual edges to the same members are mutually exclusive.
The editor asks before replacing conflicting individual edges with shared policy,
or replacing an aggregate with one individual edge. Shared edges cannot be edited
or removed individually: open their shared settings instead. All changes remain a
draft until **Save changes**. Membership changes automatically add/remove derived
edges; dissolving a group removes its internal edges and aggregate links, while
preserving its nodes and unrelated individual edges. Removing a node prunes its
membership; groups with fewer than two remaining members are removed.

Summaries display the sum of measured RX+TX rates across child edges (one endpoint
report per session, not both), maximum RTT among connected selected links, and
connected/total edge count. Missing measurements display a dash or `RTT unknown`;
partial connectivity remains visible. These are link traffic and direct-link RTT,
not unique application throughput or end-to-end routed ping. Forwarded traffic
crossing multiple edges contributes on each edge. Child failures only withdraw the
failed routes; the remaining children can still carry traffic.

Groups simplify drawing, not connection count: N members create N×(N−1)/2 internal
edges. The 100,000-edge Network limit includes all expanded internal and aggregate
edges. Upgrade every Server to v0.2.4 or newer before creating groups. Existing
Agents receive ordinary peer/route snapshots and do not require a wire-format
upgrade. Do not edit grouped networks using older Servers or administrative tools.

The topology toolbar's **Enter fullscreen** button expands the graph and toolbar
across the browser viewport in both view and edit modes, including 3D. Use
**Exit fullscreen** or **Esc** to return to the normal layout.
