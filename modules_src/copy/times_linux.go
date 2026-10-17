//go:build linux

package main

import (
	"syscall"
	"unsafe"
)

// mtime is st_mtime as Python computes it.
func mtime(st *syscall.Stat_t) float64 {
	return float64(st.Mtim.Sec) + float64(st.Mtim.Nsec)*1e-9
}

// copyLinkTimes gives the symlink dst the times of the symlink src, as
// copystat(follow_symlinks=False) does, best effort.
func copyLinkTimes(src, dst string) {
	var st syscall.Stat_t
	if syscall.Lstat(src, &st) != nil {
		return
	}
	p, err := syscall.BytePtrFromString(dst)
	if err != nil {
		return
	}
	ts := [2]syscall.Timespec{st.Atim, st.Mtim}
	const atSymlinkNofollow = 0x100
	syscall.Syscall6(syscall.SYS_UTIMENSAT, uintptr(fdcwd), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&ts[0])), atSymlinkNofollow, 0, 0)
}

// AT_FDCWD, a variable since the constant -100 overflows uintptr.
var fdcwd = -0x64
