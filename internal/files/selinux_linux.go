//go:build linux

package files

import (
	"bufio"
	"compiled/builtin/internal/ansible"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func accessTime(fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec))
	}
	return fi.ModTime()
}

const selinuxXattr = "security.selinux"

var selinuxMount = sync.OnceValue(func() string {
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
		return "/sys/fs/selinux"
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) >= 3 && fields[2] == "selinuxfs" {
			return fields[1]
		}
	}
	return ""
})

// SelinuxEnabled reports whether SELinux is enabled on the target.
func SelinuxEnabled() bool { return selinuxMount() != "" }

func selinuxMLSEnabled() bool {
	if !SelinuxEnabled() {
		return false
	}
	data, err := os.ReadFile(filepath.Join(selinuxMount(), "mls"))
	return err == nil && strings.TrimSpace(string(data)) == "1"
}

func selinuxInitialContext() []*string {
	if selinuxMLSEnabled() {
		return make([]*string, 4)
	}
	return make([]*string, 3)
}

func splitContext(s string) []*string {
	parts := strings.SplitN(strings.TrimRight(s, "\x00"), ":", 4)
	out := make([]*string, len(parts))
	for i := range parts {
		out[i] = &parts[i]
	}
	return out
}

// SelinuxDefaultContext is the policy's default context for path (matchpathcon).
func SelinuxDefaultContext(path string) []*string {
	ctx := selinuxInitialContext()
	if !SelinuxEnabled() {
		return ctx
	}
	bin := BinPath("matchpathcon")
	if bin == "" {
		return ctx
	}
	out, err := exec.Command(bin, "-n", path).Output()
	if err != nil {
		return ctx
	}
	return splitContext(strings.TrimSpace(string(out)))
}

// SelinuxContext is path's current context, without following symlinks.
func SelinuxContext(path string) []*string {
	if !SelinuxEnabled() {
		return selinuxInitialContext()
	}
	v, err := lgetxattr(path, selinuxXattr)
	if err != nil {
		ansible.Fail("Failed to retrieve selinux context.", ansible.Result{"path": path, "details": err.Error()})
	}
	return splitContext(string(v))
}

func setFileCon(path, context string) error {
	return lsetxattr(path, selinuxXattr, append([]byte(context), 0))
}

// isSpecialSelinuxPath reports whether path is on a filesystem SELinux
// labels as a whole (nfs, fuse...), and that filesystem's context.
func isSpecialSelinuxPath(path string) (bool, []*string) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false, nil
	}
	defer f.Close()
	mp := findMountPoint(path)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), " ", 5)
		if len(fields) < 4 || fields[1] != mp {
			continue
		}
		for _, fs := range ansible.Params.SelinuxSpecialFS {
			if strings.Contains(fields[2], fs) {
				return true, SelinuxContext(mp)
			}
		}
	}
	return false, nil
}

func findMountPoint(path string) string {
	p := Realpath(ansible.ExpandPath(path))
	for !isMount(p) {
		p = filepath.Dir(p)
	}
	return p
}

// isMount is os.path.ismount: a different device or the same inode as the parent.
func isMount(p string) bool {
	var st, parent syscall.Stat_t
	if syscall.Lstat(p, &st) != nil || st.Mode&syscall.S_IFMT == syscall.S_IFLNK {
		return false
	}
	if syscall.Lstat(filepath.Join(p, ".."), &parent) != nil {
		return false
	}
	return st.Dev != parent.Dev || st.Ino == parent.Ino
}

func lgetxattr(path, name string) ([]byte, error) {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return nil, err
	}
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 256)
	for {
		sz, _, e := syscall.Syscall6(syscall.SYS_LGETXATTR, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(n)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		if e == syscall.ERANGE {
			buf = make([]byte, len(buf)*4)
			continue
		}
		if e != 0 {
			return nil, e
		}
		return buf[:sz], nil
	}
}

func lsetxattr(path, name string, value []byte) error {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall6(syscall.SYS_LSETXATTR, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(n)),
		uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)), 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

// copyXattrs copies extended attributes like shutil.copystat does on Linux,
// skipping the ones the destination or user can't take.
func copyXattrs(src, dst string) {
	size, err := syscall.Listxattr(src, nil)
	if err != nil || size <= 0 {
		return
	}
	buf := make([]byte, size)
	if size, err = syscall.Listxattr(src, buf); err != nil {
		return
	}
	for _, name := range strings.Split(strings.TrimRight(string(buf[:size]), "\x00"), "\x00") {
		vsize, err := syscall.Getxattr(src, name, nil)
		if err != nil {
			continue
		}
		val := make([]byte, vsize)
		if vsize, err = syscall.Getxattr(src, name, val); err != nil {
			continue
		}
		syscall.Setxattr(dst, name, val[:vsize], 0)
	}
}
