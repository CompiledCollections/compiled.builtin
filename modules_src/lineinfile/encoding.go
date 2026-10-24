package lineinfile

import (
	"strings"

	"compiled/builtin/internal/codecs"
)

// decode reads text the way Python's text mode does: decoded strictly, with
// universal newlines (\r\n and \r become \n).
func decode(data []byte, enc string) (string, error) {
	s, err := codecs.Decode(data, enc)
	if err != nil {
		return "", err
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

func encode(s, enc string) ([]byte, error) { return codecs.Encode(s, enc) }

// readlines splits text after each \n, like Python's file.readlines.
func readlines(s string) []string {
	var lines []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:i+1])
		s = s[i+1:]
	}
	return lines
}
