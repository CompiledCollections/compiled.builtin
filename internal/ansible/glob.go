package ansible

import (
	"os"
	"regexp"
	"strings"
)

// Glob returns the paths matching pattern, like Python's glob.glob: names
// starting with a dot only match a pattern component starting with a dot,
// and a pattern without wildcards matches if the path exists (even as a
// broken symlink).
func Glob(pattern string) []string {
	dir, base := pySplit(pattern)
	if !hasMagic(pattern) {
		if base != "" {
			if _, err := os.Lstat(pattern); err == nil {
				return []string{pattern}
			}
		} else if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return []string{pattern}
		}
		return nil
	}

	var dirs []string
	switch {
	case dir == "":
		dirs = []string{""}
	case dir != pattern && hasMagic(dir):
		dirs = Glob(dir)
	default:
		dirs = []string{dir}
	}

	var out []string
	for _, d := range dirs {
		var names []string
		if hasMagic(base) {
			names = glob1(d, base)
		} else {
			names = glob0(d, base)
		}
		for _, n := range names {
			if d == "" {
				out = append(out, n)
			} else {
				out = append(out, pyJoin(d, n))
			}
		}
	}
	return out
}

func glob1(dir, pattern string) []string {
	listDir := dir
	if listDir == "" {
		listDir = "."
	}
	entries, err := os.ReadDir(listDir)
	if err != nil {
		return nil
	}
	re := fnmatchRegexp(pattern)
	hidden := strings.HasPrefix(pattern, ".")
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		if re.MatchString(name) {
			out = append(out, name)
		}
	}
	return out
}

func glob0(dir, base string) []string {
	if base == "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return []string{base}
		}
		return nil
	}
	if _, err := os.Lstat(pyJoin(dir, base)); err == nil {
		return []string{base}
	}
	return nil
}

func hasMagic(s string) bool { return strings.ContainsAny(s, "*?[") }

// pySplit is os.path.split: trailing slashes are stripped from the head
// unless it is all slashes.
func pySplit(p string) (string, string) {
	i := strings.LastIndex(p, "/") + 1
	head, tail := p[:i], p[i:]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head, tail
}

// pyJoin is os.path.join for two components.
func pyJoin(a, b string) string {
	if strings.HasPrefix(b, "/") || a == "" {
		return b
	}
	if strings.HasSuffix(a, "/") {
		return a + b
	}
	return a + "/" + b
}

// fnmatchRegexp translates a shell pattern like Python's fnmatch.translate:
// * and ? match any character (including /), [!...] negates a set, and a
// backslash is an ordinary character.
func fnmatchRegexp(pat string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	p := []rune(pat)
	for i := 0; i < len(p); {
		c := p[i]
		i++
		switch c {
		case '*':
			b.WriteString(`.*`)
			for i < len(p) && p[i] == '*' {
				i++
			}
		case '?':
			b.WriteString(`.`)
		case '[':
			j := i
			if j < len(p) && p[j] == '!' {
				j++
			}
			if j < len(p) && p[j] == ']' {
				j++
			}
			for j < len(p) && p[j] != ']' {
				j++
			}
			if j >= len(p) {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(charClass(p[i:j]))
			i = j + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`\z`)
	return regexp.MustCompile(b.String())
}

// charClass turns the inside of a [...] shell set into a regexp class.
func charClass(set []rune) string {
	negate := false
	if len(set) > 0 && set[0] == '!' {
		negate = true
		set = set[1:]
	}
	var items strings.Builder
	n := 0
	for k := 0; k < len(set); {
		lo := set[k]
		if k+2 < len(set) && set[k+1] == '-' {
			hi := set[k+2]
			k += 3
			if lo > hi {
				continue // empty range, as Python drops it
			}
			items.WriteString(classQuote(lo) + "-" + classQuote(hi))
		} else {
			k++
			items.WriteString(classQuote(lo))
		}
		n++
	}
	switch {
	case n == 0 && !negate:
		return `[^\x00-\x{10FFFF}]` // empty set never matches
	case n == 0:
		return `.`
	case negate:
		return "[^" + items.String() + "]"
	}
	return "[" + items.String() + "]"
}

func classQuote(r rune) string {
	if strings.ContainsRune(`\]^-[`, r) {
		return `\` + string(r)
	}
	return string(r)
}

var fnmatchCache = map[string]*regexp.Regexp{}

// Fnmatch is fnmatch.fnmatch on POSIX (where it is fnmatchcase): whether
// name matches the shell pattern pat.
func Fnmatch(name, pat string) bool {
	re, ok := fnmatchCache[pat]
	if !ok {
		re = fnmatchRegexp(pat)
		fnmatchCache[pat] = re
	}
	return re.MatchString(name)
}
