# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from collections.abc import Mapping

from ansible.parsing.splitter import parse_kv

try:
    from ansible.template import is_trusted_as_template, trust_as_template
except ImportError:
    # ansible-core < 2.19 has no template trust: every string can be templated.
    def is_trusted_as_template(value):
        return False

    def trust_as_template(value):
        return value

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import GoModuleAction


class ActionModule(GoModuleAction):
    # Like ansible.builtin.command, accept a free-form command string.
    supports_raw_params = True

    argument_spec = dict(
        _raw_params=dict(),
        _uses_shell=dict(type='bool', default=False),
        cmd=dict(),
        argv=dict(type='list', elements='str'),
        chdir=dict(type='str'),
        executable=dict(),
        expand_argument_vars=dict(type='bool', default=True),
        creates=dict(type='str'),
        removes=dict(type='str'),
        stdin=dict(required=False),
        stdin_add_newline=dict(type='bool', default=True),
        strip_empty_ends=dict(type='bool', default=True),
    )
    required_one_of = [['_raw_params', 'cmd', 'argv']]
    mutually_exclusive = [['_raw_params', 'cmd', 'argv']]

    def run(self, tmp=None, task_vars=None):
        self._resplit_free_form()
        return super(ActionModule, self).run(tmp, task_vars)

    def _resplit_free_form(self):
        """Split a free-form command the way Ansible's parser does for
        ansible.builtin.command.

        The parser only special-cases the builtin: for any other module it
        takes every k=v word as an option, so `echo FOO=bar` would pass a FOO
        option. The builtin keeps all words but its own options (creates,
        chdir...) in the command, so redo the split that way.
        """
        free_form = self._free_form_string()
        if free_form is None:
            return

        loose = parse_kv(free_form)
        strict = parse_kv(free_form, check_raw=True)
        misparsed = set(loose) - set(strict)
        if not misparsed:
            return

        args = dict(self._task.args)
        for key in misparsed:
            args.pop(key, None)
        raw = strict['_raw_params']
        if is_trusted_as_template(free_form):
            raw = trust_as_template(raw)
        args['_raw_params'] = self._templar.template(raw)
        self._task.args = args

    def _free_form_string(self):
        """The task's free-form string as written, before templating."""
        ds = self._task.get_ds()
        if not isinstance(ds, Mapping):
            return None
        value = ds.get(self._task.action)
        if isinstance(value, str):
            return value
        for key in ('action', 'local_action'):
            value = ds.get(key)
            if isinstance(value, str):
                name, dummy, rest = value.strip().partition(' ')
                if name == self._task.action:
                    return rest
        return None
