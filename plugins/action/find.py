# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import GoModuleAction


class ActionModule(GoModuleAction):
    argument_spec = dict(
        paths=dict(type='list', required=True, aliases=['name', 'path'], elements='str'),
        patterns=dict(type='list', default=[], aliases=['pattern'], elements='str'),
        excludes=dict(type='list', aliases=['exclude'], elements='str'),
        contains=dict(type='str'),
        read_whole_file=dict(type='bool', default=False),
        file_type=dict(type='str', default="file", choices=['any', 'directory', 'file', 'link']),
        age=dict(type='str'),
        age_stamp=dict(type='str', default="mtime", choices=['atime', 'ctime', 'mtime']),
        size=dict(type='str'),
        recurse=dict(type='bool', default=False),
        hidden=dict(type='bool', default=False),
        follow=dict(type='bool', default=False),
        get_checksum=dict(type='bool', default=False),
        checksum_algorithm=dict(type='str', default='sha1',
                                choices=['md5', 'sha1', 'sha224', 'sha256', 'sha384', 'sha512'],
                                aliases=['checksum', 'checksum_algo']),
        use_regex=dict(type='bool', default=False),
        depth=dict(type='int'),
        mode=dict(type='raw'),
        exact_mode=dict(type='bool', default=True),
        encoding=dict(type='str'),
        limit=dict(type='int')
    )
