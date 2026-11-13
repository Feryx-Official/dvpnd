# Dependency licence audit

## Current graph — sentinelhub v12.0.2 / cosmos-sdk v0.47.17 (2026-11-13)

Raw output: `dependency-licenses.csv` (133 modules, dvpnd itself left out), generated with `go-licenses v1.6.0`;
re-run with `go run github.com/google/go-licenses@v1.6.0 report ./...`. The CI licence gate
runs the matching `check` on every push.

| Licence | Modules |
|---|---|
| MIT | 54 |
| Apache-2.0 | 40 — cosmos-sdk, CometBFT (`sentinel-official/cometbft` fork), `sentinelhub` |
| BSD-3-Clause | 25 |
| BSD-2-Clause | 5 |
| ISC | 5 |
| MPL-2.0 | 3 — `hashicorp/go-immutable-radix`, `hashicorp/golang-lru`, `pgregory.net/rapid`; file-level weak copyleft, unmodified |
| BSD-2-Clause-FreeBSD | 1 |

**No GPL, LGPL or AGPL dependency. No dependency without a licence. No `Unknown`.**
`github.com/sentinel-official/sentinel-go-sdk` (unlicensed) is absent and CI refuses it.

## Fork-point graph — cosmos-sdk v0.45.16 (historical)

Generated 2026-09-06 with `go-licenses v1.6.0` under Go 1.21.13 on the unmodified
`go.mod`/`go.sum` of the fork point. Raw output: `dependency-licenses-2024.csv` (129 modules).

## Summary

| Licence | Modules | Notes |
|---|---|---|
| MIT | 50 | |
| Apache-2.0 | 41 | includes cosmos-sdk, cometbft, `sentinel-official/hub` |
| BSD-3-Clause | 24 | |
| BSD-2-Clause | 5 | |
| ISC | 4 | |
| MPL-2.0 | 3 | `hashicorp/hcl`, `hashicorp/go-immutable-radix`, `hashicorp/golang-lru` — file-level weak copyleft, compatible with Apache-2.0 distribution; unmodified |
| BSD-2-Clause-FreeBSD | 1 | `rcrowley/go-metrics` |
| Unknown | 1 | see below |

**No GPL, LGPL or AGPL dependency. No dependency without a licence.**

## The one `Unknown`: `github.com/regen-network/cosmos-proto v0.3.1`

The v0.3.1 module zip contains no LICENSE file, so scanners report it as unknown. The
repository itself is licensed **Apache-2.0** (`LICENSE` added 2021-04-21, after the v0.3.1
tag; repository now archived). It is a transitive dependency of cosmos-sdk v0.45 (gogoproto
codegen support) and disappears when the SDK is upgraded to v0.47+, which uses
`cosmos/cosmos-proto` and `cosmos/gogoproto` instead — which is what happened: it is not in
the current graph.

## Runtime components that are *not* linked

| Component | Licence | How it is used |
|---|---|---|
| WireGuard (`wireguard-tools`, kernel module) | GPL-2.0 | separate process / kernel; configured via `wg`/`wg-quick` |
| V2Ray (`v2ray` package) | MIT | separate process; `v2fly/v2ray-core/v5` library (MIT) is linked for config types |
| hnsd (Handshake resolver) | MIT | separate process built in the Docker image |

Invoking a GPL program as a separate process does not make this program a derivative work
of it; nothing GPL-licensed is compiled or linked into the binary.

## Renamed modules

`github.com/sentinel-official/hub v0.11.3` (Apache-2.0) — the repository is now
`sentinel-official/sentinelhub`; the module path and content are immutable on
proxy.golang.org and resolve normally.
