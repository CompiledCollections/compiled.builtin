# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import GoModuleAction


class ActionModule(GoModuleAction):
    argument_spec = dict(
        path=dict(type='str', required=True, aliases=['dest', 'name']),
        follow=dict(type='bool', default=False),
        get_checksum=dict(type='bool', default=True),
        get_mime=dict(type='bool', default=True, aliases=['mime', 'mime_type', 'mime-type']),
        get_attributes=dict(type='bool', default=True, aliases=['attr', 'attributes']),
        get_selinux_context=dict(type='bool', default=False),
        checksum_algorithm=dict(type='str', default='sha1',
                                choices=['md5', 'sha1', 'sha224', 'sha256', 'sha384', 'sha512'],
                                aliases=['checksum', 'checksum_algo']),
    )
