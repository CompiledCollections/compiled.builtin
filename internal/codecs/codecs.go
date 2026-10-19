// Package codecs decodes and encodes text by Python codec name, with the
// error messages Python's codecs give.
package codecs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The supported encodings, by Python codec name and aliases.

var encodingAliases = map[string]string{
	"utf-8": "utf-8", "utf8": "utf-8", "u8": "utf-8", "utf": "utf-8", "cp65001": "utf-8",
	"utf-8-sig": "utf-8-sig", "utf8-sig": "utf-8-sig",
	"ascii": "ascii", "us-ascii": "ascii", "646": "ascii", "us": "ascii",
	"latin-1": "latin-1", "latin1": "latin-1", "latin": "latin-1", "l1": "latin-1",
	"iso-8859-1": "latin-1", "iso8859-1": "latin-1", "8859": "latin-1", "cp819": "latin-1", "iso-ir-100": "latin-1",
	"cp1252": "cp1252", "windows-1252": "cp1252",
	"utf-16": "utf-16", "utf16": "utf-16", "u16": "utf-16",
	"utf-16-le": "utf-16-le", "utf-16le": "utf-16-le",
	"utf-16-be": "utf-16-be", "utf-16be": "utf-16-be",
	"utf-32": "utf-32", "utf32": "utf-32", "u32": "utf-32",
	"utf-32-le": "utf-32-le", "utf-32le": "utf-32-le",
	"utf-32-be": "utf-32-be", "utf-32be": "utf-32-be",
}

// Lookup returns the canonical name of a supported codec, or Python's
// "unknown encoding" error.
func Lookup(name string) (string, error) {
	n := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-"), " ", "-")
	if c, ok := encodingAliases[n]; ok {
		return c, nil
	}
	return "", fmt.Errorf("unknown encoding: %s", name)
}

// cp1252 bytes 0x80-0x9f; 0 marks the ones Python leaves undefined.
var cp1252 = [32]rune{
	0x20AC, 0, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017D, 0,
	0, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0, 0x017E, 0x0178,
}

// Decode decodes data, failing like Python's strict error handler.
func Decode(data []byte, enc string) (string, error) {
	c, err := Lookup(enc)
	if err != nil {
		return "", err
	}
	var s string
	switch c {
	case "utf-8", "utf-8-sig":
		if c == "utf-8-sig" {
			data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		}
		if err := CheckUTF8(data); err != nil {
			return "", err
		}
		s = string(data)
	case "ascii":
		for i, b := range data {
			if b > 0x7f {
				return "", fmt.Errorf("'ascii' codec can't decode byte 0x%02x in position %d: ordinal not in range(128)", b, i)
			}
		}
		s = string(data)
	case "latin-1", "cp1252":
		var b strings.Builder
		for i, x := range data {
			r := rune(x)
			if c == "cp1252" && x >= 0x80 && x <= 0x9f {
				if r = cp1252[x-0x80]; r == 0 {
					return "", fmt.Errorf("'charmap' codec can't decode byte 0x%02x in position %d: character maps to <undefined>", x, i)
				}
			}
			b.WriteRune(r)
		}
		s = b.String()
	case "utf-16", "utf-16-le", "utf-16-be":
		order := binary.ByteOrder(binary.LittleEndian)
		switch {
		case c == "utf-16-be":
			order = binary.BigEndian
		case c == "utf-16" && bytes.HasPrefix(data, []byte{0xfe, 0xff}):
			order, data = binary.BigEndian, data[2:]
		case c == "utf-16" && bytes.HasPrefix(data, []byte{0xff, 0xfe}):
			data = data[2:]
		}
		if len(data)%2 != 0 {
			return "", fmt.Errorf("'utf-16' codec can't decode byte 0x%02x in position %d: truncated data", data[len(data)-1], len(data)-1)
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[2*i:])
		}
		s = string(utf16.Decode(units))
	case "utf-32", "utf-32-le", "utf-32-be":
		order := binary.ByteOrder(binary.LittleEndian)
		switch {
		case c == "utf-32-be":
			order = binary.BigEndian
		case c == "utf-32" && bytes.HasPrefix(data, []byte{0, 0, 0xfe, 0xff}):
			order, data = binary.BigEndian, data[4:]
		case c == "utf-32" && bytes.HasPrefix(data, []byte{0xff, 0xfe, 0, 0}):
			data = data[4:]
		}
		if len(data)%4 != 0 {
			return "", fmt.Errorf("'utf-32' codec can't decode bytes in position %d-%d: truncated data", len(data)/4*4, len(data)-1)
		}
		var b strings.Builder
		for i := 0; i < len(data); i += 4 {
			r := rune(order.Uint32(data[i:]))
			if !utf8.ValidRune(r) {
				return "", fmt.Errorf("'utf-32' codec can't decode bytes in position %d-%d: code point not in range(0x110000)", i, i+3)
			}
			b.WriteRune(r)
		}
		s = b.String()
	}
	return s, nil
}

// Encode encodes s, failing like Python's strict error handler. Error
// positions count from the start of the line, as Python reports them when
// writing a file line by line.
func Encode(s, enc string) ([]byte, error) {
	c, err := Lookup(enc)
	if err != nil {
		return nil, err
	}
	switch c {
	case "utf-8":
		return []byte(s), nil
	case "utf-8-sig":
		return append([]byte("\xef\xbb\xbf"), s...), nil
	case "ascii", "latin-1", "cp1252":
		limit, name := rune(0xff), "latin-1"
		if c == "ascii" {
			limit, name = 0x7f, "ascii"
		}
		out := make([]byte, 0, len(s))
		pos := 0 // Python encodes each line on its own, so positions are per line
		for _, r := range s {
			switch {
			case c == "cp1252" && (r >= 0x80 && r <= 0x9f || r > 0xff):
				b, ok := cp1252Byte(r)
				if !ok {
					return nil, fmt.Errorf("'charmap' codec can't encode character %s in position %d: character maps to <undefined>", PyCharRepr(r), pos)
				}
				out = append(out, b)
			case r > limit:
				return nil, fmt.Errorf("'%s' codec can't encode character %s in position %d: ordinal not in range(%d)", name, PyCharRepr(r), pos, limit+1)
			default:
				out = append(out, byte(r))
			}
			pos++
			if r == '\n' {
				pos = 0
			}
		}
		return out, nil
	case "utf-16", "utf-16-le", "utf-16-be":
		var order binary.AppendByteOrder = binary.LittleEndian
		var out []byte
		if c == "utf-16-be" {
			order = binary.BigEndian
		}
		if c == "utf-16" {
			out = []byte{0xff, 0xfe}
		}
		for _, u := range utf16.Encode([]rune(s)) {
			out = order.AppendUint16(out, u)
		}
		return out, nil
	default: // utf-32 family
		var order binary.AppendByteOrder = binary.LittleEndian
		var out []byte
		if c == "utf-32-be" {
			order = binary.BigEndian
		}
		if c == "utf-32" {
			out = []byte{0xff, 0xfe, 0, 0}
		}
		for _, r := range s {
			out = order.AppendUint32(out, uint32(r))
		}
		return out, nil
	}
}

func cp1252Byte(r rune) (byte, bool) {
	for i, c := range cp1252 {
		if c != 0 && c == r {
			return byte(0x80 + i), true
		}
	}
	return 0, false
}

// CheckUTF8 reports invalid UTF-8 with the message Python's decoder gives.
func CheckUTF8(data []byte) error {
	for i := 0; i < len(data); {
		b := data[i]
		if b < 0x80 {
			i++
			continue
		}
		var n int
		lo, hi := byte(0x80), byte(0xbf) // valid range for the second byte
		switch {
		case b >= 0xc2 && b <= 0xdf:
			n = 2
		case b >= 0xe0 && b <= 0xef:
			n = 3
			if b == 0xe0 {
				lo = 0xa0
			} else if b == 0xed {
				hi = 0x9f
			}
		case b >= 0xf0 && b <= 0xf4:
			n = 4
			if b == 0xf0 {
				lo = 0x90
			} else if b == 0xf4 {
				hi = 0x8f
			}
		default:
			return fmt.Errorf("'utf-8' codec can't decode byte 0x%02x in position %d: invalid start byte", b, i)
		}
		for k := 1; k < n; k++ {
			if i+k >= len(data) {
				if k == 1 {
					return fmt.Errorf("'utf-8' codec can't decode byte 0x%02x in position %d: unexpected end of data", b, i)
				}
				return fmt.Errorf("'utf-8' codec can't decode bytes in position %d-%d: unexpected end of data", i, i+k-1)
			}
			c := data[i+k]
			l, h := byte(0x80), byte(0xbf)
			if k == 1 {
				l, h = lo, hi
			}
			if c < l || c > h {
				if k == 1 {
					return fmt.Errorf("'utf-8' codec can't decode byte 0x%02x in position %d: invalid continuation byte", b, i)
				}
				return fmt.Errorf("'utf-8' codec can't decode bytes in position %d-%d: invalid continuation byte", i, i+k-1)
			}
		}
		i += n
	}
	return nil
}

// PyCharRepr is how Python's codec errors quote a character: '\xe9', '\u20ac'.
func PyCharRepr(r rune) string {
	switch {
	case r < 0x100:
		return fmt.Sprintf("'\\x%02x'", r)
	case r < 0x10000:
		return fmt.Sprintf("'\\u%04x'", r)
	}
	return fmt.Sprintf("'\\U%08x'", r)
}

// DecodeLossy decodes data like Python's to_text: undecodable bytes don't
// fail (Python keeps them as surrogates; here they become U+FFFD). Only an
// unknown codec is an error.
func DecodeLossy(data []byte, enc string) (string, error) {
	if _, err := Lookup(enc); err != nil {
		return "", err
	}
	if s, err := Decode(data, enc); err == nil {
		return s, nil
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), nil
}
