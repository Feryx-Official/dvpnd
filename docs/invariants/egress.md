# Egress: what a client can reach through the node

Anyone can buy a session, so clients are untrusted. These rules hold for all six
protocols. Each rule has an ID that tests, canaries and `invariants/status.json` cite;
IDs are never renumbered or reused. `test/README.md` explains how the registry checks
them.

- **[EG-1] One egress policy, rendered by every protocol.** `services/common/egress.go`
  holds the blocked IPv4 and IPv6 networks, `localhost` by name and TCP port 25; the
  proxies render it into their daemon's configuration and the tunnel types install it as
  firewall chains. A protocol with a list of its own drifts: before 9.4.0 each proxy
  blocked something different and the tunnels blocked nothing.
- **[EG-2] Clients never reach the host's or the provider's networks.** Blocked: "this
  network" and loopback (the host's own services and the proxies' control APIs), the
  private, shared (100.64.0.0/10) and link-local ranges (the provider's metadata service
  at 169.254.169.254), benchmarking, multicast and reserved space; for IPv6 `::`, `::1`,
  `fc00::/7`, `fe80::/10` and `ff00::/8`.
- **[EG-3] No IPv4 address falls into an IPv6 entry.** Never add `::ffff:0:0/96`: Go's
  `net.IPNet`, which Hysteria's ACL matches with, treats every IPv4 address as inside it,
  so the whole internet would be blocked. Public destinations must stay reachable.
- **[EG-4] TCP port 25 is blocked unless the operator sets `[egress] allow_smtp`.** A
  node that lets anyone send mail from its address ends up on block lists. A
  `config.toml` written before `[egress]` existed keeps port 25 blocked.
- **[EG-5] The proxies match a destination given as a name by its resolved addresses,
  and block `localhost` by name.** Otherwise a client asks for a name that resolves to
  loopback or a private address and reaches what the address rules block. V2Ray and XRAY
  route with `domainStrategy: IPIfNonMatch` and explicit CIDRs (not `geoip:private`,
  which needs an asset file); Hysteria resolves before it matches.
- **[EG-6] The kernel holds the proxy account to the policy as well.** The proxies
  resolve a name once to match it and again to dial it, so a name whose answer changes
  in between (DNS rebinding) could slip through. The `DVPND-PROXY` chain on OUTPUT for
  the `dvpnd-proxy` account accepts replies, DNS to the host's resolvers (matched on the
  original destination, because Docker answers 127.0.0.11:53 through a DNAT), the
  loopback ports the daemon needs (Hysteria's auth hook) and the node API port on the
  host's public addresses only, never on loopback. It rejects every other address of the
  host, the blocked networks and port 25 unless allowed.
- **[EG-7] A tunnel's egress chains are in place before its interface carries traffic,
  and come out only after it is down.** A peer packet must never pass unfiltered, not
  even while the interface comes up. A start that fails removes the chains it
  installed.
- **[EG-8] Tunnel clients reach neither each other nor the host, apart from the node API
  port and the Handshake resolver.** Forwarding back into the tunnel is dropped
  (client to client), and the input chain drops everything that isn't a reply, DNS to
  the resolver or the API port. Services bound to all of the host's addresses are
  otherwise reachable from the tunnel.
- **[EG-9] Installing the chains and the forwarding rules is idempotent.** A node that
  died without removing them starts again cleanly: an existing chain is flushed and
  refilled, and a jump, a forwarding rule or a masquerade rule is added only where it is
  not already (OpenVPN's NAT rules outlive a node killed without Stop, and each start
  added another copy; WireGuard's PostUp checks the same way). Chain names fit iptables' 28
  characters.
- **[EG-10] A rule the kernel refuses stops the start.** The node never serves clients
  behind a partly installed firewall; the error names the command that failed.
- **[EG-11] A kernel without IPv6 still runs a tunnel node.** Only iptables is used when
  `/proc/sys/net/ipv6` is absent; calling ip6tables there would fail the start.
- **[EG-12] End to end, every protocol keeps its clients to the internet and the node
  API.** With the real daemon and a real client, under the default policy and with mail
  allowed, a client reaches the internet by address and by name and the node API. It
  does not reach the private network, the host's services on any of its addresses or
  names, the daemon's control API, or a mail relay unless mail is allowed. The blocks
  must come from the node: a control step shows that the same destinations are reachable
  without the node's rules.
- **[EG-13] Every protocol in the registry is a case of the integration suite.** A
  protocol added to `services/registry.go` without one would ship with [EG-12] never
  checked for it.
- **[EG-14] The node masquerades only its tunnels' own networks.** The masquerade rule on
  the uplink matches the tunnel's subnet as its source. Without it the rule matched every
  connection leaving through the uplink, other networks the host routes included.
