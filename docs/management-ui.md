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
5. Add Edges with **Add edge** or drag a Node's right connection handle to another
   Node's left handle. Connections are bidirectional. Drag Nodes to arrange them.
   The inspector's Node/Edge lists provide an alternative to graph selection.
6. Select an Edge to change weight, enablement, allowed transports, IPv4/IPv6
   direct methods, punching policy, or preferred candidate. Automatic preference
   uses the selector's RTT; an unavailable saved preference remains visible.
7. **Save changes** applies the entire draft as one revision. Agents then reconcile
   that revision. Removing a Node also removes its incident Edges in the draft.

Network settings include name, subnet, MTU and cipher. Node settings include
name, Agent membership and virtual IP. Agent **Manage** changes the machine name,
listen port, revocation and manual endpoints, including protocol-specific URL
paths. Discovered endpoints are read-only. Deleting an Agent removes its network
memberships; deleting a Network removes all its memberships and Edges.

TCP, UDP, WS, WSS and gRPC data transports run in the Agent. WS/WSS require
[manual endpoints](websocket-operation.md); gRPC uses [manual service prefixes](grpc-operation.md).
QUIC and punch policies can be
stored but their runtime adapters and NAT coordination remain incomplete. See [the acceptance tracker](implementation-status.md).

## Observe actual connectivity

**Observe** prevents configuration edits while allowing pan, zoom and inspection.
Node state is independent of Edge connectivity: a connected control channel does
not prove an underlay Link is working. Config application errors appear in Node
and Agent details. Edge state is connected only when both reports select the same
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

## Concurrent edits and session behavior

Topology edits remain in browser memory until saved. Navigation or closing the
page warns about unsaved topology changes. Changes to another network can safely
advance the draft's revision. Changes to the same network preserve the draft and
block saving; discard it to reload current configuration. A server-side 409 also
preserves the draft and fetches current state. Validation errors are displayed and
do not consume a revision. Agent editors reject intervening edits to that Agent.

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
pnpm --dir web test
pnpm --dir web build
go build -o bin/graphwan ./cmd/graphwan
pnpm --dir web exec playwright install chromium
pnpm --dir web test:e2e
```

`internal/webui/dist` is generated, committed and embedded with `go:embed`. Always
rebuild it after changing frontend sources. The build also preserves dependency
license notices in `/THIRD_PARTY_LICENSES.txt`. TypeScript checks browser test code
as part of the production build. Browser tests require Go and OpenSSL, start their
own loopback controller in a temporary directory, and remove it on shutdown.
Screenshots and retained failure traces live under ignored `web/test-results`.

For hot reload, run a development controller on `127.0.0.1:8443` with `--http`,
then `pnpm --dir web dev`. Vite proxies `/api` while preserving the request Host
so the existing same-origin checks still apply. Open Vite's local URL.
