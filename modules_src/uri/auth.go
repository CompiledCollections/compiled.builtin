package main

import (
	"bufio"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func basicAuth(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// digestAuthorization is AbstractDigestAuthHandler.get_authorization for
// the challenge after "Digest ". An empty result means no retry.
func (o *options) digestAuthorization(req *pyRequest, challenge string) (string, error) {
	chal := parseKeqvList(parseHTTPList(challenge))
	realm, nonce, qop := chal["realm"], chal["nonce"], chal["qop"]
	algorithm := chal["algorithm"]
	if algorithm == "" {
		algorithm = "MD5"
	}
	var newHash func() hash.Hash
	switch strings.ToUpper(algorithm) {
	case "MD5":
		newHash = md5.New
	case "SHA":
		newHash = sha1.New
	case "SHA-256":
		newHash = sha256.New
	default:
		return "", nil
	}
	H := func(s string) string {
		h := newHash()
		h.Write([]byte(s))
		return hex.EncodeToString(h.Sum(nil))
	}
	KD := func(s, d string) string { return H(s + ":" + d) }

	a1 := o.username + ":" + realm + ":" + o.password
	a2 := req.method + ":" + req.selector()
	var respdig, ncvalue, cnonce string
	switch {
	case qop == "":
		respdig = KD(H(a1), nonce+":"+H(a2))
	case containsToken(qop, "auth"):
		if nonce == digestLastNonce {
			digestNonceCount++
		} else {
			digestNonceCount = 1
		}
		digestLastNonce = nonce
		ncvalue = fmt.Sprintf("%08x", digestNonceCount)
		cnonce = makeCnonce(nonce)
		respdig = KD(H(a1), strings.Join([]string{nonce, ncvalue, cnonce, "auth", H(a2)}, ":"))
	default:
		return "", &urlError{fmt.Sprintf("qop '%s' is not supported.", qop)}
	}

	base := fmt.Sprintf(`username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		o.username, realm, nonce, req.selector(), respdig)
	if opaque := chal["opaque"]; opaque != "" {
		base += fmt.Sprintf(`, opaque="%s"`, opaque)
	}
	base += fmt.Sprintf(`, algorithm="%s"`, algorithm)
	if qop != "" {
		base += fmt.Sprintf(`, qop=auth, nc=%s, cnonce="%s"`, ncvalue, cnonce)
	}
	return base, nil
}

var (
	digestLastNonce  string
	digestNonceCount int
)

func containsToken(list, token string) bool {
	for _, t := range strings.Split(list, ",") {
		if strings.TrimSpace(t) == token {
			return true
		}
	}
	return false
}

func makeCnonce(nonce string) string {
	b := make([]byte, 8)
	rand.Read(b)
	sum := sha1.Sum([]byte(fmt.Sprintf("%d:%s:%s:%x", digestNonceCount, nonce, time.Now().Format(time.ANSIC), b)))
	return hex.EncodeToString(sum[:])[:16]
}

// parseHTTPList is urllib.request.parse_http_list: split on commas outside quotes.
func parseHTTPList(s string) []string {
	var res []string
	var part strings.Builder
	escape, quote := false, false
	for _, c := range s {
		switch {
		case escape:
			part.WriteRune(c)
			escape = false
		case quote && c == '\\':
			escape = true
		case quote && c == '"':
			quote = false
			part.WriteRune(c)
		case quote:
			part.WriteRune(c)
		case c == ',':
			res = append(res, part.String())
			part.Reset()
		case c == '"':
			quote = true
			part.WriteRune(c)
		default:
			part.WriteRune(c)
		}
	}
	if part.Len() > 0 {
		res = append(res, part.String())
	}
	for i := range res {
		res[i] = strings.TrimSpace(res[i])
	}
	return res
}

// parseKeqvList is urllib.request.parse_keqv_list.
func parseKeqvList(items []string) map[string]string {
	out := map[string]string{}
	for _, item := range items {
		if item == "" {
			continue
		}
		k, v, _ := strings.Cut(item, "=")
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// netrcLogin is netrc.netrc(os.environ.get('NETRC')).authenticators(host):
// the machine's login and password, else the default entry's.
func netrcLogin(host string) (login, password string, ok bool) {
	path := os.Getenv("NETRC")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", false
		}
		path = filepath.Join(home, ".netrc")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	type entry struct{ login, password string }
	machines := map[string]entry{}
	var def *entry
	var tokens []string
	sc := bufio.NewScanner(f)
	inMacro := false
	for sc.Scan() {
		line := sc.Text()
		if inMacro {
			if strings.TrimSpace(line) == "" {
				inMacro = false
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		fields := strings.Fields(line)
		for i := 0; i < len(fields); i++ {
			if fields[i] == "macdef" {
				inMacro = true
				break
			}
			tokens = append(tokens, fields[i])
		}
	}
	for i := 0; i < len(tokens); {
		var name string
		switch tokens[i] {
		case "machine":
			if i+1 >= len(tokens) {
				return "", "", false
			}
			name = tokens[i+1]
			i += 2
		case "default":
			name = ""
			i++
		default:
			i++
			continue
		}
		e := entry{}
		for i < len(tokens) && tokens[i] != "machine" && tokens[i] != "default" {
			if i+1 < len(tokens) {
				switch tokens[i] {
				case "login", "user":
					e.login = tokens[i+1]
				case "password":
					e.password = tokens[i+1]
				}
			}
			i += 2
		}
		if name == "" {
			if def == nil {
				d := e
				def = &d
			}
		} else if _, seen := machines[name]; !seen {
			machines[name] = e
		}
	}
	if e, found := machines[host]; found {
		return e.login, e.password, true
	}
	if def != nil {
		return def.login, def.password, true
	}
	return "", "", false
}

// cookieJar is a small http.cookiejar.CookieJar with its default policy.
type cookieJar struct {
	cookies []*jarCookie
}

type jarCookie struct {
	name, value, domain, path string
	hostOnly, secure          bool
	expires                   time.Time
}

// effectiveHost is cookiejar.eff_request_host: names without a dot get ".local".
func effectiveHost(u *url.URL) string {
	h := strings.ToLower(u.Hostname())
	if !strings.Contains(h, ".") {
		h += ".local"
	}
	return h
}

func (j *cookieJar) extract(u *url.URL, h http.Header) {
	host := effectiveHost(u)
	for _, line := range h.Values("Set-Cookie") {
		parts := strings.Split(line, ";")
		name, value, ok := strings.Cut(strings.TrimSpace(parts[0]), "=")
		if !ok || name == "" {
			continue
		}
		c := &jarCookie{name: strings.TrimSpace(name), value: strings.TrimSpace(value), hostOnly: true, domain: host}
		maxAge, hasMaxAge := 0, false
		for _, attr := range parts[1:] {
			k, v, _ := strings.Cut(strings.TrimSpace(attr), "=")
			v = strings.Trim(strings.TrimSpace(v), `"`)
			switch strings.ToLower(k) {
			case "domain":
				if v != "" {
					if !strings.HasPrefix(v, ".") {
						v = "." + v
					}
					c.domain, c.hostOnly = strings.ToLower(v), false
				}
			case "path":
				c.path = v
			case "secure":
				c.secure = true
			case "max-age":
				if n, err := strconv.Atoi(v); err == nil {
					maxAge, hasMaxAge = n, true
				}
			case "expires":
				if t, err := http.ParseTime(v); err == nil {
					c.expires = t
				} else if t, err := time.Parse("Mon, 02-Jan-2006 15:04:05 MST", v); err == nil {
					c.expires = t
				}
			}
		}
		if hasMaxAge {
			c.expires = time.Now().Add(time.Duration(maxAge) * time.Second)
		}
		if c.path == "" {
			c.path = defaultCookiePath(u)
		}
		if !c.hostOnly && !domainMatch(host, c.domain) {
			continue // the policy rejects cookies for other domains
		}
		j.remove(c)
		if c.expires.IsZero() || c.expires.After(time.Now()) {
			j.cookies = append(j.cookies, c)
		}
	}
}

func defaultCookiePath(u *url.URL) string {
	p := u.Path
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

func domainMatch(host, domain string) bool {
	d := strings.TrimPrefix(domain, ".")
	return host == d || strings.HasSuffix(host, "."+d)
}

func (j *cookieJar) remove(c *jarCookie) {
	for i, o := range j.cookies {
		if o.name == c.name && o.domain == c.domain && o.path == c.path {
			j.cookies = append(j.cookies[:i], j.cookies[i+1:]...)
			return
		}
	}
}

// header is the Cookie header for a request: matching cookies, longest path first.
func (j *cookieJar) header(u *url.URL) string {
	host := effectiveHost(u)
	var match []*jarCookie
	for _, c := range j.cookies {
		if c.secure && u.Scheme != "https" {
			continue
		}
		if c.hostOnly && c.domain != host || !c.hostOnly && !domainMatch(host, c.domain) {
			continue
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, c.path) {
			continue
		}
		match = append(match, c)
	}
	sort.SliceStable(match, func(a, b int) bool { return len(match[a].path) > len(match[b].path) })
	parts := make([]string, len(match))
	for i, c := range match {
		parts[i] = c.name + "=" + c.value
	}
	return strings.Join(parts, "; ")
}

// sorted is the jar's iteration order: by domain, path, then name.
func (j *cookieJar) sorted() []*jarCookie {
	out := append([]*jarCookie(nil), j.cookies...)
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.domain != y.domain {
			return x.domain < y.domain
		}
		if x.path != y.path {
			return x.path < y.path
		}
		return x.name < y.name
	})
	return out
}
