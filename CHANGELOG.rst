===============================
compiled.builtin Release Notes
===============================

.. contents:: Topics

Unreleased
==========

Major Changes
-------------

- All modules are now in one multi-call binary, ``multicall_<goos>_<goarch>``,
  instead of one binary per module. The shipped binary is 3.1 MB instead of
  15 MB for the 13 separate ones, which keeps the collection well under
  Galaxy's 20 MB upload limit. Each task still uploads one binary, about
  2 MB more than before, and it starts faster (about 60 ms instead of 80 ms).

Minor Changes
-------------

- The binary is packed with UPX's default method (``upx -9``) instead of
  LZMA, which would take about four times as long to unpack at every start.

v0.4.0
======

New Modules
-----------

- compiled.builtin.apk - Manage packages on Alpine Linux with apk-tools 2 or 3,
- compiled.builtin.blockinfile - Insert, update or remove a block of
  multi-line text surrounded by marker lines.
- compiled.builtin.fetch - Fetch files from remote nodes.
- compiled.builtin.find - Return a list of files based on specific criteria.
- compiled.builtin.get_url - Download files from HTTP or HTTPS to the remote
  server.
- compiled.builtin.microdnf - Manage packages with microdnf on RHEL/UBI
  minimal images, or dnf5 on Fedora minimal
- compiled.builtin.replace - Replace all instances of a pattern in a file.
- compiled.builtin.template - Template a file out to a target host.
- compiled.builtin.wait_for_connection - Wait until the remote system is
  reachable.

Major Changes
-------------

- Only ansible-core 2.21 is tested now. Sanity and integration tests no longer
  run on 2.16 to 2.20, and the sanity ignore files for those versions are
  removed. 

Minor Changes
-------------

- ``codecs.EncodeString`` reports encoding errors from the start of the text,
  as Python does for a single write.
- The module temporary directory is named like Python's
  (``ansible-moduletmp-<time>-<random>``).
  a shared ``SwapsModules`` class, which template, fetch and
  wait_for_connection also use.
- uri - ``file://`` and ``ftp://`` URLs now fail with a clear message saying
  only http and https are supported.
  tarball (386 KB less in every install).


Known Issues
------------

- replace and find run Python patterns with Go's RE2. Patterns RE2 can't run,
  such as backreferences and lookarounds, fail the task.
- get_url checksums support md5, sha1, sha224, sha256, sha384 and sha512 only.

v0.3.1
======

Release Summary
---------------

Execution environment support.

Minor Changes
-------------

- ``meta/execution-environment.yml`` and ``meta/bindep.txt`` make
  ansible-builder add Go to execution environments that include the
  collection, so the action plugins can compile modules for targets other
  than linux_amd64.
- ``examples/execution-environment.yml`` is a sample ansible-builder 3
  definition on Fedora minimal with ansible-core, ansible-runner and the
  collection.
- Add a collection logo.

v0.3.0
======

New Modules
-----------

- compiled.builtin.command - Execute commands on targets.
- compiled.builtin.copy - Copy files to remote locations.
- compiled.builtin.file - Manage files and file properties.
- compiled.builtin.lineinfile - Manage lines in text files.
- compiled.builtin.ping - Try to connect to host, verify a usable module can
  run, and return pong on success.
- compiled.builtin.shell - Execute shell commands on targets.
- compiled.builtin.slurp - Slurp a file from remote nodes.
- compiled.builtin.stat - Retrieve file or file system status.
- compiled.builtin.uri - Interacts with webservices.

Release Summary
---------------

First release. Python-free replacements for ``ansible.builtin`` modules,
written in Go and compiled to static binaries. Each takes the same options and
returns the same results as the builtin it replaces, including check mode,
diff mode, warnings and error messages, and behaves like the ansible-core 2.21
builtin

Major Changes
-------------

- Only linux_amd64 binaries are built and shipped, stripped and packed with
  UPX. Other platforms compile at runtime.
- copy is ansible-core's own copy action, with the copy, file, stat and slurp
  modules it runs on the target replaced by the compiled ones.

Minor Changes
-------------

- Add repository, documentation, homepage and issues URLs to ``galaxy.yml``
  (Galaxy requires a repository).

