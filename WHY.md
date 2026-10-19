# What is this?

compiled.builtin is a collection that replicates as much funcionality from ansible.builtin as possible, but with binary modules written in Go and compiled with no dependencies.

# Why?

A large percentage of Ansible playbooks, especially for CLI applications or related to Linux, are built using the ansible.builtin collection shipped with ansible-core. I believe we should have an alternative to that collection for:

- Minimal distros like Alpine
- Older OS where we can't or won't install current Python (RHEL <7, old Debian, BSD, AIX...)
- Appliances that don't have a package manager or where that package manager is restricted (network appliances/immutable OS/WindRiver/ESXi)
- Running automation directly on running pods/containers with podman/k8s connection plugins

Also, I want to improve the experience of playbook writers that for some reason are stuck with using `raw` and `script` .

# Pros and cons
## Pros
- No deps on Python
- Modules run faster than those written in Python
- Potentially we can support all targets in current Go: Linux, Windows, AIX, BSD, MacOS / AMD64, ARM64, i3

## Cons
- File size for module is larger, even when stripping and compressing with UPX (~0.9MB)
- Can't ship precompiled binaries for all architectures, so the Go toolchain should be installed on the controller, for anything other than Linux on x86_64

# Features
- The collection ships compiled binaries for linux/amd64 only. For any other platform, the action plugin compiles the module at runtime on the controller (or in the EE) if Go is installed, and caches the result. UPX in the EE keeps those binaries small

# Status
