# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""Shared action plugin for the compiled Go modules.

Internal to compiled.builtin: not a public API, and it can change in any release.

Each module runs as a binary named ``<module>_<goos>_<goarch>``. This action
works out the target's platform and runs the matching binary, looking in order
at:

1. plugins/modules, for binaries shipped with the collection (tests/build.py).
2. The controller-side cache, for binaries compiled earlier.
3. Neither: compile it now with ``go build`` into the cache, packed with
   UPX when it is installed and supports the platform.
"""

from __future__ import annotations

import os
import re
from collections.abc import Mapping

from ansible import constants as C
from ansible.errors import AnsibleError
from ansible.module_utils.basic import remove_values
from ansible.module_utils.common.arg_spec import ArgumentSpecValidator
from ansible.module_utils.errors import UnsupportedError
from ansible.plugins.action import ActionBase
from ansible.utils.display import Display

from ansible_collections.compiled.builtin.plugins.plugin_utils import _go_build as go_build

display = Display()

COLLECTION = 'compiled.builtin'
PLATFORM_FACT = 'compiled_builtin_platform'

# uname -s / ansible_system values (lowercased) to GOOS
GOOS = {
    'linux': 'linux',
    'darwin': 'darwin',
    'freebsd': 'freebsd',
    'openbsd': 'openbsd',
    'netbsd': 'netbsd',
    'aix': 'aix',
    'sunos': 'solaris',
}

# uname -m / ansible_architecture values to GOARCH
GOARCH = {
    'x86_64': 'amd64',
    'amd64': 'amd64',
    'aarch64': 'arm64',
    'arm64': 'arm64',
    'armv7l': 'arm',
    'armv6l': 'arm',
    'i386': '386',
    'i686': '386',
    'ppc64le': 'ppc64le',
    's390x': 's390x',
    'riscv64': 'riscv64',
}

# The <goos>_<goarch> pairs Go can build out of the two maps above, the only
# values compiled_builtin_platform can take.
PLATFORMS = frozenset([
    'aix_ppc64',
    'darwin_amd64', 'darwin_arm64',
    'freebsd_386', 'freebsd_amd64', 'freebsd_arm', 'freebsd_arm64',
    'linux_386', 'linux_amd64', 'linux_arm', 'linux_arm64', 'linux_ppc64le', 'linux_riscv64', 'linux_s390x',
    'netbsd_386', 'netbsd_amd64', 'netbsd_arm', 'netbsd_arm64',
    'openbsd_386', 'openbsd_amd64', 'openbsd_arm', 'openbsd_arm64', 'openbsd_riscv64',
    'solaris_amd64',
])


def go_platform(system, machine):
    """'<goos>_<goarch>' for a uname -s and -m pair (or the system and
    architecture facts), or None when the pair isn't supported."""
    goos, goarch = GOOS.get(system.lower()), GOARCH.get(machine.lower())
    if goos == 'aix':
        # AIX reports no usable architecture: uname -m is the machine ID,
        # and the architecture fact is getconf's MACHINE_ARCHITECTURE
        # (chrp). Go only targets AIX on 64-bit POWER anyway.
        goarch = 'ppc64'
    platform = '%s_%s' % (goos, goarch)
    return platform if platform in PLATFORMS else None


def invalid_platform_message(value):
    """The error for a compiled_builtin_platform value not in PLATFORMS."""
    msg = 'Invalid %s %r: it must be <goos>_<goarch>, with Go\'s names for the system and architecture' % (
        PLATFORM_FACT, value)
    # Spot uname-style values such as linux-x86_64, Linux x86_64 or linux/amd64.
    parts = re.split(r'[-_/ ]', value.strip(), maxsplit=1) if isinstance(value, str) else []
    guess = go_platform(*parts) if len(parts) == 2 else None
    if guess:
        msg += " (did you mean '%s'?)" % guess
    return '%s. Valid values: %s' % (msg, ', '.join(sorted(PLATFORMS)))


def cache_dir():
    """Where runtime-compiled binaries go. COMPILED_BUILTIN_CACHE_DIR overrides it."""
    override = os.environ.get('COMPILED_BUILTIN_CACHE_DIR')
    if override:
        return os.path.expanduser(override)
    base = os.environ.get('XDG_CACHE_HOME') or os.path.expanduser('~/.cache')
    return os.path.join(base, 'ansible', COLLECTION)


# AnsibleModule's add_file_common_args options. Paths and unsafe_writes'
# ANSIBLE_UNSAFE_WRITES fallback are resolved on the target by the module.
FILE_COMMON_ARGUMENTS = dict(
    mode=dict(type='raw'),
    owner=dict(type='str'),
    group=dict(type='str'),
    seuser=dict(type='str'),
    serole=dict(type='str'),
    selevel=dict(type='str'),
    setype=dict(type='str'),
    attributes=dict(type='str', aliases=['attr']),
    unsafe_writes=dict(type='bool'),
)


def inject_invocation(task_vars):
    """Whether results carry the module's arguments, as AnsibleModule does.
    ansible-core < 2.19 has no INJECT_INVOCATION setting and always adds them."""
    try:
        return C.config.get_config_value('INJECT_INVOCATION', variables=task_vars)
    except AnsibleError:
        return True


def to_plain(value):
    """Task args as the plain types a module receives once they are sent
    as JSON, without the controller's data tagging. Validation messages
    name the value's type, so they would otherwise differ."""
    if isinstance(value, Mapping):
        return {to_plain(k): to_plain(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [to_plain(v) for v in value]
    if isinstance(value, bool) or value is None:
        return value
    if isinstance(value, str):
        # str() and encode()/decode() keep subclasses such as AnsibleUnsafeText,
        # which override them; str's own __str__ copies to a plain str.
        return str.__str__(value)
    for kind in (int, float):
        if isinstance(value, kind):
            return kind(value)
    return value


class CompiledModules:
    """Runs compiled modules from an action plugin: works out the target's
    platform and finds (or compiles) the binary for it. Mixed into
    GoModuleAction, and into ansible.builtin action plugins (copy) whose
    Python modules are replaced with compiled ones."""

    # Set once the target's platform is known, for later modules in the task.
    _compiled_platform = None
    _compiled_platform_detected = False

    def _execute_compiled(self, module, module_args, task_vars, argument_spec,
                          mutually_exclusive=None, required_one_of=None, name=None, **kwargs):
        """Run the compiled module with module_args, as _execute_module runs a
        Python one: arguments are validated against argument_spec, and a
        validation error is a failed result, as AnsibleModule reports it."""
        args = to_plain(module_args)
        if kwargs.get('ignore_unknown_opts'):
            # As AnsibleModule does with _ansible_ignore_unknown_opts.
            known = set(argument_spec)
            for spec in argument_spec.values():
                known.update(spec.get('aliases', ()))
            args = {k: v for k, v in args.items() if k in known}
        validation = ArgumentSpecValidator(argument_spec, mutually_exclusive=mutually_exclusive,
                                           required_one_of=required_one_of).validate(args)
        no_log_values = getattr(validation, '_no_log_values', None)
        validated = validation.validated_parameters
        try:
            error = validation.errors[0]
        except IndexError:
            error = None
        if error:
            msg = validation.errors.msg
            if isinstance(error, UnsupportedError):
                msg = 'Unsupported parameters for (%s) module: %s' % (name or module, msg)
            result = dict(failed=True, msg=msg)
        else:
            platform, error = self._compiled_target_platform(task_vars)
            binary = None
            if platform:
                binary, error = self._find_binary(platform)
            if error:
                return dict(failed=True, msg=error)
            result = self._execute_binary(binary, dict(validated, _compiled_module=module), task_vars, **kwargs)
            if no_log_values:
                result = self.hide_no_log_values(result, no_log_values)
        if inject_invocation(task_vars):
            invocation = remove_values(validated, no_log_values) if no_log_values else validated
            result.setdefault('invocation', {'module_args': invocation})
        return result

    def _execute_binary(self, binary, module_args, task_vars, **kwargs):
        """ActionBase._execute_module for a binary, under a remote file name of
        its own for each run in the task. Running the same binary again would
        otherwise overwrite the file just run, which the kernel can still
        count as busy for a moment after the process ends (ETXTBSY)."""
        runs = self.__dict__.setdefault('_compiled_runs', {})
        runs[binary] = runs.get(binary, 0) + 1
        if runs[binary] == 1:
            return ActionBase._execute_module(self, module_name=binary, module_args=module_args,
                                              task_vars=task_vars, **kwargs)
        shell = self._connection._shell
        get_remote_filename = shell.get_remote_filename
        suffix = '_%d' % runs[binary]
        shell.get_remote_filename = lambda pathname: get_remote_filename(pathname) + suffix
        try:
            return ActionBase._execute_module(self, module_name=binary, module_args=module_args,
                                              task_vars=task_vars, **kwargs)
        finally:
            del shell.get_remote_filename

    def _compiled_target_platform(self, task_vars):
        """_target_platform, remembered for the rest of the task."""
        if self._compiled_platform is None:
            platform, detected = self._target_platform(task_vars)
            if platform is None:
                return None, detected
            self._compiled_platform, self._compiled_platform_detected = platform, detected
        return self._compiled_platform, None

    def _add_platform_fact(self, result):
        """Cache a freshly detected platform so later tasks on this host skip uname."""
        if self._compiled_platform_detected:
            result.setdefault('ansible_facts', {})[PLATFORM_FACT] = self._compiled_platform
        return result

    def hide_no_log_values(self, result, no_log_values):
        """Mask no_log option values (passwords) in the result, as
        AnsibleModule does when a module exits, keeping booleans and None."""
        # The controller has already turned these into its own objects.
        controller = ('exception', 'warnings', 'deprecations')
        preserved = {k: v for k, v in result.items() if v is None or isinstance(v, bool) or k in controller}
        result = self.mask_values({k: v for k, v in result.items() if k not in controller}, no_log_values)
        result.update(preserved)
        return result

    def mask_values(self, result, no_log_values):
        """Mask no_log values in the module's own result values."""
        return remove_values(result, no_log_values)

    def _find_binary(self, platform):
        """Return (name of the multi-call binary to execute, error message)."""
        loader = self._shared_loader_obj.module_loader
        name = go_build.binary_name(platform)

        shipped = '%s.%s' % (COLLECTION, name)
        if loader.find_plugin_with_context(shipped).resolved:
            return shipped, None

        # The hash keeps binaries from different source versions apart, and the
        # suffix keeps a binary built before UPX was installed from being reused.
        pack = go_build.can_pack(platform)
        build_dir = os.path.join(cache_dir(), go_build.source_hash() + ('-upx' if pack else ''))
        dest = os.path.join(build_dir, name)
        if not os.path.isfile(dest):
            if platform in go_build.UPX_PLATFORMS and not pack:
                display.warning('compiled.builtin: upx is not installed, so the binary for %s '
                                'will not be compressed' % platform)
            display.vv('compiled.builtin: compiling the modules for %s into %s' % (platform, dest))
            try:
                go_build.build(platform, dest)
            except go_build.BuildError as e:
                return None, 'No precompiled binary for %s, and compiling it failed: %s' % (platform, e)

        loader.add_directory(build_dir)
        if not loader.find_plugin_with_context(name).resolved:
            return None, 'Compiled %s but Ansible could not load it' % dest
        return name, None

    def _target_platform(self, task_vars):
        """Return (platform, detected) where platform is '<goos>_<goarch>'.

        detected is True when uname had to be run. On failure platform is None
        and the second item is an error message.
        """
        facts = task_vars.get('ansible_facts', {})
        cached = task_vars.get(PLATFORM_FACT) or facts.get(PLATFORM_FACT)
        if cached:
            # Set by hand when the target can't be asked, so check it here
            # rather than let go build or the target reject it.
            cached = self._templar.template(cached)
            if not (isinstance(cached, str) and cached in PLATFORMS):
                return None, invalid_platform_message(cached)
            return str(cached), False

        system, machine = facts.get('system'), facts.get('architecture')
        detected = False
        if not (system and machine):
            # No gathered facts: ask the target directly. uname is available
            # everywhere these modules can run, and needs no Python.
            res = self._low_level_execute_command('uname -sm')
            fields = res.get('stdout', '').split()
            if res.get('rc') != 0 or len(fields) != 2:
                return None, 'Could not detect target platform with uname -sm: %s' % (
                    res.get('stderr') or res.get('stdout') or 'no output')
            system, machine = fields
            detected = True

        platform = go_platform(system, machine)
        if platform is None:
            return None, 'Unsupported target platform: %s %s' % (system, machine)
        return platform, detected


class SwapsModules(CompiledModules):
    """Mixed into an ansible.builtin action plugin (listed first) so that its
    calls to Python modules run compiled modules instead. ``compiled`` maps
    the name the builtin action calls, such as ``ansible.legacy.stat``, to
    the compiled module and its argument spec."""

    compiled = {}

    def run(self, tmp=None, task_vars=None):
        result = super(SwapsModules, self).run(tmp, task_vars)
        return self._add_platform_fact(result)

    def _execute_module(self, module_name=None, module_args=None, task_vars=None, **kwargs):
        if module_name not in self.compiled:
            return super(SwapsModules, self)._execute_module(
                module_name=module_name, module_args=module_args, task_vars=task_vars, **kwargs)
        module, argument_spec = self.compiled[module_name]
        if module_args is None:
            module_args = self._task.args.copy()
        return self._execute_compiled(module, module_args, task_vars or {}, argument_spec,
                                      name='%s.%s' % (COLLECTION, module), **kwargs)


class GoModuleAction(CompiledModules, ActionBase):
    """Base for the modules' action plugins.

    Subclasses set the argument spec of the matching ansible.builtin module.
    Arguments are validated here with Ansible's own validator, so types,
    aliases, defaults and error messages match the Python module; the binary
    gets the normalized arguments. Use type 'str' for 'path' options: paths
    are expanded on the target, where ~ and $VARS mean what they should.
    """

    argument_spec = {}
    mutually_exclusive = None
    required_one_of = None

    # The Go module to run, when not the one named after the action (shell runs command).
    module_binary = None
    # Internal arguments for that module, added after validation so they don't
    # show in validation errors (get_url runs uri with _get_url).
    binary_args = None
    # The module named in validation errors, when not this action.
    validation_name = None

    _supports_async = True
    _supports_check_mode = True

    def run(self, tmp=None, task_vars=None):
        task_vars = task_vars or {}
        result = super(GoModuleAction, self).run(tmp, task_vars)
        del tmp  # tmp no longer has any effect

        self.prepare_args(task_vars)

        # Name the module in validation errors as the Python module would be
        # named, not by this plugin's import path.
        self._load_name = self.validation_name or self._task.resolved_action or self._task.action
        self._task.args = to_plain(self._task.args)
        validation, module_args = self.validate_argument_spec(
            argument_spec=self.argument_spec,
            mutually_exclusive=self.mutually_exclusive,
            required_one_of=self.required_one_of,
        )

        platform, detected = self._target_platform(task_vars)
        if platform is None:
            result.update(failed=True, msg=detected)
            return result

        module = self.module_binary or self._task.action.split('.')[-1]
        binary, error = self._find_binary(platform)
        if error:
            result.update(failed=True, msg=error)
            return result

        wrap_async = self._task.async_val and not self._connection.has_native_async
        module_result = self._execute_module(
            module_name=binary,
            module_args=dict(module_args, _compiled_module=module, **(self.binary_args or {})),
            task_vars=task_vars,
            wrap_async=wrap_async,
        )
        no_log_values = getattr(validation, '_no_log_values', None)
        if no_log_values:
            module_result = self.hide_no_log_values(module_result, no_log_values)
        result.update(module_result)

        if inject_invocation(task_vars):
            result.setdefault('invocation', {'module_args': module_args})

        # Cache a freshly detected platform so later tasks on this host skip uname.
        if detected:
            result.setdefault('ansible_facts', {})[PLATFORM_FACT] = platform

        if not wrap_async:
            self._remove_tmp_path(self._connection._shell.tmpdir)

        return result

    def prepare_args(self, task_vars):
        """Change the task args before validation, as an ansible.builtin
        action plugin would (for example, upload files). No-op by default."""
