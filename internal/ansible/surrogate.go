package ansible

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SurrogateEscaped encodes bytes as a JSON string the way a Python module
// returns them: valid UTF-8 as text, and each invalid byte as a lone
// surrogate (\udc80-\udcff, Python's surrogateescape). The controller
// treats such strings as Python modules' results get treated: since
// ansible-core 2.19 it refuses them as invalid UTF-8.
func SurrogateEscaped(data []byte) json.RawMessage {
	var b strings.Builder
	b.WriteByte('"')
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size <= 1 {
			fmt.Fprintf(&b, `\u%04x`, 0xdc00+int(data[0]))
			data = data[1:]
			continue
		}
		quoted, _ := json.Marshal(string(data[:size]))
		b.Write(quoted[1 : len(quoted)-1])
		data = data[size:]
	}
	b.WriteByte('"')
	return json.RawMessage(b.String())
}
