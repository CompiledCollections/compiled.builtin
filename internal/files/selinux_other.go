//go:build !linux

package files

import (
	"errors"
	"os"
	"time"
)

// accessTime falls back to the modification time where Stat_t differs per OS.
func accessTime(fi os.FileInfo) time.Time { return fi.ModTime() }

// SELinux only exists on Linux; elsewhere it behaves as disabled, as it does
// for Python modules without the selinux bindings.

func SelinuxEnabled() bool { return false }

func selinuxMLSEnabled() bool { return false }

func selinuxInitialContext() []*string { return make([]*string, 3) }

func SelinuxDefaultContext(path string) []*string { return selinuxInitialContext() }

func SelinuxContext(path string) []*string { return selinuxInitialContext() }

func setFileCon(path, context string) error { return errors.New("SELinux is not supported") }

func isSpecialSelinuxPath(path string) (bool, []*string) { return false, nil }

func copyXattrs(src, dst string) {}
