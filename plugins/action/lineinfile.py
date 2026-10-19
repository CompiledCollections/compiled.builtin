# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import FILE_COMMON_ARGUMENTS, GoModuleAction


class ActionModule(GoModuleAction):
    argument_spec = dict(
        path=dict(type='str', required=True, aliases=['dest', 'destfile', 'name']),
        state=dict(type='str', default='present', choices=['absent', 'present']),
        regexp=dict(type='str', aliases=['regex']),
        search_string=dict(type='str'),
        line=dict(type='str', aliases=['value']),
        encoding=dict(type='str', default='utf-8'),
        insertafter=dict(type='str'),
        insertbefore=dict(type='str'),
        backrefs=dict(type='bool', default=False),
        create=dict(type='bool', default=False),
        backup=dict(type='bool', default=False),
        firstmatch=dict(type='bool', default=False),
        validate=dict(type='str'),
        **FILE_COMMON_ARGUMENTS
    )
    mutually_exclusive = [
        ['insertbefore', 'insertafter'], ['regexp', 'search_string'], ['backrefs', 'search_string'],
    ]
