# Security policy

## Reporting a vulnerability

Please do **not** open a public issue for security problems.

Use GitHub's private vulnerability reporting on this repository
("Security" tab → "Report a vulnerability"). Reports reach the maintainer
(@proofoftrinity) directly and stay private until a fix is available.

Include what you can: affected version or commit, environment, reproduction
steps, and impact. You will get an acknowledgement within a few days.

## Supported versions

Only the `main` branch and the latest tagged release receive fixes. Release tags
are SSH-signed by the maintainer, and the installer builds only a tag whose
signature verifies. From 9.4.0, release images on ghcr.io
are signed keylessly by the release workflow and carry build provenance and an
SBOM (`docs/operator.md`, section 7).

## Scope

dvpnd drives six VPN protocols: WireGuard, AmneziaWG and OpenVPN (a tunnel
interface with NAT), and V2Ray, XRAY and Hysteria2 (a proxy daemon on a port).
It runs those daemons and talks to a public blockchain. Vulnerabilities in the
daemons themselves belong to their projects; how this node configures, starts or
drives them belongs here.

## Threat model

**Clients are untrusted.** Anyone can buy a session. What a client can reach
through the node is held to one egress policy for every protocol
(`services/common/egress.go`): the internet, but not the node host, its
loopback, private, shared or link-local networks (the provider's metadata
service included), other clients, or TCP port 25 unless the operator allows
mail. The proxies apply it themselves and, running as the unprivileged
`dvpnd-proxy` account, are held to it by an iptables chain for that account;
the tunnel types apply it with iptables chains on their interface.

**The protocol daemons are not trusted with the host.** They parse hostile
traffic, so they run as `dvpnd-proxy` (OpenVPN drops to it once its tunnel is
up), with their configuration in `/run/dvpnd` and no access to the node's home
or keys. The node itself runs as root under a systemd unit that limits its
capabilities, system calls and writable paths.

**Handshakes are authenticated both ways.** A client proves it holds the
session's account key; the node signs its reply (`X-Dvpnd-Signature`,
`docs/protocols.md`), because the TLS certificate is self-signed and nothing on
the chain vouches for it. Clients pin the certificate per session. The legacy
handshake endpoint, whose signature does not cover the peer key, is off unless
the operator turns it on. Handshakes are HTTPS only and rate-limited per
address.

**The chain RPC is trusted.** The node admits sessions and reports usage on
what its RPC endpoints answer, without light-client proofs. Endpoints must be
https unless they are on loopback; operators who can should run their own.

**The signing key is a hot key, or should be.** The node must sign
transactions unattended, so its key sits unencrypted on the server. With
`[keyring] granter` it is a hot key that may only send the node's messages and
have their fees paid (authz and feegrant); the node account, which receives the
earnings, stays in a wallet off the server. Without it, a break-in takes the
node account.

**Logs keep no client addresses** at the default level, and the session
database zeroes deleted rows.

## Known dependency advisories

CI runs govulncheck on every push and pull request (`scripts/govulncheck.sh`)
and fails on any vulnerability the code reaches that is not explained here.

govulncheck reports GO-2026-5932, `golang.org/x/crypto/openpgp`. The fix it
names, an untagged `x/crypto` commit, deletes the `openpgp` packages, which
cosmos-sdk's `crypto/armor.go` (key export and import) imports, so the node
cannot build with it. Only the armor and errors packages are linked, and they
are reached at package initialisation. Leaving it needs a cosmos-sdk newer
than the v0.47 line the Sentinel chain (sentinelhub v12) is built on.
