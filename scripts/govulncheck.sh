#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# govulncheck.sh fails when the module's code reaches a known vulnerability
# that SECURITY.md does not explain. An advisory the node cannot take a fix
# for is accepted by naming it, with the reason, under "Known dependency
# advisories" in SECURITY.md; anything else govulncheck finds reachable, and
# any failure of govulncheck itself, fails the run. CI runs it on every push
# and pull request.
#
# GOVULNCHECK names the govulncheck to run; by default the pinned release is
# installed into a temporary directory.

set -Eeuo pipefail
export LC_ALL=C

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT

govulncheck="${GOVULNCHECK:-}"
if [[ -z "${govulncheck}" ]]; then
  GOBIN="${work}" go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
  govulncheck="${work}/govulncheck"
fi

status=0
(cd "${root}" && "${govulncheck}" ./...) >"${work}/report" 2>&1 || status=$?
cat "${work}/report"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  { echo '## govulncheck'; echo; echo '```'; cat "${work}/report"; echo '```'; } >>"${GITHUB_STEP_SUMMARY}"
fi

# govulncheck exits 3 when the code reaches a vulnerability, and with any
# other status but 0 when it could not finish.
case "${status}" in
  0) exit 0 ;;
  3) ;;
  *)
    echo "::error::govulncheck could not finish (exit ${status})" >&2
    exit 1
    ;;
esac

found=$(sed -nE 's/^Vulnerability #[0-9]+: (GO-[0-9]+-[0-9]+)$/\1/p' "${work}/report" | sort -u)
if [[ -z "${found}" ]]; then
  echo "::error::govulncheck reports a reachable vulnerability, but no advisory ID could be read from its output" >&2
  exit 1
fi
accepted=$(sed -n '/^## Known dependency advisories$/,/^## /p' "${root}/SECURITY.md" |
  grep -oE 'GO-[0-9]+-[0-9]+' | sort -u || true)
unexplained=$(comm -23 <(printf '%s\n' "${found}") <(printf '%s\n' "${accepted}"))
if [[ -n "${unexplained}" ]]; then
  for id in ${unexplained}; do
    echo "::error::${id} is reachable from this code and SECURITY.md does not explain it (Known dependency advisories)" >&2
  done
  exit 1
fi
echo "Reachable, and explained in SECURITY.md: $(tr '\n' ' ' <<<"${found}")"
