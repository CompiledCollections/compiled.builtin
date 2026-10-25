//go:build darwin || freebsd || netbsd

package copy

import "syscall"

// mtime is st_mtime as Python computes it.
func mtime(st *syscall.Stat_t) float64 {
	return float64(st.Mtimespec.Sec) + float64(st.Mtimespec.Nsec)*1e-9
}

// copyLinkTimes would give a symlink the times of another; the syscall
// package can't set a symlink's own times here.
func copyLinkTimes(src, dst string) {}
