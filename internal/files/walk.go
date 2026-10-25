package files

import (
	"os"
	"strings"
)

// Walk is Python's os.walk, top-down: fn gets each directory with its
// subdirectories and files, in directory order (not sorted), as os.scandir
// lists them. Symlinks to directories count as directories but aren't
// entered. Unreadable directories are skipped, as os.walk does. Paths are
// joined as os.path.join does, so they read the same as Python's.
func Walk(root string, fn func(dir string, dirs, files []string)) {
	f, err := os.Open(root)
	if err != nil {
		return
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return
	}
	var dirs, regular []string
	for _, name := range names {
		if fi, err := os.Stat(PyJoin(root, name)); err == nil && fi.IsDir() {
			dirs = append(dirs, name)
		} else {
			regular = append(regular, name)
		}
	}
	fn(root, dirs, regular)
	for _, d := range dirs {
		p := PyJoin(root, d)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			Walk(p, fn)
		}
	}
}

// WalkTopDown is os.walk(top, topdown=True, onerror=onerror,
// followlinks=followlinks). fn gets each directory with its subdirectories
// and files, in directory order, and returns the subdirectories to go into
// (os.walk's callers prune dirs in place), and whether to stop the walk (a
// break out of the loop). A directory that can't be listed goes to onerror;
// an error it returns stops the walk and is returned, as a raise would.
func WalkTopDown(top string, followlinks bool, onerror func(error) error,
	fn func(dir string, dirs, files []string) (keep []string, stop bool)) error {
	_, err := walkTopDown(top, followlinks, onerror, fn)
	return err
}

func walkTopDown(top string, followlinks bool, onerror func(error) error,
	fn func(dir string, dirs, files []string) ([]string, bool)) (bool, error) {
	f, err := os.Open(top)
	if err == nil {
		var entries []os.DirEntry
		entries, err = f.ReadDir(-1) // directory order, like os.scandir
		f.Close()
		if err == nil {
			var dirs, nondirs []string
			for _, e := range entries {
				// DirEntry.is_dir() uses the entry's type, stats only symlinks
				// (following them), and is false when that fails.
				isDir := e.IsDir()
				if e.Type()&os.ModeSymlink != 0 {
					fi, err := os.Stat(PyJoin(top, e.Name()))
					isDir = err == nil && fi.IsDir()
				}
				if isDir {
					dirs = append(dirs, e.Name())
				} else {
					nondirs = append(nondirs, e.Name())
				}
			}
			keep, stop := fn(top, dirs, nondirs)
			if stop {
				return true, nil
			}
			for _, d := range keep {
				p := PyJoin(top, d)
				if !followlinks {
					if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
						continue
					}
				}
				if stop, err := walkTopDown(p, followlinks, onerror, fn); stop || err != nil {
					return stop, err
				}
			}
			return false, nil
		}
	}
	if onerror != nil {
		return false, onerror(err)
	}
	return false, nil
}

// PyJoin is os.path.join for two parts: an absolute b replaces a, and
// nothing is cleaned, unlike filepath.Join.
func PyJoin(a, b string) string {
	if strings.HasPrefix(b, "/") || a == "" {
		return b
	}
	if strings.HasSuffix(a, "/") {
		return a + b
	}
	return a + "/" + b
}

// PyDirname is os.path.dirname: everything before the last slash, with
// trailing slashes removed unless that leaves only slashes.
func PyDirname(p string) string {
	head := p[:strings.LastIndexByte(p, '/')+1]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}

// PyBasename is os.path.basename: everything after the last slash.
func PyBasename(p string) string {
	return p[strings.LastIndexByte(p, '/')+1:]
}
