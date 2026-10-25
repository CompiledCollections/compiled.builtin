//go:build !linux

package file

import "os"

// link is os.link; link(2) follows a symlink src on these systems, as
// Python's does.
func link(src, dst string) error { return os.Link(src, dst) }
