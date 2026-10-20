package ansible

import "strings"

// SplitLines is Python's str.splitlines(keepends=True): it splits after \n,
// \r, \r\n, \v, \f, \x1c, \x1d, \x1e, \x85,   and  , keeping them.
func SplitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		switch r {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', ' ', ' ':
			lines = append(lines, s[start:i+len(string(r))])
			start = i + len(string(r))
		case '\r':
			if strings.HasPrefix(s[i+1:], "\n") {
				continue // the \n ends the line
			}
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// PyNormpath is os.path.normpath: it collapses repeated slashes, "." and
// "dir/..", keeping a leading "//" as POSIX allows.
func PyNormpath(path string) string {
	if path == "" {
		return "."
	}
	initial := 0
	if strings.HasPrefix(path, "/") {
		initial = 1
		if strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "///") {
			initial = 2
		}
	}
	var comps []string
	for _, comp := range strings.Split(path, "/") {
		if comp == "" || comp == "." {
			continue
		}
		if comp != ".." || initial == 0 && len(comps) == 0 || len(comps) > 0 && comps[len(comps)-1] == ".." {
			comps = append(comps, comp)
		} else if len(comps) > 0 {
			comps = comps[:len(comps)-1]
		}
	}
	out := strings.Repeat("/", initial) + strings.Join(comps, "/")
	if out == "" {
		return "."
	}
	return out
}
