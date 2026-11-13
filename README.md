# dvpnd

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/Feryx-Official/dvpnd)](https://github.com/Feryx-Official/dvpnd/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Feryx-Official/dvpnd)](go.mod)

**Run a VPN node on the Sentinel dVPN network and get paid in P2P for the bandwidth you share.**

`dvpnd` is an open-source node for the decentralised VPN network on the Sentinel blockchain
(chain ID `sentinelhub-2`). You install it on a server. It puts your node on the chain with
your prices, serves VPN connections to the people who pay for them in their client app, and
reports what they used so that the chain pays you.

It is an independent project under the Apache License 2.0, not affiliated with Sentinel or
Nordic DApps Inc. (see [Provenance and license](#provenance-and-license)).

**[Install a node](#install-a-node)** · [Choose a protocol](#choose-a-protocol) ·
[Daily use](#running-your-node) · [Troubleshooting](#troubleshooting) ·
[Full operator guide](docs/operator.md) · [For developers](#for-developers)

---

## How it works

1. You install dvpnd on a server with a public IP address.
2. dvpnd registers the node on the chain with your prices per gigabyte and per hour.
3. A user picks your node in a client app and pays for a session on the chain.
4. The app connects to your node. dvpnd checks the session on the chain and opens a VPN
   connection for it.
5. dvpnd reports how much data and time each session used, and the chain settles the
   payment to your wallet.

## Why dvpnd

- **One command installs everything** on Ubuntu or Debian: build, configuration, wallet,
  certificate, firewall and system service. Running it again upgrades the node.
- **Six VPN protocols:** WireGuard, AmneziaWG, OpenVPN, V2Ray, XRAY and Hysteria2.
- **Signed releases:** the installer builds only release tags signed by the maintainer, and
  the Docker images and the installer file are signed by the release workflow.
- **Your earnings can stay off the server:** with a [hot key](#hot-key-keep-the-wallet-off-the-server)
  the server holds a key that can only run the node, not spend the wallet.
- **Your server is protected from clients:** they reach the internet, not the services on
  your server, your local network or each other. Outgoing mail (port 25) is blocked by
  default, so your IP address does not land on spam block lists. The V2Ray, XRAY, Hysteria2
  and OpenVPN programs do not run as root, and the node runs in a systemd sandbox.
- **Privacy:** at the default log level the node does not log client IP addresses.
- **Honest numbers:** the advertised speed is measured against test servers outside your
  datacenter, and the location is looked up automatically. Neither needs an API key or a
  paid plan.

## Before you start

You need:

- **A server that does nothing else.** Ubuntu 22.04 or 24.04, or Debian 12 or 13, on a
  64-bit Intel or AMD processor (x86_64). ARM (aarch64) works too, but an XRAY or
  Hysteria2 node then needs its protocol program installed by hand first.
- **A public IPv4 address.** Every VPS has one. At home, your router needs a public address
  of its own and you must be able to forward ports on it. If the WAN address your router
  shows is between `100.64.0.0` and `100.127.255.255`, your provider uses carrier-grade NAT
  and clients cannot reach you: ask the provider for a public IPv4 address or use a VPS.
- **Root access** (`sudo`).
- **Some P2P** (the coin formerly called DVPN) to pay transaction fees: every status update
  and usage report is a small transaction. 50 P2P is a comfortable start. Registering a node
  needs no deposit at the moment.
- **A traffic plan that fits:** every byte your clients use passes through your server, and
  the first start measures your link speed, which moves several gigabytes.

> [!WARNING]
> Use a machine that does nothing else. The node runs as root, keeps its wallet key on disk
> unencrypted (unless you use a [hot key](#hot-key-keep-the-wallet-off-the-server)), and
> sends strangers' traffic out of your IP address, so abuse complaints come to you. Never
> run it on a validator or next to anything that holds secrets. Running a VPN node is
> regulated or prohibited in some countries: check the law where your server is, and your
> provider's terms.

## Choose a protocol

A node serves one protocol. You pick it with `--type` when you install; without it you get
WireGuard. To offer two protocols, run two nodes.

| Protocol | `--type` | What it is | Port |
|---|---|---|---|
| **WireGuard** | `wireguard` (default) | A modern, fast VPN tunnel. The simplest choice. | UDP |
| **AmneziaWG** | `amneziawg` | WireGuard with obfuscation, so it is harder to detect and block. Also offers an opt-in AmneziaWG 3.1 tier on a second port. | UDP, two ports |
| **OpenVPN** | `openvpn` | The long-established VPN protocol, over UDP or TCP. | UDP or TCP |
| **V2Ray** | `v2ray` | A VMess proxy over TCP, optionally inside TLS. You install the `v2ray` program first (see step 3). | TCP |
| **XRAY** | `xray` | A VLESS proxy inside TLS or REALITY. With REALITY the port looks like an ordinary website to an observer. | TCP |
| **Hysteria2** | `hysteria2` | A proxy over QUIC, with optional Salamander obfuscation. | UDP |

Not sure? Start with WireGuard. For users on networks that block VPNs, AmneziaWG, XRAY and
Hysteria2 are built to be harder to detect. Each protocol's own settings are explained in
[the operator guide](docs/operator.md#2a-wireguard), sections 2a to 2f.

## Install a node

The installer does every step of the [operator guide](docs/operator.md) for you. Most of
the time goes into building dvpnd, which takes a few minutes the first time.

### 1. Log in to your server

```sh
ssh root@YOUR_SERVER_IP
```

The commands below use `sudo`. If you are logged in as root, you can leave it out.

### 2. Download the installer

```sh
sudo apt-get update && sudo apt-get install -y curl
curl -fsSL https://raw.githubusercontent.com/Feryx-Official/dvpnd/main/scripts/install.sh -o install.sh
```

It is a plain shell script: read it before you run it (`less install.sh`). To check its
signature first, see [Verify the installer first](#optional-verify-the-installer-first).

### 3. Run it

Give your node a public name of 4 to 32 characters; apps show it to users:

```sh
sudo bash install.sh --moniker "My node"
```

That makes a WireGuard node. For another protocol, add `--type`:

```sh
sudo bash install.sh --moniker "My node" --type hysteria2
```

<details>
<summary><b>V2Ray only:</b> install the <code>v2ray</code> program before you run the installer</summary>

```sh
sudo apt-get install -y unzip
curl -fsSLO https://github.com/v2fly/v2ray-core/releases/latest/download/v2ray-linux-64.zip
unzip -o v2ray-linux-64.zip v2ray && sudo install -m 0755 v2ray /usr/local/bin/v2ray
v2ray version
```

The installer installs everything the other protocols need by itself.
</details>

What the installer does:

1. installs the packages the node needs, and Go if the server has no recent version;
2. downloads the latest release, checks the maintainer's signature on it, and builds it
   (a few minutes the first time);
3. writes the configuration, with default prices of 40 P2P per GB and 97.5 P2P per hour and
   a random port for the protocol;
4. creates the node's wallet and a TLS certificate;
5. opens the node's ports in the firewall (ufw), keeping your SSH port open;
6. installs dvpnd as a system service and starts it.

All the options are in [Installer options](#installer-options).

### 4. Write down your mnemonic

The installer creates the node's wallet and shows its **mnemonic**, the list of secret
words, **once**. Then it waits for you to press Enter. The mnemonic is the only backup of
the wallet and of everything the node earns. Write it down and keep it offline: anyone who
has these words controls the wallet.

> [!NOTE]
> When the installer's output is not a terminal (cloud-init, `| tee`, a CI job), it does not
> print the mnemonic. It writes it to `/root/.dvpnd/operator-mnemonic.txt`, readable by root
> only. Copy it somewhere safe, then delete the file with `shred -u`.

Already have a wallet for this node? Add `--recover` and paste its mnemonic when asked.

### 5. Fund the node's wallet

At the end the installer prints a summary like this one:

```text
  node type        wireguard
  node API         https://203.0.113.10:8585   (tcp)
  wireguard port   41234/udp
  operator wallet  sent1…
  node address     sentnode1…
  home directory   /root/.dvpnd
```

Send some P2P to the **operator wallet** (`sent1…`). The node cannot register until the
wallet can pay fees: until then it restarts every 15 seconds and the log shows the error.
Once the coins arrive it registers by itself. You do not need to do anything else.

### 6. At home only: forward the ports

If the server sits behind your home router, the installer says so and lists the ports to
forward: the node API port (TCP) and the protocol port. Forward them on your router to the
server's local address, and give the server a fixed local address (a DHCP reservation) so
the forwarding keeps working. A VPS needs none of this.

### 7. Check that it is live

Watch the log:

```sh
journalctl -u dvpnd -f
```

On its first start the node spends a minute or two measuring the link speed, then
registers, marks itself active and starts its VPN service. Press Ctrl+C to stop watching. Errors that repeat every 15 seconds mean it cannot start: see
[Troubleshooting](#troubleshooting).

Then check it from outside your network, for example from a phone on mobile data. Open
`https://YOUR_SERVER_IP:8585/status` in the browser. The browser warns about the
certificate: that is expected, because the node uses a self-signed certificate that client
apps pin. You should see your node's details.

On the server, this shows the location and speed your node advertises:

```sh
curl -sk https://127.0.0.1:8585/status | jq .result
```

Client apps list the node a few minutes after it answers from the internet. **Your node is
running.**

### Installer options

| Option | What it does |
|---|---|
| `--type TYPE` | `wireguard` (default), `amneziawg`, `openvpn`, `v2ray`, `xray` or `hysteria2`. On an existing node it keeps the type the node has. |
| `--moniker NAME` | The node's public name, 4 to 32 characters. Asked for if missing. |
| `--gigabyte-price P` | Price per GB. Default `40000000udvpn` (40 P2P). |
| `--hourly-price P` | Price per hour. Default `97500000udvpn` (97.5 P2P). |
| `--listen-port PORT` | Port of the VPN protocol. Default: random, printed at the end. |
| `--api-port PORT` | TCP port of the node API. Default `8585`. |
| `--v3-listen-port PORT` | AmneziaWG only: port of the opt-in 3.1 tier. Default: random. |
| `--openvpn-proto P` | OpenVPN only: `udp` (default) or `tcp`. |
| `--granter ADDR` | Sign with a hot key for the node account `sent1…` held in your own wallet; see [Hot key](#hot-key-keep-the-wallet-off-the-server). |
| `--recover` | Import the mnemonic of an existing wallet instead of creating one. |
| `--version TAG` | Build this release tag instead of the latest. |
| `--source DIR` | Build a source tree you checked yourself. |
| `--home DIR` | Node home directory. Default `/root/.dvpnd`. |
| `--force` | Rewrite the existing configuration and protocol file. Needed to switch a node to another type. |
| `--no-firewall` | Leave ufw alone. |
| `--no-start` | Install the service but do not start it. |
| `--yes` | Never ask; fail where an answer is needed (for scripts). |

Prices are in `udvpn`: 1 P2P = 1,000,000 udvpn. The chain rejects prices below its minimums.

### Optional: verify the installer first

The installer always refuses to build a release whose tag is not signed by the maintainer.
To also check the installer itself before it runs, use one of these.

<details>
<summary><b>A. The signed installer from the latest release</b> (needs <a href="https://docs.sigstore.dev/cosign/system_config/installation/">cosign</a> 3 or later)</summary>

```sh
base=https://github.com/Feryx-Official/dvpnd/releases/latest/download
curl -fsSLO "$base/install.sh" && curl -fsSLO "$base/install.sh.sigstore.json"
cosign verify-blob install.sh --bundle install.sh.sigstore.json \
  --certificate-identity-regexp '^https://github.com/Feryx-Official/dvpnd/\.github/workflows/docker-publish\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

It must print `Verified OK`. Then run `sudo bash install.sh --moniker "My node"` as in step 3.
</details>

<details>
<summary><b>B. The installer from a release tag you checked yourself</b> (needs git 2.34 or later)</summary>

```sh
git clone https://github.com/Feryx-Official/dvpnd.git && cd dvpnd
tag=$(git tag --list 'v*' --sort=-v:refname | head -1)
echo '114076168+proofoftrinity@users.noreply.github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFqdfC3zNhLfD0Hl5H2bvlCvtBtOBhz0VSiietVWvMch' >allowed_signers
git -c gpg.ssh.allowedSignersFile=allowed_signers verify-tag "$tag" && git checkout --quiet "$tag"
sudo bash scripts/install.sh --source "$PWD" --moniker "My node"
```

`verify-tag` must print `Good "git" signature` with the fingerprint
`SHA256:HGWvk00cwVqxKXxfkwRIk/jUr3RiXz3WztzvWXKspq0`.
</details>

### Hot key: keep the wallet off the server

By default the node's wallet key lives on the server, so anyone who breaks into the server
can take the wallet and everything it has earned. With a **hot key**, the node account stays
in a wallet you hold somewhere else (a hardware wallet, or another computer). The server
gets a key that can only send the node's own messages for that account, with the fees paid
by the account. The hot key holds nothing; earnings still go to the node account.

1. Use a wallet you control as the node account and put some P2P on it for fees (50 is a
   comfortable start).
2. Install with `--granter` and that account's address:

   ```sh
   sudo bash install.sh --moniker "My node" --granter sent1YOUR_NODE_ACCOUNT
   ```

3. The installer prints five commands: four authorisations and one fee allowance. Run them
   with the `sentinelhub` command-line wallet, from the wallet that holds the node account.
   The node waits until they exist on the chain and then starts by itself.

By default the grants last a year and the fee allowance is capped at 100 P2P. The node
warns in its log 14 days before either runs out. A node that already runs with its key on the server
moves to a hot key the same way: run the installer again with `--granter`. The full steps
are in [the operator guide, section 3a](docs/operator.md#3a-a-hot-key-recommended).

## Running your node

### Everyday commands

| What | Command |
|---|---|
| Follow the log | `journalctl -u dvpnd -f` |
| Service status | `systemctl status dvpnd` |
| Restart (after a settings change) | `sudo systemctl restart dvpnd` |
| Stop, start | `sudo systemctl stop dvpnd`, `sudo systemctl start dvpnd` |
| Installed version | `dvpnd version` |
| What the node advertises | `curl -sk https://127.0.0.1:8585/status \| jq .result` |
| Show the settings | `sudo dvpnd --home /root/.dvpnd config show` |
| Show the wallet and node address | `sudo dvpnd --home /root/.dvpnd keys list` |
| Open firewall ports | `sudo ufw status` |

### Where things are

| Path | What it holds |
|---|---|
| `/root/.dvpnd/config.toml` | Main settings: name, prices, API port, chain endpoints, speed, location. |
| `/root/.dvpnd/<protocol>.toml` | Protocol settings: `wireguard.toml`, `amneziawg.toml`, `openvpn.toml`, `v2ray.toml`, `xray.toml` or `hysteria.toml`. |
| `/root/.dvpnd/keyring-test/` | The node's key, unencrypted so that the node can sign by itself. |
| `/root/.dvpnd/tls.crt`, `tls.key` | The self-signed certificate that clients pin. |
| `/root/.dvpnd/data.db` | The node's session database. |
| `/etc/systemd/system/dvpnd.service` | The system service. |

Keep a backup of `/root/.dvpnd`: with it you can rebuild the node on any server.

### Change prices, name or other settings

```sh
sudo dvpnd --home /root/.dvpnd config set node.gigabyte_prices 40000000udvpn
sudo dvpnd --home /root/.dvpnd config set node.hourly_prices 97500000udvpn
sudo dvpnd --home /root/.dvpnd config set node.moniker "My better node"
sudo systemctl restart dvpnd
```

At the restart the node sends the new prices to the chain. Other settings you may want:

- `bandwidth.download_mbps` and `bandwidth.upload_mbps`: declare your link speed instead of
  measuring it (a 1 Gbit/s port is `1000`). Clients choose nodes by this figure and nothing
  verifies it, so do not overstate it.
- `geoip.city`, `geoip.country`, `geoip.latitude`, `geoip.longitude`: set them only if the
  automatic location is wrong.
- `qos.max_peers`: the most clients connected at the same time (default 250).

If you change a port, open the new one in the firewall too (`sudo ufw allow PORT/udp`, or
`/tcp`) and, at home, on your router. Every setting is explained in
[the operator guide, section 2](docs/operator.md#2-configuration).

### Upgrade

Run the installer again. It keeps your configuration, key and certificate, builds the latest
signed release and restarts the node:

```sh
curl -fsSL https://raw.githubusercontent.com/Feryx-Official/dvpnd/main/scripts/install.sh -o install.sh
sudo bash install.sh
```

Connected clients drop for a moment and reconnect by themselves; their sessions are kept.
Add `--version TAG` to install a specific release.

### Your earnings

Earnings go to the operator wallet as sessions settle. Without a hot key, that wallet's key
sits unencrypted on the server: move earnings regularly to a wallet you hold elsewhere, by
importing the mnemonic into a wallet that supports the Sentinel chain. Leave enough P2P on
it for fees.

### Move to another server

1. On the old server: `sudo systemctl disable --now dvpnd`.
2. Copy `/root/.dvpnd` to the new server, as root.
3. On the new server, edit `/root/.dvpnd/config.toml` and set `remote_url` to
   `https://NEW_SERVER_IP:8585`.
4. Run the installer on the new server. It keeps the copied key and settings, and at start
   the node tells the chain its new address.

Coming from the upstream Sentinel node software? Stop the old node, then install with
`--recover` and the old operator key's mnemonic: the node keeps its address. dvpnd keeps its
files in `~/.dvpnd` and never reads `~/.sentinelnode`.

### Stop for good

```sh
sudo systemctl disable --now dvpnd
```

The chain marks the node inactive within about an hour.

## Troubleshooting

**The log repeats an error every 15 seconds.** The node stops at start and the service
starts it again. Read the error line. The usual causes:

- The wallet has no P2P yet: fund the operator wallet ([step 5](#5-fund-the-nodes-wallet)).
- With a hot key, a grant is missing or expired: the log names it. Make the grants again.
- The protocol program is missing (for example `v2ray`): install it and restart.
- `Stopping: the protocol daemon exited`: the protocol program crashed. Its own reason is
  in the lines above.

**The node runs, but apps do not show it.** Open `https://YOUR_SERVER_IP:8585/status` from
outside your network. If it does not answer:

- check `sudo ufw status` lists the API port and the protocol port;
- many VPS providers also have a firewall in their control panel: open both ports there too;
- at home, check the port forwarding and that your router's WAN address is not
  carrier-grade NAT ([Before you start](#before-you-start)).

Once the page answers from outside, apps list the node within a few minutes.

**The advertised speed is 0.** The speed test found no usable test server. Declare your
link with `bandwidth.download_mbps` and `bandwidth.upload_mbps` and restart
([settings](#change-prices-name-or-other-settings)).

**The location is wrong.** Set `geoip.city`, `geoip.country`, `geoip.latitude` and
`geoip.longitude` to where the server really is, and restart.

**The installer refuses to build a version.** It builds only release tags signed by the
maintainer. Use a tag from the [Releases page](https://github.com/Feryx-Official/dvpnd/releases)
with `--version`, or leave it out to get the latest.

More in [the operator guide, section 8](docs/operator.md#8-operating). Found a bug?
[Open an issue](https://github.com/Feryx-Official/dvpnd/issues/new/choose).

## Docker

The host install above is the recommended way. Docker is there when it does not fit: you
want the protocol programs bundled, you cannot put a Go toolchain on the host, or everything
on the host already runs in Docker. Every release publishes a signed image:

```sh
docker pull ghcr.io/feryx-official/dvpnd:latest
```

The run command for each node type, how to check the image's signature and the IPv6 setup
are in [the operator guide, section 7](docs/operator.md#7-run-with-docker-when-the-host-path-does-not-fit).
[`scripts/runner.sh`](scripts/runner.sh) wraps them (`setup`, `init`, `start`, `status`,
`update`).

## Project status

- dvpnd speaks the current chain protocol: the `sentinelhub` v12 message set
  (node/session/subscription v3) on cosmos-sdk v0.47, implemented from the Apache-licensed
  chain sources only.
- On the live network, a public node has served paid sessions over WireGuard, both
  AmneziaWG tiers, V2Ray, XRAY (TLS and REALITY) and Hysteria2. A Hysteria2 node has served
  gigabyte and hourly sessions to clients on the network, Katacomb among them, and the chain
  recorded the usage exactly as the node reported it.
- Every protocol, OpenVPN included, is tested end to end with its real program and client
  in Docker ([`test/README.md`](test/README.md)).
- Not yet exercised on the live network: OpenVPN, and sessions bought through a plan
  subscription.

## For developers

### Build from source

```sh
make build            # ./bin/dvpnd  (needs Go ≥ 1.26, gcc for sqlite)
./bin/dvpnd --help
```

`go install` does not work for this module and never has: the Cosmos stack requires three
`replace` directives in `go.mod` (a CometBFT fork among them), and Go refuses
`go install pkg@version` for any module whose `go.mod` replaces something. Build from
source, which is also what stamps the version and commit the node reports.

The module path is `github.com/Feryx-Official/dvpnd/v9`, carrying the major version as Go
requires from v2 upwards, so packages here can be imported by other modules:

```sh
go get github.com/Feryx-Official/dvpnd/v9@latest
```

Releases up to 9.4.3 were tagged before the repository moved and declare the path it had
then: `github.com/proofoftrinity/dvpnd/v9` from 9.4.1, `github.com/trinitystake/dvpnd/v9`
before. The command above works from the first release after 9.4.3.

### Node API

Clients talk to the node over HTTPS on its `remote_url`, with a self-signed certificate they
pin: `GET /` returns the node document, `POST /` is the handshake, `GET /status` the legacy
status document. The endpoint reference, the signed handshake reply and what each protocol
hands the client are in [`docs/protocols.md`](docs/protocols.md#node-api).

### Tests and docs

- `make verify` is the gate every change passes: formatting, vet, race tests, the rule
  registry, licence and provenance checks, then the mutation canaries.
  `make check-integration` runs every protocol with its real daemon and client in Docker.
  See [`test/README.md`](test/README.md).
- [`docs/operator.md`](docs/operator.md): the full operator guide, every step done by hand.
- [`docs/protocols.md`](docs/protocols.md): the node-to-client contract for each protocol.
- [`docs/architecture/`](docs/architecture/): a map of the runtime.

### Contributing and security

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) first: every commit carries a DCO sign-off
(`git commit -s`), and the clean-room rules there are not negotiable. Report security
problems privately, as [`SECURITY.md`](SECURITY.md) explains, never in a public issue.

## Provenance and license

This repository is a fork of [`sentinel-official/dvpn-node`](https://github.com/sentinel-official/dvpn-node)
(GitHub repository id 193683313, since renamed `sentinel-official/sentinel-dvpnx`), taken at commit
[`62bde16ac4ac1135a61ce3e254973f7567d4c593`](https://github.com/sentinel-official/sentinel-dvpnx/commit/62bde16ac4ac1135a61ce3e254973f7567d4c593)
of **2024-01-25** — the last commit upstream published under the **Apache License 2.0**.
The very next upstream commit (`99603eff`, 2024-03-31) replaced that license with a
non-open-source one. **No code, configuration or documentation from that commit or any later
upstream commit is included here.** The Apache 2.0 grant on everything before it is perpetual
and irrevocable (License §2), which is what makes this fork possible.

- License: Apache License 2.0, unchanged, with the original `Copyright [2017] [Sentinel]` notice
  retained — see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
- Verifiable record: [`docs/provenance/`](docs/provenance/) contains a script that re-derives the
  claims above from this repository, proxy.golang.org, sum.golang.org, apache.org and Software
  Heritage, plus its output at fork time.
- Contributing rules that keep the codebase clean: [`CONTRIBUTING.md`](CONTRIBUTING.md).
- Geolocation: dvpnd uses IP2Location.io IP geolocation web service; ipwho.is and Cloudflare are
  also queried and need no attribution. No paid plan is involved.

This project is not affiliated with, endorsed by or sponsored by Sentinel, the Sentinel dVPN
Foundation or Nordic DApps Inc. Running a VPN node is regulated or prohibited in some
jurisdictions; operators are responsible for complying with the law where they run it. The
software is provided "as is", without warranty, as the License states.
