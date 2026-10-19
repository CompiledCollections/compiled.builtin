package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// The body option is 'raw': whatever the task gave, with dict order kept.
// These helpers work on it the way the Python module does.

// orderedMap is a JSON object with its key order.
type orderedMap struct {
	keys []string
	vals map[string]any
}

// decodeOrdered decodes JSON into string, json.Number, bool, nil, []any and
// *orderedMap values, keeping object key order (as Python dicts do).
func decodeOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return decodeValue(dec)
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := &orderedMap{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				if _, seen := m.vals[k]; !seen {
					m.keys = append(m.keys, k)
				}
				m.vals[k] = v
			}
			_, err := dec.Token()
			return m, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
	}
	return tok, nil
}

// pyTypeName is the Python type name of a decoded value, for error messages.
func pyTypeName(v any) string {
	switch t := v.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case bool:
		return "bool"
	case json.Number:
		if strings.ContainsAny(string(t), ".eE") {
			return "float"
		}
		return "int"
	case []any:
		return "list"
	case *orderedMap:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// pyTruthy is Python truthiness.
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case json.Number:
		f, err := t.Float64()
		return err != nil || f != 0
	case []any:
		return len(t) > 0
	case *orderedMap:
		return len(t.keys) > 0
	}
	return true
}

// pyStr is Python's str() of a value, as to_text gives for non-strings.
func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

// pyRepr is Python's repr().
func pyRepr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return pyStrRepr(t)
	case bool:
		if t {
			return "True"
		}
		return "False"
	case json.Number:
		return string(t)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *orderedMap:
		parts := make([]string, len(t.keys))
		for i, k := range t.keys {
			parts[i] = pyStrRepr(k) + ": " + pyRepr(t.vals[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func pyStrRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\' || r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
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
	b.WriteByte(quote)
	return b.String()
}

// pyJSONDumps is Python's json.dumps(value) with default settings:
// ", " and ": " separators, ensure_ascii, and key order kept.
func pyJSONDumps(v any) string {
	var b strings.Builder
	writeJSON(&b, v)
	return b.String()
}

func writeJSON(b *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(string(t))
	case string:
		writeJSONString(b, t)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			writeJSON(b, e)
		}
		b.WriteByte(']')
	case *orderedMap:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeJSONString(b, k)
			b.WriteString(": ")
			writeJSON(b, t.vals[k])
		}
		b.WriteByte('}')
	}
}

func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				b.WriteRune(r)
			case r > 0xffff:
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
			default:
				fmt.Fprintf(b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
}

// quotePlus is urllib.parse.quote_plus with UTF-8.
func quotePlus(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', strings.IndexByte("_.-~", c) >= 0:
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pyIter is what iterating a value gives in Python: characters of a string,
// items of a list, keys of a dict. ok is false for non-iterables.
func pyIter(v any) ([]any, bool) {
	switch t := v.(type) {
	case string:
		out := []any{}
		for _, r := range t {
			out = append(out, string(r))
		}
		return out, true
	case []any:
		return t, true
	case *orderedMap:
		out := make([]any, len(t.keys))
		for i, k := range t.keys {
			out[i] = k
		}
		return out, true
	}
	return nil, false
}

// pyError is a Python exception: its type and message.
type pyError struct {
	kind, msg string
}

func (e *pyError) Error() string { return e.msg }

// formURLEncoded is the module's form_urlencoded(): a dict or a list of
// (key, value) pairs as an application/x-www-form-urlencoded string.
func formURLEncoded(body any) (any, error) {
	var items [][2]any
	switch t := body.(type) {
	case *orderedMap:
		for _, k := range t.keys {
			items = append(items, [2]any{k, t.vals[k]})
		}
	case []any:
		for _, item := range t {
			elems, ok := pyIter(item)
			if !ok {
				return nil, &pyError{"TypeError", fmt.Sprintf("cannot unpack non-iterable %s object", pyTypeName(item))}
			}
			switch {
			case len(elems) < 2:
				return nil, &pyError{"ValueError", fmt.Sprintf("not enough values to unpack (expected 2, got %d)", len(elems))}
			case len(elems) > 2:
				return nil, &pyError{"ValueError", fmt.Sprintf("too many values to unpack (expected 2, got %d)", len(elems))}
			}
			items = append(items, [2]any{elems[0], elems[1]})
		}
	default:
		return body, nil
	}

	var pairs []string
	for _, item := range items {
		key, values := item[0], item[1]
		list := []any{values}
		if _, isStr := values.(string); !isStr {
			if elems, ok := pyIter(values); ok {
				list = elems
			}
		}
		for _, v := range list {
			if v != nil {
				pairs = append(pairs, quotePlus(pyStr(key))+"="+quotePlus(pyStr(v)))
			}
		}
	}
	return strings.Join(pairs, "&"), nil
}
