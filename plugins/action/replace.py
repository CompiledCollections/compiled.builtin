# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import FILE_COMMON_ARGUMENTS, GoModuleAction


class ActionModule(GoModuleAction):
    argument_spec = dict(
        path=dict(type='str', required=True, aliases=['dest', 'destfile', 'name']),
        regexp=dict(type='str', required=True),
        replace=dict(type='str', default=''),
        after=dict(type='str'),
        before=dict(type='str'),
        backup=dict(type='bool', default=False),
        validate=dict(type='str'),
        encoding=dict(type='str', default='utf-8'),
        **FILE_COMMON_ARGUMENTS
    )
