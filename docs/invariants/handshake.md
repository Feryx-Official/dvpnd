# Handshake and API: who gets a peer, and what the node answers

The handshake is where a stranger on the internet asks the node for service.
`docs/protocols.md` is the contract client apps implement; these rules are what the
node must keep doing. IDs as in `test/README.md`.

- **[HS-1] A handshake is admitted only with a signature from the session's account
  key.** The signature covers the session id (8 bytes, big-endian) followed by the exact
  JSON of the peer request, made with a 33-byte compressed secp256k1 key; an id of zero
  is refused. The key's address must be the session's account (see [SL-6]).
- **[HS-2] Handshakes are accepted over HTTPS only.** The peer request is the UUID a
  proxy session logs in with; over plain HTTP anyone on the path could copy it. The
  status pages stay readable either way.
- **[HS-3] Handshake attempts are limited to 30 a minute per client block.** A block is
  an IPv4 address or an IPv6 /64, taken from the connection, never from a header the
  client writes (`X-Forwarded-For`). Each attempt costs chain queries, and without a cap
  one host could make the node flood its RPC providers until they throttle it. The
  counts live in memory and are dropped every window, so they never become a record of
  who connected.
- **[HS-4] The legacy handshake endpoint exists only when `[node] legacy_handshake` is
  on.** Its signature covers the session id alone, so a captured request can be replayed
  with another peer key, which evicts the real client's peer.
- **[HS-5] A successful handshake reply is signed by the node in `X-Dvpnd-Signature`,
  and the body stays what clients accept.** The TLS certificate is self-signed and
  nothing on the chain vouches for it; the signature proves the reply came from the node
  the client paid. A probe once refused extra keys in the body, so the signature is a
  header. A reply that cannot be signed still goes out, unsigned.
- **[HS-6] The signed digest is exactly the one `docs/protocols.md` specifies.** Clients
  verify against the spec; any change to the digest breaks every client that checks it.
  A fixed vector pins it, in the node and in `tools/e2e`.
- **[HS-7] Every response of a node that signs says so in `X-Dvpnd-Reply-Signing`, and
  both headers are exposed to browsers.** A client that requires signed replies can
  then refuse a node that doesn't sign before it pays for a session. Refusals and
  unknown paths carry the header too.
- **[HS-8] A failure inside the node reaches the client as a generic message.** An RPC
  endpoint's error or a daemon's is the operator's business and goes to the log; the
  client is told why a request was refused, but not the node's internals.
- **[HS-9] A handler that panics gets the client a 500, and the node keeps running.**
  The panic is logged without the request's headers.
- **[HS-10] Request bodies and connection times are bounded.** The API server has
  read, write and idle timeouts and a body limit, so a slow or huge request can't hold
  the node's resources.
- **[HS-11] A peer request for another protocol is refused with a message that names
  the node's protocol.** "uuid is missing" alone once left an operator guessing why a
  WireGuard-only client could not use a Hysteria2 node.
- **[HS-12] Every refused request is logged with what was asked and the reason given.**
  An operator otherwise has no way to see why a client or an aggregator's probe was
  turned away. Rate-limited attempts stay at debug, so a flood can't fill the log; so do
  requests for a path the node does not serve, which scanners send by the thousand. A
  refusal on a route the node serves reaches the error log. The path and the User-Agent
  are clipped, since the client writes both.
- **[HS-13] V2Ray accepts AEAD VMess headers only.** The legacy VMess header
  authenticates with MD5; client apps send AEAD headers (`alterId: 0`).
- **[HS-14] A client app in a browser can send the handshake.** The preflight before a
  JSON POST from any origin allows the `Content-Type` header. The node once listed the
  media type's value (`application/json; charset=utf-8`) as the allowed header, and a
  browser then blocked every handshake before sending it.
