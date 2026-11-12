# Sessions and usage: what the node is paid on

A session is paid on the chain; the node serves it and reports its usage with
`MsgUpdateSession`. Breaking these rules either costs the operator earnings (usage not
reported, a report refused) or gives clients service nobody pays for. IDs as in
`test/README.md`.

- **[SL-1] Every service reports each peer under the key the node stored:
  `base64(peer data)`.** The usage job looks rows up by that key; a peer reported under
  any other key is treated as unknown, and its usage is never reported.
- **[SL-2] The totals the node stores and reports are what the chain held when the peer
  was admitted plus what the service counted since.** A session outlives a node restart
  and the service's counters start again from zero, while the chain refuses a report
  lower than the last one. The reported duration is built the same way. A connection's
  bytes count once, also when it ends while a pass reads them, and a peer's counters
  start at zero when it is added, also when an earlier session used the same peer.
- **[SL-3] Usage that moved in either direction is stored.** A pass that sees only the
  download grow still stores it, reports it and checks the allocation.
- **[SL-4] Admission is checked and recorded under one lock, which the jobs share.**
  Concurrent handshakes cannot together exceed `max_peers`, and one session or one key
  is never admitted twice, not even by a retrying client or a replay.
- **[SL-5] A peer whose session row cannot be written is removed again.** A peer without
  its row would be served unmetered until the next pass.
- **[SL-6] Only an active session of this node, held by the signing account, is
  admitted.** The chain is asked for the session; a missing or inactive session, another
  account's or another node's is refused. A plan subscription must be active and the
  account must hold an allocation on it.
- **[SL-7] Admission enforces the session's byte cap and the plan allocation.** The
  allocation counts what this node has served on it but not yet reported, the account's
  connected peer up to the handshake included, so a client can't start session after
  session on usage the chain hasn't seen yet.
- **[SL-8] An account has one peer at a time.** A new handshake from the account evicts
  its earlier peer, whatever session either serves: a node session, or a plan session
  on the same subscription or another.
- **[SL-9] A connected peer the node holds no session for, or that has used its
  allocation, is removed.**
- **[SL-10] A session the chain has ended loses its peer, and its row once the chain
  marks it inactive.** A session the chain cannot be asked about waits for the next
  pass; the rest of the pass goes on.
- **[SL-11] One session the chain refuses does not hold back the other reports.** The
  chain fails a whole transaction for one bad message (a session deleted between the
  query and the send), so the batch is checked again and resent without it, and then
  one session at a time. During an RPC outage the batch waits for the next pass instead.
- **[SL-12] A failed job pass never stops the node.** An RPC outage or a daemon that
  doesn't answer is logged and retried at the next tick; stopping would cut every
  client's tunnel and restart the node into the same outage. A job that panics hands an
  error to the node, which then stops the service cleanly (see [RT-2]).
- **[SL-13] The node reports at least as often as the chain requires.** The chain
  deactivates a node, and ends a session, whose last update is older than its
  `status_timeout`; whatever the operator configured, the update intervals never exceed
  80% of it.
- **[SL-14] A restart doesn't strand clients.** Session rows left by the previous run
  point at peers that no longer exist and would make a reconnect fail with 409. At start
  the node reports what those sessions moved that the chain doesn't have yet (if they
  are still active), then clears the table.
- **[SL-15] A usage update writes both columns, zero included.** A struct update skips
  zero values, which would leave an old figure in place.
- **[SL-16] A removed peer is cut off at once and can't come back.** OpenVPN kills the
  client's connection and denies its reconnect; Hysteria kicks it; WireGuard and the
  other proxies drop the peer. When the kill or the kick fails, the peer is refused at
  its next connection but stays registered, and its open connection counted, until a
  later pass cuts it off.
- **[SL-17] An hourly session's peer is cut off when the hours it paid for are used.**
  The chain pays such a session for the duration the node reports, up to the hours the
  client deposited for, so time served beyond them is never paid. Admission refuses a
  session that has used them and records them; the usage pass removes the peer once
  they are served, also while it idles, and the pass that asks the chain removes it
  too, for a row written before the node recorded them. The usage of a peer cut off is
  still reported.
- **[SL-18] What a peer moved is stored before the node lets it go.** A handshake that
  evicts an account's earlier peer first stores what that peer moved since the last
  pass, and the node runs one more usage pass before it stops the service; the next
  pass, or the next start, reports it. The usage pass reads the counters under the
  admission lock, so figures read before an eviction never overwrite what it stored.
