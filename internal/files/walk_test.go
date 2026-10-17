package files

import "testing"

// Expected values from CPython's os.path.join, dirname and basename.
func TestPyPath(t *testing.T) {
	joins := []struct{ a, b, want string }{
		{"a", "b", "a/b"},
		{"a/", "b", "a/b"},
		{"", "b", "b"},
		{"a", "/b", "/b"},
		{"/", "b", "/b"},
		{"a//", "b", "a//b"},
		{"./x", "y", "./x/y"},
		{"a", "", "a/"},
	}
	for _, c := range joins {
		if got := PyJoin(c.a, c.b); got != c.want {
			t.Errorf("PyJoin(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
	splits := []struct{ p, dir, base string }{
		{"a/b", "a", "b"},
		{"a/b/", "a/b", ""},
		{"/a", "/", "a"},
		{"/", "/", ""},
		{"//", "//", ""},
		{"a", "", "a"},
		{"", "", ""},
		{"a//b", "a", "b"},
		{"///a//b//", "///a//b", ""},
		{"./x", ".", "x"},
	}
	for _, c := range splits {
		if got := PyDirname(c.p); got != c.dir {
			t.Errorf("PyDirname(%q) = %q, want %q", c.p, got, c.dir)
		}
		if got := PyBasename(c.p); got != c.base {
			t.Errorf("PyBasename(%q) = %q, want %q", c.p, got, c.base)
		}
	}
}
