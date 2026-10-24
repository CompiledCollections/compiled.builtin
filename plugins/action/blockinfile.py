# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import FILE_COMMON_ARGUMENTS, GoModuleAction


class ActionModule(GoModuleAction):
    argument_spec = dict(
        path=dict(type='str', required=True, aliases=['dest', 'destfile', 'name']),
        state=dict(type='str', default='present', choices=['absent', 'present']),
        marker=dict(type='str', default='# {mark} ANSIBLE MANAGED BLOCK'),
        block=dict(type='str', default='', aliases=['content']),
        insertafter=dict(type='str'),
        insertbefore=dict(type='str'),
        create=dict(type='bool', default=False),
        backup=dict(type='bool', default=False),
        validate=dict(type='str'),
        marker_begin=dict(type='str', default='BEGIN'),
        marker_end=dict(type='str', default='END'),
        append_newline=dict(type='bool', default=False),
        prepend_newline=dict(type='bool', default=False),
        encoding=dict(type='str', default='utf-8'),
        **FILE_COMMON_ARGUMENTS
    )
    mutually_exclusive = [['insertbefore', 'insertafter']]
