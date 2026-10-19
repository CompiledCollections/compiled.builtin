//go:build darwin || freebsd || netbsd

package main

import "syscall"

// platformFields adds the timestamps and the fields Python's os.stat_result
// has on macOS and the BSDs (flags, generation, birth time).
func platformFields(st *syscall.Stat_t, out map[string]any) {
	out["atime"], out["mtime"], out["ctime"] = pyTime(int64(st.Atimespec.Sec), int64(st.Atimespec.Nsec)), pyTime(int64(st.Mtimespec.Sec), int64(st.Mtimespec.Nsec)), pyTime(int64(st.Ctimespec.Sec), int64(st.Ctimespec.Nsec))
	out["blocks"] = st.Blocks
	out["disk_usage_bytes"] = st.Blocks * 512
	out["block_size"] = st.Blksize
	out["device_type"] = st.Rdev
	out["flags"] = st.Flags
	out["generation"] = st.Gen
	out["birthtime"] = pyTime(int64(st.Birthtimespec.Sec), int64(st.Birthtimespec.Nsec))
}
