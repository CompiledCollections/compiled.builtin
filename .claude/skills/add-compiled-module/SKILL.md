---
name: add-compiled-module
description: Add or change a compiled.builtin module, a Go replacement for an ansible.builtin module that needs no Python on the target. Use when asked to add a module "like ansible.builtin.X", to port or fix one of the existing modules (ping, command, shell, lineinfile, blockinfile, replace, uri, get_url, slurp, stat, find, file, copy, template, fetch, wait_for_connection, apk, microdnf), or to check that one still matches its builtin.
---

# Adding or changing a compiled.builtin module

The goal is always **full parity with `ansible.builtin.<module>`**: the same
options, aliases, types, defaults, choices, results, check mode, diff mode,
warnings and error messages. A change isn't done until the side-by-side
tests prove it, on the current ansible-core (2.21).

## Fixed decisions (don't revisit them)

- **One behaviour: current ansible-core's (2.21).** The modules behave like
  the newest builtin whatever version runs them. Older cores aren't tested,
  so only when the builtin's source shows that an older version differs,
  list it under "On ansible-core older than 2.21" in `README.md`. Never add
  version-dependent behaviour to a module.
- **Only the current ansible-core (2.21) is tested.** The collection still
  declares `requires_ansible: '>=2.16.0'` (what AAP 2.5 ships), so keep docs
  and argument specs loadable on 2.16: no doc fragments or options newer
  than it.
- **Compilation happens in the action plugin**, at runtime, when no shipped
  binary fits. `tests/build.py` is the only build and test script. **No
  make**, now or as a dependency of anything (it would complicate the EE).
- **Only `linux_amd64` is built and shipped.** Never build every platform.
  Other platforms compile at runtime; `build --platform` is only for trying
  one locally.
- **All modules are in one multi-call binary**, `multicall_<goos>_<goarch>`,
  built from `modules_src`. Never go back to a binary per module: each one
  carries Go's runtime and the shared code, and 13 of them came to 15 MB,
  near Galaxy's 20 MB limit. The action plugin names the module to run in
  `_compiled_module`.
- **The binary is stripped and packed**: `-trimpath -ldflags "-s -w"` then
  `upx -9` (UPX's default NRV method, which starts faster than LZMA; Ansible
  starts the binary on every task). `build` fails without UPX; the runtime
  path only warns.
- **One `<module>.meta` per module name**, containing `{"interpreter": "none"}`.
  Ansible needs it to resolve the name and find the docs. No per-platform
  `.meta` files.
- **Docs live in a sidecar `plugins/modules/<module>.yml`**, not in the
  action plugin.
- **Unsupported platforms stay unsupported.** Don't add GOOS/GOARCH mappings
  beyond `GOOS`, `GOARCH` and `PLATFORMS` in `_go_module.py` unless asked.
- **Internal plugin_utils files start with an underscore** and say they
  aren't a public API.
- **Use ansible-test the standard way**, as if the collection were going to
  be included in the Ansible project.

## Files for a module named `X`

| File | What it holds |
| --- | --- |
| `modules_src/X/main.go` | `package X` with `func Main()`. Reads args and writes results through `internal/ansible`. Platform-specific code goes in `_linux.go`, `_bsd.go` and `_other.go` files. |
| `modules_src/main.go` | Add `"X": X.Main` to `modules` (and the import). A unit test fails when a module directory is missing from it. |
| `internal/ansible/`, `internal/files/` | Shared code. Reuse it, and add Go versions of Python behaviour here (with golden tests) instead of in the module. |
| `plugins/action/X.py` | `class ActionModule(GoModuleAction)` with the builtin's `argument_spec` copied exactly (plus `**FILE_COMMON_ARGUMENTS` for file modules). Use `type='str'` for `path` options: paths are expanded on the target. |
| `plugins/modules/X.meta` | `{"interpreter": "none"}` |
| `plugins/modules/X.yml` | `DOCUMENTATION`, `EXAMPLES` and `RETURN` from the current builtin, renamed to `compiled.builtin.X`. Remove `version_added` from options (they're core versions, not collection versions), inline doc fragments 2.16 doesn't have, and describe every attribute. |
| `tests/integration/targets/X/` | `aliases` (`shippable/posix/group1`, `context/target`; `context/controller` for container targets), `tasks/main.yml`, `tasks/compare.yml`, `vars/main.yml` |
| `tests/sanity/ignore-2.21.txt` | Add `plugins/modules/X.meta validate-modules!skip` and the `X.yml` license line, keeping each file sorted. Don't add the binary: it's gitignored, and ansible-test doesn't see it. |
| `README.md` | Module table, any differences from the builtin, the pre-2.21 list, and the layout section if it changes. |
| `CHANGELOG.rst` | An entry in the unreleased version's section: the module under New Modules, other changes under their headings. It's edited by hand; don't add antsibull-changelog fragments. |

When an action plugin runs a compiled module more than once in a task (as
copy does with stat), go through `_execute_compiled`/`_execute_binary`.
They give each run its own remote file name, which avoids ETXTBSY.

Other shapes a module can take:

- **The builtin is mostly an action plugin** (copy, template, fetch,
  wait_for_connection): subclass the builtin action with `SwapsModules`
  first and map the `ansible.legacy.*` modules it calls to compiled ones in
  `compiled`. No Go source; the `.meta` and `.yml` are still needed.
- **Two modules share most of their code** (shell and command, get_url and
  uri): set `module_binary` to the other module's name, and pass what tells
  them apart in `binary_args`, which is added after validation so it never
  shows in validation errors.
- **No builtin to match** (apk, microdnf): there is nothing to compare
  with, so the target has `tasks/cases.yml` steps that check outcomes in
  containers (depend on `setup_podman` in `meta/main.yml`, start them with
  its `start.yml`, run with `delegate_to`, check with `raw`). Its `aliases`
  use `context/controller`, since the containers are started from the
  controller. testdocs lists these steps.

## Porting behaviour

- Port from the builtin's **current source** (ansible-core devel/2.21), and
  from the Python standard library functions it calls. Match Python's
  results, not a reading of them: error text, `str(OSError)` formatting,
  float and time handling, walk order.
- When the target's Python changes the builtin's behaviour (such as
  `os.link` in 3.14, or `rmtree`'s order in 3.12), follow the newest Python,
  mark the case `python_differs`, and say so in the README.
- When Python and Go produce different text from their own libraries,
  `compare: failed` (both must fail) is acceptable. Use it sparingly, with a
  reason.

## Tests

Each case in `vars/main.yml` runs the builtin and the compiled module with
the same arguments, and `compare.yml` asserts identical results. For modules
that change files, it also compares the files left behind (content, mode,
owner, times, links). The markers:

- `python_differs: {before: '3.NN', why: '...'}`: older Pythons on the
  target differ. Single-quote `why`; `testdocs` parses it.
- `compare: failed`: both must fail, and the text isn't compared.
- `volatile`: result keys that are never compared.

Cover every option, alias, choice, check mode, diff mode and error path of
the builtin. To see every failing case at once, run the cases with
`ignore_errors: true  # SURVEY` on the assert, read the failures, then
remove it. Never loosen a comparison to make a case pass.

Checks that aren't side-by-side go in `tasks/<name>.yml`, with a leading `#`
comment, included from `main.yml`. testdocs picks them up.

Every time cases change, run `python3 tests/build.py testdocs` to regenerate
`tests/README.md`. CI fails if it's out of date.

## Verifying (always, before saying it's done)

```
python3 tests/build.py unit                 # go vet + go test (golden: re-record with `golden`)
python3 tests/build.py sanity
python3 tests/build.py integration X        # builds linux_amd64 first
```

- Run sanity and integration on ansible-core 2.21. CI
  (`.github/workflows/ci.yml`) does this on each push and pull request.
- Never run two ansible-test runs on the same tree at once: they share
  `tests/output`. Use a separate copy for each.
- A copy for a machine without git must leave out `.git`. Otherwise
  ansible-test fails with "Required program git not found".
- Report what ran and with which result. Name any layer that didn't run.

## Packaging

`galaxy.yml` `build_ignore` keeps `tests`, `*_test.go`, `*/testdata`,
`.gitignore`, `.github`, `.claude`, `*.tar.gz` and `logos` out of the
tarball. Module sources, `internal/` and `go.mod`
must stay in, because runtime compilation needs them. After changing
packaging, build the tarball, install it, delete the shipped binary, and
check that a task still compiles it and runs.

## Commits

Make small, separate commits with messages that explain why. Don't push
unless asked.

- Start a feature branch from `dev`. Its pull request is squashed into `dev`.
- `dev` is promoted to `main` through a pull request merged with a merge
  commit.

## Releasing

- Set `galaxy.yml`'s `version` for the next release: Galaxy never accepts a
  version twice. While the collection is 0.x, new modules or features raise
  the minor version (0.3.1 to 0.4.0) and fixes the patch version.
- Give the unreleased section of `CHANGELOG.rst` that version.
- After `dev` is merged into `main`, tag `vX.Y.Z` on `main`. The release
  workflow fails if the tag doesn't match `galaxy.yml`'s version; otherwise
  it builds the collection, attaches it to a GitHub release and publishes it
  to Galaxy.
