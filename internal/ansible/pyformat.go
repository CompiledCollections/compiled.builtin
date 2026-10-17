package ansible

import (
	"errors"
	"fmt"
	"strings"
)

// PercentFormat is Python's `format % arg` with a single str argument:
// flags, width and precision work, and so do Python's errors for the
// conversions a string can't take.
func PercentFormat(format, arg string) (string, error) {
	var b strings.Builder
	used := false
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			b.WriteByte(format[i])
			continue
		}
		start := i
		i++
		if i < len(format) && format[i] == '(' {
			return "", errors.New("format requires a mapping")
		}
		left := false
		for i < len(format) && strings.IndexByte("-+ #0", format[i]) >= 0 {
			left = left || format[i] == '-'
			i++
		}
		width := 0
		for i < len(format) && format[i] >= '0' && format[i] <= '9' {
			width = width*10 + int(format[i]-'0')
			i++
		}
		precision := -1
		if i < len(format) && format[i] == '.' {
			precision = 0
			i++
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				precision = precision*10 + int(format[i]-'0')
				i++
			}
		}
		if i >= len(format) {
			return "", errors.New("incomplete format")
		}
		c := format[i]
		if c == '%' && i == start+1 {
			b.WriteByte('%')
			continue
		}
		var s string
		switch c {
		case 's', 'c':
			s = arg
		case 'r', 'a':
			s = PyRepr(arg)
		case 'd', 'i', 'u':
			return "", fmt.Errorf("%%%c format: a real number is required, not str", c)
		case 'x', 'X', 'o':
			return "", fmt.Errorf("%%%c format: an integer is required, not str", c)
		case 'e', 'E', 'f', 'F', 'g', 'G':
			return "", errors.New("must be real number, not str")
		default:
			return "", fmt.Errorf("unsupported format character '%c' (0x%x) at index %d", c, c, i)
		}
		if used {
			return "", errors.New("not enough arguments for format string")
		}
		used = true
		if precision >= 0 && precision < len([]rune(s)) {
			s = string([]rune(s)[:precision])
		}
		if pad := width - len([]rune(s)); pad > 0 {
			if left {
				s += strings.Repeat(" ", pad)
			} else {
				s = strings.Repeat(" ", pad) + s
			}
		}
		b.WriteString(s)
	}
	if !used {
		return "", errors.New("not all arguments converted during string formatting")
	}
	return b.String(), nil
}

// PyRepr is repr() of a str.
func PyRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == quote:
			b.WriteString(`\` + quote)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(quote)
	return b.String()
}
