//go:build linux

package file

import "syscall"

// fileTimes returns st_mtime and st_atime as Python computes them.
func fileTimes(st *syscall.Stat_t) (mtime, atime float64) {
	return float64(st.Mtim.Sec) + float64(st.Mtim.Nsec)*1e-9, float64(st.Atim.Sec) + float64(st.Atim.Nsec)*1e-9
}

// utimeNow is os.utime(path, None): both times set to now, which, unlike
// explicit times, the file's owner isn't the only one allowed to do.
func utimeNow(path string) error {
	const utimeNow = (1 << 30) - 1 // UTIME_NOW
	return syscall.UtimesNano(path, []syscall.Timespec{{Nsec: utimeNow}, {Nsec: utimeNow}})
}
