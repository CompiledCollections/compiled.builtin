# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""The ansible.builtin.fetch action, with compiled modules on the target.

Fetching is the action plugin's work: it asks the stat module for the
remote checksum, then downloads the file through the connection, or reads it
with the slurp module under become. This runs that same action plugin, from
the controller's ansible-core, with the compiled stat and slurp modules.
"""

from __future__ import annotations

from ansible.plugins.action.fetch import ActionModule as BuiltinFetch

from ansible_collections.compiled.builtin.plugins.action.slurp import ActionModule as SlurpAction
from ansible_collections.compiled.builtin.plugins.action.stat import ActionModule as StatAction
from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import SwapsModules


class ActionModule(SwapsModules, BuiltinFetch):
    compiled = {
        'ansible.legacy.stat': ('stat', StatAction.argument_spec),
        'ansible.legacy.slurp': ('slurp', SlurpAction.argument_spec),
    }
