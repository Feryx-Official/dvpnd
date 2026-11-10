# Licence and provenance: what keeps the fork's legal position answerable

dvpnd is a fork taken at the last Apache-2.0 commit of `sentinel-official/dvpn-node`.
Its legal position rests on that grant and on never mixing in post-relicence or
unlicensed code (`NOTICE`, `CONTRIBUTING.md`). IDs as in `test/README.md`.

- **[LIC-1] Every Go file starts with `// SPDX-License-Identifier: Apache-2.0`.**
- **[LIC-2] dvpnd never depends on `sentinel-official/sentinel-go-sdk`.** That
  repository ships no licence, which means all rights reserved; neither `go.mod`,
  `go.sum` nor any import may name it.
- **[LIC-3] A file inherited from upstream and changed in substance says so.** It
  carries `// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See
  NOTICE.` (Apache-2.0 section 4(b)). The SPDX line and the module path rename alone
  don't count as a change.
- **[LIC-4] `LICENSE` is the file the fork point had, and `NOTICE` keeps the upstream
  copyright line.** `Copyright [2017] [Sentinel]` stays.
- **[LIC-5] The provenance record verifies.** `docs/provenance/verify-fork.sh` proves the
  fork point is the last Apache-licensed upstream commit and classifies every tag.
- **[LIC-6] No wallet or node address is committed.** The chain is public, so an
  address in this public repository links the maintainer's GitHub identity to wallets.
  Test fixtures use synthetic keys; an address that must appear is listed with its reason.
- **[LIC-7] Every dependency carries a permissive or weak-copyleft licence.** Strong
  copyleft, network copyleft, source-available and unknown licences are refused, and so is
  a package with no licence file. The static test reads the licences from the module cache;
  the CI licence gate checks them again with go-licenses.
- **[LIC-8] Every link, module path and image name uses the repository's current home.**
  The repository has moved twice: its account was renamed, then it was transferred to
  `Feryx-Official/dvpnd`; the test lists both former homes. GitHub forwards a former home
  only until someone registers the old account name or creates a repository at the old
  path; from then on a link to it serves that code, and the install command runs it as
  root. A former home stays only on lines that record the past, each listed in the test
  with its reason.
