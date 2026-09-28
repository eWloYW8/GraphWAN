# gRPC peer endpoints

gRPC carries GraphWAN peer messages over a standard protobuf bidirectional stream.
It shares the Agent's TCP listen port with native TCP and WS/WSS (24752 by
default). Add a **manual** endpoint in **Agents → Manage**, enable gRPC and the
appropriate IPv4/IPv6 direct methods on its Edges, and save the configuration.
Automatic discovery continues to advertise only TCP/UDP.

GraphWAN's `grpc://` endpoint scheme always dials TLS 1.3 with HTTP/2 (`h2` ALPN).
A direct Agent certificate is verified against its configured Ed25519 identity.
A TLS proxy can instead present a certificate trusted by the connecting Agent's
system roots and valid for the URL hostname. There is no automatic TLS downgrade.
The inner Noise exchange then independently authenticates the peer identities,
Network, Edge and gRPC transport. Certificate renewal and Link reconnection do
not require the controller to be online.

## Endpoint paths

The URL path is an optional **prefix** for the gRPC service method:

| Manual endpoint | HTTP/2 RPC path |
| --- | --- |
| `grpc://node.example.com:24752` | `/graphwan.v1.Peer/Connect` |
| `grpc://node.example.com:443/overlay` | `/overlay/graphwan.v1.Peer/Connect` |

A trailing slash is ignored. Other escaped path bytes are preserved. Queries,
fragments and URL credentials are not supported. Manual URLs advertise externally
reachable endpoints; they do not bind extra ports. The shared listen port remains
the one set in Agent configuration.

The receiver admits only an RPC path matching one of its configured manual gRPC
endpoints. After Noise, it verifies that the encrypted candidate introduction
identifies that same endpoint path and an allowed Edge/method/family. Removing
an endpoint closes its Links and refuses subsequent attempts at that path.

## Reverse proxy

The frontend must support long-lived bidirectional gRPC over HTTP/2 and preserve
the full RPC path, trailers and `graphwan-protocol` response metadata. It may
terminate TLS and use plaintext HTTP/2 to the Agent's shared port. Peer messages
remain encrypted inside Noise on the backend hop. Browser gRPC-Web is not used.
Environment HTTP proxies and resolver-supplied service configurations are disabled
for Agent dialing; configure a reverse proxy explicitly as the manual endpoint.

For a TLS-enabled nginx virtual host with `http2 on;`, this location supports the
second URL above (configure its certificate/key in the virtual host separately):

```nginx
location = /overlay/graphwan.v1.Peer/Connect {
    grpc_pass grpc://127.0.0.1:24752;
    grpc_read_timeout 60s;
    grpc_send_timeout 60s;
}
```

The backend syntax follows [nginx's gRPC module](https://nginx.org/en/docs/http/ngx_http_grpc_module.html).
GraphWAN heartbeat messages keep an established standby stream active. IPv6
frontends may use IPv4 backend connections: the address-family policy applies
to the Agent's actual dial to the frontend, not the proxy's backend connection.

## Wire contract and bounds

[api/peer.proto](../api/peer.proto) defines `graphwan.v1.Peer/Connect` using
`google.protobuf.BytesValue` in both directions. Each value contains exactly one
complete peer protocol message, including the Noise handshake or encrypted Link
traffic. Values must contain 1–16,384 bytes. The protobuf envelope is limited to
16,388 bytes before decoding, and compression is not enabled. HTTP/2/gRPC framing
uses the [standard gRPC protocol](https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md).
The server acknowledges admission with `graphwan-protocol: graphwan-peer-v1`;
this acknowledgement is not Agent authentication.

The implementation uses grpc-go's stream APIs with the existing generated
`wrapperspb.BytesValue` type. No additional Go code generation is needed to build
GraphWAN. The schema is provided for interoperability; a generic gRPC client
still needs to implement the [peer authentication and Link protocol](peer-protocol.md).

Each Agent caps inbound HTTP/2 connections at 512, concurrent streams per
connection at 64, and header lists at 8 KiB. Incomplete HTTP/2 setup times out
after five seconds; connections without RPCs expire after ten seconds. Streams
share the existing eight incoming peer-handshake slots. Receive and Send support
context cancellation, including when flow control blocks a writer. Canceling an
operation closes that RPC. The dial timeout only bounds establishment and cannot
expire an already established Link. A failed Link reconnects with fresh Noise
keys through the common bounded Mesh scheduler.

## Verification

Real TLS/gRPC tests cover both directions through the maximum message size,
empty/oversized input, unknown methods, dial and per-operation cancellation,
flow control, independent CA-trusted TLS frontends and pinned direct identities.
Mesh tests cover the shared TCP/WS/WSS/gRPC listener, configured paths and
candidate binding, endpoint removal, standby fallback, IPv6-to-IPv4 TLS proxying,
and shutdown with incomplete HTTP/2 prefaces.

Run the Linux process test with `--transport grpc` for three real Agents and TUNs,
multi-hop ICMP/TCP, controller outage, offline transit-Agent restart and cleanup.
See [the native verification commands](linux-operation.md#reproduce-native-verification).
