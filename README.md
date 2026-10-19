# Ansible Collection - compiled.builtin

Replacements for `ansible.builtin` modules written in Go and compiled to
static binaries, so they run on targets without Python. See [WHY.md](WHY.md).

## Modules

| Module | Replaces |
| --- | --- |
| `compiled.builtin.ping` | `ansible.builtin.ping` |
| `compiled.builtin.command` | `ansible.builtin.command` |
| `compiled.builtin.shell` | `ansible.builtin.shell` |
| `compiled.builtin.lineinfile` | `ansible.builtin.lineinfile` |
| `compiled.builtin.uri` | `ansible.builtin.uri` |
| `compiled.builtin.slurp` | `ansible.builtin.slurp` |
| `compiled.builtin.stat` | `ansible.builtin.stat` |
| `compiled.builtin.file` | `ansible.builtin.file` |
| `compiled.builtin.copy` | `ansible.builtin.copy` |

Each module takes the same options, accepts the same values (types,
aliases, defaults, choices) and returns the same results as the builtin it
replaces, including check mode, diff mode, warnings and error messages. The
integration tests check this by running both side by side.

## Requirements

ansible-core 2.16 or late.
The targets need no Python (except for async tasks, see below).

Binaries are named `<module>_<GOOS>_<GOARCH>`, Go's names for the target
system and architecture. The action plugin maps the target's `uname -s` and
`uname -m` (or the `system` and `architecture` facts) to them:

- Systems: Linux, macOS (Darwin), FreeBSD, OpenBSD, NetBSD, Solaris and AIX.
- Architectures: x86_64/amd64, aarch64/arm64, armv7l and armv6l (as `arm`),
  i386/i686, ppc64le, s390x and riscv64.
- AIX is always `aix_ppc64`, the only AIX platform Go supports (AIX 7.2 or
  later on POWER8 or later): AIX reports a machine ID or `chrp` instead of
  an architecture.
- Anything else (big-endian ppc64, mips, loongarch64...) is unsupported, and
  tasks fail with "Unsupported target platform".

### Differences from the builtins

- **lineinfile regular expressions** use Python syntax but run on Go's RE2
  engine. Unicode `\d`, `\w`, `\s`, `$`, `\Z`, escapes, flags and named groups
  behave as in Python. Lookahead and lookbehind, backreferences inside the
  pattern (`\1`, `(?P=name)`), conditional and atomic groups and verbose
  mode `(?x)` aren't available: the task fails and names the feature.
  `\b` and `\B` only see ASCII word characters.
- **lineinfile encodings**: UTF-8 (with or without a BOM), ASCII, Latin-1,
  Windows-1252, UTF-16 and UTF-32. Python's other codecs fail as unknown.
- **SELinux** contexts are handled through extended attributes, so they
  work without Python's selinux bindings. `_default` contexts need
  `matchpathcon` on the target.
- **uri** sends the same requests as the builtin, which uses Python's
  urllib: the same headers, redirect rules, Basic and Digest auth, cookies,
  netrc, proxies (including for localhost) and error messages. It differs in:
  - `use_gssapi` (Kerberos) isn't supported: it needs the Python gssapi
    library. The task fails and says so.
  - `ciphers` takes cipher names (OpenSSL or IANA), not OpenSSL keywords and
    operators such as `HIGH` or `!aNULL`, which make the task fail.
  - TLS error messages are Python's without the `(_ssl.c:NNNN)` location.
  - Response header values lose trailing whitespace, which Python keeps.
  - Response charsets beyond the lineinfile encodings are read as UTF-8.
  - Its binary is larger (about 2.3 MB packed): Go's HTTP and TLS stack.
- **copy** is the controller's own `ansible.builtin.copy` action (finding
  the source, checksums, uploads, directory walks, diffs), with the copy,
  file, stat and slurp modules it runs on the target replaced by the
  compiled ones. Its behaviour on the controller side is therefore always
  that of the ansible-core running it.
- **file** follows Python 3.14: a hard link (`state: hard`) to a symlink
  links the symlink's target, as `os.link` does from Python 3.14 on Linux.
  With Python 3.13 or older on the target, the builtin links the symlink
  itself.
- **Async** works, but Ansible's async wrapper is Python, so async tasks
  need Python on the target.

### On ansible-core older than 2.21

The modules always behave like the builtins of current ansible-core (2.21),
whatever version runs them. Older builtins differ, so on older versions these
are differences too:

- **Free-form commands need ansible-core 2.19+.** Before that, Ansible only
  accepts `command: echo hi` for the builtin and rejects it for any other
  module when it loads the playbook. On 2.16 to 2.18, use `cmd:` or `argv:`.
- **shell** is command run through a shell, as in ansible-core: it has no
  binary of its own and shares command's differences below.
- **command, before 2.19:** the builtin has no `cmd` option, fails with
  `rc: 256` and "no command given" or "only command or argv can be given,
  not both", and puts the OS error in `msg` when a command can't run or
  `chdir` fails. compiled.builtin.command validates and reports these like
  2.19+.
- **command, before 2.21:** a non-zero exit is reported as "non-zero return
  code" instead of "The command exited with a non-zero return code."
- **slurp, before 2.21:** the builtin has no `armor` option.
- **slurp, before 2.19:** its error messages start in lowercase, and the
  fallback one names the OS error. Since 2.19 the builtin prints
  "Unable to slurp file: {source}" literally (its message isn't an
  f-string), and compiled.builtin.slurp does the same.
- **lineinfile, before 2.20:** the builtin works on bytes: it has no
  `encoding` option, keeps `\r\n` and `\r` line endings, and its regexp
  `\w`, `\d` and `\s` match ASCII only. compiled.builtin.lineinfile reads
  text (UTF-8 by default), normalizes line endings and matches Unicode, as
  the builtin does from 2.20.

- **stat, before 2.21:** the builtin doesn't return `disk_usage_bytes`.
- **stat, before 2.20:** the builtin has no `get_selinux_context` option.
- **file, before 2.21:** the builtin treats a path under a regular file
  (`file.txt/x`) as absent instead of failing, and doesn't warn when it
  can't look at a path for lack of permissions.
- **file, before 2.19:** several error messages are worded differently, and
  the builtin returns `diff` even when diff mode is off.
- **copy, before 2.21:** the builtin doesn't apply `directory_mode` to the
  destination directory of a `remote_src` directory copy.
- **copy, before 2.19:** copying a directory on the target (`remote_src`)
  doesn't keep file times or symlinks in the builtin.
- **file, before 2.18:** with `state: hard` and `follow`, the builtin links
  a symlink `src` itself rather than its target.
- **copy, before 2.17:** a `remote_src` file copy keeps the mode of the file
  it replaces instead of taking the source's.

The integration tests run every case on every version, and skip the
comparison for the cases above on the versions they apply to.

## Layout

```
modules_src/<module>/        Go source, one main package per module (Go
                             projects usually call this directory cmd/)
internal/ansible/            Shared plumbing: read args, write results, plus Go
                             versions of the Python behaviour modules rely on
                             (shlex, glob, $VAR/~ expansion, re syntax,
                             run_command)
internal/files/              File metadata: mode, owner, attributes, SELinux,
                             atomic writes, backups and os.walk (lineinfile,
                             uri, stat, file and copy)
internal/codecs/             Text encodings by Python codec name
plugins/modules/
  <module>_linux_amd64       Precompiled binary (the only platform shipped)
  <module>.meta              Makes the module name resolvable, so its docs are
                             found. Ansible ignores its content; only the name
                             matters. (shell has only this and its .yml: it
                             runs the command binary)
  <module>.yml               Sidecar docs read by ansible-doc
plugins/action/<module>.py   Validates arguments with the builtin's argument
                             spec, then picks, or compiles, the binary (copy
                             runs ansible-core's copy action instead, with
                             the compiled modules swapped in)
plugins/plugin_utils/        Shared action plugin and build code
tests/integration/targets/   Side-by-side tests against ansible.builtin
tests/build.py               Build and test script
```

## How a task runs

1. The action plugin works out the target's platform from gathered facts
   (`ansible_facts.system` and `ansible_facts.architecture`), or by running
   `uname -sm` when facts are not gathered. It caches the result in the
   `compiled_builtin_platform` fact, which you may set from inventory if 
   fact gathering isn't available.

Platform names come for Go (GOARCH and GOOS) Here are the possible values:

  - linux_amd64
  - linux_arm64
  - linux_arm
  - linux_386
  - linux_ppc64le
  - linux_s390x
  - linux_riscv64
  - darwin_arm64 / darwin_amd64
  - freebsd_amd64, openbsd_amd64, netbsd_amd64 (or _arm64)
  - solaris_amd64
  - aix_ppc64

2. If `plugins/modules/<module>_<goos>_<goarch>` exists, it runs that.
3. Otherwise it runs `go build` on the controller and caches the binary in
   `~/.cache/ansible/compiled.builtin/<source hash>/`. Set
   `COMPILED_BUILTIN_CACHE_DIR` to put the cache somewhere else. Editing the Go
   source changes the hash, so the binary is rebuilt.

Binaries are built with `-trimpath -ldflags "-s -w"` (no symbol table or
debug info), then packed with `upx --best --lzma` where UPX supports the
platform (linux amd64, 386, arm, arm64, ppc64le). That takes them from about
2.2 MB to about 0.9 MB.

Runtime compilation needs Go 1.22+ on the controller or in the execution
environment, plus `upx` to keep binaries small. Without UPX it still works,
but warns and uses the bigger uncompressed binary.

## Building and testing

There's no make dependency. `tests/build.py` needs Python, Go and UPX, plus
ansible-core for the Ansible tests.

```
python3 tests/build.py build                         # linux/amd64, the shipped platform
python3 tests/build.py build --platform linux/arm64  # try others locally (don't ship them)
python3 tests/build.py build --module ping
python3 tests/build.py unit                          # go vet + go test
python3 tests/build.py sanity                        # ansible-test sanity
python3 tests/build.py integration [command ...]     # side-by-side tests (builds first)
python3 tests/build.py test                          # unit, sanity and integration
python3 tests/build.py golden                        # re-record CPython results
python3 tests/build.py testdocs                      # regenerate tests/README.md
python3 tests/build.py clean
```

`build` fails if UPX is missing, so shipped binaries are always packed.

Every test case is listed in [tests/README.md](tests/README.md), which is
generated from the test files; `test` fails if it is out of date. The tests
come in three layers:

- **Unit** (`go test`): the Python-compatibility helpers are checked against
  results recorded from CPython (`internal/ansible/testdata/python.json`,
  made by `generate.py` there), so they match Python, not a reading of it.
- **Sanity** (`ansible-test sanity`): docs, action plugins and Python code.
  The binaries and `.meta` files are skipped in
  `tests/sanity/ignore-2.21.txt`, since validate-modules can only read text.
- **Integration** (`ansible-test integration`): each case in
  `tests/integration/targets/<module>/vars/main.yml` runs
  `ansible.builtin.<module>` and `compiled.builtin.<module>` with the same
  arguments and asserts identical results; for lineinfile, file and copy
  also identical files (content, mode, owner, times, symlinks, backups)
  afterwards. To cover a new
  option or edge case, add a case there and run `testdocs`.
