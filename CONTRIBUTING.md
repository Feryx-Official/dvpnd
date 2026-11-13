# Contributing

## License and sign-off

Every contribution is accepted under the Apache License, Version 2.0 — the same
license the project is distributed under (Section 5 of the License: inbound =
outbound). You keep your copyright.

Every commit must carry a Developer Certificate of Origin sign-off
(`git commit -s`), which adds a `Signed-off-by: Name <email>` trailer certifying
that you wrote the change or otherwise have the right to submit it under the
project license. The full text is at https://developercertificate.org/.
Commits without a sign-off are not merged. This is deliberately lightweight and
it is what keeps the license question answerable from the git log alone.

## Provenance rules — these are not negotiable

This project is a fork taken at the last Apache-licensed commit of
`sentinel-official/dvpn-node` (see `NOTICE`). Upstream relicensed everything
after that point under a non-open-source license. To keep this codebase clean:

1. **Do not read, copy, adapt or translate upstream source published after
   2024-01-25** (`sentinel-official/sentinel-dvpnx`, `sentinel-official/sentinel-go-sdk`,
   or any other post-relicensing repository of theirs). Not "just for reference".
   If you have read it, do not contribute an implementation of the same feature.
2. **Never add `github.com/sentinel-official/sentinel-go-sdk` as a dependency.**
   That repository ships no license at all, which means all rights reserved.
   CI rejects any `go.mod` that mentions it.
3. Chain protocol facts come from Apache-licensed sources only: the `.proto`
   files in `github.com/sentinel-official/sentinelhub` (verify its LICENSE at the
   version you pin), chain queries against a live node, or traffic you capture
   from a client you run yourself. The node↔client API was derived from the
   request/response handling in the maintainer's own client, Katacomb VPN
   (GPL-3.0) — never from the `sentinel-js-sdk`/`sentinel-go-sdk` packages it
   wraps, which ship no licence text.
4. Every new or modified `.go` file starts with
   `// SPDX-License-Identifier: Apache-2.0`; scripts, Dockerfiles, the Makefile, YAML
   files, systemd units and ignore files start with the same line as a `#` comment, after
   any `#!` line. Files inherited from upstream and changed here additionally carry
   `Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.` as a
   comment in their own syntax (`//`, `#` or `<!-- -->`), under the SPDX line where
   there is one.
5. Never edit `LICENSE`. Never remove or alter the `Copyright [2017] [Sentinel]`
   notice.
6. New dependencies must carry an OSI-approved permissive or weak-copyleft
   license. The `Licence gate` job in `.github/workflows/ci.yml` runs
   `go-licenses` (fails on `forbidden`, `restricted`, `unknown`), refuses
   `sentinel-go-sdk` in `go.mod`, re-checks that the pinned `sentinelhub` is still
   Apache-2.0, and requires the SPDX header on every Go file. The `Fork provenance`
   job runs `docs/provenance/verify-fork.sh`.

## Practical

- `make verify` before every commit, and `make check-integration` (Docker) for a change
  to a service, the egress policy, packaging or the systemd unit (`test/README.md`). A
  bug fix comes with a test that fails without it; a new rule goes into
  `docs/invariants/` with the test that pins it.
- Keep pull requests focused; one logical change per PR. A pull request merges with
  every CI and CodeQL check green (`.github/rulesets/main-checks.json`), and `main`
  takes signed commits only.
- Bug reports and feature requests go through GitHub issues; security issues
  do not — see `SECURITY.md`.
