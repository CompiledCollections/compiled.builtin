# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

import collections.abc as _c
import os
from copy import deepcopy

from ansible.errors import AnsibleActionFail
from ansible.module_utils.basic import sanitize_keys
from ansible.module_utils.parsing.convert_bool import boolean

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import FILE_COMMON_ARGUMENTS, GoModuleAction

# Result keys sanitize_keys() must not change (as in ansible.builtin.uri).
NO_MODIFY_KEYS = frozenset(
    ('msg', 'exception', 'warnings', 'deprecations', 'failed', 'skipped',
     'changed', 'rc', 'stdout', 'stderr', 'elapsed', 'path', 'location',
     'content_type')
)


class ActionModule(GoModuleAction):
    TRANSFERS_FILES = True

    # Like ansible.builtin.uri: requests can't be checked without being made.
    _supports_check_mode = False

    argument_spec = dict(
        # url_argument_spec()
        url=dict(type='str', required=True),
        force=dict(type='bool', default=False),
        http_agent=dict(type='str', default='ansible-httpget'),
        use_proxy=dict(type='bool', default=True),
        validate_certs=dict(type='bool', default=True),
        url_username=dict(type='str', aliases=['user']),
        url_password=dict(type='str', aliases=['password'], no_log=True),
        force_basic_auth=dict(type='bool', default=False),
        client_cert=dict(type='str'),
        client_key=dict(type='str'),
        use_gssapi=dict(type='bool', default=False),
        # url_redirect_argument_spec()
        follow_redirects=dict(type='str', default='safe', choices=['all', 'no', 'none', 'safe', 'urllib2', 'yes']),
        # ansible.builtin.uri
        dest=dict(type='str'),
        body=dict(type='raw'),
        body_format=dict(type='str', default='raw', choices=['form-urlencoded', 'json', 'raw', 'form-multipart']),
        src=dict(type='str'),
        method=dict(type='str', default='GET'),
        return_content=dict(type='bool', default=False),
        creates=dict(type='str'),
        removes=dict(type='str'),
        status_code=dict(type='list', elements='int', default=[200]),
        timeout=dict(type='int', default=30),
        headers=dict(type='dict', default={}),
        unix_socket=dict(type='str'),
        remote_src=dict(type='bool', default=False),
        ca_path=dict(type='str', default=None),
        unredirected_headers=dict(type='list', elements='str', default=[]),
        decompress=dict(type='bool', default=True),
        ciphers=dict(type='list', elements='str'),
        use_netrc=dict(type='bool', default=True),
        **FILE_COMMON_ARGUMENTS
    )
    mutually_exclusive = [['body', 'src']]

    def prepare_args(self, task_vars):
        """Upload src, and multipart files given by filename, from the
        controller, as ansible.builtin.uri's action plugin does."""
        body_format = self._task.args.get('body_format', 'raw')
        body = self._task.args.get('body')
        src = self._task.args.get('src', None)
        remote_src = boolean(self._task.args.get('remote_src', 'no'), strict=False)
        if remote_src:
            return

        kwargs = {}
        if src:
            src = self._find_needle('files', src)
            tmp_src = self._connection._shell.join_path(self._connection._shell.tmpdir, os.path.basename(src))
            kwargs['src'] = tmp_src
            self._transfer_file(src, tmp_src)
            self._fixup_perms2((self._connection._shell.tmpdir, tmp_src))
        elif body_format == 'form-multipart':
            if not isinstance(body, _c.Mapping):
                raise AnsibleActionFail('body must be mapping, cannot be type %s' % body.__class__.__name__)
            new_body = deepcopy(body)
            for value in new_body.values():
                if not isinstance(value, _c.MutableMapping):
                    continue
                content = value.get('content')
                filename = value.get('filename')
                if not filename or content:
                    continue
                filename = self._find_needle('files', filename)
                tmp_src = self._connection._shell.join_path(self._connection._shell.tmpdir, os.path.basename(filename))
                value['filename'] = tmp_src
                self._transfer_file(filename, tmp_src)
                self._fixup_perms2((self._connection._shell.tmpdir, tmp_src))
            kwargs['body'] = new_body

        self._task.args = self._task.args | kwargs

    def mask_values(self, result, no_log_values):
        # The module also sanitizes result keys (header names) before exiting.
        result = sanitize_keys(result, no_log_values, NO_MODIFY_KEYS)
        return super(ActionModule, self).mask_values(result, no_log_values)
