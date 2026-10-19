# -*- coding: utf-8 -*-
# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""Compile the Go modules: go build, stripped, then packed with UPX.

Internal to compiled.builtin: not a public API, and it can change in any release.

Kept free of Ansible imports so tests/build.py can load it directly.
"""

from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import tempfile

COLLECTION_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
MODULES_DIR = os.path.join(COLLECTION_ROOT, 'plugins', 'modules')
# One Go main package per module: modules_src/<module>/. (Go projects usually
# call this cmd/; the name matters only to people, not to the Go toolchain.)
SOURCE_DIR = 'modules_src'

# -s -w drop the symbol table and DWARF debug info; -trimpath drops local paths.
GO_BUILD_FLAGS = ['-trimpath', '-ldflags', '-s -w']
UPX_FLAGS = ['--best', '--lzma', '-q']

# Platforms UPX can pack and unpack reliably. Others ship stripped but unpacked.
UPX_PLATFORMS = {
    'linux_amd64',
    'linux_386',
    'linux_arm',
    'linux_arm64',
    'linux_ppc64le',
}


class BuildError(Exception):
    pass


def modules():
    """Names of every module with Go source in modules_src/."""
    src = os.path.join(COLLECTION_ROOT, SOURCE_DIR)
    return sorted(d for d in os.listdir(src) if os.path.isdir(os.path.join(src, d)))


def source_hash(module):
    """Hash of everything that goes into a module's binary, so edits to the
    Go source get a fresh cache entry instead of a stale binary."""
    digest = hashlib.sha256()
    paths = [os.path.join(COLLECTION_ROOT, 'go.mod'), os.path.join(COLLECTION_ROOT, 'go.sum')]
    for top in (os.path.join(SOURCE_DIR, module), 'internal'):
        for dirpath, dirnames, filenames in os.walk(os.path.join(COLLECTION_ROOT, top)):
            dirnames.sort()
            paths.extend(os.path.join(dirpath, f) for f in sorted(filenames) if f.endswith('.go'))
    for path in paths:
        if os.path.isfile(path):
            digest.update(os.path.relpath(path, COLLECTION_ROOT).encode())
            with open(path, 'rb') as f:
                digest.update(f.read())
    return digest.hexdigest()[:16]


def can_pack(platform):
    """True if UPX is installed and supports platform ('<goos>_<goarch>')."""
    return platform in UPX_PLATFORMS and shutil.which('upx') is not None


def build(module, platform, dest, pack=True):
    """Build modules_src/<module> for platform ('<goos>_<goarch>') into dest.

    With pack, the binary is compressed with UPX when the platform supports it;
    callers check can_pack() first to decide what to do when UPX is missing.
    Writes atomically, so parallel forks building the same binary never see a
    half-written file. Raises BuildError.
    """
    go = shutil.which('go')
    if go is None:
        raise BuildError('Go is not installed')
    if not os.path.isdir(os.path.join(COLLECTION_ROOT, SOURCE_DIR, module)):
        raise BuildError('No Go source for %s in %s' % (module, os.path.join(COLLECTION_ROOT, SOURCE_DIR)))

    goos, goarch = platform.split('_', 1)
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(dest), prefix='.build-')
    os.close(fd)
    env = dict(os.environ, CGO_ENABLED='0', GOOS=goos, GOARCH=goarch)
    try:
        _run([go, 'build'] + GO_BUILD_FLAGS + ['-o', tmp, './%s/%s' % (SOURCE_DIR, module)], env)
        if pack and can_pack(platform):
            # UPX refuses to overwrite its output, so pack in place.
            _run([shutil.which('upx')] + UPX_FLAGS + [tmp], env)
        os.chmod(tmp, 0o755)
        os.replace(tmp, dest)
    finally:
        if os.path.exists(tmp):
            os.remove(tmp)


def _run(argv, env):
    proc = subprocess.run(argv, cwd=COLLECTION_ROOT, env=env, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        raise BuildError('%s failed: %s' % (
            os.path.basename(argv[0]),
            proc.stderr.strip() or proc.stdout.strip() or 'exit code %d' % proc.returncode))
