package files

import (
	"compiled/builtin/internal/ansible"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// FileArgs are the common file options (add_file_common_args in AnsibleModule).
type FileArgs struct {
	Mode         json.RawMessage `json:"mode"`
	Owner        *string         `json:"owner"`
	Group        *string         `json:"group"`
	Seuser       *string         `json:"seuser"`
	Serole       *string         `json:"serole"`
	Setype       *string         `json:"setype"`
	Selevel      *string         `json:"selevel"`
	Attributes   *string         `json:"attributes"`
	UnsafeWrites *bool           `json:"unsafe_writes"`
	// Follow is the module's follow option: attributes go to a symlink's
	// target. Modules without the option leave it unset (false).
	Follow *bool `json:"follow"`
}

// CommonPath is the path load_file_common_arguments settles on: expanded,
// and resolved to its target for a symlink when follow is set.
func (fa FileArgs) CommonPath(path string) string {
	path = ansible.ExpandPath(path)
	if fa.Follow != nil && *fa.Follow {
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			path = Realpath(path)
		}
	}
	return path
}

// UnsafeWritesEnabled resolves unsafe_writes, falling back to the target's
// ANSIBLE_UNSAFE_WRITES environment variable like the Python option does.
func (fa FileArgs) UnsafeWritesEnabled() bool {
	if fa.UnsafeWrites != nil {
		return *fa.UnsafeWrites
	}
	v, ok := os.LookupEnv("ANSIBLE_UNSAFE_WRITES")
	if !ok {
		return false
	}
	b, _ := ParseBool(v)
	return b
}

// ParseBool converts a value the way Ansible's boolean() does.
func ParseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "on", "1", "true", "t", "1.0":
		return true, nil
	case "n", "no", "off", "0", "false", "f", "0.0", "":
		return false, nil
	}
	return false, fmt.Errorf("the value '%s' is not a valid boolean", s)
}

// SetFSAttributesIfDifferent applies the common file options to path and
// returns whether anything changed, recording changes in diff, like
// AnsibleModule.set_fs_attributes_if_different with load_file_common_arguments.
func (fa FileArgs) SetFSAttributesIfDifferent(path string, changed bool, diff ansible.Diff) bool {
	return fa.SetFSAttributes(fa.CommonPath(path), changed, diff)
}

// SetFSAttributes is set_fs_attributes_if_different with expand=False: path
// is used as given.
func (fa FileArgs) SetFSAttributes(path string, changed bool, diff ansible.Diff) bool {
	secontext := []*string{fa.Seuser, fa.Serole, fa.Setype}
	if selinuxMLSEnabled() {
		secontext = append(secontext, fa.Selevel)
	}
	def := SelinuxDefaultContext(path)
	for i := range def {
		if i < len(secontext) && secontext[i] != nil && *secontext[i] == "_default" {
			secontext[i] = def[i]
		}
	}
	changed = SetContextIfDifferent(path, secontext, changed, diff)
	changed = SetOwnerIfDifferent(path, fa.Owner, changed, diff)
	changed = SetGroupIfDifferent(path, fa.Group, changed, diff)
	changed = SetModeIfDifferent(path, fa.Mode, changed, diff)
	changed = SetAttributesIfDifferent(path, fa.Attributes, changed, diff)
	return changed
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func checkFileAbsentIfCheckMode(path string) bool {
	return ansible.CheckMode() && !exists(path)
}

func lstatIDs(path string) (int, int) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		ansible.Fail(err.Error(), ansible.Result{"path": path})
	}
	return int(st.Uid), int(st.Gid)
}

// SetOwnerIfDifferent is AnsibleModule.set_owner_if_different.
func SetOwnerIfDifferent(path string, owner *string, changed bool, diff ansible.Diff) bool {
	if owner == nil {
		return changed
	}
	if checkFileAbsentIfCheckMode(path) {
		return true
	}
	origUID, _ := lstatIDs(path)
	uid, err := strconv.Atoi(strings.TrimSpace(*owner))
	if err != nil {
		u, ok := ansible.LookupUser(*owner)
		if !ok {
			ansible.Fail("chown failed: failed to look up user "+*owner, ansible.Result{"path": path})
		}
		uid, _ = strconv.Atoi(u.Uid)
	}
	if origUID != uid {
		diff.Set("owner", origUID, uid)
		if ansible.CheckMode() {
			return true
		}
		if err := os.Lchown(path, uid, -1); err != nil {
			ansible.Fail("chown failed: "+errnoMessage(err), ansible.Result{"path": path})
		}
		changed = true
	}
	return changed
}

// SetGroupIfDifferent is AnsibleModule.set_group_if_different.
func SetGroupIfDifferent(path string, group *string, changed bool, diff ansible.Diff) bool {
	if group == nil {
		return changed
	}
	if checkFileAbsentIfCheckMode(path) {
		return true
	}
	_, origGID := lstatIDs(path)
	gid, err := strconv.Atoi(strings.TrimSpace(*group))
	if err != nil {
		var ok bool
		if gid, ok = ansible.LookupGroupID(*group); !ok {
			ansible.Fail("chgrp failed: failed to look up group "+*group, ansible.Result{"path": path})
		}
	}
	if origGID != gid {
		diff.Set("group", origGID, gid)
		if ansible.CheckMode() {
			return true
		}
		if err := os.Lchown(path, -1, gid); err != nil {
			ansible.Fail("chgrp failed", ansible.Result{"path": path})
		}
		changed = true
	}
	return changed
}

// SetModeIfDifferent is AnsibleModule.set_mode_if_different. mode is the raw
// JSON value: an integer, an octal string, or a symbolic mode.
func SetModeIfDifferent(path string, raw json.RawMessage, changed bool, diff ansible.Diff) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return changed
	}
	if checkFileAbsentIfCheckMode(path) {
		return true
	}
	fi, err := os.Lstat(path)
	if err != nil {
		ansible.Fail(errnoMessage(err), ansible.Result{"path": path})
	}
	var mode int64
	var str string
	switch {
	case raw[0] != '"':
		// An integer, as YAML gives for mode: 0644. Taken as is, like Python.
		var num json.Number
		if json.Unmarshal(raw, &num) != nil {
			ansible.Fail("mode must be in octal or symbolic form", ansible.Result{"path": path, "details": string(raw)})
		}
		if mode, err = num.Int64(); err != nil {
			ansible.Fail("mode must be in octal or symbolic form", ansible.Result{"path": path, "details": err.Error()})
		}
	case json.Unmarshal(raw, &str) == nil:
		if m, ok := parsePythonOctal(str); ok {
			mode = m
		} else {
			mode, err = SymbolicModeToOctal(fi, str)
			if err != nil {
				ansible.Fail("mode must be in octal or symbolic form", ansible.Result{"path": path, "details": err.Error()})
			}
			if mode&^0o7777 != 0 {
				ansible.Fail("Invalid mode supplied, only permission info is allowed", ansible.Result{"path": path, "details": mode})
			}
		}
	default:
		ansible.Fail("mode must be in octal or symbolic form", ansible.Result{"path": path, "details": string(raw)})
	}

	prev := int64(unixMode(fi))
	if prev == mode {
		return changed
	}
	diff.Set("mode", fmt.Sprintf("0%03o", prev), fmt.Sprintf("0%03o", mode))
	if ansible.CheckMode() {
		return true
	}
	isLink := fi.Mode()&os.ModeSymlink != 0
	err = chmodNoFollow(path, isLink, mode)
	if err != nil {
		var errno syscall.Errno
		errors.As(err, &errno)
		switch {
		case isLink && (errno == syscall.EACCES || errno == syscall.EPERM || errno == syscall.EROFS):
		case errno == syscall.ENOENT || errno == syscall.ELOOP:
		default:
			ansible.Fail("chmod failed", ansible.Result{"path": path, "details": errnoMessage(err)})
		}
	}
	if fi, err = os.Lstat(path); err == nil && int64(unixMode(fi)) != prev {
		changed = true
	}
	return changed
}

// chmodNoFollow changes the mode without changing a symlink's target, the
// way Python does on systems without lchmod: chmod, then undo it on the target.
func chmodNoFollow(path string, isLink bool, mode int64) error {
	if !isLink {
		return syscall.Chmod(path, uint32(mode))
	}
	before, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := syscall.Chmod(path, uint32(mode)); err != nil {
		return err
	}
	if after, err := os.Stat(path); err == nil && after.Mode() != before.Mode() {
		return syscall.Chmod(path, uint32(unixMode(before)))
	}
	return nil
}

// unixMode returns the permission bits including setuid, setgid and sticky.
func unixMode(fi os.FileInfo) uint32 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint32(st.Mode) & 0o7777
	}
	return uint32(fi.Mode().Perm())
}

// parsePythonOctal parses a string the way Python's int(s, 8) does.
func parsePythonOctal(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	sign := int64(1)
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
	}
	if strings.HasPrefix(s, "0o") || strings.HasPrefix(s, "0O") {
		s = strings.TrimPrefix(s[2:], "_")
	}
	if s == "" || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 8, 64)
	if err != nil {
		return 0, false
	}
	return sign * v, true
}

var (
	modeOperatorRe = &ansible.LazyRegexp{Expr: `[+=-]`}
	usersRe        = &ansible.LazyRegexp{Expr: `^[ugo]+$`}
	permsRe        = &ansible.LazyRegexp{Expr: `^[rwxXstugo]*$`}
)

// SymbolicModeToOctal is AnsibleModule._symbolic_mode_to_octal: chmod-style
// symbolic modes such as "u=rw,g=r,o=r" or "a+X", relative to fi's mode.
func SymbolicModeToOctal(fi os.FileInfo, symbolic string) (int64, error) {
	newMode := int64(unixMode(fi))
	for _, mode := range strings.Split(symbolic, ",") {
		permlist := modeOperatorRe.Get().Split(mode, -1)
		opers := modeOperatorRe.Get().FindAllString(mode, -1)
		users := permlist[0]
		permlist = permlist[1:]
		useUmask := users == ""
		if users == "a" || users == "" {
			users = "ugo"
		}
		if !usersRe.Get().MatchString(users) {
			return 0, fmt.Errorf("bad symbolic permission for mode: %s", mode)
		}
		for idx, perms := range permlist {
			if !permsRe.Get().MatchString(perms) {
				return 0, fmt.Errorf("bad symbolic permission for mode: %s", mode)
			}
			for _, user := range users {
				apply := octalFromSymbolicPerms(fi, user, perms, useUmask, newMode)
				newMode = applyModeOperation(user, opers[idx], apply, newMode)
			}
		}
	}
	return newMode, nil
}

func applyModeOperation(user rune, op string, apply, current int64) int64 {
	switch op {
	case "=":
		var mask int64
		switch user {
		case 'u':
			mask = 0o700 | 0o4000
		case 'g':
			mask = 0o070 | 0o2000
		case 'o':
			mask = 0o007 | 0o1000
		}
		return (current & (mask ^ 0o7777)) | apply
	case "+":
		return current | apply
	case "-":
		return current - (current & apply)
	}
	return current
}

func octalFromSymbolicPerms(fi os.FileInfo, user rune, perms string, useUmask bool, prev int64) int64 {
	applyX := fi.IsDir() || prev&0o111 > 0
	umask := int64(currentUmask())
	rev := umask ^ 0o7777
	bit := func(b int64) int64 {
		if useUmask {
			return rev & b
		}
		return b
	}
	table := map[rune]map[rune]int64{
		'u': {
			'r': bit(0o400), 'w': bit(0o200), 'x': bit(0o100), 's': 0o4000, 't': 0,
			'u': prev & 0o700, 'g': (prev & 0o070) << 3, 'o': (prev & 0o007) << 6,
		},
		'g': {
			'r': bit(0o040), 'w': bit(0o020), 'x': bit(0o010), 's': 0o2000, 't': 0,
			'u': (prev & 0o700) >> 3, 'g': prev & 0o070, 'o': (prev & 0o007) << 3,
		},
		'o': {
			'r': bit(0o004), 'w': bit(0o002), 'x': bit(0o001), 's': 0, 't': 0o1000,
			'u': (prev & 0o700) >> 6, 'g': (prev & 0o070) >> 3, 'o': prev & 0o007,
		},
	}
	xbits := map[rune]int64{'u': 0o100, 'g': 0o010, 'o': 0o001}
	if applyX {
		table[user]['X'] = xbits[user]
	} else {
		table[user]['X'] = 0
	}
	var out int64
	for _, p := range perms {
		out |= table[user][p]
	}
	return out
}

func currentUmask() int {
	m := syscall.Umask(0)
	syscall.Umask(m)
	return m
}

// BinPath finds an executable like AnsibleModule.get_bin_path: PATH plus the sbin directories.
func BinPath(name string) string {
	dirs := strings.Split(os.Getenv("PATH"), ":")
	dirs = append(dirs, "/sbin", "/usr/sbin", "/usr/local/sbin")
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := d + "/" + name
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// FileAttributes runs lsattr like AnsibleModule.get_file_attributes and
// returns the attribute flags (for example "ie"), and whether it worked.
func FileAttributes(path string) (string, bool) {
	bin := BinPath("lsattr")
	if bin == "" {
		return "", false
	}
	out, err := exec.Command(bin, "-d", path).Output()
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", false
	}
	return strings.ReplaceAll(fields[0], "-", ""), true
}

// FileAttributesVersion runs lsattr -vd, as get_file_attributes does with
// include_version: the file's version and attribute flags.
func FileAttributesVersion(path string) (version, flags string, ok bool) {
	bin := BinPath("lsattr")
	if bin == "" {
		return "", "", false
	}
	out, err := exec.Command(bin, "-vd", path).Output()
	if err != nil {
		return "", "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return "", "", false
	}
	return strings.TrimSpace(fields[0]), strings.TrimSpace(strings.ReplaceAll(fields[1], "-", "")), true
}

// fileAttributeNames is module_utils.common.file.FILE_ATTRIBUTES.
var fileAttributeNames = map[rune]string{
	'A': "noatime", 'a': "append", 'c': "compressed", 'C': "nocow", 'd': "nodump", 'D': "dirsync",
	'e': "extents", 'E': "encrypted", 'h': "blocksize", 'i': "immutable", 'I': "indexed",
	'j': "journalled", 'N': "inline", 's': "zero", 'S': "synchronous", 't': "notail",
	'T': "blockroot", 'u': "undelete", 'X': "compressedraw", 'Z': "compresseddirty",
}

// FormatAttributes is format_attributes: attribute flags as names, in flag order.
func FormatAttributes(flags string) []string {
	out := []string{}
	for _, f := range flags {
		if name, ok := fileAttributeNames[f]; ok {
			out = append(out, name)
		}
	}
	return out
}

// SetAttributesIfDifferent is AnsibleModule.set_attributes_if_different, using chattr.
func SetAttributesIfDifferent(path string, attributes *string, changed bool, diff ansible.Diff) bool {
	if attributes == nil {
		return changed
	}
	if checkFileAbsentIfCheckMode(path) {
		return true
	}
	existing, ok := FileAttributes(path)
	attrs := *attributes
	mod := "="
	if strings.HasPrefix(attrs, "-") || strings.HasPrefix(attrs, "+") {
		mod, attrs = attrs[:1], attrs[1:]
	}
	if attrs == "" || (ok && existing == attrs && mod != "-") {
		return changed
	}
	bin := BinPath("chattr")
	if bin == "" {
		return changed
	}
	changed = true
	var before any
	if ok {
		before = existing
	}
	diff.Set("attributes", before, mod+attrs)
	if !ansible.CheckMode() {
		cmd := exec.Command(bin, mod+attrs, path)
		var out, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil || stderr.Len() > 0 {
			ansible.Fail("chattr failed", ansible.Result{"path": path, "details": "Error while setting attributes: " + out.String() + stderr.String()})
		}
	}
	return changed
}
