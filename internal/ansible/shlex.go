package ansible

import (
	"errors"
	"strings"
)

// ShlexSplit splits s like Python's shlex.split(s) in POSIX mode.
func ShlexSplit(s string) ([]string, error) {
	const ws = " \t\r\n"
	var (
		tokens   []string
		token    strings.Builder
		inToken  bool
		state    = ' '
		escState rune
	)
	emit := func() {
		tokens = append(tokens, token.String())
		token.Reset()
		inToken = false
	}
	for _, c := range s {
		switch state {
		case ' ', 'a':
			switch {
			case strings.ContainsRune(ws, c):
				if inToken {
					emit()
				}
				state = ' '
			case c == '\\':
				escState, state, inToken = 'a', '\\', true
			case c == '\'' || c == '"':
				state, inToken = c, true
			default:
				token.WriteRune(c)
				state, inToken = 'a', true
			}
		case '\'':
			if c == '\'' {
				state = 'a'
			} else {
				token.WriteRune(c)
			}
		case '"':
			switch c {
			case '"':
				state = 'a'
			case '\\':
				escState, state = '"', '\\'
			default:
				token.WriteRune(c)
			}
		case '\\':
			// Inside double quotes a backslash only escapes " and itself.
			if escState == '"' && c != '"' && c != '\\' {
				token.WriteRune('\\')
			}
			token.WriteRune(c)
			state = escState
		}
	}
	switch state {
	case '\'', '"':
		return nil, errors.New("No closing quotation")
	case '\\':
		return nil, errors.New("No escaped character")
	}
	if inToken {
		emit()
	}
	return tokens, nil
}

var shlexUnsafe = &LazyRegexp{Expr: `[^A-Za-z0-9_@%+=:,./-]`}

// ShlexQuote quotes s for a POSIX shell, like Python's shlex.quote.
func ShlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !shlexUnsafe.Get().MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
