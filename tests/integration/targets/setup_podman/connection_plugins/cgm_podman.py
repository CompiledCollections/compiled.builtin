# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""A minimal connection to a podman container, for the tests only: the
targets whose modules need a particular distribution (apk, microdnf) run
them in containers without Python. Not part of the collection."""

from __future__ import annotations

DOCUMENTATION = r"""
name: cgm_podman
short_description: Run tasks in a podman container (compiled.builtin test helper)
description:
  - Runs commands with C(podman exec) and copies files through it.
options:
  remote_addr:
    description: The container's name.
    default: inventory_hostname
    vars:
      - name: inventory_hostname
      - name: ansible_host
  podman_executable:
    description: The podman command.
    default: podman
    vars:
      - name: cgm_podman_executable
"""

import shlex
import subprocess

from ansible.errors import AnsibleConnectionFailure
from ansible.plugins.connection import ConnectionBase


class Connection(ConnectionBase):
    transport = 'cgm_podman'
    has_pipelining = False

    def _podman(self, args, data=None):
        argv = [self.get_option('podman_executable')] + args
        try:
            proc = subprocess.run(argv, input=data, capture_output=True, check=False)
        except OSError as e:
            raise AnsibleConnectionFailure('Could not run podman: %s' % e)
        return proc.returncode, proc.stdout, proc.stderr

    def _connect(self):
        self._connected = True
        return self

    def exec_command(self, cmd, in_data=None, sudoable=False):
        super(Connection, self).exec_command(cmd, in_data=in_data, sudoable=sudoable)
        return self._podman(['exec', '-i', self.get_option('remote_addr'), '/bin/sh', '-c', cmd], in_data)

    def put_file(self, in_path, out_path):
        super(Connection, self).put_file(in_path, out_path)
        with open(in_path, 'rb') as f:
            rc, out, err = self._podman(['exec', '-i', self.get_option('remote_addr'), '/bin/sh', '-c',
                                         'cat > %s' % shlex.quote(out_path)], f.read())
        if rc != 0:
            raise AnsibleConnectionFailure('Could not copy %s to the container: %s' % (in_path, err))

    def fetch_file(self, in_path, out_path):
        super(Connection, self).fetch_file(in_path, out_path)
        rc, out, err = self._podman(['exec', self.get_option('remote_addr'), 'cat', in_path])
        if rc != 0:
            raise AnsibleConnectionFailure('Could not fetch %s from the container: %s' % (in_path, err))
        with open(out_path, 'wb') as f:
            f.write(out)

    def close(self):
        self._connected = False
