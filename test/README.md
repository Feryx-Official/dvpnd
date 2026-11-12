# Regression suite

The gates every change goes through. They state what dvpnd must keep doing; a
refactor or an update that breaks one of them is a regression, whatever else
it improves. The rules themselves are written down in `docs/invariants/`, each
with an ID, and every one is pinned by a test or listed with the reason it is
not yet.

| Gate | Command | Needs | Time |
|---|---|---|---|
| Fast | `make check` | Go, Python 3 | about two minutes |
| Canaries | `make check-canaries` | Go, Python 3 | seconds once the build cache is warm; two minutes the first time |
| Both | `make verify` | | what every commit passes |
| Integration | `make check-integration` | Go, Docker, the internet | about ten minutes, most of it the first image build |

`make check-all` runs all of them.

## When to run what

- **Every change** passes `make verify` before it is committed.
- **A change to a service, the egress policy, the runtime directory or the
  proxy account, the Dockerfile, `scripts/runner.sh` or `scripts/dvpnd.service`**
  also passes `make check-integration`, as does every release.
- **A bug fix comes with a test** that fails without the fix: a unit test where
  the bug can be shown without a daemon, otherwise a case in the integration
  suite. **A new behaviour comes with the test that pins it.** The suite grows
  with every change, and a test is never weakened to let a change through: if
  the behaviour is meant to change, the test changes in the same commit, and
  the commit says why.
- **A refactor that turns a test red is wrong** until shown otherwise.

## Fast gate: `make check`

- the rule registry (below);
- `gofmt`, `go vet` (the integration package included, so it always compiles);
- every unit test with the race detector, in shuffled order. Unit tests never
  touch the network, the chain or the host's firewall: iptables, the daemons'
  control APIs and the chain are faked;
- `main_test.go`, which runs dvpnd itself in a child process: a command that
  fails exits non-zero, and `config set` refuses a key its file does not have
  but takes every key the installer and the Docker runner set;
- `test/static/`: the rules about the repository itself and the shape of code
  a behaviour test cannot reach, read from the tree and git: the SPDX header
  first in every Go file, no `sentinel-go-sdk` in `go.mod`, `go.sum` or any
  import, the notice on every upstream file changed in substance, `LICENSE` as
  the fork point had it, the provenance script passing offline, no wallet or
  node address in any committed file, every dependency's licence (read from the
  module cache with the network off; CI checks it again with go-licenses), no
  link to the account's former name, every protocol in the integration suite,
  the start command stopping the service on every exit, the session database
  opened in one place (`node.OpenDatabase`), the RPC remotes looped over in
  one place (`lite`'s `eachRemote`), the node's jobs run by one loop
  (`runJob`) and every `apt-get` of the installer run by `apt_get`, so their
  tests cover every caller (the installer's wrapper runs against a fake
  `apt-get` that reports the lock taken), the installer's `safe.directory`
  entry added only by `trust_source`, which runs twice against a git
  configuration of the test's own and must leave one entry, the
  installer's `verify_tag` and `fetch_source`, which run against repositories
  and signing keys of the test's own, its `ssh_ports`, `write_config`,
  `write_protocol` and `create_key`, which run against a fake `sshd` and a
  fake `dvpnd`, and its downloads, each in a directory of its own and checked
  against a checksum the script pins; and the Docker runner's `setup`,
  `start`, `stop`, `remove`, `attach` and `update`, which run against fake
  tools, its flags held to the operator guide's `docker run` commands;
- the same SPDX and `sentinel-go-sdk` checks as shell steps, and
  `docs/provenance/verify-fork.sh`, which also asks apache.org,
  proxy.golang.org and Software Heritage when it can reach them (an
  unreachable one is reported, not failed).

`test/static/` needs the git history and the `fork-point` tag: a shallow clone
fails it, and says to fetch the tags.

CI runs the same checks, and the canaries, on every push and pull request
(`.github/workflows/ci.yml`).

## Rules and the registry

`docs/invariants/` holds the rules, one file per area, each rule a bold
sentence with an ID and the reason it exists:

| Prefix | File | Area |
|---|---|---|
| `EG` | `egress.md` | what a client can reach through the node |
| `HS` | `handshake.md` | who gets a peer, and what the node answers |
| `SL` | `sessions.md` | sessions and usage: what the node is paid on |
| `PV` | `privacy.md` | what the node keeps about clients and the operator |
| `CH` | `chain.md` | how the node talks to the chain and signs |
| `RT` | `runtime.md` | how the node runs the daemons, and what it leaves behind |
| `CT` | `contract.md` | what client apps and aggregators read from the node |
| `LIC` | `provenance.md` | what keeps the fork's legal position answerable |

A test pins a rule by carrying its ID in the comment directly above
`func TestX` (`// Rules: [SL-2].`) or in a literal `t.Run` name. A test that
calls `t.Skip` may not run, so it cites the rule without pinning it; a test in
`test/integration/` (build tag `integration`) pins it in the integration tier.

`invariants/registry.py` runs first in `make check` and fails when:

- a rule has no test that pins it and no entry in `invariants/status.json`;
- an ID is defined twice, outside its prefix's file, or cited but not defined;
- a top-level bold line in a rule doc carries no ID;
- the scan finds fewer test files, rules or pinned rules than the minimums in
  `invariants/config.json` (a broken glob must not pass everything).

`status.json` lists what is not pinned yet: `pending` (no test; the reason
says what will pin it), `partial` (pinned, with the part that is not) and
`manual` (checkable only by hand or against a live system). Each entry needs
a reason. `python3 invariants/registry.py --report` prints every rule and what
pins it.

IDs are never renumbered or reused: tests, canaries and commit messages point
at them. A new rule takes the next free number in its file.

## Canaries: `make check-canaries`

A canary is one deliberate regression: `invariants/canaries.json` names a file,
an exact piece of it to replace (it must occur once), and the test command
that must fail. `invariants/canaries.py` applies each one to a copy of the tree
(uncommitted changes included) and requires the run to fail with `--- FAIL`,
or the canary's own `expect`, so a mutation that only breaks the build does
not count. The same commands run once on a copy without the mutation and must
pass there. The copy is a git repository with no commit, so a test that lists
the tree with git runs as it does in the checkout. A canary that survives
means nothing guards its rule any more.

- `caught`: the rule's test failed, as it must;
- `SURVIVED`: every test passed with the rule broken;
- `RE-AIM`: the code moved and the anchor no longer matches once. Re-aim it at
  the moved code; never delete it;
- `WRONG RED`: the command failed without a test failing (a build error);
- `RED BEFORE`: the command fails without the mutation too, so its failure
  proves nothing. Make it pass on a clean copy; never delete the canary.

The copies build with `-trimpath`, so they share the Go build cache and only
the mutated package is compiled again. Every money, root and privacy rule has
at least one canary, except a rule whose test is skipped as a known bug: its
canary comes with the fix. `python3 invariants/canaries.py --only SL-2` runs
the canaries whose name contains `SL-2`.

## Integration gate: `make check-integration`

`test/integration/run.sh` builds the node image from this tree and runs the
Go tests in `test/integration/` (build tag `integration`) inside containers.
Two Docker networks stand for the internet (`203.0.113.0/24`) and the
provider's private network (`10.99.0.0/24`, internal); a target container
sits on both with a service on port 18080 and a mail relay on port 25. Every
protocol is driven the way the node drives it: the service writes the
daemon's configuration and starts it, a peer is added through the handshake
path, and a real client is configured from the handshake payload alone.

For every protocol, under the default policy and again with `allow_smtp`:

- the client reaches the internet, by address and by name, and the node API;
- it does not reach the private network, the host's other services on any
  of its addresses or names (loopback, `localhost`, a name the host resolves
  to loopback, the host's public address), the daemon's control API, or a mail
  relay unless mail is allowed;
- the node counts the client's traffic (what the node bills on);
- once the peer is removed the client reaches nothing;
- once the service stops, no daemon and no firewall rule is left.

The tunnel tests then take the node's chains out of the path and check that
the same client reaches the private network and the host: the blocks come from
the node, not from a network that would have dropped the traffic anyway.

| Stage | What runs | How |
|---|---|---|
| `proxies` | V2Ray, XRAY (REALITY and TLS), Hysteria2; the proxy account's kernel chain on its own; the tunnel chains in the real iptables | a container with exactly the capabilities `scripts/runner.sh` gives a proxy node (read from the runner) |
| `tunnels` | WireGuard, AmneziaWG (default and 3.1 tier), OpenVPN, each with a client in a network namespace | a privileged container, with the runner's sysctls |
| `systemd` | XRAY, Hysteria2, the OpenVPN server dropping to `dvpnd-proxy` | `scripts/dvpnd.service` as shipped, in a Debian container running systemd; a drop-in replaces only `ExecStart` |

Run one stage with `test/integration/run.sh <stage>`. The daemons log to the
output; a failing case prints its client's log.

Not covered: legacy (non-nft) iptables, IPv6 through a tunnel, the Handshake
resolver, V2Ray under the unit (the image's V2Ray is Alpine's build), and
anything on the chain. The WireGuard stage uses the host kernel's in-tree
wireguard module, which the kernel loads on demand, as it does for any
WireGuard node in Docker.

## Adding a case

- A rule: the next free ID in its `docs/invariants/` file, a test with the ID
  above it, and a canary when it guards money, root or privacy.
- A proxy protocol: a `proxyCase` in `test/integration/proxies_test.go` (how
  the node's configuration is written, how a client is built from the
  handshake entry).
- A tunnel protocol or tier: a `tunnelCase` or `tunnelTier` in
  `test/integration/tunnels_test.go`.
- A destination clients must or must not reach: `clientView` in
  `test/integration/main_test.go`, which every protocol is checked against.
