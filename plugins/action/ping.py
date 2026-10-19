# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import GoModuleAction


class ActionModule(GoModuleAction):
    argument_spec = dict(
        data=dict(type='str', default='pong'),
    )
