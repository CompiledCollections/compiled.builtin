//go:build !linux && !darwin && !freebsd && !netbsd

package main

import "syscall"

// platformFields adds the timestamps; other platforms' extra fields differ
// too much to list.
func platformFields(st *syscall.Stat_t, out map[string]any) {
	out["atime"], out["mtime"], out["ctime"] = pyTime(int64(st.Atim.Sec), int64(st.Atim.Nsec)), pyTime(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)), pyTime(int64(st.Ctim.Sec), int64(st.Ctim.Nsec))
	out["blocks"] = st.Blocks
	out["disk_usage_bytes"] = int64(st.Blocks) * 512
	out["block_size"] = st.Blksize
	out["device_type"] = st.Rdev
}
