package files

import (
	"os"
	"syscall"
	"testing"
)

type fakeInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (f fakeInfo) IsDir() bool       { return f.mode.IsDir() }
func (f fakeInfo) Sys() any          { return nil }

func setUmask(m int) { syscall.Umask(m) }

func TestSymbolicModeToOctal(t *testing.T) {
	old := currentUmask()
	defer setUmask(old)
	setUmask(0o022)

	file := fakeInfo{mode: 0o644}
	exe := fakeInfo{mode: 0o744}
	dir := fakeInfo{mode: os.ModeDir | 0o755}
	cases := []struct {
		fi   os.FileInfo
		mode string
		want int64
	}{
		{file, "u+x", 0o744},
		{file, "u+x,g-r,o=", 0o700},
		{file, "a=r", 0o444},
		{file, "=rw", 0o644}, // no users: the umask applies
		{file, "+x", 0o755},
		{file, "a+X", 0o644}, // X needs an existing x bit
		{exe, "a+X", 0o755},
		{dir, "a+X", 0o755},
		{exe, "g=u", 0o774},
		{file, "o=u", 0o646},
		{file, "u+s,g+s,o+t", 0o7644},
		{file, "u=rwx,g=rx,o=", 0o750},
		{file, "ug-w", 0o444},
	}
	for _, c := range cases {
		got, err := SymbolicModeToOctal(c.fi, c.mode)
		if err != nil || got != c.want {
			t.Errorf("SymbolicModeToOctal(%o, %q) = %o, %v; want %o", c.fi.Mode(), c.mode, got, err, c.want)
		}
	}
	for _, bad := range []string{"abc", "u+z", "x=r", "rw"} {
		if _, err := SymbolicModeToOctal(file, bad); err == nil {
			t.Errorf("SymbolicModeToOctal(%q) succeeded, want an error", bad)
		}
	}
}

func TestParsePythonOctal(t *testing.T) {
	cases := map[string]int64{"0644": 0o644, "644": 0o644, "0o755": 0o755, " 600 ": 0o600, "7_55": 0o755}
	for in, want := range cases {
		if got, ok := parsePythonOctal(in); !ok || got != want {
			t.Errorf("parsePythonOctal(%q) = %o, %v; want %o", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "8", "u+x", "0x1f", "_7", "7__5"} {
		if _, ok := parsePythonOctal(bad); ok {
			t.Errorf("parsePythonOctal(%q) succeeded, want failure", bad)
		}
	}
}
