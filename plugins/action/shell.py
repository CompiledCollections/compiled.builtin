# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible.errors import AnsibleActionFail

from ansible_collections.compiled.builtin.plugins.action.command import ActionModule as CommandAction


class ActionModule(CommandAction):
    """compiled.builtin.shell is compiled.builtin.command with _uses_shell,
    as ansible.builtin.shell is ansible.builtin.command: it runs the command
    binary and validates with command's argument spec."""

    module_binary = 'command'
    validation_name = 'compiled.builtin.command'

    def run(self, tmp=None, task_vars=None):
        del tmp  # tmp no longer has any effect

        self._task.args['_uses_shell'] = True

        # Shell shares the command module. Fail if command specific options are set.
        if 'expand_argument_vars' in self._task.args:
            raise AnsibleActionFail(f"Unsupported parameters for ({self._task.action}) module: expand_argument_vars")

        return super(ActionModule, self).run(task_vars=task_vars)
