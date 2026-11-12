# Client contract: what client apps and aggregators read from the node

Client apps on the network work against every node, whichever software runs it.
`docs/protocols.md` is the contract in full; these rules are the parts whose breaking
leaves users unable to connect, or shows them a wrong fact. IDs as in `test/README.md`.

- **[CT-1] The protocol numbers and names are the ones the network's clients use.** The
  number is what `GET /status` reports and aggregators key on; the name is the root
  document's `service_type`. Every registry entry agrees with the service it builds.
- **[CT-2] The root document has the layout nodes on the network answer with.** `addr`,
  `uplink`, `downlink` (bytes per second, as strings), `handshake_dns`, `location`,
  `moniker`, `peers`, `service_type`, `service_metadata` and `version{name, tag,
  commit}`; every response carries `Server: dvpnd/<version>`. The legacy status document
  keeps its own fields and says where its bandwidth figure came from.
- **[CT-3] `service_metadata` lists the keys of the handshake entry with everything
  per-session or secret blank.** Per type, as the network publishes it.
- **[CT-4] Each protocol's handshake payload has exactly the shape client apps assemble
  a configuration from.** The per-type table in `docs/protocols.md`: addresses with
  their prefix, the port and key, the TLS pin (colon-separated hex for Hysteria2, plain
  hex for XRAY), the REALITY keys, OpenVPN's CA, tls-crypt key and client certificate.
- **[CT-5] A peer request is accepted in every form clients send.** A UUID as a
  canonical string or as a 16-byte array; a WireGuard key as base64 of 32 bytes.
- **[CT-6] The first two bytes of `Info()` are the listen port, big-endian.** The
  legacy endpoint returns it raw to old clients.
- **[CT-7] AmneziaWG's default tier never changes, and only a client that asks with
  `awg_version: 3` gets the 3.1 tier.** Current apps bundle the 2.0 engine and must
  keep working against every node. The default tier keeps single-value headers and `s3`
  and `s4` at 0; a node without a `[v3]` section runs the default tier only.
- **[CT-8] AmneziaWG tunnel subnets are the node's own, derived from each tier's key.**
  A node whose clients all got `10.8.0.x` failed the network's health probe. The subnets
  survive restarts, keep clear of 10.0–10.10, never overlap between tiers, and an
  operator's `ipv4_subnet`/`ipv6_subnet` wins.
- **[CT-9] No two peers hold the same tunnel address, and the node's own address is
  never handed out.** Two clients on one address would see each other's traffic. A full
  pool refuses rather than wrapping around. Nor is a network or broadcast address handed
  out, whatever the subnet's size: a custom AmneziaWG subnet smaller than a /24 handed out
  its broadcast address. A peer added again keeps the addresses it holds.
- **[CT-10] The XRAY control messages the node encodes by hand decode with xray-core's
  own generated code.** xray-core and v2ray-core can't be linked into one binary, so the
  four messages are written with protowire.
- **[CT-11] The advertised bandwidth never overstates the link.** A server inside the
  host's own facility reports the local network's throughput, so it is never measured
  (a latency floor on the minimum round trip); independent servers are combined by
  taking the lowest; a failed sample is no figure. An operator's declared link wins
  without probing.
- **[CT-12] A restart doesn't lose the advertised bandwidth.** The last measurement for
  the same address is reused, and after a failed measurement an old one, however old;
  a corrupt or foreign cache is measured again.
- **[CT-13] The location comes from free, commercially usable sources, in a fixed
  order.** ip-api.com is used only when the operator chooses it (its free tier is
  non-commercial); each source is asked at most once per start; configured fields win.
- **[CT-14] The address a GeoIP source returns becomes the node's address only when it
  is a public IPv4 address.** Clients are sent it in the handshake reply. A source that
  answers with no address, an IPv6 one or one no client can reach counts as failed: `auto`
  asks the next source, a chosen provider fails the start. An empty or IPv6 answer once
  reached clients as `<nil>`.
- **[CT-15] V2Ray serves VMess over TCP only.** TCP is the one transport client apps are
  confirmed to use; any other `transport` stops the start, naming `tcp`. The inherited
  table refused `http`, took the typo `httpt` instead, and passed six transports nothing
  had tested, each advertised under a code no client was checked against.
