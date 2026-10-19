// Stat equivalent module, written in Go. Mirrors ansible.builtin.stat.
package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/files"
)

type Args struct {
	Path              string `json:"path"`
	Follow            bool   `json:"follow"`
	GetChecksum       *bool  `json:"get_checksum"`
	GetMime           *bool  `json:"get_mime"`
	GetAttributes     *bool  `json:"get_attributes"`
	GetSelinuxContext bool   `json:"get_selinux_context"`
	ChecksumAlgorithm string `json:"checksum_algorithm"`
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// pyTime is a st_*time as Python computes it: seconds + nanoseconds * 1e-9.
func pyTime(sec, nsec int64) ansible.PyFloat {
	return ansible.PyFloat(float64(sec) + float64(nsec)*1e-9)
}

func main() {
	var args Args
	ansible.ReadArgs(&args)
	path := ansible.ExpandPath(args.Path)

	var st syscall.Stat_t
	var err error
	if args.Follow {
		err = syscall.Stat(path, &st)
	} else {
		err = syscall.Lstat(path, &st)
	}
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			ansible.Exit(ansible.Result{"changed": false, "stat": map[string]any{"exists": false}})
		}
		errno := ansible.Errno(err)
		ansible.FailCause(strerror(errno), ansible.OSErrorMessage(&os.PathError{Op: "stat", Path: path, Err: errno}, false), nil)
	}

	mode := uint32(st.Mode)
	ft := mode & syscall.S_IFMT
	out := map[string]any{
		"exists": true,
		"path":   path,
		"mode":   octal4(mode & 0o7777),
		"isdir":  ft == syscall.S_IFDIR, "ischr": ft == syscall.S_IFCHR, "isblk": ft == syscall.S_IFBLK,
		"isreg": ft == syscall.S_IFREG, "isfifo": ft == syscall.S_IFIFO, "islnk": ft == syscall.S_IFLNK,
		"issock": ft == syscall.S_IFSOCK,
		"uid":    int(st.Uid), "gid": int(st.Gid), "size": st.Size, "inode": st.Ino, "dev": st.Dev,
		"nlink": st.Nlink,
		"wusr":  mode&0o200 != 0, "rusr": mode&0o400 != 0, "xusr": mode&0o100 != 0,
		"wgrp": mode&0o020 != 0, "rgrp": mode&0o040 != 0, "xgrp": mode&0o010 != 0,
		"woth": mode&0o002 != 0, "roth": mode&0o004 != 0, "xoth": mode&0o001 != 0,
		"isuid": mode&0o4000 != 0, "isgid": mode&0o2000 != 0,
	}
	platformFields(&st, out)

	// Permissions for the user running the module (os.access)
	for _, p := range []struct {
		key  string
		mode uint32
	}{{"readable", 4}, {"writeable", 2}, {"executable", 1}} {
		out[p.key] = syscall.Access(path, p.mode) == nil
	}

	if out["islnk"] == true {
		out["lnk_source"] = files.Realpath(path)
		out["lnk_target"], _ = os.Readlink(path)
	}
	if name, ok := ansible.UserName(int(st.Uid)); ok {
		out["pw_name"] = name
	}
	if name, ok := ansible.GroupName(int(st.Gid)); ok {
		out["gr_name"] = name
	}

	if out["isreg"] == true && out["readable"] == true && boolOr(args.GetChecksum, true) {
		out["checksum"] = digest(path, args.ChecksumAlgorithm)
	}

	if boolOr(args.GetMime, true) {
		out["mimetype"], out["charset"] = "unknown", "unknown"
		if bin := files.BinPath("file"); bin != "" {
			if res, err := exec.Command(bin, "--mime-type", "--mime-encoding", ansible.ExpandPath(path)).Output(); err == nil {
				s := string(res)
				if i := strings.LastIndex(s, ":"); i >= 0 {
					if mimetype, charset, ok := strings.Cut(s[i+1:], ";"); ok && !strings.Contains(charset, ";") {
						if _, cs, ok := strings.Cut(charset, "="); ok && !strings.Contains(cs, "=") {
							out["mimetype"] = strings.TrimSpace(mimetype)
							out["charset"] = strings.TrimSpace(cs)
						}
					}
				}
			}
		}
	}

	if boolOr(args.GetAttributes, true) {
		out["version"], out["attributes"], out["attr_flags"] = nil, []string{}, ""
		if version, flags, ok := files.FileAttributesVersion(path); ok {
			out["version"], out["attr_flags"], out["attributes"] = version, flags, files.FormatAttributes(flags)
		}
	}

	if args.GetSelinuxContext {
		out["selinux_context"] = files.SelinuxContext(path)
	}

	ansible.Exit(ansible.Result{"changed": false, "stat": out})
}

func octal4(m uint32) string { return fmt.Sprintf("%04o", m) }

// strerror is the C error text Python's OSError.strerror holds.
func strerror(errno syscall.Errno) string {
	s := errno.Error()
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// digest is AnsibleModule.digest_from_file for a regular file.
func digest(path, algorithm string) string {
	var h hash.Hash
	switch algorithm {
	case "md5":
		h = md5.New()
	case "", "sha1":
		h = sha1.New()
	case "sha224":
		h = sha256.New224()
	case "sha256":
		h = sha256.New()
	case "sha384":
		h = sha512.New384()
	case "sha512":
		h = sha512.New()
	}
	f, err := os.Open(files.Realpath(path))
	if err != nil {
		ansible.CrashError(err)
	}
	defer f.Close()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}
