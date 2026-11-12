# Protocols: design note

What a protocol needs from the host, from Docker and from the node's code, for the six
protocols dvpnd runs. This is the record of the decisions taken on 2026-09-08, when the
four newest were added; `docs/operator.md` is what an operator reads. Update this file
whenever a protocol changes or a decision here does.

## Two classes

| Class | Protocols | Data plane | Host path | Docker path |
|---|---|---|---|---|
| **A: tunnel + NAT on the host** | WireGuard, AmneziaWG, OpenVPN | kernel module or tun device, `ip_forward`, MASQUERADE | recommended: one NAT hop, native IPv6, kernel data plane | NET_ADMIN, NET_RAW (+SYS_MODULE and `/lib/modules` for kernel modules, `--device /dev/net/tun` for tun), sysctls, a second NAT hop, `daemon.json` for IPv6; kernel modules cannot ship in an image |
| **B: userspace proxy on a port** | V2Ray, XRAY, Hysteria2 | a child process listening on one TCP/UDP port | equal: the binary on `PATH`, no privileges | equal: `--cap-drop ALL`, one published port |

One protocol per node process, chosen with `[node] type`. A node that should offer two
protocols is two nodes. Running several protocols in one process would touch the status
API, the handshake, the session table and every client; that is a separate decision.

## Status

| Type | Name | Class | State |
|---|---|---|---|
| 1 | `wireguard` | A | shipped, exercised against the public network |
| 2 | `v2ray` | B | shipped, not yet exercised end to end |
| 3 | `openvpn` | A | shipped; an OpenVPN client tunnelled through it between two containers over UDP and TCP, and a removed peer was killed and denied |
| 4 | `xray` | B | shipped; a VLESS client tunnelled through it on a test machine over TLS and over REALITY |
| 5 | `amneziawg` | A | shipped; two tiers on one 3.1 engine: between two containers, a client with the engine the apps bundle tunnelled on the default tier and a 3.1 client on the 3.1 tier (`tools/awgcheck`) |
| 6 | `hysteria2` | B | shipped; the Hysteria client tunnelled through it on a test machine, with and without obfuscation |

The numbers and names are what client apps on the network use; they were taken from the
request/response handling of the maintainer's own client (see `CONTRIBUTING.md`), never from
the network's node software. The number is what `GET /status` reports and what aggregators
key on; the name is what `GET /` reports as `service_type`.

## The contract every service must satisfy

- `Info()`: the first two bytes are the listen port, big-endian; the rest is
  protocol-specific. The legacy session endpoint returns it raw to old clients; that
  endpoint is registered only with `[node] legacy_handshake = true`.
- `GET /` answers the root document in the layout nodes on the network use (observed from
  a client): `{addr, uplink, downlink (bytes per second, as strings; "0" when the node has no
  usable measurement), handshake_dns,
  location{city, country, country_code, latitude, longitude}, moniker, peers,
  service_type, service_metadata, version{name, tag, commit}}`. Aggregators read
  `version.tag`; `version.name` is `dvpnd` (the network's other node software does not send
  it, and clients ignore keys they do not know), and every response carries a
  `Server: dvpnd/<version>` header. Clients read the major version as the node API level:
  below 9 they assume the node publishes no inbound list and rank it last, so a dvpnd
  release tag must keep the major at 9 or above. The legacy status fields (with `version`
  as a string) stay on `GET /status`.
- `service_metadata` in the root document is the service's `PublicMetadata()`: the keys of
  the handshake entry with everything per-session or secret blanked, per type as the
  network publishes it. wireguard `{port: 0, public_key: null}`; amneziawg the same plus
  `s1..s4, h1..h4: 0` and `awg_version`, one entry per tier offered (2, and 3 when the 3.1
  tier is enabled); v2ray `{port: "", proxy_protocol, transport_protocol,
  transport_security, tls_pin: ""}`; xray the same plus `flow`, `method`, `key` and the
  `reality_*` keys, all blank; hysteria2 `{port: 0, tls_pin: "", obfs_password:
  "<redacted>" or ""}`; openvpn `{port: 0, protocol, ca: null, tls: null}`. Codes: proxy
  VLESS = 1, VMess = 2; transport tcp = 1; security none = 1, tls = 2, reality = 3; flow
  none = 1, vision = 2.
- `POST /` (handshake): body `{data: base64(JSON peer request), id, pub_key:
  "secp256k1:…", signature}`; the signature covers `BE64(id) || raw JSON`. The answer is
  `{success: true, result: {data: base64(JSON payload), addrs: [node hosts]}}`.
- The answer to a successful `POST /` carries the node's signature in a response header,
  so the body stays byte-identical to what clients and probes accept (a probe once refused
  extra keys in the body). Clients pin a self-signed certificate nothing on the chain
  vouches for; the signature is what proves the reply came from the node they paid:

  ```
  X-Dvpnd-Signature: secp256k1:<base64 compressed public key>;<base64 r||s>
  digest = SHA-256( "dvpnd/handshake-reply/v1" || BE64(id) || SHA-256(request data)
                    || SHA-256(reply data) || addrs joined by "\n" )
  ```

  `request data` is the peer request the client signed (its body's `data`, decoded),
  `reply data` is `result.data` decoded, `addrs` is `result.addrs` in order. The signature
  is made the way the client signs its request: secp256k1 ECDSA over SHA-256 of the
  32-byte digest, low-s, 64 bytes. A client accepts it when the key's account address is
  the node's (the bytes of its `sentnode` address), or, for a node that signs with an authz
  hot key, when the chain holds a grant from the node account to that key's address for
  `/sentinel.node.v3.MsgUpdateNodeStatusRequest` (authz `Grants` query with granter, grantee
  and that message type). The digest of fixed inputs is pinned in `api/session`'s
  `TestReplyDigestVector` for client implementers. The legacy endpoint's replies are not
  signed.
- Every response of a node that signs, the root document included, carries
  `X-Dvpnd-Reply-Signing: dvpnd/handshake-reply/v1`, so a client that requires signed
  replies can refuse a node that does not sign before it pays for a session. It proves
  nothing by itself: anyone on the path can strip it or add it, and only the signature on
  the reply counts. Both headers are exposed to browsers through CORS, and the preflight
  allows the `Content-Type` header a JSON POST from a browser sends.
- The session key stored by the node is `base64(peer data)`; `Peers()` must return each
  peer with exactly that key, or its usage is never reported.
- `RemovePeer` is called by the node (session expired, allocation exceeded, paid hours
  used, account evicted), never by the client.
- A peer's counters start at zero when `AddPeer` adds it: xray and v2ray keep a removed
  user's counters (seen with both binaries), so a later session with the client's UUID
  would start with what an earlier one moved; `AddPeer` resets them first (`QueryStats`
  with reset).
- Egress: a client reaches the internet and nothing on the node host or around it. One
  policy in `services/common/egress.go` (the blocked IPv4 and IPv6 networks, `localhost` by
  name, TCP 25 unless `[egress] allow_smtp`) is rendered by every service, so the protocols
  cannot drift apart. Proxies render it into their own configuration and must match a
  destination given as a name by its resolved addresses too; run as the proxy account, they
  are held to it, and kept off every address of the host but the API port, by
  `common.ProxyEgress` in the kernel. Tunnel services install it with
  `common.TunnelEgress` (per-interface chains jumped to from the top of FORWARD and INPUT)
  before the interface carries traffic, and remove it after.

Per type:

| Type | Peer request | Peer data | Payload in `result.data` |
|---|---|---|---|
| 1 wireguard | `{public_key}` base64, 32 bytes | the 32 bytes | `{addrs: ["10.8.0.x/32", "…/128"], metadata: [{port, public_key}]}` |
| 2 v2ray | `{uuid}` 16-byte array or canonical string | `0x01` + 16 bytes | `{metadata: [inbound with tls_pin]}` |
| 4 xray | `{uuid}` 16-byte array | proxy byte + 16 bytes | `{metadata: [{port, proxy_protocol: 1, transport_protocol: 1, transport_security: 2 or 3, tls_pin (hex sha256 of the cert), flow?: 2 (xtls-rprx-vision), reality_server_name, reality_short_id, reality_public_key (32-byte x25519, base64), reality_fingerprint}]}`; clients refuse an all-cleartext list |
| 6 hysteria2 | `{uuid}` canonical string (accept a 16-byte array too) | the 16 bytes | `{metadata: [{port, tls_pin (the certificate's SHA-256 as colon-separated hex byte pairs, mandatory), obfs_password}]}`; the client authenticates with the uuid string |
| 5 amneziawg | `{public_key}` base64, 32 bytes, plus an optional `awg_version`: absent or 2 for the default tier, 3 for the AmneziaWG 3.1 tier | the 32 bytes | `{addrs, metadata: [{port, public_key, s1, s2, s3?, s4?, h1, h2, h3, h4, i1..i5?}]}`; junk-packet counts (Jc/Jmin/Jmax) are per side, S/H must match the server, I1–I5 are forwarded when present. With `awg_version: 3` the entry is the 3.1 interface's (its port and key, `s1..s4` all at least 12) and adds `awg_version: 3`, `header_protection_key` (base64, 32 bytes), `random_trailers` (the client sets the same) and `mtu` (1280) |
| 3 openvpn | `{uuid}` 16-byte array | the 16 bytes | `{metadata: [{port, protocol: "udp" or "tcp", ca: base64 DER, tls: base64 256-byte tls-crypt key}], cert: base64 DER client certificate, key: base64 DER PKCS#8}`; the host comes from the top-level `addrs` |

## Adding a protocol

A protocol is one package under `services/<name>/` that implements `types.Service` (the
data-plane methods plus `Name`, `ParsePeerRequest`, `HandshakePayload` and `Metadata`), and
one entry in the list in `services/registry.go` (name, type number, constructor, CLI subtree,
whether Handshake DNS may run next to it). Everything else goes through the registry: the
`[node] type` validation, the `dvpnd <name> config init|show|set` commands, the root
document's `service_type` and `service_metadata`, and both halves of the handshake.
`services/common/` has what protocols share: UUID parsing for peer requests, the TLS
certificate pin, a self-signed certificate generator, the generic config CLI built from a
`ConfigSpec`, a child-process helper (start, reap, SIGTERM then kill), a peer set, the
NAT and forwarding rule set for tunnel interfaces, the egress policy with its tunnel
firewall, and what the tunnel protocols share from the host: uplink detection, the
forwarding switches and the check every interface and uplink name passes.

Outside the code, a protocol also needs a `scripts/runner.sh` branch, its binary in the
`Dockerfile`, and its sections in `docs/operator.md` (§2x, §5, §6, §7).

Every service ships with unit tests that never touch the network (fake binaries on `PATH`,
loopback fakes for control channels, no root), the binary pinned with a checksum in the
image, and a real session from a client, described in words only in the README.

## Per-protocol design

### WireGuard (shipped)

`wg-quick` brings `wg0` up from a rendered config; PostUp adds the FORWARD accept rules (both
directions) and MASQUERADE of the tunnel's own networks on the uplink, each rule only where it
is not already, from the same rule set OpenVPN installs itself (`services/common/nat.go`); peers via `wg set … allowed-ips …`; usage from
`wg show … transfer`. IPv4 and optional IPv6 pools hand out tunnel addresses. The egress
chains go in before `wg-quick up` and come out after `wg-quick down`; they sit ahead of the
PostUp accept rules, which stay as they are.

### V2Ray (shipped)

`v2ray run --config <json>` as a child process; VMess inbound on `listen_port`, optional TLS
with the node's certificate (pin advertised); peers and usage over the gRPC control API on
loopback, on `[api] port` in `v2ray.toml` (it was fixed at 23 before the egress policy, and
a port below 1024 needs root). Routing sends the egress policy to a blackhole with
`domainStrategy: IPIfNonMatch`. VMess accepts AEAD headers only, v2ray's default: clients
use `alterId: 0` on a v2ray or xray core, which sends them (checked with Katacomb's settings
against v2ray 5.47).

### XRAY (shipped)

Generalised from V2Ray: the same JSON shape with a VLESS inbound over raw TCP,
`security = "tls"` (node certificate, pin advertised) or `"reality"` (x25519 key pair and
short id generated at `config init`, `server_name` chosen by the operator, default
`www.apple.com`), XTLS Vision on by default, control over xray's gRPC API on a configurable
loopback port. The egress policy is routed to a blackhole by explicit CIDRs (not
`geoip:private`, which needs the asset file) with `domainStrategy: IPIfNonMatch`, so a
destination given as a name is matched by its addresses as well and a client cannot reach
the control port.

The four API messages the node sends (`AlterInbound` with `AddUserOperation` /
`RemoveUserOperation`, `QueryStats`) are encoded by hand with `protowire` in
`services/xray/wire.go`: xray-core and v2ray-core register the same proto file paths, so
both cannot be linked into one binary. xray-core stays a test-only dependency whose real
generated code decodes what the node encodes. Binary: Xray release 26.3.27, sha256 checked
in the Dockerfile.

Found while testing against the real binary: REALITY with `www.microsoft.com` as the
imitated site fails the handshake with xray 26.3.27 (the site's certificate chain is too
large for the handshake replay); `www.apple.com` and `www.cloudflare.com` work.

### Hysteria2 (shipped)

`hysteria server -c <yaml>`: one UDP port, TLS with the node's certificate (the pin is
mandatory for clients), optional Salamander obfuscation password. The node serves the
server's HTTP authentication hook on loopback and answers `{ok, id}` for a registered peer,
where the password is the peer's canonical UUID and the id is the session key, so the
statistics API files traffic under the session key directly. Usage comes from
`GET /traffic?clear=1` (deltas, accumulated by the node; the server counts from the
client's point of view: `tx` is the client's upload, `rx` its download, checked with a large
download), read after a plain `GET /traffic` whose reading counts if the clearing reply
arrives broken. Removal is `POST /kick`; the authentication hook refuses the peer at once,
but the node keeps it, and counts it, until the kick succeeds. The node raises
`net.core.rmem_max`/`wmem_max` on a host (not namespaced, so in Docker it is a host
setting). The egress policy is an inline ACL of
`reject(...)` rules (CIDRs, not `geoip:private`, which makes hysteria download a database);
hysteria resolves a name before it matches, and reuses that address to dial. Binary: release
app/v2.10.0, sha256 checked in the Dockerfile. Port hopping is out of scope.

### AmneziaWG (shipped)

The WireGuard service parameterised by a `Variant` (tool names `awg`/`awg-quick`, interface
`awg0`, configuration directory `/etc/amnezia/amneziawg`) plus extra `[Interface]` lines;
`services/amneziawg` reads `amneziawg.toml` (WireGuard's keys, an `[obfuscation]` section
and a `[v3]` section, generated at `config init`), hands each tier's WireGuard part and
parameter lines to a WireGuard core of its own, and adds `s1`–`s4`, `h1`–`h4` and `i1`–`i5`
to the handshake metadata entry. Junk packet counts (`jc`, `jmin`, `jmax`) are per side and
not sent; the node sends few and small ones. Validation mirrors what clients check: paddings
within a datagram, `s1 + 56 != s2`, headers all distinct and above 4 (or all zero). The
public listing carries one blank entry per tier, with its `awg_version`.

Tunnel subnets are per node, not WireGuard's fixed `10.8.0.0/24`: a node whose AmneziaWG
clients all got `10.8.0.x` failed the network's health probe (which fetches the root
document, sends the handshake and opens no tunnel), and passed once the same reply carried
an address from another subnet; nodes on the network hand out addresses from subnets of
their own. Each tier derives a `10.x.y.0/24` (x from 11 to 250, so 10.0–10.10 stay clear)
and a unique local `/120` from SHA-256 of a fixed label and its public key, so they survive
restarts; `ipv4_subnet` and `ipv6_subnet` override them. A derived 3.1 subnet that meets the
default tier's moves to the next `/24`.

**Two tiers, one engine.** AmneziaWG versions differ on the wire: 1.0 replaced the four
message type values and prefixed the two handshake messages with junk, 1.5 added the
signature packets, 2.0 added prefixes on cookie and transport packets and header ranges, and
3.x (engine tags v3.0.0 and v3.1.x) added header protection (a 32-byte interface-wide key
that encrypts the type field and header of every packet, which needs prefixes of at least
12 bytes), random trailers, content padding and randomised timers. None of it is negotiated
in-band: with a key set the server drops an unprotected handshake as an unknown message,
and a 2.0 receiver drops a packet with trailers as the wrong size. Every 3.x key is optional
in the engine, and with all of them unset the 3.1 engine's send and receive paths are the
2.0 ones byte for byte (read in amneziawg-go at tags v0.2.19 and v3.1.20260828). So the
node runs one engine and offers two parameter tiers on two interfaces:

- The default tier (`awg0`, `[obfuscation]`): single-value headers, `s3` and `s4` at 0
  (they cost tunnel MTU), optional signature packets. Every client engine from AmneziaWG
  1.0 up accepts it, current apps get it without asking, and it never changes: the apps
  must keep working against every node on the network, whichever software runs it.
- The 3.1 tier (`awg1`, `[v3]`: its own port, keys and tunnel subnets): header protection, `s1`–`s4` of at least 12, random trailers, a
  little content padding and MTU 1280 (Amnezia's recommendation for 3.1). Only a client
  that sends `awg_version: 3` in its peer request lands on it, and the tunnel address a
  peer was assigned says which tier it is on. The extra keys are named after the engine's
  own configuration keys.

Header protection is a per-interface setting, which is why the tiers cannot share an
interface, and also why an app needs changes to *use* 3.1 (a 3.x engine, the extra keys,
the request field) but none to keep working. A node upgraded with a file that has no
`[v3]` section runs the default tier only; `config init --force` writes both. Should the
network's other node software ship a 3.1 contract of its own, its shape is learned only
from traffic captured with a client we run.

Versions: amneziawg-go v3.1.20260828 and amneziawg-tools v3.1.20260812, built from source
in the image at the commits those tags name (the Dockerfile checks); on a host the operator
installs the tools and either the DKMS kernel module (v3.1.20260906 or later) or
amneziawg-go. In Docker only the userspace implementation is possible (`--device
/dev/net/tun`, no SYS_MODULE). The client apps bundle the 2.0 engine (v0.2.19); the default
tier is what they are checked against.

`tools/awgcheck/check.sh` is that check: it builds the node image from the tree and two
client images (the engine the apps bundle, and the 3.1 engine), brings the node up with
both tiers without a chain (`tools/awgcheck/main.go` drives the service the way a node
does), and runs each client on each tier. The apps' engine must tunnel on the default tier
and fail to parse the 3.1 tier's configuration; the 3.1 engine must tunnel on both. Each
client pings the node's tunnel address and fetches over HTTPS through it, and the node's
per-peer counters are printed alongside.

### OpenVPN (shipped)

The node is its own certificate authority (`services/openvpn/pki.go`): an ECDSA P-256 CA,
server certificate and a 2048-bit tls-crypt key created on the first start and kept under
`<home>/openvpn/`; every `AddPeer` issues a client certificate (common name = the peer's
UUID) and PKCS#8 key, returned in the handshake with the CA and the tls-crypt key in the
shape the client apps assemble a profile from. The server configuration matches the
profile's pins (ECDSA TLS cipher, AES-GCM data channel, SHA256, `remote-cert-tls`).

Admission is `management-client-auth` with `auth-user-pass-optional`: the node keeps one
connection to the management interface (`services/openvpn/management.go`), answers every
`>CLIENT:CONNECT` with `client-auth-nt` for a registered common name or `client-deny`
otherwise, reads per-client bytes from `status 3` (received = the client's upload, sent =
its download), banks a connection's final counters from `>CLIENT:DISCONNECT` so a reconnect
does not lose usage (by client id, so a connection that ends while a pass reads the table
counts once), and removes a peer with `client-kill <id> RESTART,…`, which makes the
client reconnect at once and be denied; the peer is denied from the start of its removal,
but kept, and counted, until the kill succeeds. NAT and forwarding use the same rule set as
WireGuard (`services/common/nat.go`), installed by the node around the server's lifetime,
with the egress chains installed before the server starts and removed after it ends.
Without `client-to-client`, traffic between clients goes through the kernel and the egress
chain drops it.

Binary: `openvpn` from apk (2.7 in the image) and apt. The management port is a loopback TCP
port without a password, as OpenVPN warns at start: on a dedicated node the only other user
of loopback is root already.

## Packaging policy

One image. Each protocol's binary is added when the protocol lands, pinned to a release and
checked against a sha256, like the hnsd commit check in the Dockerfile. Server versions
follow the versions the maintainer's client bundles, so a node and a client are tested
against the same code. On a host the operator installs the same binary; §6 of the operator
guide lists each one.
