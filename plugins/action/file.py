# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import FILE_COMMON_ARGUMENTS, GoModuleAction


class ActionModule(GoModuleAction):
    argument_spec = dict(
        state=dict(type='str', choices=['absent', 'directory', 'file', 'hard', 'link', 'touch']),
        path=dict(type='str', required=True, aliases=['dest', 'name']),
        _original_basename=dict(type='str'),  # Internal use only, for recursive ops
        recurse=dict(type='bool', default=False),
        force=dict(type='bool', default=False),
        follow=dict(type='bool', default=True),
        _diff_peek=dict(type='bool'),  # Internal use only, for internal checks in the action plugins
        src=dict(type='str'),
        modification_time=dict(type='str'),
        modification_time_format=dict(type='str', default='%Y%m%d%H%M.%S'),
        access_time=dict(type='str'),
        access_time_format=dict(type='str', default='%Y%m%d%H%M.%S'),
        **FILE_COMMON_ARGUMENTS
    )
