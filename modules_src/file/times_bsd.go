//go:build darwin || freebsd || netbsd

package main

import (
	"syscall"
	"time"
)

// fileTimes returns st_mtime and st_atime as Python computes them.
func fileTimes(st *syscall.Stat_t) (mtime, atime float64) {
	return float64(st.Mtimespec.Sec) + float64(st.Mtimespec.Nsec)*1e-9, float64(st.Atimespec.Sec) + float64(st.Atimespec.Nsec)*1e-9
}

// utimeNow is os.utime(path, None), here with explicit current times:
// only the file's owner (or root) may set them.
func utimeNow(path string) error {
	now := syscall.NsecToTimespec(time.Now().UnixNano())
	return syscall.UtimesNano(path, []syscall.Timespec{now, now})
}
