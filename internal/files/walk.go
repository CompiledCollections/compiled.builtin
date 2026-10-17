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
