#!/usr/bin/env bash
# Re-runnable provenance check for this fork of sentinel-official/dvpn-node.
# Proves: the tree descends from the last Apache-2.0 upstream commit, no later
# upstream object exists in this repository, and the LICENSE is the unmodified
# Apache-2.0 text as independently archived by proxy.golang.org / sum.golang.org
# and Software Heritage. Run from the repository root:  bash docs/provenance/verify-fork.sh
set -u
FORK=62bde16ac4ac1135a61ce3e254973f7567d4c593    # last Apache-2.0 commit, 2024-01-25
RELICENSE=99603eff6e495e350f26940aa809aaa86b5f7f0f  # "chore: update LICENSE", 2024-03-31
V071=977111472f9f7dd63f91f8adf420af41f7602a60    # tag v0.7.1, last Apache-2.0 release
POST=(d58a610b 9cb7ac9122358aebed599f8740b46af810917b98)  # v8.0.0, v9.0.0 upstream commits
fail=0; pass(){ printf 'PASS  %s\n' "$*"; }; fail(){ printf 'FAIL  %s\n' "$*"; fail=1; }; info(){ printf 'info  %s\n' "$*"; }
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
echo "# Fork provenance report — $(date -u +'%Y-%m-%dT%H:%M:%SZ') — $(git --version)"
echo
echo "## 1. Fork point"
h=$(git rev-parse fork-point^{commit}); [ "$h" = "$FORK" ] && pass "tag fork-point -> $FORK" || fail "fork-point resolves to $h"
git merge-base --is-ancestor "$FORK" HEAD && pass "HEAD descends from the fork point" || fail "HEAD does not descend from the fork point"
d=$(git log -1 --format=%ad --date=short "$FORK"); [ "$d" = 2024-01-25 ] && pass "fork commit dated $d" || fail "fork commit dated $d"
n=$(git rev-list --count "$FORK"); [ "$n" = 433 ] && pass "$n upstream commits reachable from fork point" || fail "$n commits reachable (expected 433)"
newest=$(git log "$FORK" --format=%ad --date=short | sort | tail -1); [ "$newest" = 2024-01-25 ] && pass "newest upstream commit date $newest" || fail "newest upstream commit date $newest"
git tag -v fork-point >/dev/null 2>&1 && pass "fork-point tag signature verifies ($(git for-each-ref refs/tags/fork-point --format='%(taggername) %(taggeremail)'))" || info "fork-point tag signature not verifiable here (allowed_signers not configured)"
echo
echo "## 2. No post-relicensing upstream object exists in this repository"
for s in "$RELICENSE" "${POST[@]}"; do git cat-file -e "$s^{commit}" 2>/dev/null && fail "object $s PRESENT" || pass "object ${s:0:8} absent"; done
u=$(git fsck --unreachable --no-reflogs 2>/dev/null | wc -l); info "unreachable objects: $u"
r=$(git remote -v | grep -c sentinel-official || true); [ "$r" = 0 ] && pass "no remote points at sentinel-official" || fail "a remote points at sentinel-official"
echo
echo "## 3. Tags"
# Upstream's tags (v0.1.0 .. v0.7.1) are ancestors of the fork point. This fork's own
# releases descend from it and are numbered v9.x: client apps read the major version as
# the node API level, so the numbers coincide with upstream's later series by design.
# The names may match; the commits do not (section 2 proves the upstream objects absent).
up=0; ours=0; bad=0
for t in $(git tag | grep -v '^fork-point$'); do
  if git merge-base --is-ancestor "$t" "$FORK"; then up=$((up+1))
  elif git merge-base --is-ancestor "$FORK" "$t"; then ours=$((ours+1))
  else fail "tag $t is neither an upstream tag (ancestor of the fork point) nor a fork release (descendant)"; bad=1; fi
done
[ $bad = 0 ] && pass "$up upstream tags are ancestors of the fork point ($(git tag --merged "$FORK" | grep -v fork-point | sort -V | head -1) .. $(git tag --merged "$FORK" | grep -v fork-point | sort -V | tail -1)); $ours fork release tags descend from it"
[ "$(git tag --merged "$FORK" | grep -cE '^v[89]\.' || true)" = 0 ] && pass "no upstream v8/v9 (post-relicense) tag in the inherited history" || fail "upstream post-relicense tag in the inherited history"
bad=0; for t in $(git tag --no-merged "$FORK"); do c=$(git rev-parse "$t^{commit}"); for p in "${POST[@]}"; do case "$c" in "$p"*) fail "fork tag $t points at upstream post-relicense commit $p"; bad=1;; esac; done; done
[ $bad = 0 ] && pass "no fork tag points at an upstream post-relicense commit (upstream's v9.0.0 is ${POST[1]:0:8}, absent here)"
echo
echo "## 4. LICENSE integrity"
sha_head=$(sha256sum LICENSE | cut -d' ' -f1); sha_fork=$(git show "$FORK:LICENSE" | sha256sum | cut -d' ' -f1); sha_v071=$(git show "$V071:LICENSE" | sha256sum | cut -d' ' -f1)
info "sha256 LICENSE (working tree) $sha_head"
[ "$sha_head" = "$sha_fork" ] && pass "identical to fork-point LICENSE" || fail "differs from fork-point LICENSE ($sha_fork)"
[ "$sha_head" = "$sha_v071" ] && pass "identical to v0.7.1 LICENSE" || fail "differs from v0.7.1 LICENSE ($sha_v071)"
git cat-file -e "$FORK:LICENSE.md" 2>/dev/null && fail "LICENSE.md exists at fork point" || pass "no LICENSE.md at fork point"
git cat-file -e "$FORK:NOTICE" 2>/dev/null && fail "upstream NOTICE exists at fork point" || pass "no upstream NOTICE at fork point (Apache §4(d) not triggered)"
lc=$(git log "$FORK" --format=%h -- LICENSE | tr '\n' ' '); [ "$lc" = "85ec150 " ] && pass "LICENSE touched by exactly one upstream commit ($lc'Create LICENCE', 2021-06-10)" || fail "LICENSE history: $lc"
head -2 LICENSE | grep -q 'Apache License' && sed -n 2p LICENSE | grep -q 'Version 2.0' && pass "header reads Apache License Version 2.0" || fail "header is not Apache 2.0"
grep -q '^   Copyright \[2017\] \[Sentinel\]$' LICENSE && pass "original copyright line retained verbatim" || fail "original copyright line missing"
echo
echo "## 5. Independent attestations"
if curl -sSf -o "$T/canon.txt" https://www.apache.org/licenses/LICENSE-2.0.txt 2>/dev/null; then
  diff -B <(sed 's/[[:space:]]*$//' "$T/canon.txt") <(sed 's/[[:space:]]*$//' LICENSE) > "$T/diff.txt"
  if [ "$(grep -c '^<' "$T/diff.txt")" = 1 ] && grep -q '^< .*\[yyyy\]' "$T/diff.txt" && [ "$(grep -c '^>' "$T/diff.txt")" = 1 ] && grep -q '^> .*\[2017\] \[Sentinel\]' "$T/diff.txt"; then
    pass "apache.org canonical text: only the appendix copyright line differs"; else fail "apache.org canonical text: unexpected diff"; sed 's/^/      /' "$T/diff.txt"; fi
else info "apache.org unreachable; skipped canonical diff"; fi
if curl -sSf -o "$T/v071.zip" https://proxy.golang.org/github.com/sentinel-official/dvpn-node/@v/v0.7.1.zip 2>/dev/null; then
  sha_zip=$(unzip -p "$T/v071.zip" '*/LICENSE' | sha256sum | cut -d' ' -f1)
  [ "$sha_zip" = "$sha_head" ] && pass "proxy.golang.org dvpn-node@v0.7.1 zip carries the identical LICENSE ($sha_zip)" || fail "proxy.golang.org LICENSE differs ($sha_zip)"
  curl -sSf https://sum.golang.org/lookup/github.com/sentinel-official/dvpn-node@v0.7.1 2>/dev/null | grep 'h1:' | sed 's/^/      sum.golang.org: /'
else info "proxy.golang.org unreachable; skipped"; fi
# The status decides: an error page served while the archive is down says nothing about the
# commit, and a 404 is the archive's answer that it does not hold it.
for s in "$FORK" "$RELICENSE"; do
  rm -f "$T/swh.json"
  code=$(curl -s -o "$T/swh.json" -w '%{http_code}' "https://archive.softwareheritage.org/api/1/revision/$s/" 2>/dev/null)
  j=$(cat "$T/swh.json" 2>/dev/null)
  case "$code" in
    200)
      if printf '%s' "$j" | grep -q "\"id\":\"$s\""; then
        dt=$(printf '%s' "$j" | grep -o '"date":"[^"]*"' | head -1 | cut -d'"' -f4); msg=$(printf '%s' "$j" | grep -o '"message":"[^"]*"' | head -1 | cut -d'"' -f4)
        pass "Software Heritage archives $s ($dt, \"${msg%\\n}\")"
      else fail "Software Heritage: unexpected response for $s"; fi ;;
    404) fail "Software Heritage does not archive $s" ;;
    429) info "Software Heritage rate-limited; re-run later for $s" ;;
    *) info "Software Heritage unreachable for $s (HTTP ${code:-none})" ;;
  esac
done
echo
[ $fail = 0 ] && echo "RESULT: all checks passed" || echo "RESULT: FAILURES PRESENT"
exit $fail
