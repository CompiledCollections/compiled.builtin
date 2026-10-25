package uri

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// prepareMultipart is module_utils.urls.prepare_multipart: a dict of fields
// as a multipart/form-data body, byte for byte as Python's email package
// writes it (with a different random boundary).
//
// A field is a string (a text/plain part) or a dict with filename, content,
// mime_type and multipart_encoding. A filename without content is read
// from the target.
func prepareMultipart(fields any) (contentType string, body []byte, err error) {
	m, ok := fields.(*orderedMap)
	if !ok {
		return "", nil, &pyError{"TypeError", "Mapping is required, cannot be type " + pyTypeName(fields)}
	}
	names := append([]string(nil), m.keys...)
	sort.Strings(names)

	var parts [][]byte
	for _, field := range names {
		part, err := multipartPart(field, m.vals[field])
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, part)
	}

	boundary := newBoundary(parts)
	var b strings.Builder
	for _, part := range parts {
		b.WriteString("--" + boundary + "\r\n")
		b.Write(part)
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return `multipart/form-data; boundary="` + boundary + `"`, []byte(b.String()), nil
}

func multipartPart(field string, value any) ([]byte, error) {
	var (
		mainType, subType string
		content           any
		filename          string
		encoding          = "base64"
	)
	switch v := value.(type) {
	case string:
		mainType, subType, content = "text", "plain", v
	case *orderedMap:
		if f := v.vals["filename"]; pyTruthy(f) {
			filename = pyStr(f)
		}
		if e := v.vals["multipart_encoding"]; pyTruthy(e) {
			encoding = pyStr(e)
		}
		content = v.vals["content"]
		if filename == "" && !pyTruthy(content) {
			return nil, &pyError{"ValueError", "at least one of filename or content must be provided"}
		}
		mime := ""
		if t := v.vals["mime_type"]; pyTruthy(t) {
			mime = pyStr(t)
		} else {
			mime = guessType(filename)
		}
		mainType, subType, _ = strings.Cut(mime, "/")
	default:
		return nil, &pyError{"TypeError", "value must be a string, or mapping, cannot be type " + pyTypeName(value)}
	}

	var headers []string
	var payload []byte
	if !pyTruthy(content) && filename != "" {
		if encoding != "base64" && encoding != "7or8bit" {
			return nil, &pyError{"ValueError", "multipart_encoding must be one of dict_keys(['base64', '7or8bit'])."}
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, &pyError{"OSError", osErrorText(err, filename)}
		}
		// MIMEApplication: the encoder adds Content-Transfer-Encoding, then
		// the module replaces Content-Type, which moves it after.
		if encoding == "base64" {
			headers = append(headers, "Content-Transfer-Encoding: base64")
			payload = []byte(base64Lines(data))
		} else {
			cte := "7bit"
			for _, c := range data {
				if c > 0x7f {
					cte = "8bit"
					break
				}
			}
			headers = append(headers, "Content-Transfer-Encoding: "+cte)
			payload = crlfLines(data)
		}
		headers = append(headers, "Content-Type: "+mainType+"/"+subType)
	} else {
		headers = append(headers, "Content-Type: "+mainType+"/"+subType)
		payload = crlfLines([]byte(pyStr(content)))
	}
	disposition := "Content-Disposition: form-data; name=" + quoteParam(field)
	if filename != "" {
		disposition += "; filename=" + quoteParam(filepath.Base(filename))
	}
	headers = append(headers, disposition)

	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + string(payload)), nil
}

// quoteParam is email.utils quoting of a header parameter value.
func quoteParam(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// base64Lines is base64.encodebytes, 76 characters per line, with the
// lines ended by CRLF as the HTTP policy writes them.
func base64Lines(data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	if enc != "" {
		b.WriteString(enc + "\r\n")
	}
	return b.String()
}

var lineEnd = regexp.MustCompile("\r\n|\r|\n")

// crlfLines rewrites line endings to CRLF, as Python's generator does to
// every non-base64 payload (binary 7or8bit files included).
func crlfLines(data []byte) []byte {
	return lineEnd.ReplaceAll(data, []byte("\r\n"))
}

// newBoundary is email.generator._make_boundary: 15 '=', 19 random digits, '=='.
func newBoundary(parts [][]byte) string {
	for {
		n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
		boundary := fmt.Sprintf("===============%019d==", n)
		clash := false
		for _, p := range parts {
			if strings.Contains(string(p), boundary) {
				clash = true
			}
		}
		if !clash {
			return boundary
		}
	}
}

// guessType is mimetypes.guess_type(filename, strict=False)[0], falling
// back to application/octet-stream: Python's default table, overridden by
// the system mime.types files Python reads too.
func guessType(filename string) string {
	ext := filepath.Ext(filename)
	if ext == "" {
		return "application/octet-stream"
	}
	types := systemTypes()
	for _, candidate := range []string{ext, strings.ToLower(ext)} {
		if t, ok := types[candidate]; ok {
			return t
		}
		if t, ok := pyTypesMap[candidate]; ok {
			return t
		}
		if t, ok := pyCommonTypes[candidate]; ok {
			return t
		}
	}
	return "application/octet-stream"
}

var systemTypesCache map[string]string

// systemTypes reads the mime.types files in mimetypes.knownfiles order.
func systemTypes() map[string]string {
	if systemTypesCache != nil {
		return systemTypesCache
	}
	systemTypesCache = map[string]string{}
	for _, path := range []string{
		"/etc/mime.types", "/etc/httpd/mime.types", "/etc/httpd/conf/mime.types",
		"/etc/apache/mime.types", "/etc/apache2/mime.types",
		"/usr/local/etc/httpd/conf/mime.types", "/usr/local/lib/netscape/mime.types",
		"/usr/local/etc/httpd/conf/mime.types", "/usr/local/etc/mime.types",
	} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			fields := strings.Fields(line)
			for _, ext := range fields[min(1, len(fields)):] {
				systemTypesCache["."+ext] = fields[0]
			}
		}
		f.Close()
	}
	return systemTypesCache
}
