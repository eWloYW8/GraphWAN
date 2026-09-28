# WS and WSS peer endpoints

WS/WSS carry the same authenticated peer protocol, overlay packets, heartbeats
and active-Link selection as TCP/UDP. They are available only through manual
endpoints. Automatic interface discovery never guesses HTTP URLs or paths.

In **Agents → Manage**, add an endpoint such as
`ws://192.0.2.10:24752/overlay` or `wss://node.example.com:24752/overlay`.
Enable the corresponding transport and IPv4/IPv6 direct method on the Edge.
Both directions may dial if both Agents publish an eligible endpoint. A single
reachable endpoint is sufficient to establish a bidirectional Link.

The Agent's configured listen port (24752 by default) accepts native TCP, WS, WSS
and gRPC on the same TCP listener. UDP uses the same port number on its separate UDP
socket. A manual endpoint advertises a reachable URL; it does not open an extra
listener. Different external ports require port forwarding or a reverse proxy.
An empty URL path means `/`; otherwise the exact escaped path must be preserved.
Queries, fragments and URL credentials are not supported.

## TLS and reverse proxies

Direct WSS presents an automatically renewed TLS 1.3 certificate tied to the
Agent's durable Ed25519 public key. Peers receive that public key through the
authenticated controller configuration and verify it without needing public DNS
or a public certificate. Renewal continues with the controller offline.

A TLS-terminating reverse proxy can instead present a certificate accepted by
the connecting Agent's system trust store and valid for the manual URL hostname.
The inner Noise handshake still authenticates the actual Agent, configured Edge
and Network behind that proxy. The proxy can observe connection metadata, but
does not receive the decrypted overlay messages. Unknown certificates, wrong
hostnames and expired certificates are rejected. No insecure TLS option is used
to bypass these checks; the custom verifier checks either the identity pin or
the full certificate chain and hostname.

For example, a TLS-enabled nginx virtual host can forward this location to the
Agent's shared listener (supply the virtual host's certificates separately).
The upgrade headers follow [nginx's WebSocket proxy documentation](https://nginx.org/en/docs/http/websocket.html):

```nginx
location = /overlay {
    proxy_pass http://127.0.0.1:24752;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 60s;
    proxy_send_timeout 60s;
}
```

Advertise `wss://node.example.com:443/overlay` in this case. Preserve the original
path and `Sec-WebSocket-Protocol` header. The backend accepts the WSS subprotocol
over the proxy's plaintext HTTP connection; Agent authentication remains inside
Noise. IPv6 frontends may forward to IPv4 backends. The candidate's address-family
policy applies to the dialer-to-proxy socket, not the proxy-to-Agent socket.

Endpoints are Agent protocol services, not browser WebSockets. Requests with an
Origin header are rejected. Redirects and environment HTTP proxies are disabled
for peer dialing, so the configured destination and address family determine the
actual connection. Explicit reverse proxies remain supported as manual endpoints.

## Wire behavior and verification

Each nonempty binary WebSocket message carries one peer protocol message, with
a maximum of 16 KiB. Text and oversized messages terminate the connection.
Compression is disabled. Negotiated subprotocols are `graphwan.ws.v1` and
`graphwan.wss.v1`, respectively; the encrypted handshake binds the same transport.
Noise and session rekey apply to both WS and WSS.
Peer admission limits cover slow TCP/TLS prefaces and incomplete HTTP headers;
an incomplete upgrade cannot escape the eight-slot classifier limit.

Real-socket tests cover binary boundaries, cancellation, TLS pin and CA validation,
TLS proxy termination, IPv6-to-IPv4 proxying, wrong-path introduction rejection,
shared-port transport selection and endpoint removal. The native Linux E2E test
has separate `--transport ws` and `--transport wss` modes. Each starts three real
Agents and TUNs, verifies multi-hop IP traffic, then verifies controller outage,
offline transit-Agent restart and interface cleanup. See
[the native verification commands](linux-operation.md#reproduce-native-verification).
