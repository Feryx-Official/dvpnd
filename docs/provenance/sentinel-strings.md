# "Sentinel" string inventory

**Status (2026-09-06):** Buckets A and D applied under the name `dvpnd` (module
`github.com/Feryx-Official/dvpnd/v9`). What remains in the tree is bucket B (attribution), bucket C
(protocol identifiers and the Apache-2.0 `hub` dependency) and the legacy-home hint text.
Re-check with the grep below; every hit must fall in one of those.

Every occurrence of `sentinel` (case-insensitive) in the tree at the fork point, classified.
Apache 2.0 §6 grants no trademark rights, so branding must go; §4(c) requires attribution
notices to stay; and some strings are protocol identifiers that are neither.
Regenerate the raw list with `grep -rIn -i sentinel --exclude-dir=.git --exclude-dir=docs .`

## Bucket A — trademark / branding: **remove** (Phase E, needs the new project name)

| Location | String | Replacement |
|---|---|---|
| `README.md:1` | `# Sentinel dVPN Node` | new project name |
| `README.md:3-9` | seven badges under `sentinel-official/dvpn-node` | badges for the new repo |
| `README.md:11` | `https://docs.sentinel.co/node-setup` | own docs |
| `Makefile:7-8` | `version.Name=sentinel`, `version.AppName=sentinelnode` | new names |
| `Makefile:20,32` | `-o ./bin/sentinelnode` | new binary name |
| `Makefile:36` | `--tag sentinel-dvpn-node` | new image tag |
| `Dockerfile:14` | `/go/bin/sentinelnode` | new binary name |
| `main.go:19` | `Use: "sentinelnode"` | new binary name |
| `scripts/runner.sh:5` | `CONTAINER_NAME=sentinelnode` | new name |
| `scripts/runner.sh:6,52,560,578,602,624` | `~/.sentinelnode` | new home dir |
| `scripts/runner.sh:7` | `NODE_IMAGE=ghcr.io/sentinel-official/dvpn-node:latest` | **own image — see note** |
| `.github/CODEOWNERS` | `@bsrinivas8687` (upstream maintainer) | delete file |
| `CODE_OF_CONDUCT.md:58` | upstream maintainer's contact email | project contact |

**Note on `runner.sh:7`:** as written, the helper script pulls upstream's *current* container
image at runtime, i.e. post-relicensing code. This is the one place the fork would silently
re-acquire non-Apache material. It must be repointed before the script ships.

## Bucket B — attribution: **keep verbatim**

| Location | String | Why |
|---|---|---|
| `LICENSE:189` | `Copyright [2017] [Sentinel]` | Apache §4(c); never edit |
| `NOTICE`, `README.md` provenance section | references to `sentinel-official/dvpn-node` | identifies the origin of the code, as the license requires |

## Bucket C — functional identifiers: **keep** (renaming breaks the software, not the trademark)

| Location | String | Why it stays |
|---|---|---|
| `go.mod:13`, `go.sum:996-997`, 12 Go files | `github.com/sentinel-official/hub` (`types`, `x/node`, `x/session`, `x/subscription`, `x/vpn`) | Apache-2.0 dependency (repo now `sentinelhub`); an import path is a factual reference. Bumped, not removed, in Phase F. |
| `main.go:17` | `hubtypes.GetConfig().Seal()` | sets the chain's bech32 address prefixes (`sent…`, `sentnode…`) — wire format |
| `utils/keys.go` | `hubtypes.NodeAddress` | same — on-chain node address encoding |
| `types/config.go:181` | `c.ID = "sentinelhub-2"` | **chain ID**; part of every signed transaction |
| `types/config.go:182` | `c.RPCAddresses = "https://rpc.sentinel.co:443"` | default RPC endpoint; a public service, configurable by the operator. Consider adding independent RPCs as defaults so the node doesn't depend on one operator's infra. |
| `scripts/runner.sh:87,114` | `lcd.sentinel.co`, `rpc.sentinel.co`, `rpc.sentinel.quokkastake.io`, `sentinel-rpc.badgerbite.io` | same — public chain endpoints |
| 36 Go files | `github.com/sentinel-official/dvpn-node/...` (own module path) | rewritten mechanically to the new module path in Phase E |

## Bucket D — runtime identifiers: **rename with fallback** (decision 2026-09-06)

| Location | String | Plan |
|---|---|---|
| `types/keys.go:14` | `KeyringName = "sentinel"` | new name + `LegacyKeyringName` |
| `types/keys.go:28` | `~/.sentinelnode` | new dir + `LegacyHomeDirectory`; `start`/`keys` log a migration hint when only the legacy dir exists. No automatic move. |

Totals at fork point: 98 matches in 33 files; 60 are the module path, 17 are `hub` imports.
