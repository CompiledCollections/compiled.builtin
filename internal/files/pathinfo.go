package files

import (
	"compiled/builtin/internal/ansible"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func init() { ansible.OnResult(addPathInfo) }

// addPathInfo adds the file's details to a result that names a path or
// dest, like AnsibleModule.add_path_info does for every exit_json/fail_json.
func addPathInfo(r ansible.Result) {
	var path string
	if v, ok := r["path"]; ok {
		path, _ = v.(string)
	} else if v, ok := r["dest"]; ok {
		path, _ = v.(string)
	}
	if path == "" || !exists(path) {
		return
	}
	var st syscall.Stat_t
	if syscall.Lstat(ansible.ExpandPath(path), &st) != nil {
		return
	}
	uid, gid := int(st.Uid), int(st.Gid)
	r["uid"], r["gid"] = uid, gid
	r["owner"], r["group"] = userName(uid), groupName(gid)
	r["mode"] = fmt.Sprintf("0%03o", uint32(st.Mode)&0o7777)

	lfi, _ := os.Lstat(path)
	fi, _ := os.Stat(path)
	switch {
	case lfi != nil && lfi.Mode()&os.ModeSymlink != 0:
		r["state"] = "link"
	case fi != nil && fi.IsDir():
		r["state"] = "directory"
	case fi != nil && nlink(fi) > 1:
		r["state"] = "hard"
	default:
		r["state"] = "file"
	}
	if SelinuxEnabled() {
		r["secontext"] = joinContext(SelinuxContext(path))
	}
	r["size"] = st.Size
}

func nlink(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}

func userName(uid int) string {
	if name, ok := ansible.UserName(uid); ok {
		return name
	}
	return strconv.Itoa(uid)
}

func groupName(gid int) string {
	if name, ok := ansible.GroupName(gid); ok {
		return name
	}
	return strconv.Itoa(gid)
}
