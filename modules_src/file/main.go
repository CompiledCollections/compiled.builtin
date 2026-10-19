// File equivalent module, written in Go. Mirrors ansible.builtin.file.
package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/files"
)

type Args struct {
	files.FileArgs
	State                  *string `json:"state"`
	Path                   string  `json:"path"`
	OriginalBasename       *string `json:"_original_basename"`
	Recurse                bool    `json:"recurse"`
	Force                  bool    `json:"force"`
	DiffPeek               *bool   `json:"_diff_peek"`
	Src                    *string `json:"src"`
	ModificationTime       *string `json:"modification_time"`
	ModificationTimeFormat string  `json:"modification_time_format"`
	AccessTime             *string `json:"access_time"`
	AccessTimeFormat       string  `json:"access_time_format"`
}

var (
	args   Args
	follow bool
	// touched is the file execute_touch created, removed if the task then fails.
	touched string
)

func main() {
	ansible.ReadArgs(&args)
	follow = args.Follow == nil || *args.Follow
	args.Follow = &follow
	// Type 'path' options are expanded by AnsibleModule.
	args.Path = ansible.ExpandPath(args.Path)
	if args.Src != nil {
		src := ansible.ExpandPath(*args.Src)
		args.Src = &src
	}
	ansible.OnResult(func(r ansible.Result) {
		if touched != "" && r["failed"] == true {
			os.Remove(touched)
		}
	})

	additionalParameterHandling()
	state, path := *args.State, args.Path

	if ansible.CheckMode() && state != "absent" {
		if args.Owner != nil && *args.Owner != "" {
			checkOwnerExists(*args.Owner)
		}
		if args.Group != nil && *args.Group != "" {
			checkGroupExists(*args.Group)
		}
	}

	ts := timestamps{
		mtime:       backwardCompatibleTimestamp(args.ModificationTime, state),
		mtimeFormat: args.ModificationTimeFormat,
		atime:       backwardCompatibleTimestamp(args.AccessTime, state),
		atimeFormat: args.AccessTimeFormat,
	}

	if args.DiffPeek != nil {
		ansible.Exit(ansible.Result{"path": path, "changed": false, "appears_binary": diffPeek(path)})
	}

	var result ansible.Result
	switch state {
	case "file":
		result = ensureFileAttributes(path, ts)
	case "directory":
		result = ensureDirectory(path, ts)
	case "link":
		result = ensureSymlink(path, args.Src, ts)
	case "hard":
		result = ensureHardlink(path, args.Src, ts)
	case "touch":
		result = executeTouch(path, ts)
	case "absent":
		result = ensureAbsent(path)
	}
	if !ansible.DiffMode() {
		delete(result, "diff")
	}
	ansible.Exit(result)
}

// additionalParameterHandling puts path inside a directory it names,
// defaults state to the current one, and checks the options that depend
// on state.
func additionalParameterHandling() {
	state := ""
	if args.State != nil {
		state = *args.State
	}
	if state != "link" && state != "absent" && isDir(args.Path) {
		basename := ""
		if args.OriginalBasename != nil && *args.OriginalBasename != "" {
			basename = *args.OriginalBasename
		} else if args.Src != nil && *args.Src != "" {
			basename = files.PyBasename(*args.Src)
		}
		if basename != "" {
			args.Path = files.PyJoin(args.Path, basename)
		}
	}

	prevState := getState(args.Path)
	if args.State == nil {
		switch {
		case prevState != "absent":
			state = prevState
		case args.Recurse:
			state = "directory"
		default:
			state = "file"
		}
		args.State = &state
	}

	if args.Recurse && state != "directory" {
		ansible.Fail("recurse option requires state to be 'directory'", ansible.Result{"path": args.Path})
	}
	if args.Src != nil && *args.Src != "" && state != "link" && state != "hard" {
		ansible.Fail("src option requires state to be 'link' or 'hard'", ansible.Result{"path": args.Path})
	}
}

func getState(path string) string {
	fi, err := os.Lstat(path)
	if err != nil {
		switch ansible.Errno(err) {
		case syscall.ENOENT:
			return "absent"
		case syscall.EACCES, syscall.EPERM:
			ansible.Warn(fmt.Sprintf("Insufficient permissions to access %s. Treating as absent", path))
			return "absent"
		}
		// Uncaught in Python (NotADirectoryError, for example).
		ansible.Crash(ansible.OSErrorMessage(err, true), "")
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return "link"
	case fi.IsDir():
		return "directory"
	case nlink(fi) > 1:
		return "hard"
	}
	// Could be many other things, but defaulting to file.
	return "file"
}

func nlink(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func isDir(path string) bool { fi, err := os.Stat(path); return err == nil && fi.IsDir() }

func isLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func inode(path string) uint64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		ansible.Crash(ansible.OSErrorMessage(&os.PathError{Op: "stat", Path: path, Err: err}, true), "")
	}
	return uint64(st.Ino)
}

// readlink is os.readlink, which raises (uncaught) for anything not a link.
func readlink(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		ansible.Crash(ansible.OSErrorMessage(err, true), "")
	}
	return target
}

func initialDiff(path, state, prevState string) ansible.Diff {
	before := map[string]any{"path": path}
	after := map[string]any{"path": path}
	if prevState != state {
		before["state"] = prevState
		after["state"] = state
		if state == "absent" && prevState == "directory" {
			dirs, regular := []string{}, []string{}
			files.Walk(path, func(dir string, subdirs, names []string) {
				for _, d := range subdirs {
					dirs = append(dirs, files.PyJoin(dir, d))
				}
				for _, n := range names {
					regular = append(regular, files.PyJoin(dir, n))
				}
			})
			before["path_content"] = map[string]any{"directories": dirs, "files": regular}
		}
	}
	return ansible.Diff{"before": before, "after": after}
}

// Timestamps

type stampKind int

const (
	preserve stampKind = iota
	now
	fixed
)

type stamp struct {
	kind  stampKind
	value float64
}

type timestamps struct {
	mtime, atime             *string
	mtimeFormat, atimeFormat string
}

func (t timestamps) resolve() (mtime, atime stamp) {
	return timestampFor(t.mtime, t.mtimeFormat), timestampFor(t.atime, t.atimeFormat)
}

func backwardCompatibleTimestamp(param *string, state string) *string {
	if param != nil {
		return param
	}
	switch state {
	case "file", "hard", "directory", "link":
		s := "preserve"
		return &s
	case "touch":
		s := "now"
		return &s
	}
	return nil
}

func timestampFor(formatted *string, format string) stamp {
	switch *formatted {
	case "preserve":
		return stamp{kind: preserve}
	case "now":
		return stamp{kind: now}
	}
	v, err := ansible.Strptime(*formatted, format)
	if err != nil {
		ansible.Fail(fmt.Sprintf("Error while obtaining timestamp for time %s using format %s: %s", *formatted, format, err), nil)
	}
	return stamp{kind: fixed, value: v}
}

// pyNow is time.time().
func pyNow() float64 {
	ns := time.Now().UnixNano()
	if ns%1e9 == 0 {
		return float64(ns / 1e9)
	}
	return float64(ns) / 1e9
}

// toTimespec converts seconds the way os.utime does (rounding down).
func toTimespec(secs float64) syscall.Timespec {
	intpart, frac := math.Modf(secs)
	ns := math.Floor(frac * 1e9)
	if ns >= 1e9 {
		ns -= 1e9
		intpart++
	} else if ns < 0 {
		ns += 1e9
		intpart--
	}
	return syscall.NsecToTimespec(int64(intpart)*1e9 + int64(ns))
}

func updateTimestampForFile(path string, mtime, atime stamp, diff ansible.Diff) bool {
	fail := func(err error) {
		ansible.Fail("Error while updating modification or access time: "+ansible.OSErrorMessage(err, true), ansible.Result{"path": path})
	}
	var st syscall.Stat_t
	statTimes := func() (float64, float64) {
		if err := syscall.Stat(path, &st); err != nil {
			fail(&os.PathError{Op: "stat", Path: path, Err: err})
		}
		return fileTimes(&st)
	}

	var prevMtime, prevAtime, newMtime, newAtime float64
	setNow := false
	if mtime.kind == now && atime.kind == now {
		// utime(path, None) needs no ownership of the file, unlike explicit
		// times; the diff gets the current time.
		newMtime = pyNow()
		newAtime = newMtime
		prevMtime, prevAtime = statTimes()
		setNow = true
	} else {
		if mtime.kind == preserve && atime.kind == preserve {
			return false
		}
		prevMtime, prevAtime = statTimes()
		newMtime, newAtime = pick(mtime, prevMtime), pick(atime, prevAtime)
		if newMtime == prevMtime && newAtime == prevAtime {
			return false
		}
	}

	if !ansible.CheckMode() {
		var err error
		if setNow {
			err = utimeNow(path)
		} else {
			err = syscall.UtimesNano(path, []syscall.Timespec{toTimespec(newAtime), toTimespec(newMtime)})
		}
		if err != nil {
			fail(&os.PathError{Op: "utime", Path: path, Err: err})
		}
	}

	if newMtime != prevMtime {
		diff.Set("mtime", ansible.PyFloat(prevMtime), ansible.PyFloat(newMtime))
	}
	if newAtime != prevAtime {
		diff.Set("atime", ansible.PyFloat(prevAtime), ansible.PyFloat(newAtime))
	}
	return true
}

func pick(s stamp, previous float64) float64 {
	switch s.kind {
	case preserve:
		return previous
	case now:
		return pyNow()
	}
	return s.value
}

func diffPeek(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 8192)
	n, err := io.ReadFull(f, head)
	if err != nil && n == 0 {
		return false
	}
	return bytes.IndexByte(head[:n], 0) >= 0
}

// States

func ensureAbsent(path string) ansible.Result {
	prevState := getState(path)
	if prevState == "absent" {
		return ansible.Result{"path": path, "changed": false, "state": "absent"}
	}
	diff := initialDiff(path, "absent", prevState)
	if !ansible.CheckMode() {
		if prevState == "directory" {
			if err := rmtree(path); err != nil {
				ansible.Fail("rmtree failed: "+ansible.OSErrorMessage(err, false), nil)
			}
		} else if err := syscall.Unlink(path); err != nil && err != syscall.ENOENT {
			ansible.FailCause("Unlinking failed.", ansible.OSErrorMessage(&os.PathError{Op: "unlink", Path: path, Err: err}, true), ansible.Result{"path": path})
		}
	}
	return ansible.Result{"path": path, "changed": true, "diff": diff, "state": "absent"}
}

// rmtree is shutil.rmtree as Python 3.12 and later run it: each
// directory's files are removed as it is listed, then its subdirectories,
// last listed first. The first error ends it, naming the directory being
// worked on (for a file that can't be removed, the one holding it).
func rmtree(top string) error {
	type step struct {
		rmdir bool
		path  string
	}
	stack := []step{{path: top}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		fail := func(err error) error {
			return &os.PathError{Op: "rmtree", Path: s.path, Err: ansible.Errno(err)}
		}
		if s.rmdir {
			if err := syscall.Rmdir(s.path); err != nil && (err != syscall.ENOENT || s.path == top) {
				return fail(err)
			}
			continue
		}
		names, err := readdirnames(s.path)
		if err != nil {
			if ansible.Errno(err) == syscall.ENOENT && s.path != top {
				continue
			}
			return fail(err)
		}
		stack = append(stack, step{rmdir: true, path: s.path})
		for _, name := range names {
			child := files.PyJoin(s.path, name)
			if fi, err := os.Lstat(child); err == nil && fi.IsDir() {
				stack = append(stack, step{path: child})
				continue
			}
			if err := syscall.Unlink(child); err != nil && err != syscall.ENOENT {
				return fail(err)
			}
		}
	}
	return nil
}

func executeTouch(path string, ts timestamps) ansible.Result {
	prevState := getState(path)
	changed := false
	result := ansible.Result{"dest": path}
	mtime, atime := ts.resolve()

	if prevState == "absent" {
		if ansible.CheckMode() {
			result["changed"] = true
			return result
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			ansible.FailCause("Error, could not touch target.", ansible.OSErrorMessage(err, true), ansible.Result{"path": path})
		}
		f.Close()
		changed = true
		touched = path
	}
	diff := initialDiff(path, "touch", prevState)
	target := args.CommonPath(path)
	changed = args.SetFSAttributes(target, changed, diff)
	changed = updateTimestampForFile(target, mtime, atime, diff) || changed
	touched = ""

	result["changed"] = changed
	result["diff"] = diff
	return result
}

func ensureFileAttributes(path string, ts timestamps) ansible.Result {
	prevState := getState(path)
	target := args.CommonPath(path)
	mtime, atime := ts.resolve()

	if prevState != "file" && follow && prevState == "link" {
		// Follow the symlink and operate on the original.
		path = files.Realpath(path)
		prevState = getState(path)
		target = path
	}
	if prevState != "file" && prevState != "hard" {
		// The file is absent, and any other state is a conflict.
		ansible.Fail(fmt.Sprintf("file (%s) is %s, cannot continue", path, prevState), ansible.Result{"path": path, "state": prevState})
	}

	diff := initialDiff(path, "file", prevState)
	changed := args.SetFSAttributes(target, false, diff)
	changed = updateTimestampForFile(target, mtime, atime, diff) || changed
	return ansible.Result{"path": path, "changed": changed, "diff": diff}
}

func ensureDirectory(path string, ts timestamps) ansible.Result {
	prevState := getState(path)
	target := args.CommonPath(path)
	mtime, atime := ts.resolve()

	// For followed symlinks, operate on the target of the link.
	if follow && prevState == "link" {
		path = files.Realpath(path)
		target = path
		prevState = getState(path)
	}

	changed := false
	diff := initialDiff(path, "directory", prevState)

	switch prevState {
	case "absent":
		if ansible.CheckMode() {
			return ansible.Result{"path": path, "changed": true, "diff": diff}
		}
		// Like mkdir -p, applying the attributes to each directory created.
		curpath := ""
		for _, dirname := range strings.Split(strings.Trim(path, "/"), "/") {
			curpath = curpath + "/" + dirname
			if !strings.HasPrefix(path, "/") {
				curpath = strings.TrimLeft(curpath, "/")
			}
			if exists(curpath) {
				continue
			}
			if err := syscall.Mkdir(curpath, 0o777); err != nil {
				// Something else may have created it since; only a directory is fine.
				if !(err == syscall.EEXIST && isDir(curpath)) {
					err := &os.PathError{Op: "mkdir", Path: curpath, Err: err}
					ansible.Fail(fmt.Sprintf("There was an issue creating %s as requested: %s", curpath, ansible.OSErrorMessage(err, true)), ansible.Result{"path": path})
				}
			} else {
				changed = true
			}
			changed = args.SetFSAttributes(curpath, changed, diff)
			changed = updateTimestampForFile(target, mtime, atime, diff) || changed
		}
		return ansible.Result{"path": path, "changed": changed, "diff": diff}
	case "directory":
	default:
		ansible.Fail(fmt.Sprintf("%s already exists as a %s", path, prevState), ansible.Result{"path": path})
	}

	changed = args.SetFSAttributes(target, changed, diff)
	changed = updateTimestampForFile(target, mtime, atime, diff) || changed
	if args.Recurse {
		changed = recursiveSetAttributes(path, mtime, atime) || changed
	}
	return ansible.Result{"path": path, "changed": changed, "diff": diff}
}

func recursiveSetAttributes(root string, mtime, atime stamp) bool {
	changed := false
	apply := func(path string) {
		changed = args.SetFSAttributes(path, changed, nil) || changed
		changed = updateTimestampForFile(path, mtime, atime, nil) || changed
	}
	files.Walk(root, func(dir string, dirs, names []string) {
		for _, name := range append(dirs, names...) {
			fsname := files.PyJoin(dir, name)
			// A link itself gets the attributes too.
			apply(fsname)
			if follow && isLink(fsname) {
				target := files.PyJoin(dir, readlink(fsname))
				// The link target could be nonexistent.
				if exists(target) {
					if isDir(target) {
						changed = recursiveSetAttributes(target, mtime, atime) || changed
					}
					apply(target)
				}
			}
		}
	})
	return changed
}

// tmpPath is the temporary name links are created under before being
// renamed over path: .<pid>.<time.time()>.tmp beside it.
func tmpPath(path string) string {
	return files.PyDirname(path) + "/" + fmt.Sprintf(".%d.%s.tmp", os.Getpid(), ansible.PyFloat(pyNow()))
}

func pyStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func ensureSymlink(path string, srcp *string, ts timestamps) ansible.Result {
	prevState := getState(path)
	mtime, atime := ts.resolve()
	// src is also passed by template and copy just for information.
	if srcp == nil && follow && exists(path) {
		// Use the current target of the link as the source.
		s := readlink(path)
		srcp = &s
	}

	var relpath string
	if !isLink(path) && isDir(path) {
		relpath = path
	} else {
		relpath = files.PyDirname(path)
	}

	// Without src, an existing link is being updated.
	absrc := ""
	if srcp != nil {
		absrc = files.PyJoin(relpath, *srcp)
	}
	if !args.Force && srcp != nil && !exists(absrc) {
		ansible.Fail("src file does not exist, use 'force=yes' if you really want to create the link: "+absrc,
			ansible.Result{"path": path, "src": *srcp})
	}

	switch prevState {
	case "directory":
		if !args.Force {
			ansible.Fail(fmt.Sprintf("refusing to convert from %s to symlink for %s", prevState, path), ansible.Result{"path": path})
		}
		if names, _ := readdirnames(path); len(names) > 0 {
			// Refuse to replace a directory that has files in it.
			ansible.Fail(fmt.Sprintf("the directory %s is not empty, refusing to convert it", path), ansible.Result{"path": path})
		}
	case "file", "hard":
		if !args.Force {
			ansible.Fail(fmt.Sprintf("refusing to convert from %s to symlink for %s", prevState, path), ansible.Result{"path": path})
		}
	}

	diff := initialDiff(path, "link", prevState)
	changed := false
	switch prevState {
	case "hard", "file", "directory", "absent":
		if srcp == nil {
			ansible.Fail("src is required for creating new symlinks", nil)
		}
		changed = true
	case "link":
		if srcp != nil {
			if old := readlink(path); old != *srcp {
				diff.Set("src", old, *srcp)
				changed = true
			}
		}
	default:
		ansible.Fail("unexpected position reached", ansible.Result{"dest": path, "src": pyStr(srcp)})
	}

	if changed && !ansible.CheckMode() {
		if prevState != "absent" {
			// Replace atomically.
			tmp := tmpPath(path)
			err := func() error {
				if prevState == "directory" {
					if err := syscall.Rmdir(path); err != nil {
						return &os.PathError{Op: "rmdir", Path: path, Err: err}
					}
				}
				if err := os.Symlink(*srcp, tmp); err != nil {
					return err
				}
				return os.Rename(tmp, path)
			}()
			if err != nil {
				if exists(tmp) {
					os.Remove(tmp)
				}
				ansible.Fail("Error while replacing: "+ansible.OSErrorMessage(err, true), ansible.Result{"path": path})
			}
		} else if err := os.Symlink(*srcp, path); err != nil {
			ansible.Fail("Error while linking: "+ansible.OSErrorMessage(err, true), ansible.Result{"path": path})
		}
	}

	if ansible.CheckMode() && !exists(path) {
		return ansible.Result{"dest": path, "src": pyStr(srcp), "changed": changed, "diff": diff}
	}

	// Only now, with the link in place, can it be followed.
	target := args.CommonPath(path)
	// A link to a nonexistent target can't have attributes set on the target.
	if follow && isLink(path) && !exists(target) {
		ansible.Warn("Cannot set fs attributes on a non-existent symlink target. follow should be set to False to avoid this.")
	} else {
		changed = args.SetFSAttributes(target, changed, diff)
		changed = updateTimestampForFile(target, mtime, atime, diff) || changed
	}
	return ansible.Result{"dest": path, "src": pyStr(srcp), "changed": changed, "diff": diff}
}

func readdirnames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

func ensureHardlink(path string, srcp *string, ts timestamps) ansible.Result {
	prevState := getState(path)
	target := args.CommonPath(path)
	mtime, atime := ts.resolve()

	// src is required to create a new hardlink.
	if prevState != "hard" && srcp == nil {
		ansible.Fail("src is required for creating new hardlinks", nil)
	}
	// Even if the link already exists, src must exist: inodes are compared.
	if srcp != nil && !exists(*srcp) {
		ansible.Fail("src does not exist", ansible.Result{"dest": path, "src": *srcp})
	}

	diff := initialDiff(path, "hard", prevState)
	changed := false
	switch prevState {
	case "absent":
		changed = true
	case "link":
		if old := readlink(path); old != *srcp {
			diff.Set("src", old, *srcp)
			changed = true
		}
	case "hard":
		if srcp != nil && inode(path) != inode(*srcp) {
			changed = true
			if !args.Force {
				ansible.Fail("Cannot link, different hard link exists at destination", ansible.Result{"dest": path, "src": *srcp})
			}
		}
	case "file":
		changed = true
		if !args.Force {
			ansible.Fail(fmt.Sprintf("Cannot link, %s exists at destination", prevState), ansible.Result{"dest": path, "src": *srcp})
		}
	case "directory":
		changed = true
		if exists(path) {
			if inode(path) == inode(*srcp) {
				return ansible.Result{"path": path, "changed": false}
			} else if !args.Force {
				ansible.Fail("Cannot link: different hard link exists at destination", ansible.Result{"dest": path, "src": *srcp})
			}
		}
	default:
		ansible.Fail("unexpected position reached", ansible.Result{"dest": path, "src": pyStr(srcp)})
	}

	if changed && !ansible.CheckMode() {
		src := *srcp
		if prevState != "absent" {
			// Replace atomically.
			tmp := tmpPath(path)
			err := func() error {
				if prevState == "directory" && exists(path) {
					// unlink, which fails for a directory; os.Remove would rmdir it.
					if err := syscall.Unlink(path); err != nil && err != syscall.ENOENT {
						return &os.PathError{Op: "unlink", Path: path, Err: err}
					}
				}
				if err := link(src, tmp); err != nil {
					return err
				}
				return os.Rename(tmp, path)
			}()
			if err != nil {
				if exists(tmp) {
					os.Remove(tmp)
				}
				ansible.Fail("Error while replacing: "+ansible.OSErrorMessage(err, true), ansible.Result{"path": path})
			}
		} else {
			if follow && isLink(src) {
				src = readlink(src)
			}
			if err := link(src, path); err != nil {
				ansible.Fail("Error while linking: "+ansible.OSErrorMessage(err, true), ansible.Result{"path": path})
			}
		}
	}

	if ansible.CheckMode() && !exists(path) {
		return ansible.Result{"dest": path, "src": pyStr(srcp), "changed": changed, "diff": diff}
	}

	changed = args.SetFSAttributes(target, changed, diff)
	changed = updateTimestampForFile(target, mtime, atime, diff) || changed
	return ansible.Result{"dest": path, "src": pyStr(srcp), "changed": changed, "diff": diff}
}

// pyInt is int() on a string: surrounding whitespace, a sign and digit
// separators are allowed.
func pyInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.ReplaceAll(s, "_", ""))
	return n, err == nil
}

func checkOwnerExists(owner string) {
	if uid, ok := pyInt(owner); ok {
		if _, found := ansible.UserName(uid); !found {
			ansible.Warn(fmt.Sprintf("failed to look up user with uid %d. Create user up to this point in real play", uid))
		}
	} else if _, found := ansible.LookupUser(owner); !found {
		ansible.Warn(fmt.Sprintf("failed to look up user %s. Create user up to this point in real play", owner))
	}
}

func checkGroupExists(group string) {
	if gid, ok := pyInt(group); ok {
		if _, found := ansible.GroupName(gid); !found {
			ansible.Warn(fmt.Sprintf("failed to look up group with gid %d. Create group up to this point in real play", gid))
		}
	} else if _, found := ansible.LookupGroupID(group); !found {
		ansible.Warn(fmt.Sprintf("failed to look up group %s. Create group up to this point in real play", group))
	}
}
