//go:build linux

package file

import (
	"os"
	"syscall"
	"unsafe"
)

// link is os.link as Python 3.14 and later run it on Linux: linkat with
// AT_SYMLINK_FOLLOW, so a symlink src links its target, where link(2)
// would link the symlink itself.
func link(src, dst string) error {
	const atSymlinkFollow = 0x400
	s, err := syscall.BytePtrFromString(src)
	if err != nil {
		return &os.LinkError{Op: "link", Old: src, New: dst, Err: err}
	}
	d, err := syscall.BytePtrFromString(dst)
	if err != nil {
		return &os.LinkError{Op: "link", Old: src, New: dst, Err: err}
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, uintptr(fdcwd), uintptr(unsafe.Pointer(s)),
		uintptr(fdcwd), uintptr(unsafe.Pointer(d)), atSymlinkFollow, 0)
	if errno != 0 {
		return &os.LinkError{Op: "link", Old: src, New: dst, Err: errno}
	}
	return nil
}

// AT_FDCWD, a variable since the constant -100 overflows uintptr.
var fdcwd = -0x64
