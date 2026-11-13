#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# release-gate.sh TAG: what the release workflow checks before it builds or
# publishes anything.
#
# The tag must be one the installer would build: signed with the
# maintainer's key and naming itself. The check is the installer's own
# verify_tag against the key it pins, TAG_SIGNER, both read from
# scripts/install.sh, so a release and an install accept the same tags.
#
# CI must have passed on the tagged commit: the integration suite is one of
# its jobs, and nothing is released that fails it. A run still going is
# waited for, up to GATE_WAIT seconds (45 minutes by default); CI runs on
# every release tag pushed, so the commit has one.
#
# Needs the tag's object fetched, and GH_TOKEN and GITHUB_REPOSITORY, as a
# workflow step has them.

set -Eeuo pipefail

tag="${1:?usage: release-gate.sh TAG}"
root=$(cd "$(dirname "$0")/.." && pwd)
wait_for="${GATE_WAIT:-2700}"
poll="${GATE_POLL:-30}"

TAG_SIGNER=""
# shellcheck source=/dev/null
source <(sed -n -e '/^TAG_SIGNER=/p' -e '/^verify_tag() {$/,/^}$/p' "${root}/scripts/install.sh")
if [[ -z "${TAG_SIGNER}" ]] || ! declare -F verify_tag >/dev/null; then
  echo "::error::scripts/install.sh defines no TAG_SIGNER and verify_tag to check the tag with" >&2
  exit 1
fi
if ! verify_tag "${root}" "${tag}"; then
  echo "::error::${tag} is not a tag the installer would build: it is not signed with the maintainer's key, or it names another tag" >&2
  exit 1
fi
commit=$(git -C "${root}" rev-parse "refs/tags/${tag}^{commit}")
echo "${tag} is signed with the maintainer's key and names itself; it tags ${commit}"

deadline=$((SECONDS + wait_for))
while :; do
  runs=$(gh api "repos/${GITHUB_REPOSITORY}/actions/workflows/ci.yml/runs?head_sha=${commit}&per_page=100" \
    --jq '.workflow_runs[] | "\(.status) \(.conclusion // "")"')
  pending=0
  failed=()
  while read -r status conclusion; do
    [[ -n "${status}" ]] || continue
    if [[ "${status}" != completed ]]; then
      pending=1
    elif [[ "${conclusion}" == success ]]; then
      echo "CI passed on ${commit}"
      exit 0
    else
      failed+=("${conclusion}")
    fi
  done <<<"${runs}"
  if ((!pending && ${#failed[@]} > 0)); then
    echo "::error::CI did not pass on ${commit} (${failed[*]}): re-run it, then this workflow" >&2
    exit 1
  fi
  if ((SECONDS >= deadline)); then
    echo "::error::no CI run passed on ${commit} within ${wait_for} seconds" >&2
    exit 1
  fi
  sleep "${poll}"
done
