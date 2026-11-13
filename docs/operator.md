# Running a dvpnd node

What you need: a Linux host (Ubuntu 22.04/24.04 or Debian 12/13, x86_64; the installer also
takes aarch64, where an XRAY or Hysteria2 node needs its binary on `PATH` first, and the image
is x86_64 only) with a **public IPv4 address** (a home connection behind carrier-grade NAT
will not work — check that your router's WAN address is not in 100.64.0.0/10), root access,
and an account on the Sentinel chain holding a little P2P for gas (the coin formerly called
DVPN; on the chain its denomination is still `udvpn`). Registration deposit is currently 0.

This guide covers WireGuard, AmneziaWG, OpenVPN, V2Ray, XRAY and Hysteria2 nodes; the
protocol is chosen with `[node] type`. A WireGuard node has been run this way against the
public network with real clients, and every type but OpenVPN has served paid sessions on a
public node on the live network, the client side driven by `tools/e2e`. OpenVPN has served
its own client end to end between two containers, as every protocol does in the integration
suite (`test/README.md`).

## Which way to run it

Run the node on the host as a systemd service (§6), on a VPS that does nothing else. Use
Docker (§7) only when the host path does not fit: you want the bundled `v2ray` and `hnsd`
without installing them, you cannot put a Go toolchain on the host, or everything on that
host already runs in Docker.

The reason is where the protocol's data plane lives. A **WireGuard**, **AmneziaWG** or
**OpenVPN** node creates a tunnel interface on the host and NATs the peers' traffic out of
the uplink: on the host that is one
NAT hop with native IPv6; in a container it is two NAT hops, IPv6 needs a Docker daemon
change, and the container has to be given NET_ADMIN, SYS_MODULE and the host's kernel modules
anyway, so the isolation is nominal. A **V2Ray**, **XRAY** or **Hysteria2** node is a userspace
proxy on one port with no privileges at all: host and Docker are equal, and the host wins only
for having one way of running things across node types.

Whichever way: a dedicated machine with its own IP. The node runs as root, keeps an
unencrypted key, and routes strangers' traffic out of that IP, so abuse complaints land there.
Never on a validator host, never next to anything with secrets.

## 0. The short way: the installer

`scripts/install.sh` does sections 1 to 6 on a Debian or Ubuntu host: it installs Go and
the build tools, builds the latest release, writes the configuration and the protocol file,
creates or recovers the operator key, makes the TLS certificate, opens ufw and installs the
systemd unit. Read it first; then:

```sh
curl -fsSL https://raw.githubusercontent.com/Feryx-Official/dvpnd/main/scripts/install.sh -o install.sh
sudo bash install.sh --moniker "My node"          # --type amneziawg|openvpn|v2ray|xray|hysteria2, --help for the rest
```

With `--granter sent1…` it creates a hot key instead of the operator key and prints the
grants to make from the node account's wallet (§3a); on an existing node that is the move to
a hot key. It builds only a release tag whose SSH signature checks out against the maintainer's key
pinned in the script (the key GitHub shows as verified on the tags); to build anything else,
check the tree yourself and pass `--source`. It keeps an existing configuration, key and
certificate unless given `--force`, so re-running it is the upgrade path. On a machine behind a router it prints the ports to
forward. The sections below are what it does, for doing it by hand or understanding the
result.

The script above comes from the `main` branch, and the signature check lives inside it. To
run only what a signed release holds, check the tag yourself and run the installer from it
(git 2.34 or newer; the key is the one the script pins, fingerprint
`SHA256:HGWvk00cwVqxKXxfkwRIk/jUr3RiXz3WztzvWXKspq0`):

```sh
git clone https://github.com/Feryx-Official/dvpnd.git && cd dvpnd
tag=$(git tag --list 'v*' --sort=-v:refname | head -1)
echo '114076168+proofoftrinity@users.noreply.github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFqdfC3zNhLfD0Hl5H2bvlCvtBtOBhz0VSiietVWvMch' >allowed_signers
git -c gpg.ssh.allowedSignersFile=allowed_signers verify-tag "$tag" && git checkout --quiet "$tag"
sudo bash scripts/install.sh --source "$PWD" --moniker "My node"
```

`verify-tag` must print `Good "git" signature` with that fingerprint.

Releases after 9.4.4 also carry the installer itself, signed by the release workflow the
way the image is (§7). With [cosign](https://docs.sigstore.dev/cosign/system_config/installation/)
3 or later, check it before it runs:

```sh
base=https://github.com/Feryx-Official/dvpnd/releases/latest/download
curl -fsSLO "$base/install.sh" && curl -fsSLO "$base/install.sh.sigstore.json"
cosign verify-blob install.sh --bundle install.sh.sigstore.json \
  --certificate-identity-regexp '^https://github.com/Feryx-Official/dvpnd/\.github/workflows/docker-publish\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sudo bash install.sh --moniker "My node"
```

`verify-blob` must print `Verified OK`. The installer then builds the latest release,
checking its tag as above.

When the installer's output is not a terminal (cloud-init, `| tee`, a CI job), it does not print a new key's
mnemonic, which a log would keep: it writes it to `/root/.dvpnd/operator-mnemonic.txt`,
readable by root only. Copy it off the server, then delete the file (`shred -u`).
Turning ufw on, it opens every port `sshd -T` reports, so an SSH port set in
`/etc/ssh/sshd_config.d` stays reachable.

## 1. Build

Build on the host (cgo is needed for sqlite, so cross-compiling is awkward; there is no
binary release yet):

```sh
sudo apt-get update && sudo apt-get install -y git build-essential
# Go 1.26 or newer: https://go.dev/dl/  (apt's may be too old); the build fetches the
# toolchain go.mod names (Go 1.27.4), checked against Go's checksum database
git clone <this repository> dvpnd && cd dvpnd      # or copy the source tree over
make build && sudo install -m 0755 bin/dvpnd /usr/local/bin/dvpnd
dvpnd version
```

## 2. Configuration

```sh
dvpnd config init                     # writes ~/.dvpnd/config.toml (run as root: the node runs as root)
```

Edit `~/.dvpnd/config.toml`:

| Key | Set to |
|---|---|
| `[keyring] backend` | `test` — the node must sign transactions unattended, so the key is stored unencrypted under `~/.dvpnd/keyring-test` (mode 700). Prefer a **hot key** (`[keyring] granter`, §3a): the operator key and the earnings then stay off the server. Otherwise use a dedicated operator key and sweep earnings out regularly; see §8. |
| `[keyring] granter` | empty, or the node account's `sent1…` address when `from` is a hot key (§3a). |
| `[keyring] from` | the key name you will create in step 3, e.g. `operator` |
| `[node] type` | `wireguard`, `amneziawg`, `openvpn`, `v2ray`, `xray` or `hysteria2`; see §2a to §2f for the protocol's own file |
| `[node] moniker` | your node's public name (4–32 characters) |
| `[node] gigabyte_prices` | e.g. `40000000udvpn` (40 P2P per GB). Only denoms the chain lists in its node params are accepted; prices below the chain's minimums are rejected at registration. |
| `[node] hourly_prices` | e.g. `97500000udvpn` |
| `[node] remote_url` | `https://<public-ip>:8585` — this becomes the on-chain `remote_addrs` (`host:port`); clients connect to it directly |
| `[node] listen_on` | `0.0.0.0:8585` |
| `[node] legacy_handshake` | `false` (the default). `true` also serves the legacy handshake endpoint, `POST /accounts/<address>/sessions/<id>`, which only old clients use. Its signature covers the session id but not the peer key, so anyone who captures one request can replay it with a key of their own and cut the real client off. |
| `[handshake] enable` | `false` (the default) unless `hnsd` (Handshake DNS resolver) is installed, as in the Docker image; must be `false` on a proxy node (V2Ray, XRAY, Hysteria2). The resolver listens on the node's tunnel address only (e.g. `10.8.0.1:53` on WireGuard), so only connected clients reach it. Without `hnsd` the node logs that the resolver stays off and reports `handshake_dns: false`. With ufw, let clients reach it: `sudo ufw allow in on wg0 to any port 53` (`awg0`/`awg1`, `ovpn0` for those types) |
| `[egress] allow_smtp` | `false` (the default). Clients never reach the node host, private or link-local networks, or each other (§5); outgoing mail to TCP port 25 is blocked too, because a node that relays anyone's mail lands on block lists and providers suspend it. Set `true` only if you accept that. |
| `[geoip]` | Leave `provider = "auto"`: at start the node asks ipwho.is, then ip2location.io, for the location of its public IP and cross-checks the country against Cloudflare; a disagreement is logged. No key, no cost, and both services allow commercial use on their free tier. Check the result with `curl -sk https://127.0.0.1:8585/status \| jq .result.location` (`source` names the service that answered). The location a node reports is self-declared and nothing verifies it; clients use it to choose a node, so only if the lookup is wrong set `city`, `country` (name or ISO code), `latitude`, `longitude` to the server's real physical location. The node logs the contradiction and reports `source = "static"`. Do not use `ip-api` on a node that earns unless you pay for it: its free tier is non-commercial only (a paid key goes in `url`). `ipinfo` returns the country only on its free plan. |
| `[bandwidth]` | Leave both at `0`: at start the node measures its link. It picks speed test servers by measured latency, discards any that answer from inside its own datacenter (common on cloud hosts: those measure the local network and can overstate the link several times over), and reports the lowest of two or three independent servers. That takes one to two minutes and moves several gigabytes on a fast link; the result is kept in `~/.dvpnd/bandwidth.json` and reused for a week, or until the public IP changes (delete the file to measure again). If you know what your provider sells you, set `download_mbps` and `upload_mbps` (a 1 Gbit/s port is `1000`) and the measurement is skipped. See which you got with `curl -sk https://127.0.0.1:8585/status | jq .result.bandwidth`: `source` is `speedtest`, `config` or `none`. Nothing verifies the figure and clients use it to choose a node: do not overstate it. |
| `[chain] rpc_addresses` | comma-separated, tried in order; the defaults are public endpoints from the [chain registry](https://github.com/cosmos/chain-registry/blob/master/sentinel/chain.json). Put your own RPC first if you run one. Each must be `https`; plain `http` is accepted only on loopback (`http://127.0.0.1:26657` for an RPC on the same host), because the node admits clients on what the RPC answers. An endpoint that answers with an HTTP redirect does not work with this client. |

At start the node names, in an error line, any key in `config.toml` that no setting takes
(a misspelt key, or one left by the upstream node's file) and then ignores it; `config set`
refuses such a key outright. A GeoIP `url` must be `https` when a `token` is set.

Geolocation services: ipwho.is and Cloudflare require no attribution. dvpnd uses IP2Location.io
IP geolocation web service.

### 2a. WireGuard

```sh
dvpnd wireguard config init           # writes ~/.dvpnd/wireguard.toml
```

Pick a fixed `listen_port` (the default is random) and keep it — it is what clients are told
to connect to. `uplink` may stay empty; the interface of the default route is detected at
start and used for NAT. `interface` and `uplink` take up to 15 letters, digits, `_`, `.` or
`-`, starting with a letter or digit; the node refuses anything else. At every start the node
writes `/etc/wireguard/<interface>.conf` (AmneziaWG: `/etc/amnezia/amneziawg/`), marked as its
own; a file there that it did not write, or an interface of that name that does not hold the
node's key, is left alone and the node stops, so a host with a `wg0` of its own needs another
`interface`. `enable_ipv6` is `true`, as on every other node: the host must then
reach the IPv6 internet (check in §6; Docker needs extra steps, §7). Set it `false` for an
IPv4-only tunnel; clients then exit with the node's IPv4 address only. On a kernel booted
without IPv6 the node runs the tunnel IPv4-only whatever the setting, and logs a warning.

### 2b. V2Ray

```sh
dvpnd v2ray config init               # writes ~/.dvpnd/v2ray.toml
```

Pick a fixed `listen_port` (TCP). `transport` must be `"tcp"`, the only transport confirmed
with current client apps; the node refuses to start with any other. `tls = true` wraps the VMess inbound in TLS using the node's
`tls.crt`/`tls.key` from §4 and advertises the certificate's pin to clients; `false` relies
on VMess's own encryption. The node needs the `v2ray` binary on `PATH` (see §6) and drives it
over loopback `[api] port`, which `config init` picks at random; nothing else may bind it. A
`v2ray.toml` written before `[api]` existed gets a new random port at every start; fix one
with `dvpnd v2ray config set api.port <port>`. Clients cannot reach that port, or anything else
on the host, through the proxy (§5).

### 2c. XRAY

```sh
dvpnd xray config init                # writes ~/.dvpnd/xray.toml with a fresh REALITY key pair
```

One VLESS inbound on `[vless] listen_port` (TCP; pick a fixed one). `security` chooses how
it is wrapped:

- `tls` (default): the node's `tls.crt`/`tls.key` from §4; clients receive the certificate's
  pin in the handshake and connect to nothing else.
- `reality`: no certificate. The node imitates the TLS handshake of `[reality] server_name`,
  so to an observer the port looks like that site. The site must serve TLS 1.3 with HTTP/2
  on port 443 and answer with a small certificate chain: `www.apple.com` (the default) and
  `www.cloudflare.com` work with current xray, `www.microsoft.com` does not. The key pair
  and `short_id` are generated by `config init`; clients receive the public key, short id,
  server name and `fingerprint` in the handshake. Only change the keys with a fresh
  `config init --force`.

There is no third choice: VLESS has no cipher of its own, so without TLS or REALITY the
traffic would cross the network in cleartext. `xray config set` refuses any other value,
and the node refuses to start with one.

`flow = true` enables XTLS Vision, which current clients support and expect. The node drives
xray over loopback `[api] port`; nothing else may bind it. Clients cannot reach that port, or
anything else on the host, through the proxy (§5). The node needs the `xray` binary on `PATH`
(see §6).

### 2d. Hysteria2

```sh
dvpnd hysteria2 config init           # writes ~/.dvpnd/hysteria.toml
```

One QUIC listener on `[server] listen_port` (UDP; pick a fixed one), always wrapped in TLS
with the node's `tls.crt`/`tls.key` from §4; clients receive the certificate's pin in the
handshake and refuse to connect without it. `obfs_password` turns on Salamander obfuscation
with that password, which clients receive in the handshake; empty means none. `up`/`down`
cap what each client gets (e.g. `"100 mbps"`); empty lets the client choose. Clients
authenticate with their session's UUID: the server asks the node on loopback `[api]
auth_port` for every new connection, and the node reads usage from the server's statistics
API on `[api] stats_port`; nothing else may bind those ports. The node needs the `hysteria`
binary on `PATH` (see §6). On a host the node raises the kernel's UDP buffer limits
(`net.core.rmem_max`, `wmem_max`) at start, which QUIC wants; in Docker set them on the host.

### 2e. AmneziaWG

```sh
dvpnd amneziawg config init           # writes ~/.dvpnd/amneziawg.toml with fresh parameters for both tiers
```

WireGuard with obfuscation: the same keys as `wireguard.toml` (interface `awg0`, a fixed
`listen_port`, `uplink`, `enable_ipv6`, all as in §2a) plus an `[obfuscation]` section that
`config init` fills: junk packets the node sends before a handshake (`jc`, `jmin`, `jmax`;
each side has its own), padding on the handshake messages (`s1`, `s2`; `s3`, `s4` stay 0 so
the tunnel keeps its MTU), and four message type values replacing WireGuard's (`h1`–`h4`).
Clients receive `s1`–`s4`, `h1`–`h4` and any signature packets `i1`–`i5` in the handshake and
must match them. This default tier is what every client app speaks today.

Each tier hands its clients addresses from tunnel subnets of its own, which the node derives
from that tier's `private_key`: a `10.x.y.0/24` (x from 11 to 250) and an IPv6 unique local
`/120`, the same on every start and different from node to node. A node that handed every
client an address in `10.8.0.0/24`, the subnet most WireGuard servers use, failed the
network's health probe. Leave `ipv4_subnet` and `ipv6_subnet` empty unless a derived subnet
clashes with a network the host is on; then set a private network in CIDR form (IPv4 `/16`
to `/28`, IPv6 `/64` to `/124`). The two tiers' subnets must not overlap. A file written
before these settings existed gets the derived subnets.

The `[v3]` section is a second interface (`awg1`, its own `listen_port` and keys) speaking
AmneziaWG 3.1: header protection, random trailers, content padding, MTU 1280. Only a client
that asks for it in the handshake lands there; every other client gets the default tier,
unchanged. Set `enabled = false` to run the default tier only. A file written before the
section existed runs the default tier only; `config init --force` writes both tiers with
fresh parameters, which disconnects every client, as changing any parameter does. The node
needs `awg` and `awg-quick` on `PATH` and either the AmneziaWG kernel module or
`amneziawg-go` (see §6), of the 3.1 generation for the `[v3]` tier.

### 2f. OpenVPN

```sh
dvpnd openvpn config init             # writes ~/.dvpnd/openvpn.toml
```

Interface `ovpn0`, a fixed `listen_port`, `proto = "udp"` (recommended) or `"tcp"`,
`uplink` and `enable_ipv6` as in §2a. The node admits clients and reads their traffic over
the server's management interface, a unix socket in `/run/dvpnd` that only root may use; a
`[management] port` left in a file from an older version is ignored. The node is its
own certificate authority: on the first start it creates a CA, a server certificate and a
tls-crypt key under `~/.dvpnd/openvpn/` (keep them: they persist across restarts, and a
client's profile embeds the CA and the tls-crypt key), and it issues every session its own
client certificate and key, valid for a week, which the client presents. A connecting
certificate is admitted only while its session is registered; removing a peer kills its
connection and denies the certificate from then on. The node needs the `openvpn` binary on
`PATH` (see §6).

## 3. Key

```sh
dvpnd keys add operator                # prints the mnemonic once: store it safely
dvpnd keys add operator --recover      # or import an existing mnemonic
dvpnd keys list                        # shows the sent1… operator and sentnode1… node address
```

Send a few P2P to the `sent1…` address for gas (each status update and usage report is a
transaction; budget roughly 0.02 P2P per transaction, a status update every 48 minutes).

### 3a. A hot key (recommended)

With the key above on the server, anyone who breaks into the host takes the node account and
everything it has earned. Instead, keep the node account in a wallet you hold elsewhere (a
hardware wallet, or a machine that is not the node) and give the server a **hot key** that may
only send the node's messages for that account (authz) and have their fees paid by it
(feegrant, limited to those messages). The node account is still the one the chain knows, and
earnings still reach it; the hot key holds nothing.

```sh
dvpnd keys add hot                                      # on the server; no backup needed
dvpnd config set keyring.from hot
dvpnd config set keyring.granter sent1…                 # the node account
dvpnd keys authz-commands                               # prints the grants to make
```

Run the five commands it prints with the `sentinelhub` CLI from the wallet that holds the node
account: four authz grants (register, update details, update status, update session) and one
fee allowance limited to `MsgExec`. They last a year by default (`--valid-for`) and the fee
allowance is capped at 100 P2P (`--spend-limit`). The fee grant also creates the hot key's
account on the chain. Keep a few P2P on the node account for the fees.

At start the node checks the grants and refuses to run while one is missing or expired,
naming it. It warns in the log when a grant expires within 14 days or the fee allowance would
run out within that time; make the grants again before then. Clients see the node account as
the operator; the node signs its handshake replies with the hot key, and a client that checks
them looks up the grant on the chain (docs/protocols.md).

**Moving an existing node to a hot key:** do the above (the installer does it with
`--granter sent1…`), restart, and check `journalctl -u dvpnd` shows `Signing with a hot key`
and a successful status update. Then, holding the operator key's mnemonic somewhere safe, delete
the key from the server with `dvpnd keys delete operator`, and delete any file that holds the
mnemonic. The node address and its sessions are unchanged.

## 4. TLS certificate

Clients pin the certificate per session, so a self-signed one is what the network expects:

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
  -subj "/CN=dvpnd" -keyout ~/.dvpnd/tls.key -out ~/.dvpnd/tls.crt
```

## 5. Firewall and forwarding

| Node type | Port | Protocol |
|---|---|---|
| all | `[node] listen_on`, 8585 above | tcp |
| wireguard | `listen_port` in `wireguard.toml` | udp |
| amneziawg | `listen_port` in `amneziawg.toml`, and the `[v3]` section's when it is enabled | udp |
| openvpn | `listen_port` in `openvpn.toml` | `proto` in `openvpn.toml` |
| v2ray | `listen_port` in `v2ray.toml` | tcp |
| xray | `listen_port` in `xray.toml` | tcp |
| hysteria2 | `listen_port` in `hysteria.toml` | udp |

```sh
sudo ufw allow OpenSSH && sudo ufw allow 8585/tcp && sudo ufw allow <listen_port>/udp && sudo ufw enable
```

WireGuard, AmneziaWG and OpenVPN only: peer traffic is NAT-ed through the uplink interface. The node enables
`net.ipv4.ip_forward` and `net.ipv6.conf.all.forwarding` itself when it brings the interface
up, and it accepts established replies back into the tunnel itself, so a FORWARD policy of
DROP (ufw's default, or a host where Docker is or was installed) is fine. Ports published by
Docker bypass ufw: on a Docker host the ufw rules above only protect what is not published.

**What clients can reach.** Every node type applies the same egress policy
(`services/common/egress.go`): clients reach the internet, and nothing on the node host or
around it. Blocked are the host itself (`127.0.0.0/8`, `::1`, `0.0.0.0/8`, `::`), private,
shared and link-local networks (`10/8`, `172.16/12`, `192.168/16`, `100.64/10`,
`169.254/16`, `fc00::/7`, `fe80::/10`, so also the provider's metadata service and other
clients on the node), plus `192.0.0/24`, `198.18/15`, multicast and reserved space, and
TCP port 25 unless `[egress] allow_smtp = true`. None of this depends on ufw.

- V2Ray, XRAY and Hysteria2 enforce it inside the proxy: a routing rule to a blackhole, or
  Hysteria2's ACL. A destination given as a name is resolved and matched by its addresses
  too, so `localhost` or any name that points into a blocked network is refused.
- On a host, where they run as `dvpnd-proxy` (§6), the kernel holds them to it too: the
  `DVPND-PROXY` chain in OUTPUT rejects whatever that account dials in a blocked network or
  on port 25, so even a name whose answer changes between the proxy's check and its dial
  (DNS rebinding) cannot get through. It also rejects every address of the host itself
  except the node API port, so a service bound to the public address that ufw keeps from
  the outside is not reachable through a proxy either. It lets the account reach the
  resolvers in `/etc/resolv.conf` (Docker's `127.0.0.11` included) and Hysteria2's
  authentication hook on loopback.
- WireGuard, AmneziaWG and OpenVPN enforce it with two iptables chains (and the same in
  ip6tables) per tunnel interface, jumped to from the top of FORWARD and INPUT:
  `DVPND-FWD-<interface>` and `DVPND-IN-<interface>`. From the tunnel the host accepts only
  replies, DNS to the Handshake resolver when it runs, and the node API port. Services bound
  to all of the host's addresses (SSH included) are not reachable through the tunnel. The
  node creates the chains before the interface comes up and removes them when it stops; a
  node that crashed leaves them behind, and the next start refills them in place. Inspect
  them with `sudo iptables -S DVPND-FWD-wg0` (use your interface name).

## 6. Run as a service (recommended)

Before the first start, install what the protocol needs on the host:

- **WireGuard:** `sudo apt-get install -y wireguard-tools`. Every kernel since 5.6 has the
  module. With `enable_ipv6 = true` the host must reach the IPv6 internet:
  `ping -6 -c1 2606:4700:4700::1111` from the host must answer, otherwise set it `false`.
  No sysctl or daemon change: the node turns forwarding on at every start.
- **AmneziaWG:** `awg` and `awg-quick` from
  [amneziawg-tools](https://github.com/amnezia-vpn/amneziawg-tools) (v3.1.20260812, the
  version the Docker image builds), built from source: `git clone`, `git checkout
  v3.1.20260812`, `make -C src && sudo make -C src install` (needs `build-essential`,
  `bash`, `iproute2`). Then the data plane, one of: the kernel module from
  [amneziawg-linux-kernel-module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module)
  (v3.1.20260906 or later, the same generation as the tools) via DKMS (kernel headers
  needed, rebuilt on every kernel update; fastest), or
  [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) at tag v3.1.20260828 (`go
  build -o /usr/local/bin/amneziawg-go .`), which `awg-quick` uses when the module is
  absent. The client apps bundle the older 2.0 engine; the default tier's parameters never
  use a 3.x mechanism, so the newer engine speaks to them unchanged, and the `[v3]` tier is
  what needs the 3.1 generation on the host. The node refuses to start when the tools are
  missing. Everything about IPv6 under WireGuard applies.
- **OpenVPN:** `sudo apt-get install -y openvpn` (2.6 or newer; Debian 13 and Ubuntu 24.04
  ship it). With a kernel that has the `ovpn` data channel offload module (6.16 and newer,
  or the DKMS package) OpenVPN uses it on its own; without it the data plane runs in
  userspace, which is fine. The node refuses to start when the binary is missing.
- **V2Ray:** the `v2ray` binary (v5) on `PATH`. Download `v2ray-linux-64.zip` from the
  [v2fly/v2ray-core releases](https://github.com/v2fly/v2ray-core/releases), unzip it and
  `sudo install -m 0755 v2ray /usr/local/bin/v2ray`; `v2ray version` must work. The Docker
  image bundles Alpine's package; `docker run --rm dvpnd v2ray version` shows that version if
  you want to match it. The node refuses to start when the binary is missing.
- **XRAY:** the `xray` binary on `PATH`, release 26.3.27, the version current client apps
  bundle and the one the Docker image pins. Download `Xray-linux-64.zip` from the
  [XTLS/Xray-core release](https://github.com/XTLS/Xray-core/releases/tag/v26.3.27),
  check it against the `.dgst` file published next to it, unzip only the binary and
  `sudo install -m 0755 xray /usr/local/bin/xray`; `xray version` must work. The node
  refuses to start when the binary is missing.
- **Hysteria2:** the `hysteria` binary on `PATH`, release app/v2.10.0, the version current
  client apps bundle and the one the Docker image pins. Download `hysteria-linux-amd64`
  from the [apernet/hysteria release](https://github.com/apernet/hysteria/releases/tag/app%2Fv2.10.0),
  check it against `hashes.txt` published next to it, and
  `sudo install -m 0755 hysteria-linux-amd64 /usr/local/bin/hysteria`; `hysteria version`
  must work. The node refuses to start when the binary is missing.

Then create the account the protocol daemons run as, and install the unit:

```sh
sudo useradd --system --user-group --no-create-home --home-dir /nonexistent \
  --shell /usr/sbin/nologin dvpnd-proxy
sudo cp scripts/dvpnd.service /etc/systemd/system/dvpnd.service
sudo systemctl daemon-reload && sudo systemctl enable --now dvpnd
journalctl -u dvpnd -f
```

The unit sandboxes the node: it still runs as root (tunnels and NAT need it), but sees the
system read-only except its home and the tunnel configuration folders (`/etc/wireguard`,
`/etc/amnezia`), gets a private `/tmp`, cannot gain privileges, keeps only the capabilities a
node uses and makes only the system calls a service makes. With a node home other than
`/root/.dvpnd`, set it in both `ExecStart` and `ReadWritePaths` (the installer does).

The protocol daemons do not run as root. V2Ray, XRAY and Hysteria2 run as `dvpnd-proxy`, read
their configuration and a copy of the TLS key from `/run/dvpnd`, and an iptables chain for
that account (`DVPND-PROXY` in OUTPUT) holds whatever they dial to the egress policy (§5).
OpenVPN opens its tunnel as root and then drops to `dvpnd-proxy`; the node drives it over a
unix socket in `/run/dvpnd` that only root may use. Without the account the daemons run as
root and the node logs how to create it. Check with `ps -eo user,comm | grep -E
'xray|v2ray|hysteria|openvpn'`.

First start: the node measures its link unless `[bandwidth]` declares it (one to two minutes
and several gigabytes, then reused for a week), registers (`MsgRegisterNode`),
marks itself active (`MsgUpdateNodeStatus`), starts its service (`wg0` up, or the proxy as a
child process) and serves `https://<ip>:8585`. Check it:

```sh
curl -sk https://127.0.0.1:8585/ | head -c 400            # {"success":true,"result":{"service_type":"wireguard",…}}
```

On the chain, the node appears in `sentinel/node/v3/nodes/<sentnode1…>` with status
`active`. Node aggregators that client apps read re-probe active nodes every few minutes;
once your `GET /` answers from the internet your node becomes visible in the apps.

The node adapts its cadence to the chain: it never lets `interval_update_status` or
`interval_update_sessions` exceed 80% of the chain's `status_timeout` parameters (a silent
node is deactivated by the chain after that timeout; a session that reports nothing is
cancelled).

`systemctl stop` or `restart` sends SIGTERM; the node stops its service first (tunnel down
and NAT rules removed, or the proxy child exited) and exits. Connected peers are dropped
and reconnect on their own; the session database is kept and reconciled with the chain at
the next start. A client that reconnects keeps its on-chain session, and the node keeps
reporting that session's totals on top of what the chain already holds (the chain refuses a
report lower than the last one, and one refused report fails the whole batch).

If the protocol daemon (xray, v2ray, Hysteria or OpenVPN) exits on its own, or OpenVPN's
management channel or Hysteria's authentication hook stops, the node logs `Stopping: the
protocol daemon exited`, stops its service and exits with an error; systemd (`Restart=always`)
or Docker (`--restart unless-stopped`) starts it again. Look above that line in the log for
the daemon's own reason.

## 7. Run with Docker (when the host path does not fit)

Use this when you want the bundled `v2ray`, `xray`, `hysteria`, `openvpn`, AmneziaWG tools
and `hnsd`, cannot install Go on the host, or the host already runs everything in Docker.
Every GitHub release publishes the image as `ghcr.io/feryx-official/dvpnd:<version>` and
`:latest`, built by the repository's own workflow from that tag once the tag's signature
checks out against the maintainer's key and CI, the integration suite included, has passed on
its commit. Pull it and give it the local name the commands below use:

```sh
docker pull ghcr.io/feryx-official/dvpnd:latest && docker tag ghcr.io/feryx-official/dvpnd:latest dvpnd
```

Images from 9.4.0 on are signed by that workflow, with no signing key anyone
holds (Sigstore keyless), and carry their build provenance and an SBOM. Check one before you
run it, with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 3 or
later (cosign 2 cannot read the signature format and answers "no signatures found") or the
GitHub CLI:

```sh
cosign verify ghcr.io/feryx-official/dvpnd:latest \
  --certificate-identity-regexp '^https://github.com/Feryx-Official/dvpnd/\.github/workflows/docker-publish\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/feryx-official/dvpnd:latest --repo Feryx-Official/dvpnd
```

Both must succeed; a failure means the image was not built by this repository's release
workflow. Images up to 9.4.3 were built before the repository moved to Feryx-Official: pull
them from `ghcr.io/proofoftrinity/dvpnd`, and to check one, write `proofoftrinity` for
`Feryx-Official` in the identity and the `--repo` value (`trinitystake` for 9.4.0, signed
before the account was renamed), and add `--bundle-from-oci` to the `gh` command: GitHub no
longer finds their attestations by the old repository name, so `gh` reads them from the image.

Or build it yourself (the Dockerfile uses BuildKit cache mounts, so BuildKit must be on — it
is by default on current Docker; otherwise prefix the command with `DOCKER_BUILDKIT=1`):

```sh
make build-image                    # docker build ... --tag dvpnd
```

**IPv6 (WireGuard nodes).** The tunnel is dual-stack by default (`enable_ipv6 = true` in
`wireguard.toml`), so the node must reach the IPv6 internet, otherwise every IPv6 connection
a client opens through the tunnel is answered with "unreachable"; browsers fall back to IPv4
but other software may fail. On Docker's default bridge a container cannot reach IPv6, so
either set `enable_ipv6 = false` for an IPv4-only tunnel, or give containers IPv6 with
`/etc/docker/daemon.json` and a Docker restart (a running container restarts with it):

```json
{
  "ipv6": true,
  "fixed-cidr-v6": "fd00:d0c:1::/64",
  "ip6tables": true
}
```

```sh
sudo systemctl restart docker
docker run --rm alpine ping -6 -c1 2606:4700:4700::1111   # optional: a reply means containers reach IPv6
```

Docker enables IPv6 forwarding on the host itself, so no sysctl change is needed. If the
check fails and the host has no IPv6 connectivity at all, set `enable_ipv6 = false` instead.
Docker NATs the container's IPv6 to the host's address, so with IPv6 on, clients exit with
the host's IPv6 address on IPv6-capable sites. A V2Ray container without IPv6 only fails
for IPv6-only destinations, which are rare. `scripts/runner.sh setup` writes this
`daemon.json` and restarts Docker only on a host that has none; an existing one is left as
it is, with a note when it leaves containers without IPv6. It needs Docker installed first,
from the distribution or from [Docker's apt repository](https://docs.docker.com/engine/install/).

**Configuration.** The image is named `dvpnd` and its entrypoint binary is `process`; the
command after the image name is passed to the node. Prepare `config.toml`, the protocol's
file, the key and `tls.crt`/`tls.key` in `/root/.dvpnd` exactly as in sections 2 to 4 (run
the host binary, or `docker run --rm -v /root/.dvpnd:/root/.dvpnd dvpnd process config init`).
The container runs as root, so those files must be root-owned.

**WireGuard node:**

```sh
docker run --detach --name dvpnd --restart unless-stopped \
  --log-opt max-size=50m --log-opt max-file=3 \
  --volume /lib/modules:/lib/modules:ro \
  --volume /root/.dvpnd:/root/.dvpnd \
  --cap-drop ALL \
  --cap-add NET_ADMIN --cap-add NET_BIND_SERVICE --cap-add NET_RAW --cap-add SYS_MODULE \
  --sysctl net.ipv4.ip_forward=1 \
  --sysctl net.ipv6.conf.all.disable_ipv6=0 \
  --sysctl net.ipv6.conf.all.forwarding=1 \
  --sysctl net.ipv6.conf.default.forwarding=1 \
  --publish 8585:8585/tcp \
  --publish <listen_port>:<listen_port>/udp \
  dvpnd process start
```

The container drops every capability except the four WireGuard needs. IP forwarding is passed
in with `--sysctl` because `/proc/sys` is read-only inside an unprivileged container: the node
reads those switches and, finding them already on, leaves them alone. Publish the same UDP
port as `wireguard.toml`'s `listen_port`.

**AmneziaWG node:** the image carries `amneziawg-go` and the tools, so the tunnel runs in
userspace on a tun device; a kernel module cannot be loaded from an image. The WireGuard
command with `amneziawg.toml`'s `listen_port` (and the `[v3]` section's, when that tier is
enabled), `--device /dev/net/tun` instead of the `/lib/modules` volume, and without
`--cap-add SYS_MODULE`:

```sh
docker run --detach --name dvpnd --restart unless-stopped \
  --log-opt max-size=50m --log-opt max-file=3 \
  --device /dev/net/tun \
  --volume /root/.dvpnd:/root/.dvpnd \
  --cap-drop ALL \
  --cap-add NET_ADMIN --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
  --sysctl net.ipv4.ip_forward=1 \
  --sysctl net.ipv6.conf.all.disable_ipv6=0 \
  --sysctl net.ipv6.conf.all.forwarding=1 \
  --sysctl net.ipv6.conf.default.forwarding=1 \
  --publish 8585:8585/tcp \
  --publish <listen_port>:<listen_port>/udp \
  --publish <v3 listen_port>:<v3 listen_port>/udp \
  dvpnd process start
```

**OpenVPN node:** the AmneziaWG command above with `openvpn.toml`'s `listen_port` published
as `/udp` or `/tcp` to match `proto`, plus `--cap-add SETUID --cap-add SETGID --cap-add CHOWN
--cap-add KILL`. The tun device and NET_ADMIN are what OpenVPN needs; the data channel runs
in userspace inside the container, and the server drops to the image's `dvpnd-proxy` account
once its tunnel is up (the four extra capabilities let it, and let the node stop it).

**V2Ray node:**

```sh
docker run --detach --name dvpnd --restart unless-stopped \
  --log-opt max-size=50m --log-opt max-file=3 \
  --volume /root/.dvpnd:/root/.dvpnd \
  --cap-drop ALL --cap-add NET_BIND_SERVICE \
  --cap-add NET_ADMIN --cap-add NET_RAW \
  --cap-add SETUID --cap-add SETGID --cap-add CHOWN --cap-add KILL \
  --publish 8585:8585/tcp \
  --publish <listen_port>:<listen_port>/tcp \
  dvpnd process start
```

No modules, no sysctls: the proxy is a plain process on a port. The node runs it as the
image's `dvpnd-proxy` account (SETUID, SETGID, CHOWN for its runtime files, KILL to stop it)
behind an iptables chain for that account inside the container's own network namespace
(NET_ADMIN, NET_RAW); see §5. Publish the same TCP port as `v2ray.toml`'s `listen_port`.

**XRAY node:** the same command with `xray.toml`'s `listen_port`. The image pins xray
26.3.27 and checks its sha256 at build time.

**Hysteria2 node:** the same command with `hysteria.toml`'s `listen_port` published as
`/udp`, and `sudo sysctl -w net.core.rmem_max=16777216 net.core.wmem_max=16777216` on the
host (persist it under `/etc/sysctl.d/`), since a container cannot raise them. The image
pins hysteria app/v2.10.0 and checks its sha256 at build time.

The `--log-opt` flags cap the container's log at three files of 50 MB; Docker's default
json-file log grows without bound. `scripts/runner.sh` wraps these commands (`init`, `start`,
`stop`, `status`, `update`) for every node type, with the same flags; a test holds the two
together. Its `NODE_IMAGE` is the published `ghcr.io/feryx-official/dvpnd:latest`, so edit it
only to pin a version or to use your own build. `setup` and `update` check the pulled image
with cosign as above when cosign is installed, and remove an image that fails; `start` runs
only an image already pulled. Its `init` sets the `test` keyring backend: with `file`, the
node asks for the passphrase at every start, through `scripts/runner.sh attach`.

No automated test runs against the chain. `tools/e2e` is the client side for a check by hand:
it buys a session on a running node and handshakes the way client apps do; nodes of every
type but OpenVPN served real traffic this way and reported it on chain.

## 8. Operating

- **Logs:** `journalctl -u dvpnd` on the host, `docker logs dvpnd` in Docker. Every
  transaction logs its hash and code.
- **What the log keeps about clients:** at the default level, no client IP addresses. A
  refused request is logged with its path, status, user agent and the reason the client was
  given; a request for a path the node does not serve (a scanner's `/wp-login.php`) is
  logged at debug only. Peer keys appear as a short tag, because a proxy key is the client's password.
  Session ids and wallet addresses are logged, since the chain already shows them for this
  node. The OpenVPN, XRAY and Hysteria2 daemons log errors only. Run the node with
  `--log_level debug` to chase a fault: request lines then carry the client's address, and
  the daemons log at their usual level, which includes client addresses and (for the
  proxies) the destinations clients reach. Switch back once done. `data.db` is readable
  by its owner only, zeroes deleted rows and is compacted at every start. journald keeps old lines until its size cap; to keep
  less, set `MaxRetentionSec=7day` in `/etc/systemd/journald.conf` and
  `sudo systemctl restart systemd-journald` (Docker: the 3×50MB cap in §7 already applies).
- **Handshakes** are accepted over HTTPS only (plain HTTP gets 403; the status pages answer
  either way) and at most 30 a minute from one address (one IPv6 /64); the excess gets 429
  with `Retry-After`. Refused excess attempts are logged at debug only, with one line a minute
  saying limiting is active.
- **Advertised bandwidth:** `curl -sk https://127.0.0.1:8585/status | jq .result.bandwidth`
  shows the figure and its `source`. `speedtest` is a measurement; the log has one
  `Speed test result` line per server used, and a `rejected: too close to be off this host's
  network` line for every server that answered from inside the datacenter (those would have
  reported the local network). `config` is the `[bandwidth]` setting. `none` with zeros means
  no measurement was possible: every reachable speed test server was inside this host's
  network, or the speed test service was unreachable and nothing was cached. The node runs
  regardless; declare the link in `[bandwidth]` or restart once the service is back.
- **Earnings** accrue to the operator `sent1…` address as sessions settle. With a hot key
  (§3a) that account is not on the server at all. Without one, sweep them to a wallet you
  hold offline; the key on the node is unencrypted, and so is any file holding its mnemonic.
- **Upgrade:** on the host, re-run `scripts/install.sh` (it rebuilds the latest release and
  restarts the service), or build the new version, `sudo systemctl stop dvpnd`, install the
  binary, `sudo systemctl start dvpnd`. In Docker, rebuild or pull the image, `docker rm -f
  dvpnd` and rerun the run command. Existing peers are dropped on restart; the local session
  database (`data.db`) is kept and reconciled with the chain.
- **Moving hosts:** copy `~/.dvpnd` (keyring, config, TLS, protocol file) to the new host and
  change `remote_url`; the next start sends `MsgUpdateNodeDetails` with the new address.
- **Stopping for good:** `sudo systemctl disable --now dvpnd`. The chain marks the node
  inactive after `status_timeout` (currently 1 h) without an update; to do it immediately run
  `go run ./tools/e2e -home /root/.dvpnd -deactivate` from the source tree.
- **Upstream node data:** if this host ran the upstream node, its files are in
  `~/.sentinelnode`; `dvpnd` never reads them but prints a hint when only that directory exists.
