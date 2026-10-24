# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""The ansible.builtin.template action, writing with compiled.builtin.copy.

Templates are rendered on the controller; the builtin action then hands the
result to the copy action, which runs the copy, file and stat modules on the
target. This runs that same template action, from the controller's
ansible-core, and only hands the result to compiled.builtin.copy instead, so
none of those modules needs Python.
"""

from __future__ import annotations

from ansible.plugins.action.template import ActionModule as BuiltinTemplate


class _ActionLoader:
    """The action loader, loading compiled.builtin.copy for ansible.legacy.copy."""

    def __init__(self, loader):
        self._loader = loader

    def get(self, name, *args, **kwargs):
        if name == 'ansible.legacy.copy':
            name = 'compiled.builtin.copy'
        return self._loader.get(name, *args, **kwargs)

    def __getattr__(self, attr):
        return getattr(self._loader, attr)


class _SharedLoaders:
    """The shared plugin loaders, with _ActionLoader as the action loader."""

    def __init__(self, shared):
        self._shared = shared
        self.action_loader = _ActionLoader(shared.action_loader)

    def __getattr__(self, attr):
        return getattr(self._shared, attr)


class ActionModule(BuiltinTemplate):

    def run(self, tmp=None, task_vars=None):
        shared = self._shared_loader_obj
        self._shared_loader_obj = _SharedLoaders(shared)
        try:
            return super(ActionModule, self).run(tmp, task_vars)
        finally:
            self._shared_loader_obj = shared
