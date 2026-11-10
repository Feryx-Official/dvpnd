# dvpnd

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/Feryx-Official/dvpnd)](go.mod)

`dvpnd` is an open source dVPN node daemon for the Sentinel blockchain (chain ID
`sentinelhub-2`). It registers the node on-chain, serves VPN sessions over WireGuard,
AmneziaWG, OpenVPN, V2Ray, XRAY or Hysteria2, and reports usage. It is licensed under the
Apache License 2.0 and is not affiliated with Sentinel or Nordic DApps Inc. — see the
provenance section below.

## Status

Speaks the current chain protocol: `sentinelhub` v12 message set (node/session/subscription
v3) on cosmos-sdk v0.47, implemented against the Apache-licensed chain sources only; see
`CONTRIBUTING.md` for the rules that keep it that way.

Verified end-to-end on the live network (`sentinelhub-2`) with a WireGuard node behind NAT
and the client side driven by `tools/e2e`, which handshakes the way current client apps do:
node registration, status keep-alive, session purchase, the `GET /` and `POST /` handshake,
tunnel traffic both ways, and usage reporting that the chain recorded.

Also verified in Docker: the image built from `Dockerfile` ran the node with every capability
dropped except the four WireGuard needs; a client in a second container bought a session,
tunnelled through the node with its traffic leaving under the node's public address, and the
chain recorded the usage the node reported. The run command is in `docs/operator.md`, section 7.

Protocols: WireGuard, AmneziaWG (WireGuard with obfuscation), OpenVPN, V2Ray (VMess), XRAY
(VLESS over TLS or REALITY, with Vision) and Hysteria2 (QUIC, optional Salamander
obfuscation), one per node, chosen with `[node] type`; `docs/protocols.md` records what each
needs and the node-to-client contract. An AmneziaWG node offers the parameter set every
client app speaks today and, on a second interface, an opt-in AmneziaWG 3.1 tier for
clients that ask for it. The XRAY, Hysteria2, AmneziaWG and OpenVPN services
were driven against the real binaries: peers added and removed through their control
interfaces, the protocol's own client tunnelled traffic through the node (for AmneziaWG and
OpenVPN between two containers, the latter over UDP and TCP), a wrong Hysteria2 password
was refused, a kicked Hysteria2 client lost its connection, a removed OpenVPN client was
killed and denied on reconnect, and the per-peer counters the node reports moved
accordingly.

Verified on a public node on the live network as well: paid sessions over Hysteria2, XRAY
(TLS and REALITY), V2Ray, WireGuard and both AmneziaWG tiers, with the client side driven by
`tools/e2e`; and on a Hysteria2 node, gigabyte and hourly sessions opened by clients on the
network, Katacomb among them. Through a client's tunnel the provider's metadata service and
outgoing mail were refused, and the chain recorded the bytes and duration the node reported
exactly as the node had stored them.

Not yet exercised on the live network: OpenVPN, and plan-subscription sessions
(`lite/live_test.go` covers read-only queries, opt-in via `DVPND_LIVE_RPC`).

The node adapts its update cadence to the chain: it never lets `interval_update_status` or
`interval_update_sessions` exceed 80% of the chain's `status_timeout` parameters (currently
1 h and 2 h), because the chain deactivates a silent node and cancels a silent session.

## Node API

Clients talk to the node over HTTPS on `remote_url` (self-signed certificate; clients pin it).

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/` | Node document: `service_type` (the protocol name), `service_metadata` (the protocol's public inbound description), `version{name: "dvpnd", tag, commit}`, location, measured or declared bandwidth and peer count. Every response also carries a `Server: dvpnd/<version>` header. |
| `POST` | `/` | Handshake used by current client apps. Body `{data, id, pub_key, signature}`: `data` is the base64 peer request (`{"public_key"}` for WireGuard, `{"uuid"}` for V2Ray), `pub_key` is `secp256k1:` + base64 compressed key, `signature` the compact signature over the 8-byte big-endian session id followed by the raw `data` bytes. Returns `{result:{data, addrs}}` with `data` = base64 JSON of the client configuration (WireGuard: `addrs` assigned to the client and `metadata[{port, public_key}]`; V2Ray: `metadata[{port, proxy_protocol, transport_protocol, transport_security, tls_pin}]`). |
| `GET` | `/status` | Legacy status document. `bandwidth` carries `download`, `upload` (bytes per second) and `source`: `speedtest` (measured), `config` (declared by the operator) or `none`. `location` carries `city`, `country`, `country_code` (ISO 3166-1 alpha-2), `latitude`, `longitude` and `source`: the service that geolocated the node's IP, `static` when the operator set values by hand, or `none`. |
| `POST` | `/accounts/:acc_address/sessions/:id` | Legacy handshake (`{key, signature}`, signature over the session id, verified against the account's on-chain public key). Off unless `[node] legacy_handshake = true`: the signature does not cover the key, so a captured request can be replayed. |

Errors are `{success:false, error:{code, message}}`; a session or key that already
exists answers `409`.

## Running a node

See [`docs/operator.md`](docs/operator.md): host requirements (a public IPv4 — not behind
carrier-grade NAT), build, configuration, TLS, firewall, the systemd unit in
[`scripts/dvpnd.service`](scripts/dvpnd.service), and day-to-day operation.
[`scripts/install.sh`](scripts/install.sh) does all of that on a Debian or Ubuntu host, VPS
or at home, in one run.

## Build

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

Configuration lives in `~/.dvpnd/config.toml` (`dvpnd config init`). Existing installs of
the upstream node keep their data in `~/.sentinelnode`; `dvpnd` does not move it — it logs
a hint and you copy the directory when you are ready.

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
