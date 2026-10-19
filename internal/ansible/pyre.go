package ansible

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Python's str patterns are Unicode-aware; RE2's \d, \w and \s are ASCII.
const (
	pyDigit     = `\p{Nd}`
	pyWordClass = `\p{L}\p{N}_`
	pySpace     = `\s\v\x{1c}-\x{1f}\x{85}\p{Z}`
)

var (
	repeatFromZeroRe = &LazyRegexp{Expr: `^\{,(\d+)\}`}
	scopedFlagsRe    = &LazyRegexp{Expr: `^\(\?([aimsux]*)(?:-([imsx]+))?:`}
	globalFlagsRe    = &LazyRegexp{Expr: `^\(\?[aiLmsux]+\)`}
)

// CompilePython compiles a Python re pattern with Go's regexp package,
// translating the syntax that differs. Features RE2 cannot express, such as
// lookarounds and backreferences, are reported as errors instead of being
// silently matched differently.
func CompilePython(pat string) (*regexp.Regexp, error) {
	tr, err := translatePython(pat)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(tr)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression %q: %v", pat, err)
	}
	return re, nil
}

func translatePython(pat string) (string, error) {
	p := []rune(pat)
	i := 0

	// Global inline flags must come first in Python 3.11+.
	var flags string
	for i+2 < len(p) && p[i] == '(' && p[i+1] == '?' && strings.ContainsRune("aiLmsux", p[i+2]) {
		j := i + 2
		for j < len(p) && strings.ContainsRune("aiLmsux", p[j]) {
			j++
		}
		if j >= len(p) || p[j] != ')' {
			break // scoped flags such as (?i:...), handled below
		}
		flags += string(p[i+2 : j])
		i = j + 1
	}
	if strings.ContainsAny(flags, "x") {
		return "", unsupported(pat, "verbose mode (?x)")
	}
	if strings.ContainsAny(flags, "L") {
		return "", unsupported(pat, "locale mode (?L)")
	}
	ascii := strings.ContainsRune(flags, 'a')
	multiline := strings.ContainsRune(flags, 'm')

	var b strings.Builder
	var goFlags string
	for _, f := range "ims" {
		if strings.ContainsRune(flags, f) {
			goFlags += string(f)
		}
	}
	if goFlags != "" {
		b.WriteString("(?" + goFlags + ")")
	}

	inClass := false
	for i < len(p) {
		c := p[i]
		if c == '\\' {
			out, n, err := translateEscape(pat, p[i+1:], inClass, ascii)
			if err != nil {
				return "", err
			}
			b.WriteString(out)
			i += 1 + n
			continue
		}
		if inClass {
			switch c {
			case ']':
				inClass = false
				b.WriteRune(c)
			case '[':
				b.WriteString(`\[`) // Python has no POSIX [:classes:]
			default:
				b.WriteRune(c)
			}
			i++
			continue
		}
		switch c {
		case '[':
			inClass = true
			b.WriteRune('[')
			i++
			if i < len(p) && p[i] == '^' {
				b.WriteRune('^')
				i++
			}
			if i < len(p) && p[i] == ']' {
				b.WriteString(`\]`)
				i++
			}
			continue
		case '$':
			if multiline {
				b.WriteRune('$')
			} else {
				// Python's $ also matches just before a trailing newline.
				b.WriteString(`(?:\n?\z)`)
			}
		case '(':
			n, err := translateGroup(pat, p[i:], &b)
			if err != nil {
				return "", err
			}
			i += n
			continue
		case '{':
			// Python reads {,n} as {0,n}; RE2 would take it literally.
			if m := repeatFromZeroRe.Get().FindString(string(p[i:])); m != "" {
				b.WriteString("{0," + m[2:])
				i += utf8.RuneCountInString(m)
				continue
			}
			b.WriteRune(c)
		default:
			b.WriteRune(c)
		}
		i++
	}
	return b.String(), nil
}

// translateGroup handles a '(' at the start of p and returns how many runes it consumed.
func translateGroup(pat string, p []rune, b *strings.Builder) (int, error) {
	s := string(p)
	switch {
	case !strings.HasPrefix(s, "(?"):
		b.WriteRune('(')
		return 1, nil
	case strings.HasPrefix(s, "(?P<"), strings.HasPrefix(s, "(?:"):
		b.WriteString(string(p[:3]))
		return 3, nil
	case strings.HasPrefix(s, "(?#"):
		end := strings.IndexRune(s, ')')
		if end < 0 {
			return 0, fmt.Errorf("missing ), unterminated comment in %q", pat)
		}
		return utf8.RuneCountInString(s[:end+1]), nil
	case strings.HasPrefix(s, "(?P="):
		return 0, unsupported(pat, "backreferences (?P=name)")
	case strings.HasPrefix(s, "(?="), strings.HasPrefix(s, "(?!"),
		strings.HasPrefix(s, "(?<="), strings.HasPrefix(s, "(?<!"):
		return 0, unsupported(pat, "lookahead and lookbehind assertions")
	case strings.HasPrefix(s, "(?>"):
		return 0, unsupported(pat, "atomic groups")
	case strings.HasPrefix(s, "(?("):
		return 0, unsupported(pat, "conditional groups")
	}
	// Scoped flags: (?i:...), (?-i:...), (?i-s:...)
	if m := scopedFlagsRe.Get().FindStringSubmatch(s); m != nil {
		if strings.ContainsAny(m[1]+m[2], "x") {
			return 0, unsupported(pat, "verbose mode (?x)")
		}
		on := strings.NewReplacer("a", "", "u", "").Replace(m[1])
		g := "(?" + on
		if m[2] != "" {
			g += "-" + m[2]
		}
		b.WriteString(g + ":")
		return utf8.RuneCountInString(m[0]), nil
	}
	if globalFlagsRe.Get().MatchString(s) {
		return 0, fmt.Errorf("global flags not at the start of the expression in %q", pat)
	}
	return 0, fmt.Errorf("unknown extension in %q", pat)
}

// translateEscape translates the escape following a backslash. p starts
// after the backslash; it returns the replacement and how many runes of p
// it consumed.
func translateEscape(pat string, p []rune, inClass, ascii bool) (string, int, error) {
	if len(p) == 0 {
		return "", 0, fmt.Errorf("bad escape (end of pattern) in %q", pat)
	}
	c := p[0]
	if !ascii {
		if inClass {
			switch c {
			case 'd':
				return pyDigit, 1, nil
			case 'w':
				return pyWordClass, 1, nil
			case 's':
				return pySpace, 1, nil
			}
		} else {
			switch c {
			case 'd':
				return pyDigit, 1, nil
			case 'D':
				return `\P{Nd}`, 1, nil
			case 'w':
				return "[" + pyWordClass + "]", 1, nil
			case 'W':
				return "[^" + pyWordClass + "]", 1, nil
			case 's':
				return "[" + pySpace + "]", 1, nil
			case 'S':
				return "[^" + pySpace + "]", 1, nil
			}
		}
	}
	switch c {
	case 'Z':
		if !inClass {
			return `\z`, 1, nil
		}
	case 'b':
		if inClass {
			return `\x{8}`, 1, nil
		}
	case 'u', 'U', 'x':
		width := map[rune]int{'x': 2, 'u': 4, 'U': 8}[c]
		if len(p) < 1+width {
			return "", 0, fmt.Errorf("incomplete escape \\%c in %q", c, pat)
		}
		hex := string(p[1 : 1+width])
		if _, err := strconv.ParseUint(hex, 16, 32); err != nil {
			return "", 0, fmt.Errorf("incomplete escape \\%c%s in %q", c, hex, pat)
		}
		return `\x{` + hex + `}`, 1 + width, nil
	case 'N':
		return "", 0, unsupported(pat, `named Unicode escapes \N{...}`)
	}
	if c >= '0' && c <= '9' {
		// Octal: \0, \0N, \0NN, or three octal digits; otherwise a group reference.
		n := 0
		if c == '0' {
			n = 1
			for n < 3 && n < len(p) && p[n] >= '0' && p[n] <= '7' {
				n++
			}
		} else if inClass {
			for n < 3 && n < len(p) && p[n] >= '0' && p[n] <= '7' {
				n++
			}
		} else if len(p) >= 3 && isOct(p[0]) && isOct(p[1]) && isOct(p[2]) {
			n = 3
		}
		if n == 0 {
			return "", 0, unsupported(pat, "backreferences such as \\1")
		}
		v, _ := strconv.ParseUint(string(p[:n]), 8, 32)
		if v > 0o377 {
			return "", 0, fmt.Errorf("octal escape value \\%s outside of range 0-0o377 in %q", string(p[:n]), pat)
		}
		return fmt.Sprintf(`\x{%x}`, v), n, nil
	}
	return `\` + string(c), 1, nil
}

func isOct(r rune) bool { return r >= '0' && r <= '7' }

func unsupported(pat, what string) error {
	return fmt.Errorf("regular expression %q uses %s, which Go's RE2 engine does not support", pat, what)
}

// ExpandPython expands a Python re replacement template (as match.expand
// does) for the match of re in s at loc: \1, \g<1>, \g<name>, and the
// escapes \n, \t, \\ and friends. Groups that did not participate are empty.
func ExpandPython(re *regexp.Regexp, template, s string, loc []int) (string, error) {
	group := func(g, pos int) (string, error) {
		if g < 0 || g > re.NumSubexp() {
			return "", fmt.Errorf("invalid group reference %d at position %d", g, pos)
		}
		if loc[2*g] < 0 {
			return "", nil
		}
		return s[loc[2*g]:loc[2*g+1]], nil
	}
	escapes := map[rune]string{'a': "\a", 'b': "\b", 'f': "\f", 'n': "\n", 'r': "\r", 't': "\t", 'v': "\v", '\\': `\`}

	t := []rune(template)
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] != '\\' {
			b.WriteRune(t[i])
			continue
		}
		if i+1 >= len(t) {
			return "", fmt.Errorf("bad escape (end of pattern) at position %d", i)
		}
		c := t[i+1]
		switch {
		case c == 'g':
			rest := string(t[i+2:])
			end := strings.IndexRune(rest, '>')
			if !strings.HasPrefix(rest, "<") || end < 0 {
				return "", fmt.Errorf("missing group name in template")
			}
			name := rest[1:end]
			var g int
			if n, err := strconv.Atoi(name); err == nil {
				g = n
			} else if g = re.SubexpIndex(name); g < 0 {
				return "", fmt.Errorf("unknown group name '%s'", name)
			}
			v, err := group(g, i+3)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i += 2 + utf8.RuneCountInString(rest[:end+1]) - 1
		case c == '0':
			n := 1
			for n < 3 && i+1+n < len(t) && isOct(t[i+1+n]) {
				n++
			}
			v, _ := strconv.ParseUint(string(t[i+1:i+1+n]), 8, 32)
			b.WriteRune(rune(v))
			i += n
		case c >= '1' && c <= '9':
			n := 1
			if i+2 < len(t) && t[i+2] >= '0' && t[i+2] <= '9' {
				if i+3 < len(t) && isOct(c) && isOct(t[i+2]) && isOct(t[i+3]) {
					v, _ := strconv.ParseUint(string(t[i+1:i+4]), 8, 32)
					if v > 0o377 {
						return "", fmt.Errorf("octal escape value \\%s outside of range 0-0o377", string(t[i+1:i+4]))
					}
					b.WriteRune(rune(v))
					i += 3
					continue
				}
				n = 2
			}
			g, _ := strconv.Atoi(string(t[i+1 : i+1+n]))
			v, err := group(g, i+1)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i += n
		case escapes[c] != "":
			b.WriteString(escapes[c])
			i++
		case c < utf8.RuneSelf && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'):
			return "", fmt.Errorf(`bad escape \%c at position %d`, c, i)
		default:
			b.WriteRune('\\')
			b.WriteRune(c)
			i++
		}
	}
	return b.String(), nil
}
