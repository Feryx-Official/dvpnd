# Privacy: what the node keeps about clients and the operator

The public chain already shows a session's wallet address next to the node. What it
doesn't show (where a client connects from, what it reaches, its peer keys and
passwords) must not pile up on the node either. IDs as in `test/README.md`.

- **[PV-1] At the default log level the node logs no client IP address.** The address a
  request came from goes only into the debug line every request gets. The web server's
  own messages (a failed TLS handshake names the remote address) go to the node's logger
  at debug, not to stderr.
- **[PV-2] The protocol daemons log per-connection detail only when the node runs at
  debug.** Client addresses and the destinations they reach appear at Hysteria's info
  level, OpenVPN's verb 3 and XRAY's warnings; V2Ray logs nothing.
- **[PV-3] Peer keys and session passwords appear in the log only as a short tag.**
- **[PV-4] A deleted session leaves no trace in the database file.** The database is
  opened with `secure_delete`, which zeroes deleted rows, and compacted (VACUUM) at
  start, because rows deleted by an earlier release can still sit in free pages with
  their wallet addresses and peer keys.
- **[PV-5] A recovered mnemonic is never printed back.** A new key's mnemonic is shown
  once, as its only backup; echoing a recovered one would leave it in the scrollback or
  in a log of the install.
- **[PV-6] The node's files are readable by their owner only, and a configured value
  stays one value.** Config files hold keys and passwords; a value passed to
  `config set` is escaped so it reads back as the same single value.
- **[PV-7] The configuration the node logs at start is redacted.**
- **[PV-8] The Handshake resolver is off by default; when on, it listens on the tunnel
  address only and keeps no log.** On every address it would be an open resolver anyone
  could use against third parties, and its log would be a record of every name clients
  looked up.
- **[PV-9] A node never serves VLESS without TLS or REALITY.** VLESS has no cipher of its
  own; without them the proxy hop between client and node is cleartext. The V2Ray type
  renders only a VMess inbound, and the XRAY type refuses to start with a
  `vless.security` other than `tls` or `reality`. `xray config set` refuses one too, so
  the value is refused when it is set rather than at the next start.
- **[PV-10] A protocol daemon gets only what it runs on.** Its environment is the node's
  PATH, locale, time zone and temporary directory, the daemons' own tuning variables and
  what its service adds, not whatever the operator or the container passed the node. The
  files the node writes for it (a configuration that can hold a REALITY private key or the
  stats secret, a copy of the TLS key) are removed when the node stops.
- **[PV-11] The GeoIP token is sent over HTTPS only.** A `url` override for a provider the
  token goes to (ipinfo, ip2location) must be `https` when a token is set; over `http` it
  would cross the network readable by anyone on the path.
