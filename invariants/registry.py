#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""The invariant registry.

Every rule the docs state carries an ID where it is stated (`**[REL-1] Refund on any
failure.**`), and every test that pins a rule carries that ID in its title or in the
comment directly above it. This script makes that link mechanical: delete the last
test for a rule, or add a rule with no test and no recorded reason, and it fails.

It reads config.json from its own directory: which docs define rules, which files are
tests and in what language, which test tiers some gate runs, where status.json lives.
The project's suite doc describes the keys. Run it from the project's test command so
the suite goes red with it. Python 3.8+, standard library only.

    python3 invariants/registry.py            # check; exit 0 green, 1 red, 2 bad config
    python3 invariants/registry.py --report   # also print every rule and what pins it
    --config PATH   another config.json (its directory replaces this script's)
    --root PATH     the repository root (default: the git top level of the config's directory)
    -h, --help      this text; any other argument exits 2, checking nothing
"""
import ast
import json
import os
import re
import subprocess
import sys
from collections import defaultdict

ID_RE = re.compile(r'\[([A-Z]+-\d+)\]')
# A definition is an ID that opens a bold run: `**[REL-1] …**` or a bare `**[NT-2]**`.
# A plain `[REL-1]` anywhere else is a reference to it.
DEF_RE = re.compile(r'\*\*\[([A-Z]+-\d+)\]')
SKIP_DIRS = {'.git', 'node_modules', 'target', 'vendor', '.venv', 'venv', 'dist', 'build', 'out', '__pycache__'}

HERE = os.path.dirname(os.path.abspath(__file__))
CONFIG = os.path.join(HERE, 'config.json')


def ids_in(text):
    return ID_RE.findall(text)


# --- files ---------------------------------------------------------------------------

def repo_root(config_dir):
    try:
        out = subprocess.run(['git', 'rev-parse', '--show-toplevel'], cwd=config_dir, capture_output=True, text=True, check=True)
        return out.stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return os.path.dirname(config_dir)


def list_files(root):
    """Tracked and untracked-but-not-ignored files, so ignored build output is never scanned."""
    try:
        out = subprocess.run(['git', 'ls-files', '-co', '--exclude-standard', '-z'], cwd=root,
                             capture_output=True, check=True)
        files = [f for f in out.stdout.decode('utf-8', 'replace').split('\0') if f]
        return sorted({f for f in files if os.path.isfile(os.path.join(root, f))})
    except (OSError, subprocess.CalledProcessError):
        found = []
        for d, dirs, names in os.walk(root):
            dirs[:] = [x for x in dirs if x not in SKIP_DIRS and not x.startswith('.')]
            found += [os.path.relpath(os.path.join(d, n), root).replace(os.sep, '/') for n in names]
        return sorted(found)


def glob_re(pattern):
    """`**/` spans directories (zero or more), `*` and `?` stay inside one."""
    out, i = '', 0
    while i < len(pattern):
        if pattern.startswith('**/', i):
            out, i = out + '(?:.*/)?', i + 3
        elif pattern.startswith('**', i):
            out, i = out + '.*', i + 2
        elif pattern[i] == '*':
            out, i = out + '[^/]*', i + 1
        elif pattern[i] == '?':
            out, i = out + '[^/]', i + 1
        else:
            out, i = out + re.escape(pattern[i]), i + 1
    return re.compile(out + r'\Z')


def matching(files, patterns):
    res = [glob_re(p) for p in patterns]
    return [f for f in files if any(r.match(f) for r in res)]


def read(root, rel):
    with open(os.path.join(root, rel), encoding='utf-8', errors='replace') as fh:
        return fh.read()


# --- test scanners: each yields (id, pins, line) --------------------------------------
# A citation PINS a rule when the test it names actually runs. A skipped, todo or
# ignored test cites the rule without enforcing it.

JS_FNS = 'test|it|describe|suite|context|xit|xtest|xdescribe|fit|fdescribe'
JS_CALL = re.compile(r'(?<![\w$.])(' + JS_FNS + r')((?:\.[A-Za-z_]\w*)*)\s*\(\s*([\'"`])((?:\\.|(?!\3).)*?)\3', re.S)
JS_NOT_RUN = {'skip', 'todo', 'fixme'}


def scan_js(src):
    """node:test, Jest, Vitest, Mocha: `test('[REL-1] …', …)`; a describe title pins for its block."""
    for m in JS_CALL.finditer(src):
        line_start = src.rfind('\n', 0, m.start()) + 1
        prefix = src[line_start:m.start()]
        if '//' in prefix or prefix.lstrip().startswith(('*', '/*')):
            continue  # commented out
        base, mods = m.group(1), m.group(2).split('.')[1:]
        pins = not base.startswith('x') and not JS_NOT_RUN.intersection(mods)
        opts = re.match(r'\s*,\s*\{([^{}]*)\}', src[m.end():m.end() + 400])
        if opts and re.search(r'\b(skip|todo)\b(?!\s*:\s*false)', opts.group(1)):
            pins = False  # node:test's { skip } / { todo } options
        line = src.count('\n', 0, m.start()) + 1
        for i in ids_in(m.group(4)):
            yield i, pins, line


GO_SKIP = re.compile(r'\b\w+\.Skip(?:f|Now)?\(')


def scan_go(src, in_tier=False):
    """`t.Run("[PH-3] …")` names, or the comment block directly above `func TestX` / `func FuzzX`.
    A test function that calls t.Skip, t.Skipf or t.SkipNow anywhere in its body may not run
    under the default `go test`, so its IDs cite without pinning (Go's `todo`). In a declared
    tier (`in_tier`, see go_tier) a skip is the tier's environment guard and the test pins."""
    lines = src.split('\n')
    skipping = set()  # line indexes inside a test function that may skip
    if not in_tier:
        for n, line in enumerate(lines):
            if re.match(r'func (Test|Fuzz)\w*\(', line):
                end = n if line.rstrip().endswith('}') else next(
                    (k for k in range(n + 1, len(lines)) if lines[k] == '}'), len(lines) - 1)
                body = [l for l in lines[n:end + 1] if not l.lstrip().startswith('//')]
                if any(GO_SKIP.search(l) for l in body):
                    skipping.update(range(n, end + 1))
    for n, line in enumerate(lines):
        if re.match(r'func (Test|Fuzz)\w*\(', line):
            j = n - 1
            while j >= 0 and lines[j].lstrip().startswith('//'):
                for i in ids_in(lines[j]):
                    yield i, n not in skipping, j + 1
                j -= 1
        if line.lstrip().startswith('//'):
            continue
        for m in re.finditer(r'\b\w+\.Run\(\s*"((?:\\.|[^"\\])*)"', line):
            for i in ids_in(m.group(1)):
                yield i, n not in skipping, n + 1


# Build tags the toolchain sets on its own: a constraint made only of these (or their
# negations) is built by a plain `go test` on some platform, so it stays in the default tier.
GO_KNOWN_TAGS = set('''aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9
    solaris wasip1 windows zos unix 386 amd64 amd64p32 arm arm64 arm64be armbe loong64 mips mipsle mips64 mips64le
    mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm cgo gc gccgo'''.split())


def go_tier(src, tiers):
    """The tier a Go test file belongs to, from its `//go:build` line: '' when a plain `go test`
    builds it; the tag's name when it needs a custom build tag that config "tiers" declares (a gate
    runs it, e.g. `go test -tags integration`); None when no gate builds it (the tag is undeclared,
    or `ignore`), so nothing in it pins."""
    for line in src.split('\n'):
        s = line.strip()
        if s.startswith('package '):
            break
        if not s.startswith('//go:build '):
            continue
        tags = re.findall(r'(!?)\s*([A-Za-z_][\w.]*)', s[len('//go:build '):])
        custom = [t for neg, t in tags if not neg and t not in GO_KNOWN_TAGS and not re.match(r'go1\.\d+$', t)]
        if not custom:
            return ''
        return next((t for t in custom if t in tiers), None)
    return ''


PY_TEST = re.compile(r'^\s*(?:async\s+)?def\s+test\w*\s*\(|^\s*class\s+Test\w*\b')
PY_SKIP = re.compile(r'@\s*(?:pytest\.mark\.(?:skip|skipif|xfail)|unittest\.(?:skip|skipIf|skipUnless|expectedFailure))\b')


def py_not_run(not_run=()):
    """Decorator / pytestmark names that keep a test from running: skip, skipif, xfail, unittest's
    skips, plus the project's markers its test command deselects (config "notRunMarkers")."""
    marks = '|'.join(['skip', 'skipif', 'xfail'] + [re.escape(m) for m in not_run])
    return re.compile(r'(?:pytest\.)?mark\.(?:' + marks + r')|unittest\.(?:skip|skipIf|skipUnless|expectedFailure)')


def _dotted(node):
    """`pytest.mark.skip(...)` -> 'pytest.mark.skip'; anything else -> its dotted name or ''."""
    if isinstance(node, ast.Call):
        node = node.func
    parts = []
    while isinstance(node, ast.Attribute):
        parts.append(node.attr)
        node = node.value
    if isinstance(node, ast.Name):
        parts.append(node.id)
    return '.'.join(reversed(parts))


def scan_py(src, not_run=()):
    """pytest / unittest: the docstring's text, or the comment block directly above `def test_…`
    or `class Test…` (a class's IDs pin for its methods). A test that does not run cites without
    pinning: skip/skipif/xfail or a deselected marker on the test, on its class, or in a module- or
    class-level `pytestmark`. A `pytest.param(..., marks=xfail)` case does not stop the test's other
    cases, so it still pins. Parsed with `ast`; a file that does not parse falls back to lines."""
    try:
        tree = ast.parse(src)
    except (SyntaxError, ValueError):
        yield from scan_py_lines(src)
        return
    lines = src.split('\n')
    off_re = py_not_run(not_run)

    def off(expr):  # a decorator, or a pytestmark value (one mark or a list of them)
        items = expr.elts if isinstance(expr, (ast.List, ast.Tuple)) else [expr]
        return any(off_re.fullmatch(_dotted(e)) for e in items)

    def marked_off(body):
        for s in body:
            targets = s.targets if isinstance(s, ast.Assign) else [s.target] if isinstance(s, ast.AnnAssign) else []
            if any(isinstance(t, ast.Name) and t.id == 'pytestmark' for t in targets) and s.value is not None and off(s.value):
                return True
        return False

    def visit(body, inherited_off):
        for node in body:
            if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                continue
            is_class = isinstance(node, ast.ClassDef)
            not_running = inherited_off or any(off(d) for d in node.decorator_list) or (is_class and marked_off(node.body))
            if node.name.startswith('Test' if is_class else 'test') or (is_class and any(_dotted(b).endswith('TestCase') for b in node.bases)):
                first = min([d.lineno for d in node.decorator_list] + [node.lineno])
                j = first - 2
                while j >= 0 and lines[j].strip().startswith('#'):  # the comment block directly above
                    for i in ids_in(lines[j]):
                        yield i, not not_running, j + 1
                    j -= 1
                for k in range(first - 1, node.lineno - 1):  # comments among the decorators
                    if lines[k].strip().startswith('#'):
                        for i in ids_in(lines[k]):
                            yield i, not not_running, k + 1
                doc = ast.get_docstring(node, clean=False)
                if doc:
                    for i in ids_in(doc):
                        yield i, not not_running, node.body[0].lineno
            if is_class:
                yield from visit(node.body, not_running)

    yield from visit(tree.body, marked_off(tree.body))


def scan_py_lines(src):
    """Line-based fallback for a test file `ast` cannot parse (e.g. newer syntax than this Python):
    the same places, but only decorators directly on the test are seen."""
    lines = src.split('\n')
    for n, line in enumerate(lines):
        if not PY_TEST.match(line):
            continue
        above, j = [], n - 1
        while j >= 0 and lines[j].strip().startswith(('#', '@')):
            above.append((j, lines[j]))
            j -= 1
        pins = not any(PY_SKIP.search(text) for _, text in above)
        for j, text in above:
            if text.strip().startswith('#'):
                for i in ids_in(text):
                    yield i, pins, j + 1
        # The signature may span lines: it ends at the first line closing on ':' at depth 0.
        depth, k = 0, n
        while k < len(lines):
            code = lines[k].split('#', 1)[0]
            depth += code.count('(') + code.count('[') - code.count(')') - code.count(']')
            if depth <= 0 and code.rstrip().endswith(':'):
                break
            k += 1
        k += 1
        while k < len(lines) and not lines[k].strip():
            k += 1
        if k < len(lines):
            m = re.match(r'\s*[rRuU]?("""|\'\'\'|"|\')', lines[k])
            if m:
                quote, doc, end = m.group(1), [], k
                rest = lines[k][m.end():]
                while True:
                    if quote in rest:
                        doc.append(rest.split(quote, 1)[0])
                        break
                    doc.append(rest)
                    end += 1
                    if end >= len(lines) or len(quote) == 1:
                        break
                    rest = lines[end]
                for i in ids_in('\n'.join(doc)):
                    yield i, pins, k + 1


RS_FN = re.compile(r'^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+\w+')
RS_TEST_ATTR = re.compile(r'#\[\s*(?:[\w:]+::)?(?:test|rstest|test_case|quickcheck)\b')


def scan_rs(src):
    """`#[test]` (and tokio::test, rstest, test_case, proptest's #[test]): the `//` or `///`
    comments among the attributes directly above the fn. `#[ignore]` cites without pinning."""
    lines = src.split('\n')
    for n, line in enumerate(lines):
        if not RS_FN.match(line):
            continue
        block, j = [], n - 1
        while j >= 0 and lines[j].strip().startswith(('#[', '//')):
            block.append((j, lines[j]))
            j -= 1
        if not any(RS_TEST_ATTR.search(text) for _, text in block):
            continue
        pins = not any(re.search(r'#\[\s*ignore\b', text) for _, text in block)
        for j, text in block:
            if text.strip().startswith('//'):
                for i in ids_in(text):
                    yield i, pins, j + 1


SCANNERS = {'js': scan_js, 'ts': scan_js, 'go': scan_go, 'py': scan_py, 'python': scan_py, 'rs': scan_rs, 'rust': scan_rs}


# --- the registry ---------------------------------------------------------------------

def load(root, config):
    with open(config, encoding='utf-8') as fh:
        cfg = json.load(fh)
    for key in ('docs', 'tests'):
        if not cfg.get(key):
            raise ValueError(f'config.json: "{key}" is required')
    for t in cfg['tests']:
        if t.get('lang') not in SCANNERS:
            raise ValueError(f'config.json: test entry {t} needs "lang", one of {sorted(SCANNERS)}')
    files = list_files(root)
    status_path = cfg.get('status', os.path.relpath(os.path.join(os.path.dirname(config), 'status.json'), root))
    status = json.loads(read(root, status_path)) if os.path.exists(os.path.join(root, status_path)) else {}
    for group in ('pending', 'partial', 'manual'):
        status.setdefault(group, {})

    definitions = defaultdict(list)  # id -> [file]
    for f in matching(files, cfg['docs']):
        for m in DEF_RE.finditer(read(root, f)):
            definitions[m.group(1)].append(f)

    tiers = cfg.get('tiers', {})
    if not isinstance(tiers, dict) or any(not isinstance(v, str) or len(v.strip()) < 10 for v in tiers.values()):
        raise ValueError('config.json: "tiers" maps a build tag to the gate that runs it, e.g. '
                         '{"integration": "make check-integration (CI job)"}')

    citations = []  # (id, file:line, pins, tier); tier '' is the default `test` command
    test_files = 0
    for t in cfg['tests']:
        for f in matching(files, [t['glob']]):
            test_files += 1
            scanner = SCANNERS[t['lang']]
            src = read(root, f)
            tier, opts = '', {}
            if scanner is scan_py:
                opts = {'not_run': cfg.get('notRunMarkers', [])}
            elif scanner is scan_go:
                tier = go_tier(src, tiers)
                opts = {'in_tier': bool(tier)}
            for i, pins, line in scanner(src, **opts):
                citations.append((i, f'{f}:{line}', pins and tier is not None, tier or ''))
    for f in matching(files, cfg.get('citesOnly', [])):
        for i in ids_in(read(root, f)):
            citations.append((i, f, False, ''))  # canaries name the rule they break: a citation, not a pin

    return cfg, files, status, status_path, definitions, citations, test_files


def check(root, config):
    cfg, files, status, status_path, definitions, citations, test_files = load(root, config)
    pinned = defaultdict(list)
    tiers_of = defaultdict(set)  # id -> the tiers whose tests pin it ('' = the default test command)
    for i, where, pins, tier in citations:
        if pins:
            pinned[i].append(where)
            tiers_of[i].add(tier)
    homes = cfg.get('homes', {})
    problems = defaultdict(list)

    mins = {'testFiles': 1, 'rules': 1, **cfg.get('minimums', {})}
    if test_files < mins['testFiles'] or len(definitions) < mins['rules'] or len(pinned) < mins.get('pinned', 0):
        problems['the scan found the suite (a walker that silently reads nothing passes everything)'].append(
            f'{test_files} test files, {len(definitions)} rule IDs, {len(pinned)} pinned; minimums {mins}')

    key = 'every ID is defined exactly once, in the file its prefix belongs to'
    for i, where in sorted(definitions.items()):
        prefix = i.split('-')[0]
        if homes and prefix not in homes:
            problems[key].append(f'{i}: unknown prefix (add it to "homes" in config.json and to docs/testing.md)')
        elif homes and any(f != homes[prefix] for f in where):
            problems[key].append(f'{i}: defined in {", ".join(where)}, but {prefix} lives in {homes[prefix]}')
        if len(where) > 1:
            problems[key].append(f'{i}: defined {len(where)} times; IDs are never reused')

    key = 'every rule in the rule docs carries an ID'
    for f in matching(files, cfg.get('ruleDocs', [])):
        for n, line in enumerate(read(root, f).split('\n')):
            # Top-level rules: a bullet or a paragraph that opens in bold.
            if re.match(r'(- )?\*\*', line) and not re.match(r'(- )?\*\*\[[A-Z]+-\d+\]', line):
                problems[key].append(f'{f}:{n + 1}: {line[:70]}  (tag it with the next free ID: '
                                     f'- **[{next_free_id(f, homes, definitions)}] ...**)')

    key = 'every ID a test, canary or status.json cites is defined in a doc'
    cited = [(i, w) for i, w, _, _ in citations] + [(i, status_path) for g in status.values() for i in g]
    for i, where in cited:
        if i not in definitions:
            problems[key].append(f'{i} ({where})')

    key = 'every status entry carries a reason, and an ID sits in one group at most'
    seen = {}
    for group, entries in status.items():
        for i, reason in entries.items():
            if not isinstance(reason, str) or len(reason.strip()) < 10:
                problems[key].append(f'{group}.{i}: give a real reason')
            if i in seen:
                problems[key].append(f'{i}: in both {seen[i]} and {group}')
            seen[i] = group

    key = 'every rule is pinned by a test, or its status.json entry says why not'
    for i in sorted(definitions):
        if i not in pinned and i not in status['pending'] and i not in status['manual']:
            problems[key].append(f'{i}: pin it with a test titled "[{i}] ...", or (with the user\'s OK) record why in {status_path}')

    key = 'pending and manual list only unpinned rules, partial only pinned ones'
    for i in status['pending']:
        if i in pinned:
            problems[key].append(f'{i} is pinned now: remove it from pending')
    for i in status['manual']:
        if i in pinned:
            problems[key].append(f'{i} is pinned now: remove it from manual')
    for i in status['partial']:
        if i not in pinned:
            problems[key].append(f'{i} has no test: partial needs one, so it belongs in pending')

    only = only_in_tier(definitions, tiers_of)
    by_tier = ''.join(f', {n} only by the {t} tier' for t, n in sorted(
        {t: sum(1 for x in only.values() if x == t) for t in set(only.values())}.items()))
    summary = (f'{len(definitions)} rules: {len([i for i in definitions if i in pinned])} pinned '
               f'({len(status["partial"])} of them partially{by_tier}), {len(status["pending"])} pending, '
               f'{len(status["manual"])} manual')
    return problems, summary, definitions, pinned, status, only


def next_free_id(doc, homes, definitions):
    """The ID a new rule in doc takes: its prefix's highest number plus one ('PREFIX-n' when the
    doc's prefix cannot be told, because no "homes" entry names it)."""
    prefixes = [p for p, home in homes.items() if home == doc] or sorted(
        {i.split('-')[0] for i, where in definitions.items() if doc in where})
    if len(prefixes) != 1:
        return 'PREFIX-n'
    used = [int(i.split('-')[1]) for i in definitions if i.split('-')[0] == prefixes[0]]
    return f'{prefixes[0]}-{max(used, default=0) + 1}'


def only_in_tier(definitions, tiers_of):
    """id -> tier for the rules no test of the default command pins, only a slower tier's."""
    return {i: sorted(tiers_of[i])[0] for i in definitions if tiers_of.get(i) and '' not in tiers_of[i]}


def option(argv, name, default):
    return argv[argv.index(name) + 1] if name in argv and argv.index(name) + 1 < len(argv) else default


def bad_argument(argv, flags):
    """The first argument that is not one of `flags` (name -> takes a value), or a flag missing its
    value; None when every argument is known. A typo must not start a full run."""
    i = 0
    while i < len(argv):
        if argv[i] not in flags or (flags[argv[i]] and i + 1 >= len(argv)):
            return argv[i]
        i += 2 if flags[argv[i]] else 1
    return None


def main(argv):
    if '-h' in argv or '--help' in argv:
        print(__doc__.strip())
        return 0
    bad = bad_argument(argv, {'--report': False, '--config': True, '--root': True})
    if bad:
        print(f'registry: unknown argument, or one missing its value: {bad!r} (see --help)', file=sys.stderr)
        return 2
    config = os.path.abspath(option(argv, '--config', CONFIG))
    root = os.path.abspath(option(argv, '--root', '') or repo_root(os.path.dirname(config)))
    try:
        problems, summary, definitions, pinned, status, only = check(root, config)
    except (OSError, ValueError, json.JSONDecodeError) as e:
        print(f'registry: {e}', file=sys.stderr)
        return 2
    if '--report' in argv:
        for i in sorted(definitions, key=lambda x: (x.split('-')[0], int(x.split('-')[1]))):
            where = sorted({w.split(':')[0] for w in pinned.get(i, [])})
            state = next((f'{g}: {status[g][i]}' for g in ('pending', 'partial', 'manual') if i in status[g]), '')
            pins = f'pinned by {", ".join(where)}' if where else 'NOT PINNED'
            if i in only:
                pins += f'  ({only[i]} tier only)'
            print(f'{i:<10} {pins}' + (f'\n{"":<10} {state}' if state else ''))
        print()
    for check_name, items in problems.items():
        print(f'FAIL  {check_name}')
        for item in items:
            print(f'      {item}')
    print(('registry red: ' if problems else 'registry green: ') + summary)
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
