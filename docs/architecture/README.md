# Architecture diagram

`dvpnd.architecture.json` is the typed source for the runtime architecture
map: the client handshake path from the HTTP API through session admission to
the VPN service and the host data plane, the three node jobs that report usage
and status, the chain client that is the only thing talking to the network,
and the start-up probes that geolocate the node and measure its bandwidth. It
is the only file here that is committed.

The rendered viewer is a generated artifact and is **gitignored**: it is about
800 KB, almost all of it the vendored Archify template, and it is rewritten
whole on every render, so it would bloat history with diffs nobody can read.

## Rendering it

The renderer is [Archify](https://github.com/tt-a1i/archify), installed
separately, not a dependency of this repo. Install it, point `ARCHIFY_HOME` at
the directory that holds its `bin/archify.mjs`, and from the repo root, with
Node.js:

```bash
node "$ARCHIFY_HOME/bin/archify.mjs" deliver architecture \
  docs/architecture/dvpnd.architecture.json \
  docs/architecture/dvpnd-architecture.html \
  --quality showcase --repo-root .
```

Open the HTML in a browser: it is self-contained, switches dark/light, carries
three guided views (handshake path, usage loop, start-up probes) and exports
PNG/SVG. `deliver` refuses to write an artifact that fails the showcase
checks, so a hand-edited JSON cannot silently produce a diagram with
overlapping labels or an edge drawn through a component.

## Keeping it true

Components carry `sources` pins into real files, and the validator reads those
blobs at a commit. `scripts/check-architecture-doc.sh` (also `make
check-architecture`) re-pins to HEAD and validates, so a renamed or deleted
package fails the check. It skips, without failing, when `ARCHIFY_HOME` does
not name the renderer: CI and a fresh clone stay green without it. What it
cannot catch is a package whose *meaning* changed, a new component nobody
drew, or an edge that no longer exists. Those are the author's job, and
`CONTRIBUTING.md` carries the rule.

`meta.repository.revision` in the committed JSON records the commit the
diagram was last verified against. Update it when you update the diagram; the
check itself always validates at HEAD and leaves the file alone.

It is eleven components on purpose: an orientation map, not an inventory. The
six protocol packages under `services/` are one box, because the node runs
exactly one of them and the rest of the daemon does not know which;
`docs/protocols.md` is where they differ.
