//go:build darwin || freebsd || netbsd

package main

import (
	"syscall"

	"compiled/builtin/internal/ansible"
)

func pyTime(sec, nsec int64) ansible.PyFloat {
	return ansible.PyFloat(float64(sec) + float64(nsec)*1e-9)
}

// statTimes are os.stat_result's st_atime, st_mtime and st_ctime.
func statTimes(st *syscall.Stat_t) map[string]ansible.PyFloat {
	return map[string]ansible.PyFloat{
		"atime": pyTime(int64(st.Atimespec.Sec), int64(st.Atimespec.Nsec)),
		"mtime": pyTime(int64(st.Mtimespec.Sec), int64(st.Mtimespec.Nsec)),
		"ctime": pyTime(int64(st.Ctimespec.Sec), int64(st.Ctimespec.Nsec)),
	}
}

// platformFields adds what the builtin adds when os.stat_result has st_blocks.
func platformFields(st *syscall.Stat_t, out map[string]any) {
	out["blocks"] = st.Blocks
	out["disk_usage_bytes"] = st.Blocks * 512
}
