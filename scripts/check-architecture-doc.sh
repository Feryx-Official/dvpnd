#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Re-checks docs/architecture/dvpnd.architecture.json against the code it
# claims to describe (`make check-architecture`).
#
# The JSON is the typed source for the runtime architecture map (see
# docs/architecture/README.md). Its components carry `sources` pins into real
# files, and Archify's validator reads those blobs AT A COMMIT, so re-pinning
# to HEAD and validating turns "a package this diagram names was renamed, moved
# or deleted" into a red check instead of a diagram that quietly stops being
# true. A stale architecture picture is worse than none.
#
# What it cannot catch: a package that changed MEANING without changing its
# path, a new component nobody drew, or an edge that no longer exists. Those
# are on the author, which is why CLAUDE.md carries the rule as well.
#
# SKIPS, never fails, when Archify is not installed. It is an agent skill
# living in the user's home (`npx skills add tt-a1i/archify -g`), not a repo
# dependency, so CI and a fresh clone must stay green without it.
#
# The pinned revision in the committed JSON is left alone: it records the
# commit the diagram was last verified against. This check always validates at
# HEAD, using a temporary copy.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC="$ROOT/docs/architecture/dvpnd.architecture.json"
SKILL="${ARCHIFY_HOME:-$HOME/.claude/skills/archify}"

skip() { echo "check-architecture-doc: skipped ($1)"; exit 0; }

[ -f "$DOC" ] || skip "no $DOC"
command -v node >/dev/null 2>&1 || skip "no node on PATH"
[ -f "$SKILL/bin/archify.mjs" ] || skip "Archify skill not installed at $SKILL"

# No network from a check run: this also stops the skill's update reminder.
export ARCHIFY_UPDATE_CHECK_DISABLED=1

HEAD_REV="$(git -C "$ROOT" rev-parse HEAD)"
# Archify also requires meta.repository.url to equal this checkout's origin.
# That is environment specific (an ssh alias here, an HTTPS clone elsewhere)
# and not what this check is for, so the temp copy takes the LOCAL origin. What
# is being verified is that the pinned paths still exist at HEAD, not where the
# tree was cloned from.
ORIGIN="$(git -C "$ROOT" remote get-url origin 2>/dev/null || true)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
PINNED="$TMP/architecture.json"

node -e '
  const fs = require("fs");
  const [src, dest, revision, origin] = process.argv.slice(1);
  const doc = JSON.parse(fs.readFileSync(src, "utf8"));
  doc.meta.repository = { ...doc.meta.repository, revision };
  if (origin) doc.meta.repository.url = origin;
  fs.writeFileSync(dest, JSON.stringify(doc, null, 2));
' "$DOC" "$PINNED" "$HEAD_REV" "$ORIGIN"

OUT="$TMP/validate.log"
if node "$SKILL/bin/archify.mjs" validate architecture "$PINNED" \
     --quality showcase --repo-root "$ROOT" >"$OUT" 2>&1; then
  cat "$OUT"
  exit 0
fi

cat "$OUT" >&2

# The validator addresses components by index (/components/8/sources/0/path).
# Translate that back to the id and path an author can act on.
node -e '
  const fs = require("fs");
  const [doc, log] = process.argv.slice(1).map((f) => fs.readFileSync(f, "utf8"));
  const components = JSON.parse(doc).components;
  const seen = new Set();
  for (const m of log.matchAll(/\/components\/(\d+)\/sources\/(\d+)\/path/g)) {
    const component = components[Number(m[1])];
    const source = component?.sources?.[Number(m[2])];
    if (!source || seen.has(m[0])) continue;
    seen.add(m[0]);
    console.error(`  component "${component.id}" (${component.label}) pins ${source.path}`);
  }
' "$DOC" "$OUT" >&2 || true

cat >&2 <<MSG

check-architecture-doc: FAILED at HEAD ($HEAD_REV).

  docs/architecture/dvpnd.architecture.json no longer describes this tree.
  A source pin usually means a file moved; a layout error means the JSON was
  edited by hand without re-running the validator.

  Fix the JSON, then re-render and re-pin:

    node ~/.claude/skills/archify/bin/archify.mjs deliver architecture \\
      docs/architecture/dvpnd.architecture.json \\
      docs/architecture/dvpnd-architecture.html \\
      --quality showcase --repo-root .

MSG
exit 1
