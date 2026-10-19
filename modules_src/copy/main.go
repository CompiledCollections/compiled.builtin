// Copy equivalent module, written in Go. Mirrors ansible.builtin.copy: the
// target side of a copy, after the action plugin has uploaded the source
// (or, with remote_src, a copy between paths on the target).
package main

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/files"
)

type Args struct {
	files.FileArgs
	Src              *string         `json:"src"`
	OriginalBasename *string         `json:"_original_basename"`
	Dest             string          `json:"dest"`
	Backup           bool            `json:"backup"`
	Force            *bool           `json:"force"`
	Validate         *string         `json:"validate"`
	DirectoryMode    json.RawMessage `json:"directory_mode"`
	RemoteSrc        bool            `json:"remote_src"`
	LocalFollow      *bool           `json:"local_follow"`
	Checksum         *string         `json:"checksum"`
}

var args Args

func main() {
	ansible.ReadArgs(&args)
	// Type 'path' options are expanded by AnsibleModule.
	src := "None"
	if args.Src != nil {
		src = ansible.ExpandPath(*args.Src)
	}
	paramDest := ansible.ExpandPath(args.Dest)
	dest := paramDest
	// Always have a directory component for later processing.
	if !strings.Contains(dest, "/") {
		dest = "./" + dest
	}
	force := args.Force == nil || *args.Force
	follow := args.Follow != nil && *args.Follow

	if !exists(src) {
		ansible.Fail(fmt.Sprintf("Source %s not found", src), nil)
	}
	if syscall.Access(src, rOK) != nil {
		ansible.Fail(fmt.Sprintf("Source %s not readable", src), nil)
	}

	// mode=preserve is usually handled by the action plugin, but with
	// remote_src it has to be done here.
	var mode string
	if json.Unmarshal(args.Mode, &mode) == nil && mode == "preserve" {
		fi, _ := os.Stat(src)
		args.Mode, _ = json.Marshal(fmt.Sprintf("0%03o", fileMode(fi)))
	}

	changed := false
	var checksumSrc, checksumDest, md5sumSrc any

	if isFile(src) {
		if sum, err := digest(src, sha1.New()); err != nil {
			ansible.Warn("Unable to calculate src checksum, assuming change.")
		} else {
			checksumSrc = sum
		}
		if sum, err := digest(src, md5.New()); err == nil {
			md5sumSrc = sum
		}
	} else if args.RemoteSrc && !isDir(src) {
		ansible.Fail(fmt.Sprintf("Cannot copy invalid source '%s': not a file", src), nil)
	}

	if args.Checksum != nil && *args.Checksum != "" && checksumSrc != *args.Checksum {
		ansible.Fail("Copied file does not match the expected checksum. Transfer failed.",
			ansible.Result{"checksum": checksumSrc, "expected_checksum": *args.Checksum})
	}

	// For a recursive copy, create the intermediate directories.
	if strings.HasSuffix(dest, "/") {
		if args.OriginalBasename != nil && *args.OriginalBasename != "" {
			dest = files.PyJoin(dest, *args.OriginalBasename)
		}
		dirname := files.PyDirname(dest)
		if !exists(dirname) {
			preExisting, newDirs := splitPreExistingDir(dirname)
			if ansible.CheckMode() {
				ansible.Exit(ansible.Result{"msg": fmt.Sprintf("dest directory %s would be created", dirname), "changed": true, "src": src})
			}
			if err := os.MkdirAll(dirname, 0o777); err != nil {
				ansible.Crash(ansible.OSErrorMessage(err, true), "")
			}
			changed = true
			dirArgs := args.FileArgs
			dirArgs.Mode = nil
			if hasValue(args.DirectoryMode) {
				dirArgs.Mode = args.DirectoryMode
			}
			for _, d := range newDirs {
				preExisting = files.PyJoin(preExisting, d)
				dirArgs.SetFSAttributesIfDifferent(preExisting, changed, nil)
			}
		}
	}

	bDest := dest
	if isDir(dest) {
		basename := files.PyBasename(src)
		if args.OriginalBasename != nil && *args.OriginalBasename != "" {
			basename = *args.OriginalBasename
		}
		dest = files.PyJoin(dest, basename)
		bDest = dest
	}

	if exists(dest) {
		if isLink(dest) && follow {
			dest = files.Realpath(dest)
			bDest = dest
		}
		if !force {
			ansible.Exit(ansible.Result{"msg": "file already exists", "src": src, "dest": dest, "changed": false})
		}
		if syscall.Access(dest, rOK) == nil && isFile(dest) {
			sum, err := digest(dest, sha1.New())
			if err != nil {
				ansible.Crash(ansible.OSErrorMessage(err, true), "")
			}
			checksumDest = sum
		}
	} else if dir := files.PyDirname(bDest); !exists(dir) {
		// exists() is false too for a directory without the execute
		// bit, where stat() fails.
		if err := syscall.Stat(dir, &syscall.Stat_t{}); err != nil {
			if strings.Contains(strings.ToLower(ansible.OSErrorMessage(&os.PathError{Op: "stat", Path: dir, Err: err}, true)), "permission denied") {
				ansible.Fail(fmt.Sprintf("Destination directory %s is not accessible", files.PyDirname(dest)), nil)
			}
		}
		ansible.Fail(fmt.Sprintf("Destination directory %s does not exist", files.PyDirname(dest)), nil)
	}

	if syscall.Access(files.PyDirname(bDest), wOK) != nil && !args.UnsafeWritesEnabled() {
		ansible.Fail(fmt.Sprintf("Destination %s not writable", files.PyDirname(dest)), nil)
	}

	backupFile := ""
	if checksumSrc != checksumDest || isLink(bDest) {
		if !ansible.CheckMode() {
			copyFile(src, dest, bDest, mode, &backupFile)
		}
		changed = true
	}

	// With neither checksum, src and dest are both directories.
	checksumsNone := checksumSrc == nil && checksumDest == nil
	bothDirectories := isDir(src) && (isDir(paramDest) || !exists(paramDest))
	if checksumsNone && args.RemoteSrc && bothDirectories {
		dsrc, ddest := src, paramDest
		if !strings.HasSuffix(dsrc, "/") {
			ddest = files.PyJoin(ddest, files.PyBasename(dsrc))
			dsrc = files.PyJoin(dsrc, "")
		}
		bDest = ddest
		changed = copyDirectory(dsrc, ddest) || changed
	}

	result := ansible.Result{"dest": dest, "src": src, "md5sum": md5sumSrc, "checksum": checksumSrc, "changed": changed}
	if backupFile != "" {
		result["backup_file"] = backupFile
	}

	fileArgs := args.FileArgs
	if isDir(bDest) && hasValue(args.DirectoryMode) {
		fileArgs.Mode = args.DirectoryMode
	}
	result["changed"] = fileArgs.SetFSAttributesIfDifferent(dest, changed, nil)
	ansible.Exit(result)
}

// copyFile puts src in place of dest: backup, validation, then an atomic move.
// OSErrors become "Failed to copy" errors caused by them.
func copyFile(src, dest, bDest, mode string, backupFile *string) {
	fail := func(err error) {
		ansible.Crash(fmt.Sprintf("Failed to copy %s to %s.", ansible.PyRepr(src), ansible.PyRepr(dest)), ansible.OSErrorMessage(err, true))
	}
	if args.Backup && exists(bDest) {
		b, err := files.BackupLocal(dest)
		if err != nil {
			ansible.CrashError(err)
		}
		*backupFile = b
	}
	// Allow for conversion from a symlink.
	if isLink(bDest) {
		if err := syscall.Unlink(bDest); err != nil {
			fail(&os.PathError{Op: "unlink", Path: bDest, Err: err})
		}
		f, err := os.OpenFile(bDest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			fail(err)
		}
		f.Close()
	}
	if args.Validate != nil && *args.Validate != "" {
		// Validation may need the mode and ownership the file will have.
		files.SetModeIfDifferent(src, args.Mode, false, nil)
		chownPath(src)
		if !strings.Contains(*args.Validate, "%s") {
			ansible.Fail("validate must contain %s: "+*args.Validate, nil)
		}
		cmd, err := ansible.PercentFormat(*args.Validate, src)
		if err != nil {
			ansible.Crash(err.Error(), "")
		}
		rc, stdout, stderr := ansible.RunCommand(cmd, ansible.RunOptions{})
		if rc != 0 {
			ansible.Fail("failed to validate", ansible.Result{"exit_status": rc, "stdout": stdout, "stderr": stderr})
		}
	}

	mysrc := src
	if args.RemoteSrc && isFile(src) {
		tmp, err := os.CreateTemp(files.PyDirname(bDest), "tmp")
		if err != nil {
			fail(err)
		}
		tmp.Close()
		mysrc = tmp.Name()
		if err := files.CopyFile(src, mysrc); err != nil {
			fail(err)
		}
		if err := files.CopyStat(src, mysrc); err != nil {
			if ansible.Errno(err) == syscall.ENOSYS && mode == "preserve" {
				ansible.Warn("Unable to copy stats " + src)
			} else {
				fail(err)
			}
		}
	}
	// At this point there is always a temporary file.
	if err := files.AtomicMoveKeep(mysrc, dest, args.UnsafeWritesEnabled(), !args.RemoteSrc); err != nil {
		ansible.CrashError(err)
	}
}

// splitPreExistingDir returns the first directory up from dirname that
// exists, and the ones under it to create.
func splitPreExistingDir(dirname string) (string, []string) {
	head, tail := files.PyDirname(dirname), files.PyBasename(dirname)
	if head == "" {
		return ".", []string{tail}
	}
	if exists(head) {
		return head, []string{tail}
	}
	if head == "/" {
		ansible.Fail("The '/' directory doesn't exist on this machine.", nil)
	}
	pre, dirs := splitPreExistingDir(head)
	return pre, append(dirs, tail)
}

func chownPath(path string) bool {
	changed := files.SetOwnerIfDifferent(path, args.Owner, false, nil)
	return files.SetGroupIfDifferent(path, args.Group, changed, nil)
}

func chownRecursive(path string) bool {
	changed := false
	files.Walk(path, func(dir string, dirs, names []string) {
		changed = chownPath(dir) || changed
		for _, d := range dirs {
			changed = chownPath(files.PyJoin(dir, d)) || changed
		}
		for _, n := range names {
			changed = chownPath(files.PyJoin(dir, n)) || changed
		}
	})
	return changed
}

// Directory copies (remote_src with a directory src)

// dircmp is the part of filecmp.dircmp the copy uses. With the bytes paths
// copy passes, no names are ignored. Like dircmp, which works each list
// out on first use, contents are only compared when diffFiles is wanted:
// comparing reads the files, which moves their access times.
type dircmp struct {
	leftOnly, commonDirs, diffFiles []string
}

func compareDirs(a, b string, wantDiff bool) dircmp {
	left, right := listdir(a), listdir(b)
	inRight := map[string]bool{}
	for _, n := range right {
		inRight[n] = true
	}
	var d dircmp
	var commonFiles []string
	for _, n := range left {
		if !inRight[n] {
			d.leftOnly = append(d.leftOnly, n)
			continue
		}
		var sa, sb syscall.Stat_t
		if syscall.Stat(files.PyJoin(a, n), &sa) != nil || syscall.Stat(files.PyJoin(b, n), &sb) != nil {
			continue
		}
		ta, tb := sa.Mode&syscall.S_IFMT, sb.Mode&syscall.S_IFMT
		switch {
		case ta != tb:
		case ta == syscall.S_IFDIR:
			d.commonDirs = append(d.commonDirs, n)
		case ta == syscall.S_IFREG:
			commonFiles = append(commonFiles, n)
		}
	}
	for _, n := range commonFiles {
		if !wantDiff {
			break
		}
		if same, err := sameFiles(files.PyJoin(a, n), files.PyJoin(b, n)); err == nil && !same {
			d.diffFiles = append(d.diffFiles, n)
		}
	}
	return d
}

// listdir is os.listdir, sorted as dircmp sorts it.
func listdir(path string) []string {
	names, err := readdirnames(path)
	if err != nil {
		ansible.Crash(ansible.OSErrorMessage(err, true), "")
	}
	sort.Strings(names)
	return names
}

func readdirnames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

// sameFiles is filecmp.cmp with shallow=True: same type, size and mtime
// count as the same; otherwise the contents are compared.
func sameFiles(a, b string) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false, err
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false, err
	}
	if sa.Mode&syscall.S_IFMT != syscall.S_IFREG || sb.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return false, nil
	}
	if sa.Size == sb.Size && mtime(&sa) == mtime(&sb) {
		return true, nil
	}
	if sa.Size != sb.Size {
		return false, nil
	}
	ca, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	cb, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return string(ca) == string(cb), nil
}

func copyDiffFiles(src, dest string) bool {
	diff := compareDirs(src, dest, true).diffFiles
	changed := len(diff) > 0
	if !ansible.CheckMode() {
		for _, item := range diff {
			s, d := files.PyJoin(src, item), files.PyJoin(dest, item)
			if isLink(s) && args.LocalFollow != nil && !*args.LocalFollow {
				must(os.Symlink(readlink(s), d))
			} else {
				must(files.CopyFile(s, d))
				must(files.CopyMode(s, d))
			}
			chownPath(d)
			changed = true
		}
	}
	return changed
}

func copyLeftOnly(src, dest string) bool {
	leftOnly := compareDirs(src, dest, false).leftOnly
	changed := len(leftOnly) > 0
	lf := args.LocalFollow
	followTrue, followFalse := lf != nil && *lf, lf != nil && !*lf
	if !ansible.CheckMode() {
		for _, item := range leftOnly {
			s, d := files.PyJoin(src, item), files.PyJoin(dest, item)
			link := isLink(s)
			if link && isDir(s) && followTrue {
				must(copytree(s, d, !followTrue))
				chownRecursive(d)
			}
			if link && isDir(s) && followFalse {
				must(os.Symlink(readlink(s), d))
			}
			if link && isFile(s) && followTrue {
				must(files.CopyFile(s, d))
				chownPath(d)
			}
			if link && isFile(s) && followFalse {
				must(os.Symlink(readlink(s), d))
			}
			if !link && isFile(s) {
				must(files.CopyFile(s, d))
				must(files.CopyMode(s, d))
				chownPath(d)
			}
			if !link && isDir(s) {
				must(copytree(s, d, !followTrue))
				chownRecursive(d)
			}
			changed = true
		}
	}
	return changed
}

func copyCommonDirs(src, dest string) bool {
	changed := false
	for _, item := range compareDirs(src, dest, false).commonDirs {
		s, d := files.PyJoin(src, item), files.PyJoin(dest, item)
		diffChanged := copyDiffFiles(s, d)
		leftChanged := copyLeftOnly(s, d)
		if diffChanged || leftChanged {
			changed = true
		}
		// Recurse into the subdirectory.
		changed = copyCommonDirs(s, d) || changed
	}
	return changed
}

func copyDirectory(src, dest string) bool {
	if !exists(dest) {
		if !ansible.CheckMode() {
			must(copytree(src, dest, !(args.LocalFollow != nil && *args.LocalFollow)))
			chownRecursive(dest)
		}
		return true
	}
	diffChanged := copyDiffFiles(src, dest)
	leftChanged := copyLeftOnly(src, dest)
	commonChanged := copyCommonDirs(src, dest)
	ownerChanged := chownRecursive(dest)
	return diffChanged || leftChanged || commonChanged || ownerChanged
}

// copytree is shutil.copytree with copy2: errors are collected, and
// raised together at the end as shutil.Error.
func copytree(src, dst string, symlinks bool) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	var errs treeErrors
	for _, name := range names {
		s, d := files.PyJoin(src, name), files.PyJoin(dst, name)
		err := func() error {
			fi, err := os.Lstat(s)
			if err != nil {
				return err
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(s)
				if err != nil {
					return err
				}
				if symlinks {
					if err := os.Symlink(target, d); err != nil {
						return err
					}
					copyLinkTimes(s, d)
					return nil
				}
				if isDir(s) {
					return copytree(s, d, symlinks)
				}
				return files.Copy2(s, d)
			}
			if fi.IsDir() {
				return copytree(s, d, symlinks)
			}
			return files.Copy2(s, d)
		}()
		if te, ok := err.(treeErrors); ok {
			errs = append(errs, te...)
		} else if err != nil {
			errs = append(errs, [3]string{s, d, ansible.OSErrorMessage(err, true)})
		}
	}
	if err := files.CopyStat(src, dst); err != nil {
		errs = append(errs, [3]string{src, dst, ansible.OSErrorMessage(err, true)})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// treeErrors is shutil.Error: (src, dst, why) for each failure.
type treeErrors [][3]string

func (e treeErrors) Error() string {
	parts := make([]string, len(e))
	for i, t := range e {
		parts[i] = fmt.Sprintf("(b%s, b%s, %s)", ansible.PyRepr(t[0]), ansible.PyRepr(t[1]), ansible.PyRepr(t[2]))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// must ends the module the way an uncaught Python exception would.
func must(err error) {
	if err == nil {
		return
	}
	if te, ok := err.(treeErrors); ok {
		ansible.Crash(te.Error(), "")
	}
	ansible.Crash(ansible.OSErrorMessage(err, true), "")
}

// Helpers

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func isDir(path string) bool { fi, err := os.Stat(path); return err == nil && fi.IsDir() }

func isFile(path string) bool { fi, err := os.Stat(path); return err == nil && fi.Mode().IsRegular() }

func isLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func readlink(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		ansible.Crash(ansible.OSErrorMessage(err, true), "")
	}
	return target
}

func hasValue(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func fileMode(fi os.FileInfo) uint32 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint32(st.Mode) & 0o7777
	}
	return uint32(fi.Mode().Perm())
}

// digest is AnsibleModule.digest_from_file.
func digest(path string, h hash.Hash) (any, error) {
	if !exists(path) {
		return nil, nil
	}
	if isDir(path) {
		ansible.Fail("attempted to take checksum of directory: "+path, nil)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// access(2) modes.
const (
	rOK = 4
	wOK = 2
)
