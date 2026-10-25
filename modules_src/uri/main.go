// Uri equivalent module, written in Go. A port of ansible.builtin.uri and
// the parts of module_utils.urls (fetch_url, open_url) it uses; client.go
// reproduces the urllib behaviour those rely on.
package uri

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/codecs"
	"compiled/builtin/internal/files"
)

type Args struct {
	files.FileArgs
	URL                 string          `json:"url"`
	Dest                *string         `json:"dest"`
	URLUsername         *string         `json:"url_username"`
	URLPassword         *string         `json:"url_password"`
	Body                json.RawMessage `json:"body"`
	BodyFormat          string          `json:"body_format"`
	Src                 *string         `json:"src"`
	Method              string          `json:"method"`
	ReturnContent       bool            `json:"return_content"`
	FollowRedirects     string          `json:"follow_redirects"`
	Creates             *string         `json:"creates"`
	Removes             *string         `json:"removes"`
	StatusCode          []int           `json:"status_code"`
	Timeout             *int            `json:"timeout"`
	Headers             json.RawMessage `json:"headers"`
	UnixSocket          *string         `json:"unix_socket"`
	CAPath              *string         `json:"ca_path"`
	UnredirectedHeaders []string        `json:"unredirected_headers"`
	Decompress          *bool           `json:"decompress"`
	Ciphers             []string        `json:"ciphers"`
	UseNetrc            *bool           `json:"use_netrc"`
	Force               bool            `json:"force"`
	HTTPAgent           *string         `json:"http_agent"`
	UseProxy            *bool           `json:"use_proxy"`
	ValidateCerts       *bool           `json:"validate_certs"`
	ForceBasicAuth      bool            `json:"force_basic_auth"`
	ClientCert          *string         `json:"client_cert"`
	ClientKey           *string         `json:"client_key"`
	UseGSSAPI           bool            `json:"use_gssapi"`

	// get_url's own options; GetURL is set when the binary runs as get_url.
	GetURL   bool    `json:"_get_url"`
	Backup   bool    `json:"backup"`
	Checksum string  `json:"checksum"`
	TmpDest  *string `json:"tmp_dest"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func pathOpt(p *string) *string {
	if p == nil {
		return nil
	}
	v := ansible.ExpandPath(*p)
	return &v
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var jsonCandidates = map[string]bool{"json": true, "javascript": true}

var args Args

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	ansible.ReadArgs(&args)
	if args.GetURL {
		getURL()
		return
	}

	rawURL := args.URL
	bodyFormat := strings.ToLower(args.BodyFormat)
	if bodyFormat == "" {
		bodyFormat = "raw"
	}
	method := strings.ToUpper(args.Method)
	if args.Method == "" {
		method = "GET"
	}
	dest := pathOpt(args.Dest)
	creates, removes := pathOpt(args.Creates), pathOpt(args.Removes)
	statusCode := args.StatusCode
	if statusCode == nil {
		statusCode = []int{200}
	}
	timeout := 30
	if args.Timeout != nil {
		timeout = *args.Timeout
	}

	maskedURL := maskURL(rawURL)

	if !regexp.MustCompile(`^[A-Z]+$`).MatchString(method) {
		ansible.Fail("Parameter 'method' needs to be a single word in uppercase, like GET or POST.", nil)
	}

	headers := &orderedMap{vals: map[string]any{}}
	if len(args.Headers) > 0 && string(args.Headers) != "null" {
		h, err := decodeOrdered(args.Headers)
		if err == nil {
			if m, ok := h.(*orderedMap); ok {
				headers = m
			}
		}
	}
	setHeader := func(name string, value any) {
		if _, ok := headers.vals[name]; !ok {
			headers.keys = append(headers.keys, name)
		}
		headers.vals[name] = value
	}
	hasContentType := func() bool {
		for _, k := range headers.keys {
			if strings.ToLower(k) == "content-type" {
				return true
			}
		}
		return false
	}

	// The body, as the task gave it.
	var body any
	if len(args.Body) > 0 {
		v, err := decodeOrdered(args.Body)
		if err != nil {
			ansible.Fail("failed to parse body: "+err.Error(), nil)
		}
		body = v
	}

	switch bodyFormat {
	case "json":
		if _, isStr := body.(string); !isStr {
			body = pyJSONDumps(body)
		}
		if !hasContentType() {
			setHeader("Content-Type", "application/json")
		}
	case "form-urlencoded":
		if _, isStr := body.(string); !isStr {
			encoded, err := formURLEncoded(body)
			if err != nil {
				pe := err.(*pyError)
				if pe.kind != "ValueError" {
					ansible.Crash(pe.msg, "")
				}
				ansible.Fail("failed to parse body as form_urlencoded: "+pe.msg, ansible.Result{"elapsed": 0})
			}
			body = encoded
		}
		if !hasContentType() {
			setHeader("Content-Type", "application/x-www-form-urlencoded")
		}
	case "form-multipart":
		contentType, data, err := prepareMultipart(body)
		if err != nil {
			pe := err.(*pyError)
			if pe.kind == "OSError" {
				ansible.Crash(pe.msg, "")
			}
			ansible.Fail("failed to parse body as form-multipart: "+pe.msg, nil)
		}
		body = data
		setHeader("Content-Type", contentType)
	}

	if creates != nil && exists(*creates) {
		ansible.Exit(ansible.Result{"stdout": fmt.Sprintf("skipped, since '%s' exists", *creates), "changed": false})
	}
	if removes != nil && !exists(*removes) {
		ansible.Exit(ansible.Result{"stdout": fmt.Sprintf("skipped, since '%s' does not exist", *removes), "changed": false})
	}

	// uri(): the request data
	var data []byte
	hasData := false
	if src := pathOpt(args.Src); src != nil && *src != "" {
		content, err := os.ReadFile(*src)
		if err != nil {
			ansible.Fail("Unable to open source file "+*src, ansible.Result{"elapsed": 0})
		}
		fi, _ := os.Stat(*src)
		setHeader("Content-Length", json.Number(fmt.Sprint(fi.Size())))
		data, hasData = content, true
	} else {
		switch b := body.(type) {
		case nil:
		case string:
			data, hasData = []byte(b), true
		case []byte:
			data, hasData = b, true
		case *orderedMap, []any:
			ansible.Fail(fmt.Sprintf("Content-Length should be specified for iterable data of type <class '%s'> %s", pyTypeName(b), pyRepr(b)),
				ansible.Result{"url": maskedURL, "status": -1})
		default:
			ansible.Crash(fmt.Sprintf("object of type '%s' has no len()", pyTypeName(b)), "")
		}
	}

	var lastMod string
	if dest != nil {
		if fi, err := os.Stat(*dest); err == nil && fi.Mode().IsRegular() {
			lastMod = fi.ModTime().UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT"
		}
	}

	if args.FollowRedirects == "no" || args.FollowRedirects == "yes" {
		// Same as the builtin: the option values go away with ansible-core 2.22.
		ansible.Deprecate("Using 'yes' or 'no' for 'follow_redirects' parameter is deprecated.", "2.22", "ansible.builtin", "ansible.builtin.uri")
	}

	start := time.Now()
	resp, info := fetchURL(rawURL, method, data, hasData, headers, timeout, lastMod)
	elapsed := int(time.Since(start).Seconds()) % 86400

	if resp != nil && dest != nil {
		if fi, err := os.Stat(*dest); err == nil && fi.IsDir() {
			name := responseFilename(resp)
			if name == "" {
				name = "index.html"
			}
			d := filepath.Join(*dest, name)
			dest = &d
		}
	}

	contentType, subType, charset := "application/octet-stream", "octet-stream", "utf-8"
	if resp != nil && !resp.fpNone {
		contentType, subType, charset = parseContentType(resp.header.Get("Content-Type"))
	}
	maybeJSON := false
	if subType != "" {
		st := strings.ToLower(subType)
		if _, suffix, ok := strings.Cut(st, "+"); ok {
			st = suffix
		}
		maybeJSON = contentType != "" && jsonCandidates[st]
	}
	status := info["status"].(int)
	inStatus := func(code int) bool {
		for _, c := range statusCode {
			if c == code {
				return true
			}
		}
		return false
	}
	maybeOutput := maybeJSON || args.ReturnContent || !inStatus(status)

	// The content, as bytes; nil when the module would keep the response
	// unread (to stream into dest).
	var content []byte
	contentRead := false
	if maybeOutput {
		if resp != nil && !resp.isError {
			b, err := resp.decodedBody()
			if err != nil {
				ansible.Crash(err.Error(), "")
			}
			content = b
		} else {
			if b, ok := info["body"].([]byte); ok {
				content = b
			} else {
				content = []byte{}
			}
			delete(info, "body")
		}
		contentRead = true
	}

	r := ansible.Result{}
	r["redirected"] = info["url"] != maskedURL
	for k, v := range info {
		r[k] = v
	}
	r["elapsed"] = elapsed
	r["status"] = status
	r["changed"] = false
	if b, ok := r["body"].([]byte); ok {
		r["body"] = string(b)
	}

	if resp != nil && dest != nil {
		if inStatus(status) && status != 304 {
			if resp.isError && !contentRead {
				failf(r, "Failed to create temporary content file: I/O operation on closed file.")
			}
			payload := content
			if !contentRead {
				b, err := resp.decodedBody()
				if err != nil {
					ansible.Crash(err.Error(), "")
				}
				payload = b
			}
			writeFile(*dest, payload, r)
			r["changed"] = true
			r["changed"] = args.FileArgs.SetFSAttributesIfDifferent(*dest, true, nil)
		}
		r["path"] = *dest
	}

	// Header names as result keys: lowercase, '-' becomes '_'.
	u := ansible.Result{}
	for k, v := range r {
		u[strings.ToLower(strings.ReplaceAll(k, "-", "_"))] = v
	}
	if loc, ok := u["location"].(string); ok {
		u["location"] = urljoin(maskedURL, loc)
	}

	var text *string
	if contentRead {
		t, err := codecs.DecodeLossy(content, charset)
		if err != nil {
			t = strings.ToValidUTF8(string(content), "�")
		}
		text = &t
		if maybeJSON {
			var js any
			dec := json.NewDecoder(strings.NewReader(t))
			dec.UseNumber()
			if err := dec.Decode(&js); err == nil && !dec.More() {
				u["json"] = js
			}
		}
	}

	if !inStatus(status) {
		msg, _ := u["msg"].(string)
		u["msg"] = fmt.Sprintf("Status code was %d and not %s: %s", status, pyIntList(statusCode), msg)
		if args.ReturnContent {
			u["content"] = textOrNil(text)
		}
		ansible.Fail(u["msg"].(string), u)
	}
	if args.ReturnContent {
		u["content"] = textOrNil(text)
	}
	ansible.Exit(u)
}

func textOrNil(t *string) any {
	if t == nil {
		return nil
	}
	return *t
}

func pyIntList(list []int) string {
	parts := make([]string, len(list))
	for i, n := range list {
		parts[i] = fmt.Sprint(n)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// failf is format_message + fail_json: the error, then the response's msg.
func failf(r ansible.Result, err string) {
	msg, _ := r["msg"].(string)
	delete(r, "msg")
	if msg != "" {
		err += " " + msg
	}
	ansible.Fail(err, r)
}

// writeFile writes content to dest only when it changed, like the module's write_file.
func writeFile(dest string, content []byte, r ansible.Result) {
	tmp, err := os.CreateTemp(ansible.Tmpdir(), "tmp")
	if err != nil {
		failf(r, "Failed to create temporary content file: "+err.Error())
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		failf(r, "Failed to create temporary content file: "+err.Error())
	}
	tmp.Close()

	if sha1File(tmpName) != sha1File(dest) {
		if err := files.AtomicMove(tmpName, dest, args.FileArgs.UnsafeWritesEnabled()); err != nil {
			msg := err.Error()
			if ce, ok := err.(*ansible.CausedError); ok {
				msg = ce.Msg
			}
			failf(r, fmt.Sprintf("failed to copy %s to %s: %s", tmpName, dest, msg))
		}
	}
}

func sha1File(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// maskURL is module_utils.urls.mask_url: credentials in the URL become ****.
func maskURL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	netloc := rest[:end]
	at := strings.LastIndex(netloc, "@")
	if at < 0 {
		return raw
	}
	userinfo := netloc[:at]
	user, password, _ := strings.Cut(userinfo, ":")
	switch {
	case password != "":
		netloc = strings.Replace(netloc, user+":"+password+"@", "****:****@", 1)
	case user != "":
		netloc = strings.Replace(netloc, user+"@", "****@", 1)
	default:
		return raw
	}
	return scheme + "://" + netloc + rest[end:]
}

// urljoin is urllib.parse.urljoin for the Location header. Credentials in
// the base URL (masked as ****) are kept as written: Go would escape them.
func urljoin(base, ref string) string {
	userinfo := ""
	if scheme, rest, ok := strings.Cut(base, "://"); ok {
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
			userinfo = rest[:at+1]
			base = scheme + "://" + rest[at+1:]
		}
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	out := b.ResolveReference(r)
	if userinfo != "" && r.Host == "" && r.Scheme == "" {
		scheme, rest, _ := strings.Cut(out.String(), "://")
		return scheme + "://" + userinfo + rest
	}
	return out.String()
}

// parseContentType is module_utils.urls.parse_content_type: email's
// get_content_type (text/plain when missing or malformed) and the charset.
func parseContentType(value string) (contentType, subType, charset string) {
	mediaType, params, _ := strings.Cut(value, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.Count(mediaType, "/") != 1 {
		mediaType = "text/plain"
	}
	contentType = strings.Split(mediaType, ",")[0]
	_, subType, _ = strings.Cut(contentType, "/")
	charset = "utf-8"
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "charset") {
			if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
				charset = strings.Split(v, ",")[0]
			}
		}
	}
	return contentType, subType, charset
}

var dispositionFilename = regexp.MustCompile(`(?i)(?:^|;)\s*filename\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^;]+))`)

// responseFilename is module_utils.urls.get_response_filename.
func responseFilename(r *response) string {
	if m := dispositionFilename.FindStringSubmatch(r.header.Get("Content-Disposition")); m != nil {
		name := m[1]
		if name == "" {
			name = strings.TrimSpace(m[2])
		} else {
			name = regexp.MustCompile(`\\(.)`).ReplaceAllString(name, "$1")
		}
		if name != "" {
			return path.Base(name)
		}
	}
	u, err := url.Parse(r.url)
	if err != nil {
		return ""
	}
	name := path.Base(strings.TrimRight(u.EscapedPath(), "/"))
	if name == "." || name == "/" {
		return ""
	}
	if un, err := url.PathUnescape(name); err == nil {
		name = un
	}
	return name
}
