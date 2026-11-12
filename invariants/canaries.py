#!/usr/bin/env python3
"""Mutation canaries.

A canary is one deliberate regression - drop the refund call, let the root helper
accept 0.0.0.0 - and the commands that must catch it. Each is applied to a private
copy of the working tree (uncommitted changes included) and its commands run there;
the run must FAIL. The same commands run once on a copy without the mutation, where
they must pass: a command that fails anyway would count as catching any mutation.
A canary the suite survives means nothing guards that rule any more.

It reads canaries.json from its own directory; the project's suite doc describes the keys.
Python 3.8+, standard library only.

    python3 invariants/canaries.py               # run all; exit 0 when every canary is caught
    python3 invariants/canaries.py --only REL-1  # just the canaries whose name contains it
    python3 invariants/canaries.py --list        # names only
    --config PATH / --root PATH                  # as in registry.py
    -h, --help                                   # this text; any other argument exits 2, running nothing
"""
import hashlib
import json
import os
import queue
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.abspath(__file__))
CONFIG = os.path.join(HERE, 'canaries.json')


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


def repo_root(config_dir):
    try:
        out = subprocess.run(['git', 'rev-parse', '--show-toplevel'], cwd=config_dir, capture_output=True, text=True, check=True)
        return out.stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return os.path.dirname(config_dir)


def text(v):
    """`find` / `replace` may be a string or a list of lines (joined with newlines)."""
    return '\n'.join(v) if isinstance(v, list) else v


def tree_files(root):
    out = subprocess.run(['git', 'ls-files', '-co', '--exclude-standard', '-z'], cwd=root, capture_output=True, check=True)
    return sorted({f for f in out.stdout.decode('utf-8', 'replace').split('\0') if f and os.path.isfile(os.path.join(root, f))})


def under(path, prefixes):
    return any(path == p or path.startswith(p.rstrip('/') + '/') for p in prefixes)


def make_template(root, links):
    """Copy every tracked or untracked-unignored file; link the heavy trees instead."""
    template = tempfile.mkdtemp(prefix='canary-template-')
    for f in tree_files(root):
        if under(f, links):
            continue
        dest = os.path.join(template, f)
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        shutil.copy2(os.path.join(root, f), dest, follow_symlinks=False)
    for link in links:
        src = os.path.join(root, link)
        if os.path.exists(src):
            dest = os.path.join(template, link)
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            os.symlink(src, dest)
    # Tests list the tree with git, as tree_files does, so the copy is a repository too, with
    # no commit: outside one they fail on any copy.
    subprocess.run(['git', '-c', 'init.defaultBranch=main', 'init', '-q', template], capture_output=True, check=True)
    return template


def privatize(copy, rel):
    """When the canary's file sits under a LINKED tree, written through the link the
    mutation would land in the real checkout. Each directory on the way down becomes
    real (its other entries still links), and the file itself a copy."""
    cur = copy
    parts = rel.split('/')
    for i, part in enumerate(parts):
        p = os.path.join(cur, part)
        if os.path.islink(p):
            real = os.path.realpath(p)
            os.unlink(p)
            if i == len(parts) - 1:
                shutil.copy2(real, p)
                return
            os.mkdir(p)
            for e in os.listdir(real):
                os.symlink(os.path.join(real, e), os.path.join(p, e))
        cur = p


def digest(path):
    with open(path, 'rb') as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def fresh_copy(template):
    copy = tempfile.mkdtemp(prefix='canary-')
    os.rmdir(copy)
    shutil.copytree(template, copy, symlinks=True)
    return copy


def run_steps(steps, copy, root, cfg, slot):
    """Runs a canary's commands in `copy` until one fails. Returns (status, command, output), the
    status 'pass', 'fail' or 'timeout' and the command the one that ended the run."""
    env = {k: v for k, v in os.environ.items() if k not in set(cfg.get('unsetEnv', ['NODE_TEST_CONTEXT']))}
    fill = lambda s: s.replace('{slot}', str(slot)).replace('{root}', root).replace('{copy}', copy)
    env.update({k: fill(v) for k, v in cfg.get('env', {}).items()})
    output = ''
    for step in steps:
        cmd, cwd = (step, '.') if isinstance(step, str) else (step['cmd'], step.get('cwd', '.'))
        # A session of its own, so a timeout stops what the shell started too (go test and the
        # test binary), not the shell alone.
        p = subprocess.Popen(fill(cmd), shell=True, cwd=os.path.join(copy, cwd), env=env, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, text=True, start_new_session=True)
        try:
            out, err = p.communicate(timeout=cfg.get('timeout', 900))
        except subprocess.TimeoutExpired:
            try:
                os.killpg(p.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            p.communicate()
            return 'timeout', cmd, output
        output += out + err
        if p.returncode != 0:
            return 'fail', cmd, output
    return 'pass', None, output


def run_bare(steps, template, root, cfg, slots):
    """A canary's commands on a copy without its mutation."""
    slot = slots.get()
    copy = None
    try:
        copy = fresh_copy(template)
        return run_steps(steps, copy, root, cfg, slot)
    finally:
        if copy:
            shutil.rmtree(copy, ignore_errors=True)
        slots.put(slot)


def run_canary(c, template, root, cfg, slots):
    slot = slots.get()
    copy = None
    started = time.time()
    try:
        copy = fresh_copy(template)
        privatize(copy, c['file'])
        path = os.path.join(copy, c['file'])
        with open(path, encoding='utf-8') as fh:
            src = fh.read()
        find, replace = text(c['find']), text(c['replace'])
        hits = src.count(find)
        if hits != 1:
            return 'RE-AIM', f'its anchor matches {hits} times in {c["file"]} (must be exactly 1): re-aim it at the moved code, never delete it'
        with open(path, 'w', encoding='utf-8') as fh:
            fh.write(src.replace(find, replace))
        status, cmd, output = run_steps(c['run'], copy, root, cfg, slot)
        if status == 'timeout':
            return 'TIMEOUT', f'`{cmd}` ran past {cfg.get("timeout", 900)} s: bound the waits in the tests it runs'
        if status == 'fail':
            expect = c.get('expect')
            if expect and not re.search(expect, output):
                return 'WRONG RED', (f'`{cmd}` failed, but its output never matches /{expect}/: the mutation probably '
                                     f'broke the build rather than tripping the rule\'s test\n' + output[-1500:])
            return 'caught', f'by `{cmd}` in {time.time() - started:.0f} s'
        return 'SURVIVED', 'every command passed with the rule broken: nothing guards it any more\n' + output[-1500:]
    finally:
        if copy:
            shutil.rmtree(copy, ignore_errors=True)
        slots.put(slot)


def judge(cfg, bare, mutated):
    """The canary's verdict: its own, once its commands passed without the mutation."""
    status, cmd, output = bare.result()
    if status == 'timeout':
        return 'TIMEOUT', f'`{cmd}` ran past {cfg.get("timeout", 900)} s without the mutation: bound the waits in the tests it runs'
    if status == 'fail':
        return 'RED BEFORE', (f'`{cmd}` fails without the mutation too, so its failure proves nothing: make it pass on '
                              f'a clean copy, never delete the canary\n' + output[-1500:])
    return mutated.result()


def main(argv):
    if '-h' in argv or '--help' in argv:
        print(__doc__.strip())
        return 0
    bad = bad_argument(argv, {'--only': True, '--list': False, '--config': True, '--root': True})
    if bad:
        print(f'canaries: unknown argument, or one missing its value: {bad!r} (see --help)', file=sys.stderr)
        return 2
    config = os.path.abspath(option(argv, '--config', CONFIG))
    root = os.path.abspath(option(argv, '--root', '') or repo_root(os.path.dirname(config)))
    with open(config, encoding='utf-8') as fh:
        cfg = json.load(fh)
    canaries = cfg.get('canaries', [])
    only = option(argv, '--only', None)
    if only:
        canaries = [c for c in canaries if only in c['name']]
    if '--list' in argv:
        for c in canaries:
            print(c['name'])
        return 0
    names = [c['name'] for c in canaries]
    dupes = sorted({n for n in names if names.count(n) > 1})
    if dupes:
        print(f'canaries: duplicate names {dupes}', file=sys.stderr)
        return 2
    if not canaries:
        print('canaries: none to run', file=sys.stderr)
        return 2 if not only else 1

    links = cfg.get('link', [])
    targets = {c['file'] for c in canaries if os.path.exists(os.path.join(root, c['file']))}
    before = {f: digest(os.path.join(root, f)) for f in targets}
    workers = cfg.get('concurrency') or max(1, (os.cpu_count() or 2) // 2)
    slots = queue.Queue()
    for s in range(workers):
        slots.put(s)

    template = make_template(root, links)
    started = time.time()
    try:
        with ThreadPoolExecutor(max_workers=workers) as pool:
            bare = {}
            for c in canaries:
                key = json.dumps(c['run'], sort_keys=True)
                if key not in bare:
                    bare[key] = pool.submit(run_bare, c['run'], template, root, cfg, slots)
            futures = [(c, bare[json.dumps(c['run'], sort_keys=True)], pool.submit(run_canary, c, template, root, cfg, slots))
                       for c in canaries]
            results = []
            for c, b, f in futures:
                try:
                    verdict, detail = judge(cfg, b, f)
                except Exception as e:  # a broken canary entry is a failure, not a crash of the run
                    verdict, detail = 'ERROR', f'{type(e).__name__}: {e}'
                results.append((c, verdict, detail))
                print(f'{verdict:<9} {c["name"]}' + (f'\n          {detail}' if verdict != 'caught' else ''), flush=True)
    finally:
        shutil.rmtree(template, ignore_errors=True)

    changed = [f for f in targets if digest(os.path.join(root, f)) != before[f]]
    if changed:
        print(f'\nTHE REAL CHECKOUT CHANGED during the run: {changed}. A link let a mutation through; '
              'check "link" in canaries.json, and restore these files from git.', file=sys.stderr)
        return 3
    bad = [r for r in results if r[1] != 'caught']
    print(f'\n{len(results) - len(bad)}/{len(results)} canaries caught in {time.time() - started:.0f} s')
    return 1 if bad else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
