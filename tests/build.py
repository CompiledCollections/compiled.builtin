# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""Build and test the compiled.builtin modules. Run from anywhere:

    python3 tests/build.py build [--platform linux/amd64 ...] [--module ping ...]
    python3 tests/build.py unit
    python3 tests/build.py sanity
    python3 tests/build.py integration [TARGET ...]
    python3 tests/build.py test
    python3 tests/build.py golden
    python3 tests/build.py testdocs [--check]
    python3 tests/build.py clean

build        Writes stripped, UPX-packed binaries to plugins/modules, the same
             way the action plugin compiles at runtime. Only linux/amd64 is
             shipped; build other platforms only to try them out locally.
unit         go vet and go test. The Python-compatibility helpers (shlex,
             glob, $VAR expansion, regex translation...) are checked against
             results recorded from CPython in internal/ansible/testdata.
sanity       ansible-test sanity.
integration  Builds, then runs ansible-test integration: every case runs
             ansible.builtin.X and compiled.builtin.X with the same arguments
             and asserts the results (and files left behind) are the same.
test         build, unit, sanity and integration.
golden       Re-records the CPython results for the unit tests.
testdocs     Writes tests/README.md, the list of every test case, from the
             test files. --check fails if it is out of date; test runs that.
"""

from __future__ import annotations

import argparse
import glob
import importlib.util
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
COLLECTION_ROOT = os.path.dirname(HERE)
SHIPPED_PLATFORMS = ['linux/amd64']

# Load the build code straight from its file: it has no Ansible imports, so
# building doesn't need the collection installed or ansible-core importable.
_spec = importlib.util.spec_from_file_location(
    'go_build', os.path.join(COLLECTION_ROOT, 'plugins', 'plugin_utils', '_go_build.py'))
go_build = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(go_build)


def cmd_build(args):
    platforms = [p.replace('/', '_') for p in args.platform or SHIPPED_PLATFORMS]
    modules = args.module or go_build.modules()

    for platform in platforms:
        if platform in go_build.UPX_PLATFORMS and not go_build.can_pack(platform):
            # Shipped binaries must always be packed; the runtime path only warns.
            sys.exit('upx is not installed; install it to build packed binaries')

    for module in modules:
        for platform in platforms:
            dest = os.path.join(go_build.MODULES_DIR, '%s_%s' % (module, platform))
            try:
                go_build.build(module, platform, dest)
            except go_build.BuildError as e:
                sys.exit('%s %s: %s' % (module, platform, e))
            packed = 'packed' if go_build.can_pack(platform) else 'not packed, unsupported by upx'
            print('%-40s %6.0f KiB  (%s)' % (os.path.relpath(dest, COLLECTION_ROOT),
                                             os.path.getsize(dest) / 1024, packed))


def cmd_unit(args):
    run(['go', 'vet', './...'], cwd=COLLECTION_ROOT)
    run(['go', 'test', './...'], cwd=COLLECTION_ROOT)


def cmd_sanity(args):
    # validate-modules can't read binaries. In a git checkout ansible-test
    # doesn't see them (they're gitignored); otherwise move them out of the
    # way while it runs.
    visible = [path for path in glob.glob(os.path.join(go_build.MODULES_DIR, '*_*_*'))
               if not os.path.basename(path).endswith(('.yml', '.meta')) and not git_ignored(path)]
    with tempfile.TemporaryDirectory() as aside:
        for path in visible:
            shutil.move(path, aside)
        try:
            run(['ansible-test', 'sanity', '--local'], cwd=COLLECTION_ROOT)
        finally:
            for path in visible:
                shutil.move(os.path.join(aside, os.path.basename(path)), path)


def git_ignored(path):
    """True if path is in a git checkout that ignores it."""
    if shutil.which('git') is None:
        return False
    return subprocess.run(['git', 'check-ignore', '-q', path], cwd=COLLECTION_ROOT, capture_output=True,
                          check=False).returncode == 0


def cmd_integration(args):
    # Shipped binaries win over the source, so never test stale ones.
    cmd_build(argparse.Namespace(platform=None, module=None))
    with tempfile.TemporaryDirectory() as tmp:
        # Keep runtime compiles out of the user's cache.
        env = dict(os.environ, COMPILED_BUILTIN_CACHE_DIR=os.path.join(tmp, 'cache'))
        run(['ansible-test', 'integration', '--local'] + args.target, cwd=COLLECTION_ROOT, env=env)


def cmd_test(args):
    cmd_testdocs(argparse.Namespace(check=True))
    cmd_unit(args)
    cmd_sanity(args)
    cmd_integration(argparse.Namespace(target=[]))


def ansible_python():
    """The Python interpreter ansible-core runs under, from ansible --version."""
    if shutil.which('ansible') is None:
        sys.exit('ansible is not installed')
    out = subprocess.run(['ansible', '--version'], capture_output=True, text=True, stdin=subprocess.DEVNULL, check=False).stdout
    m = re.search(r'python version = .*\((/[^)]+)\)\s*$', out, re.M)
    if not m:
        sys.exit('could not find the Python ansible runs under in `ansible --version`')
    return m.group(1)


def cmd_golden(args):
    # Standard library behaviour: any Python 3 will do.
    testdata = os.path.join(COLLECTION_ROOT, 'internal', 'ansible', 'testdata')
    with open(os.path.join(testdata, 'python.json'), 'w') as out:
        run([sys.executable, 'generate.py'], cwd=testdata, stdout=out)
    # uri compares with ansible-core's own code, so use Ansible's Python.
    uri = os.path.join(COLLECTION_ROOT, 'modules_src', 'uri')
    with open(os.path.join(uri, 'testdata', 'python.json'), 'w') as out:
        run([ansible_python(), 'testdata/generate.py'], cwd=uri, stdout=out)


TESTDOCS = os.path.join(HERE, 'README.md')

TESTDOCS_INTRO = """\
# Tests

<!-- Generated by `python3 tests/build.py testdocs` from the test files. Don't edit by hand. -->

Run everything with `python3 tests/build.py test`, or one layer with `unit`,
`sanity` or `integration [TARGET ...]`. See the collection's README for what
each layer needs.

- [Unit tests](#unit-tests)
- [Sanity tests](#sanity-tests)
- [Integration tests](#integration-tests)
{targets}

## Unit tests

`go test ./...` checks the Go versions of the Python behaviour the modules
rely on. Most compare against results recorded from CPython, and for uri from
ansible-core's own uri code, in `testdata/python.json` files (each made by the
`generate.py` next to it; re-record them with `python3 tests/build.py golden`).

{unit}

## Sanity tests

`ansible-test sanity`: documentation, action plugins and Python code. The
`<module>.meta` name placeholders are skipped (`tests/sanity/ignore-*.txt`):
they aren't modules. validate-modules can't read the binaries either: in a
git checkout ansible-test doesn't see them (they're gitignored), and
otherwise `sanity` moves them aside while it runs.

## Integration tests

`ansible-test integration`, one target per module in
`tests/integration/targets/`. Each case runs `ansible.builtin.<module>` and
`compiled.builtin.<module>` with the same arguments and asserts that the
results are identical, apart from timing values. For lineinfile, file and
copy both start from the same files, diff mode is on, and the files left
behind must also be identical: content, mode, owner, group, link count,
times, symlinks and backups (times the run sets to now, and access times of
symlinks, which move whenever they're read, aren't compared). Cases marked
*fails only* just check that both fail, because the error text comes from
Python's or Go's own libraries. Cases marked *not compared before
ansible-core X* (or *Python X* on the target) still run there, but older
builtins, or the urllib and ssl of older Pythons they rely on, behave
differently; the compiled modules always behave like the current builtin.

To add a case, add an entry to the target's `vars/main.yml` and run
`python3 tests/build.py testdocs`.
"""

CASE_LABELS = {
    'content': 'file', 'content_b64': 'file bytes', 'args': 'args', 'environment': 'environment',
    'file_mode': 'file mode', 'symlink': 'through a symlink',
}


def code(value):
    fence = '``' if '`' in value else '`'
    return '%s%s%s' % (fence, value, fence)


def case_line(name, keys):
    parts = []
    for key, value in keys:
        if key == 'compare' and value == 'failed':
            parts.append('*fails only*')
        elif key == 'compare' and "'failed' if" in value:
            parts.append('*as root, only checks that both fail or both succeed*')
        elif key == 'check_mode' and value == 'true':
            parts.append('check mode')
        elif key == 'diff' and value == 'false':
            parts.append('diff mode off')
        elif key == 'no_path':
            parts.append('no path given')
        elif key == 'symlink':
            parts.append(CASE_LABELS[key])
        elif key in ('builtin_differs', 'python_differs'):
            m = re.match(r"""\{before: "([^"]+)", why: '(.*)'\}$""", value)
            what = 'ansible-core' if key == 'builtin_differs' else 'Python (on the target)'
            parts.append('*not compared before %s %s: %s*' % (what, m.group(1), m.group(2).replace("''", "'"))
                         if m else '%s %s' % (key, code(value)))
        else:
            parts.append('%s %s' % (CASE_LABELS.get(key, key), code(value)))
    return '- %s: %s' % (name, ', '.join(parts)) if parts else '- %s' % name


def target_cases(target):
    """(section, [lines]) for the cases in a target's vars/main.yml, using
    its comments as section names."""
    path = os.path.join(HERE, 'integration', 'targets', target, 'vars', 'main.yml')
    sections, case, in_cases = [], None, False

    def flush():
        if case:
            if not sections:
                sections.append(('Cases', []))
            sections[-1][1].append(case_line(*case))

    with open(path) as f:
        for line in f:
            line = line.rstrip('\n')
            if re.match(r'^\w+_cases:$', line):
                in_cases = True
                continue
            if not in_cases:
                continue
            if line and not line.startswith(' '):
                break
            if m := re.match(r'^  # (.*)', line):
                flush()
                case = None
                sections.append((m.group(1), []))
            elif m := re.match(r'^  - name: (.*)', line):
                flush()
                case = (m.group(1), [])
            elif (m := re.match(r'^    (\w+): (.*)', line)) and case:
                case[1].append((m.group(1), m.group(2)))
    flush()
    return sections


def dedicated_checks(target):
    """Task files main.yml includes besides compare.yml, with their leading comment."""
    tasks = os.path.join(HERE, 'integration', 'targets', target, 'tasks')
    with open(os.path.join(tasks, 'main.yml')) as f:
        included = re.findall(r'include_tasks: (\S+\.yml)', f.read())
    out = []
    for name in included:
        if name == 'compare.yml':
            continue
        with open(os.path.join(tasks, name)) as f:
            comment = []
            for line in f:
                if not line.startswith('#'):
                    break
                comment.append(line[1:].strip())
        out.append('- `tasks/%s`: %s' % (name, ' '.join(comment) or '(no description)'))
    return out


def unit_section():
    lines = []
    test_files = glob.glob(os.path.join(COLLECTION_ROOT, 'internal', '*', '*_test.go'))
    test_files += glob.glob(os.path.join(COLLECTION_ROOT, go_build.SOURCE_DIR, '*', '*_test.go'))
    for path in sorted(test_files):
        with open(path) as f:
            tests = re.findall(r'^func (Test\w+)\(', f.read(), re.M)
        lines.append('`%s`: %s' % (os.path.relpath(path, COLLECTION_ROOT), ', '.join('`%s`' % t for t in tests)))
    recorded = []
    for path in sorted(glob.glob(os.path.join(COLLECTION_ROOT, '*', '*', 'testdata', 'python.json'))):
        with open(path) as f:
            golden = json.load(f)
        counts = ', '.join('%s (%d)' % (k, len(v)) for k, v in golden.items() if isinstance(v, list))
        recorded.append('- `%s`: %s' % (os.path.relpath(path, COLLECTION_ROOT), counts))
    return '\n'.join(['- ' + line for line in lines] + ['', 'Results recorded from CPython (and, for uri, ansible-core), by kind:', ''] + recorded)


def leading_comment(path):
    """The comment lines a file starts with, joined."""
    comment = []
    with open(path) as f:
        for line in f:
            if not line.startswith('#'):
                break
            comment.append(line[1:].strip())
    return ' '.join(comment)


def render_testdocs():
    root = os.path.join(HERE, 'integration', 'targets')
    targets = sorted(d for d in os.listdir(root) if os.path.isfile(os.path.join(root, d, 'vars', 'main.yml')))
    helpers = sorted(d for d in os.listdir(root) if d.startswith('setup_'))
    # Targets for modules with no builtin to compare with: tasks/cases.yml
    # runs the module and checks what it did.
    checked = sorted(d for d in os.listdir(root) if os.path.isfile(os.path.join(root, d, 'tasks', 'cases.yml')))
    toc = '\n'.join('  - [%s](#%s)' % (t, t) for t in sorted(targets + checked))
    out = [TESTDOCS_INTRO.format(targets=toc, unit=unit_section())]
    for target in targets:
        sections = target_cases(target)
        total = sum(len(cases) for dummy, cases in sections)
        out.append('### %s\n\n%d side-by-side cases in `tests/integration/targets/%s/vars/main.yml`.\n' % (target, total, target))
        for title, cases in sections:
            out.append('#### %s\n\n%s\n' % (title, '\n'.join(cases)))
        checks = dedicated_checks(target)
        if checks:
            out.append('#### Dedicated checks\n\n%s\n' % '\n'.join(checks))
    for target in checked:
        with open(os.path.join(root, target, 'tasks', 'cases.yml')) as f:
            steps = [m.group(1) for m in re.finditer(r'^- name: (.*)$', f.read(), re.M)]
        out.append('### %s\n\n%s\n\n%d steps in `tests/integration/targets/%s/tasks/cases.yml`:\n' % (
            target, leading_comment(os.path.join(root, target, 'tasks', 'main.yml')), len(steps), target))
        out.append('\n'.join('- %s' % step for step in steps) + '\n')
    if helpers:
        out.append('### Helper targets\n')
        for helper in helpers:
            with open(os.path.join(root, helper, 'tasks', 'main.yml')) as f:
                comment = []
                for line in f:
                    if not line.startswith('#'):
                        break
                    comment.append(line[1:].strip())
            out.append('- `%s`: %s' % (helper, ' '.join(comment)))
        out.append('')
    return '\n'.join(out)


def cmd_testdocs(args):
    text = render_testdocs()
    if args.check:
        try:
            with open(TESTDOCS) as f:
                current = f.read()
        except FileNotFoundError:
            current = None
        if current != text:
            sys.exit('tests/README.md is out of date; run: python3 tests/build.py testdocs')
        return
    with open(TESTDOCS, 'w') as f:
        f.write(text)
    print('wrote', os.path.relpath(TESTDOCS, COLLECTION_ROOT))


def cmd_clean(args):
    for module in go_build.modules():
        for path in glob.glob(os.path.join(go_build.MODULES_DIR, '%s_*_*' % module)):
            os.remove(path)
            print('removed', os.path.relpath(path, COLLECTION_ROOT))


def run(argv, **kwargs):
    if shutil.which(argv[0]) is None:
        sys.exit('%s is not installed' % argv[0])
    print('+', ' '.join(argv))
    if subprocess.run(argv, check=False, **kwargs).returncode != 0:
        sys.exit(1)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest='command', required=True)

    p = sub.add_parser('build', help='compile modules into plugins/modules')
    p.add_argument('--platform', action='append', metavar='GOOS/GOARCH',
                   help='platform to build, repeatable (default: %s)' % ' '.join(SHIPPED_PLATFORMS))
    p.add_argument('--module', action='append', help='module to build, repeatable (default: all)')
    p.set_defaults(func=cmd_build)

    p = sub.add_parser('unit', help='go vet and go test')
    p.set_defaults(func=cmd_unit)

    p = sub.add_parser('sanity', help='ansible-test sanity')
    p.set_defaults(func=cmd_sanity)

    p = sub.add_parser('integration', help='compare each module with ansible.builtin (ansible-test integration)')
    p.add_argument('target', nargs='*', help='targets to run (default: all)')
    p.set_defaults(func=cmd_integration)

    p = sub.add_parser('test', help='unit, sanity and integration')
    p.set_defaults(func=cmd_test)

    p = sub.add_parser('golden', help='re-record CPython results for the unit tests')
    p.set_defaults(func=cmd_golden)

    p = sub.add_parser('testdocs', help='write tests/README.md, the list of test cases')
    p.add_argument('--check', action='store_true', help='fail if tests/README.md is out of date')
    p.set_defaults(func=cmd_testdocs)

    p = sub.add_parser('clean', help='remove compiled binaries from plugins/modules')
    p.set_defaults(func=cmd_clean)

    args = parser.parse_args()
    args.func(args)


if __name__ == '__main__':
    main()
