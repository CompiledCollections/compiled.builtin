# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""The ansible.builtin.copy action, with compiled modules on the target.

Copying is mostly the action plugin's work: it finds the source, compares
checksums, uploads files and walks directories, calling the copy, file,
stat and slurp modules on the target as it goes. This runs that same
action plugin, from the controller's ansible-core, and only swaps those
module calls for the compiled modules, so none of them needs Python.
"""

from __future__ import annotations

from ansible.plugins.action.copy import ActionModule as BuiltinCopy

from ansible_collections.compiled.builtin.plugins.action.file import ActionModule as FileAction
from ansible_collections.compiled.builtin.plugins.action.slurp import ActionModule as SlurpAction
from ansible_collections.compiled.builtin.plugins.action.stat import ActionModule as StatAction
from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import FILE_COMMON_ARGUMENTS, CompiledModules

COPY_ARGUMENT_SPEC = dict(
    src=dict(type='str'),
    _original_basename=dict(type='str'),  # used to handle 'dest is a directory' via template, a slight hack
    content=dict(type='str', no_log=True),
    dest=dict(type='str', required=True),
    backup=dict(type='bool', default=False),
    force=dict(type='bool', default=True),
    validate=dict(type='str'),
    directory_mode=dict(type='raw'),
    remote_src=dict(type='bool', default=False),
    local_follow=dict(type='bool'),
    checksum=dict(type='str'),
    follow=dict(type='bool', default=False),
    **FILE_COMMON_ARGUMENTS
)

# The modules the builtin action runs, and the compiled module run instead.
COMPILED = {
    'ansible.legacy.copy': ('copy', COPY_ARGUMENT_SPEC),
    'ansible.legacy.file': ('file', FileAction.argument_spec),
    'ansible.legacy.stat': ('stat', StatAction.argument_spec),
    'ansible.legacy.slurp': ('slurp', SlurpAction.argument_spec),
}


class ActionModule(BuiltinCopy, CompiledModules):

    def run(self, tmp=None, task_vars=None):
        result = super(ActionModule, self).run(tmp, task_vars)
        return self._add_platform_fact(result)

    def _execute_module(self, module_name=None, module_args=None, task_vars=None, **kwargs):
        if module_name not in COMPILED:
            return super(ActionModule, self)._execute_module(
                module_name=module_name, module_args=module_args, task_vars=task_vars, **kwargs)
        module, argument_spec = COMPILED[module_name]
        if module_args is None:
            module_args = self._task.args.copy()
        return self._execute_compiled(module, module_args, task_vars or {}, argument_spec,
                                      name='compiled.builtin.%s' % module, **kwargs)
