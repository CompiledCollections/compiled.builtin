# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""The ansible.builtin.wait_for_connection action, with the compiled ping.

The builtin action waits until the ping module answers, which never happens
on a target without Python. This runs that same action plugin, from the
controller's ansible-core, with the compiled ping module.
"""

from __future__ import annotations

from ansible.plugins.action.wait_for_connection import ActionModule as BuiltinWaitForConnection

from ansible_collections.compiled.builtin.plugins.action.ping import ActionModule as PingAction
from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import SwapsModules


class ActionModule(SwapsModules, BuiltinWaitForConnection):
    compiled = {
        'ansible.legacy.ping': ('ping', PingAction.argument_spec),
    }
