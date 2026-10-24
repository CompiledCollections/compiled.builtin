//go:build linux

package stat

import "syscall"

// platformFields adds the timestamps and the fields Python's os.stat_result
// has on Linux.
func platformFields(st *syscall.Stat_t, out map[string]any) {
	out["atime"], out["mtime"], out["ctime"] = pyTime(int64(st.Atim.Sec), int64(st.Atim.Nsec)), pyTime(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)), pyTime(int64(st.Ctim.Sec), int64(st.Ctim.Nsec))
	out["blocks"] = st.Blocks
	out["disk_usage_bytes"] = st.Blocks * 512
	out["block_size"] = st.Blksize
	out["device_type"] = st.Rdev
}
