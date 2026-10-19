package files

import (
	"compiled/builtin/internal/ansible"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// SetContextIfDifferent is AnsibleModule.set_context_if_different.
func SetContextIfDifferent(path string, context []*string, changed bool, diff ansible.Diff) bool {
	if !SelinuxEnabled() {
		return changed
	}
	if checkFileAbsentIfCheckMode(path) {
		return true
	}
	cur := SelinuxContext(path)
	next := append([]*string(nil), cur...)
	if special, sp := isSpecialSelinuxPath(path); special {
		next = sp
	} else {
		for i := range cur {
			if i < len(context) {
				if context[i] != nil && (cur[i] == nil || *context[i] != *cur[i]) {
					next[i] = context[i]
				} else if context[i] == nil {
					next[i] = cur[i]
				}
			}
		}
	}
	if joinContext(cur) == joinContext(next) && len(cur) == len(next) {
		return changed
	}
	diff.Set("secontext", cur, next)
	if ansible.CheckMode() {
		return true
	}
	if err := setFileCon(path, joinContext(next)); err != nil {
		ansible.Fail("invalid selinux context: "+errnoMessage(err), ansible.Result{
			"path": path, "new_context": next, "cur_context": cur, "input_was": context,
		})
	}
	return true
}

func joinContext(ctx []*string) string {
	parts := make([]string, len(ctx))
	for i, p := range ctx {
		if p != nil {
			parts[i] = *p
		}
	}
	return strings.Join(parts, ":")
}

// Realpath resolves symlinks like Python's os.path.realpath (non-strict):
// missing components are kept as they are.
func Realpath(p string) string {
	if !strings.HasPrefix(p, "/") {
		if wd, err := os.Getwd(); err == nil {
			p = wd + "/" + p
		}
	}
	parts := strings.Split(p, "/")
	resolved := "/"
	links := 0
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, part)
		target, err := os.Readlink(next)
		if err != nil || links > 40 {
			resolved = next
			continue
		}
		links++
		if strings.HasPrefix(target, "/") {
			resolved = "/"
		}
		parts = append(strings.Split(target, "/"), parts[i+1:]...)
		i = -1
	}
	return resolved
}

func errnoOf(err error) syscall.Errno { return ansible.Errno(err) }

func errnoMessage(err error) string { return ansible.OSErrorMessage(err, false) }

// ignoreEPERM drops permission errors, which Python ignores for chown/copystat
// when not running as root.
func ignoreEPERM(err error) error {
	if errnoOf(err) == syscall.EPERM {
		return nil
	}
	return err
}

// copyFile copies src's content to dst (shutil.copyfile), following symlinks.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyMode is shutil.copymode.
func copyMode(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	return syscall.Chmod(dst, unixMode(fi))
}

// copyStat is shutil.copystat: extended attributes, times and mode.
func copyStat(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	copyXattrs(src, dst)
	if err := os.Chtimes(dst, accessTime(fi), fi.ModTime()); err != nil {
		return err
	}
	return syscall.Chmod(dst, unixMode(fi))
}

// copy2 is shutil.copy2: content plus metadata.
func copy2(src, dst string) error {
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return copyStat(src, dst)
}

// CopyFile is shutil.copyfile.
func CopyFile(src, dst string) error { return copyFile(src, dst) }

// CopyMode is shutil.copymode.
func CopyMode(src, dst string) error { return copyMode(src, dst) }

// CopyStat is shutil.copystat.
func CopyStat(src, dst string) error { return copyStat(src, dst) }

// Copy2 is shutil.copy2.
func Copy2(src, dst string) error { return copy2(src, dst) }

func statIDs(path string) (uid, gid int, ok bool) {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// AtomicMove replaces dest with src, keeping dest's ownership, mode and
// SELinux context, like AnsibleModule.atomic_move.
func AtomicMove(src, dest string, unsafeWrites bool) error {
	return AtomicMoveKeep(src, dest, unsafeWrites, true)
}

// AtomicMoveKeep is atomic_move with keep_dest_attrs. When false, src keeps
// its own ownership, mode and times instead of taking those of dest.
func AtomicMoveKeep(src, dest string, unsafeWrites, keepDestAttrs bool) error {
	var context []*string
	destUID, destGID, destExists := statIDs(dest)
	destExists = destExists && keepDestAttrs
	if destExists {
		if err := ignoreEPERM(os.Chown(src, destUID, destGID)); err != nil {
			return err
		}
		if err := ignoreEPERM(copyStat(dest, src)); err != nil {
			return err
		}
		now := time.Now()
		if err := ignoreEPERM(os.Chtimes(src, now, now)); err != nil {
			return err
		}
		if SelinuxEnabled() {
			context = SelinuxContext(dest)
		}
	} else if SelinuxEnabled() {
		context = SelinuxDefaultContext(dest)
	}
	creating := !exists(dest)

	if err := os.Rename(src, dest); err != nil {
		switch errnoOf(err) {
		case syscall.EPERM, syscall.EXDEV, syscall.EACCES, syscall.ETXTBSY, syscall.EBUSY:
			if err := moveViaDestDir(src, dest, unsafeWrites, keepDestAttrs, context, destExists, destUID, destGID); err != nil {
				return err
			}
		default:
			return &ansible.CausedError{Msg: fmt.Sprintf("Could not replace '%s' with '%s'.", dest, src), Cause: err}
		}
	}

	if creating {
		umask := currentUmask()
		syscall.Chmod(dest, uint32(0o666&^umask))
		var dirSt syscall.Stat_t
		if syscall.Stat(filepath.Dir(dest), &dirSt) == nil {
			gid := os.Getegid()
			if dirSt.Mode&syscall.S_ISGID != 0 {
				gid = int(dirSt.Gid)
			}
			os.Chown(dest, os.Geteuid(), gid) // best effort, as in Python
		}
	}
	if SelinuxEnabled() {
		SetContextIfDifferent(dest, context, false, nil)
	}
	return nil
}

// moveViaDestDir is atomic_move's fallback when a plain rename fails: copy
// into a temporary file next to dest, then rename that.
func moveViaDestDir(src, dest string, unsafeWrites, keepDestAttrs bool, context []*string, destExists bool, destUID, destGID int) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".ansible_tmp*"+filepath.Base(dest))
	if err != nil {
		if unsafeWrites {
			return unsafeWrite(src, dest)
		}
		return &ansible.CausedError{Msg: fmt.Sprintf("The destination directory '%s' is not writable by the current user.", filepath.Dir(dest)), Cause: err}
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	copyErr := func() error {
		// shutil.move with copy_function=shutil.copy, or shutil.copy2
		// (metadata too) when not keeping the destination's attributes
		if err := os.Rename(src, tmpName); err != nil {
			if err := copyFile(src, tmpName); err != nil {
				return err
			}
			copyMeta := copyMode
			if !keepDestAttrs {
				copyMeta = copyStat
			}
			if err := copyMeta(src, tmpName); err != nil {
				return err
			}
		}
		if SelinuxEnabled() {
			SetContextIfDifferent(tmpName, context, false, nil)
		}
		if uid, gid, ok := statIDs(tmpName); ok && destExists && (uid != destUID || gid != destGID) {
			if err := ignoreEPERM(os.Chown(tmpName, destUID, destGID)); err != nil {
				return err
			}
		}
		if !keepDestAttrs {
			return nil
		}
		now := time.Now()
		return ignoreEPERM(os.Chtimes(tmpName, now, now))
	}()
	if copyErr != nil {
		if unsafeWrites {
			return unsafeWrite(src, dest)
		}
		return &ansible.CausedError{Msg: fmt.Sprintf("Failed to replace '%s' with '%s'.", dest, src), Cause: copyErr}
	}
	if err := os.Rename(tmpName, dest); err != nil {
		if unsafeWrites && errnoOf(err) == syscall.EBUSY {
			return unsafeWrite(tmpName, dest)
		}
		return &ansible.CausedError{Msg: fmt.Sprintf("Unable to make '%s' into to '%s', failed final rename from '%s'.", src, dest, tmpName), Cause: err}
	}
	return nil
}

// unsafeWrite overwrites dest in place, for unsafe_writes.
func unsafeWrite(src, dest string) error {
	if err := copyFile(src, dest); err != nil {
		return &ansible.CausedError{Msg: fmt.Sprintf("Could not write data to file '%s' from '%s'.", dest, src), Cause: err}
	}
	return nil
}

// BackupLocal copies fn to fn.PID.YYYY-MM-DD@HH:MM:SS~ keeping its
// metadata, and returns the backup's name, like AnsibleModule.backup_local.
func BackupLocal(fn string) (string, error) {
	if !exists(fn) {
		return "", nil
	}
	dest := fmt.Sprintf("%s.%d.%s", fn, os.Getpid(), time.Now().Format("2006-01-02@15:04:05~"))
	if !ansible.CheckMode() {
		if err := preservedCopy(fn, dest); err != nil {
			return "", &ansible.CausedError{Msg: fmt.Sprintf("Could not make backup of '%s' to '%s'.", fn, dest), Cause: err}
		}
	}
	return dest, nil
}

// preservedCopy copies a file with its ownership, permissions, context and attributes.
func preservedCopy(src, dest string) error {
	if err := copy2(src, dest); err != nil {
		return err
	}
	if SelinuxEnabled() {
		SetContextIfDifferent(dest, SelinuxContext(src), false, nil)
	}
	if uid, gid, ok := statIDs(src); ok {
		if tuid, tgid, ok := statIDs(dest); ok && (tuid != uid || tgid != gid) {
			if err := ignoreEPERM(os.Chown(dest, uid, gid)); err != nil {
				return err
			}
		}
	}
	attrs, _ := FileAttributes(src)
	SetAttributesIfDifferent(dest, &attrs, true, nil)
	return nil
}
