# Chain and keys: how the node talks to the chain and signs

The node admits sessions and reports usage on what its RPC endpoints answer, and it
signs transactions unattended, so its key lives on the server. IDs as in
`test/README.md`.

- **[CH-1] RPC endpoints must be https, unless they are on loopback.** The node trusts
  what its RPC answers, without light-client proofs; plain http lets anyone on the path
  answer instead.
- **[CH-2] With `[keyring] granter`, every transaction is one MsgExec from the hot key,
  with the granter paying the fee.** The node account, which receives the earnings,
  then stays in a wallet off the server, and a break-in takes only a key that can send
  the node's messages.
- **[CH-3] The granter is a valid address and never the signing key itself.**
- **[CH-4] A hot-key node refuses to start without its grants, and warns before they run
  out.** Every transaction would fail; the error names the grants to make. A grant or
  allowance expiring within the warning window is logged at every start.
- **[CH-5] The chain's "authorization not found" answer means no grant, not a fault.**
  The chain answers a Grants query for a missing grant with an error of code Unknown;
  read as an RPC failure, it hid the grant the operator had to make (found in the mainnet
  test of 9.4.0).
- **[CH-6] The message types the node asks grants for are exactly the ones it sends.**
  `dvpnd keys authz-commands` prints them; a grant for the wrong type URL is a grant the
  node can't use.
- **[CH-7] The fee allowance the node asks for covers what it spends.** The expected
  fees are a status update and a session report per interval, at the configured gas
  and price.
- **[CH-8] Every RPC remote is tried in turn, and each one's error is logged.** A remote
  that redirects or refuses must not stop the node while another one works, and the
  operator must see which ones failed.
- **[CH-9] The granter's fee allowance is found on whichever page the chain lists it.**
  Anyone can grant an allowance to any address. Read from the first page only, a hundred
  dust grants that sort before the granter's made a hot-key node refuse to start.
