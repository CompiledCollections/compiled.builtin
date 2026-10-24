# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)

from __future__ import annotations

from ansible_collections.compiled.builtin.plugins.plugin_utils._go_module import FILE_COMMON_ARGUMENTS, GoModuleAction


class ActionModule(GoModuleAction):
    """compiled.builtin.get_url runs the uri binary with _get_url: the two
    share the code that makes requests, which is most of the binary."""

    module_binary = 'uri'
    binary_args = {'_get_url': True}

    argument_spec = dict(
        # url_argument_spec(), with get_url's aliases
        url=dict(type='str', required=True),
        force=dict(type='bool', default=False),
        http_agent=dict(type='str', default='ansible-httpget'),
        use_proxy=dict(type='bool', default=True),
        validate_certs=dict(type='bool', default=True),
        url_username=dict(type='str', aliases=['username']),
        url_password=dict(type='str', aliases=['password'], no_log=True),
        force_basic_auth=dict(type='bool', default=False),
        client_cert=dict(type='str'),
        client_key=dict(type='str'),
        use_gssapi=dict(type='bool', default=False),
        # get_url's own
        dest=dict(type='str', required=True),
        backup=dict(type='bool', default=False),
        checksum=dict(type='str', default=''),
        timeout=dict(type='int', default=10),
        headers=dict(type='dict'),
        tmp_dest=dict(type='str'),
        unredirected_headers=dict(type='list', elements='str', default=[]),
        decompress=dict(type='bool', default=True),
        ciphers=dict(type='list', elements='str'),
        use_netrc=dict(type='bool', default=True),
        **FILE_COMMON_ARGUMENTS
    )
