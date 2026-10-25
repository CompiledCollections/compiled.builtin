package ansible

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
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

// PythonGlobalFlags returns the inline flags a Python pattern sets for the
// whole pattern, such as "m" for (?m): the letters of re.MULTILINE and
// friends that would show in the compiled pattern's flags.
func PythonGlobalFlags(pat string) string {
	flags, _ := globalFlags([]rune(pat))
	return flags
}

// globalFlags reads the global inline flags at the start of p, which must
// come first in Python 3.11+, and returns them and where the pattern goes on.
func globalFlags(p []rune) (string, int) {
	var flags string
	i := 0
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
	return flags, i
}

func translatePython(pat string) (string, error) {
	p := []rune(pat)
	flags, i := globalFlags(p)
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

// UnsupportedError is a valid Python pattern using something RE2 can't do.
type UnsupportedError struct{ Pattern, What string }

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("regular expression %q uses %s, which Go's RE2 engine does not support", e.Pattern, e.What)
}

func unsupported(pat, what string) error { return &UnsupportedError{pat, what} }

// TemplateError is a re.error raised for a replacement template, with
// Python's message and position.
type TemplateError struct{ Msg string }

func (e *TemplateError) Error() string { return e.Msg }

// UnknownGroupError is the IndexError Python raises for a template naming
// a group the pattern doesn't have.
type UnknownGroupError struct{ Name string }

func (e *UnknownGroupError) Error() string { return "unknown group name " + PyRepr(e.Name) }

// Template is a parsed replacement template: literal text and group references.
type Template struct {
	parts []templatePart
}

type templatePart struct {
	literal string
	group   int // -1 for literal text
}

// ParseTemplate parses a re replacement template as Python's
// _parser.parse_template does: \1 and \g<name> group references, octal
// escapes, \n, \t, \\ and friends. Errors are a *TemplateError or an
// *UnknownGroupError, raised as Python raises them: before any matching.
func ParseTemplate(re *regexp.Regexp, template string) (*Template, error) {
	t := []rune(template)
	var parts []templatePart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, templatePart{literal: lit.String(), group: -1})
			lit.Reset()
		}
	}
	fail := func(pos int, format string, a ...any) error {
		return &TemplateError{fmt.Sprintf(format, a...) + fmt.Sprintf(" at position %d", pos)}
	}
	addGroup := func(index *big.Int, pos int) error {
		if !index.IsInt64() || index.Int64() > int64(re.NumSubexp()) {
			return fail(pos, "invalid group reference %s", index.String())
		}
		flush()
		parts = append(parts, templatePart{group: int(index.Int64())})
		return nil
	}
	escapes := map[rune]string{'a': "\a", 'b': "\b", 'f': "\f", 'n': "\n", 'r': "\r", 't': "\t", 'v': "\v", '\\': `\`}

	for i := 0; i < len(t); i++ {
		if t[i] != '\\' {
			lit.WriteRune(t[i])
			continue
		}
		if i+1 >= len(t) {
			return nil, fail(len(t)-1, "bad escape (end of pattern)")
		}
		c := t[i+1]
		next := i + 2 // what Python's tokenizer reads next
		peek := func(j int) rune {
			if j < len(t) {
				return t[j]
			}
			return 0
		}
		switch {
		case c == 'g':
			if peek(next) != '<' {
				return nil, fail(next, "missing <")
			}
			start := next + 1
			end := start
			for end < len(t) && t[end] != '>' {
				end++
			}
			name := string(t[start:end])
			if end >= len(t) {
				if name == "" {
					return nil, fail(end, "missing group name")
				}
				return nil, fail(start, "missing >, unterminated name")
			}
			if name == "" {
				return nil, fail(start, "missing group name")
			}
			if isASCIIDigits(name) {
				index, _ := new(big.Int).SetString(name, 10)
				if err := addGroup(index, start); err != nil {
					return nil, err
				}
			} else {
				if !isIdentifier(name) {
					return nil, fail(start, "bad character in group name %s", PyRepr(name))
				}
				g := re.SubexpIndex(name)
				if g < 0 {
					return nil, &UnknownGroupError{name}
				}
				flush()
				parts = append(parts, templatePart{group: g})
			}
			i = end
		case c == '0':
			n := 1
			for n < 3 && isOct(peek(i+1+n)) {
				n++
			}
			v, _ := strconv.ParseUint(string(t[i+1:i+1+n]), 8, 32)
			lit.WriteRune(rune(v & 0xff))
			i += n
		case c >= '1' && c <= '9':
			digits := string(c)
			if d := peek(next); d >= '0' && d <= '9' {
				digits += string(d)
				if isOct(c) && isOct(d) && isOct(peek(next+1)) {
					digits += string(peek(next + 1))
					v, _ := strconv.ParseUint(digits, 8, 32)
					if v > 0o377 {
						return nil, fail(i, "octal escape value \\%s outside of range 0-0o377", digits)
					}
					lit.WriteRune(rune(v))
					i += 3
					continue
				}
			}
			index, _ := new(big.Int).SetString(digits, 10)
			if err := addGroup(index, i+1); err != nil {
				return nil, err
			}
			i += len(digits)
		case escapes[c] != "":
			lit.WriteString(escapes[c])
			i++
		case c < utf8.RuneSelf && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'):
			return nil, fail(i, `bad escape \%c`, c)
		default:
			lit.WriteRune('\\')
			lit.WriteRune(c)
			i++
		}
	}
	flush()
	return &Template{parts: parts}, nil
}

// Expand fills the template in for the match of a pattern in s at loc;
// groups that did not participate are empty.
func (t *Template) Expand(s string, loc []int) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.group < 0 {
			b.WriteString(p.literal)
		} else if loc[2*p.group] >= 0 {
			b.WriteString(s[loc[2*p.group]:loc[2*p.group+1]])
		}
	}
	return b.String()
}

func isASCIIDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// isIdentifier approximates str.isidentifier.
func isIdentifier(s string) bool {
	for i, r := range s {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r):
		case i > 0 && (unicode.IsDigit(r) || unicode.In(r, unicode.Mn, unicode.Mc, unicode.Pc)):
		default:
			return false
		}
	}
	return s != ""
}

// PythonRegexp is a Python pattern compiled for Go, which can also list its
// matches the way Python's re does.
type PythonRegexp struct {
	*regexp.Regexp
	// at is the pattern after one character of context, anchored: it tells
	// what the pattern matches at a given position.
	at *regexp.Regexp
}

// CompilePythonRegexp is CompilePython, returning a *PythonRegexp.
func CompilePythonRegexp(pat string) (*PythonRegexp, error) {
	tr, err := translatePython(pat)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(tr)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression %q: %v", pat, err)
	}
	at, err := regexp.Compile(`\A(?s:.)(?:` + tr + `)`)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression %q: %v", pat, err)
	}
	return &PythonRegexp{Regexp: re, at: at}, nil
}

// FindAllPython returns the submatch indexes of every match in s, as
// re.finditer finds them. Go skips an empty match right after a non-empty
// one; Python 3.7+ keeps it.
func (p *PythonRegexp) FindAllPython(s string) [][]int {
	found := p.FindAllStringSubmatchIndex(s, -1)
	var out [][]int
	for i, m := range found {
		out = append(out, m)
		end := m[1]
		if m[0] == end || i+1 < len(found) && found[i+1][0] == end {
			continue
		}
		if empty := p.emptyAt(s, end); empty != nil {
			out = append(out, empty)
		}
	}
	return out
}

// emptyAt returns the match at pos if it is empty, else nil. pos > 0.
func (p *PythonRegexp) emptyAt(s string, pos int) []int {
	_, size := utf8.DecodeLastRuneInString(s[:pos])
	from := pos - size
	loc := p.at.FindStringSubmatchIndex(s[from:])
	if loc == nil || loc[1] != size {
		return nil
	}
	out := make([]int, len(loc))
	out[0], out[1] = pos, pos
	for i := 2; i < len(loc); i++ {
		if loc[i] >= 0 {
			out[i] = loc[i] + from
		} else {
			out[i] = -1
		}
	}
	return out
}

// SubnPython is re.subn(pattern, template, s): s with every match replaced
// by the expanded template, and the number of replacements.
func (p *PythonRegexp) SubnPython(t *Template, s string) (string, int) {
	var b strings.Builder
	last, n := 0, 0
	for _, m := range p.FindAllPython(s) {
		b.WriteString(s[last:m[0]])
		b.WriteString(t.Expand(s, m))
		last = m[1]
		n++
	}
	b.WriteString(s[last:])
	return b.String(), n
}

// ExpandPython expands a Python re replacement template (as match.expand
// does) for the match of re in s at loc: \1, \g<1>, \g<name>, and the
// escapes \n, \t, \\ and friends. Groups that did not participate are empty.
func ExpandPython(re *regexp.Regexp, template, s string, loc []int) (string, error) {
	t, err := ParseTemplate(re, template)
	if err != nil {
		return "", err
	}
	return t.Expand(s, loc), nil
}
