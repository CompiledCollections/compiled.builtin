package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// This is an HTTP client that behaves like the urllib opener
// module_utils.urls builds: the same headers on the wire, the same
// redirect, auth and cookie handling, and the same errors.

// options are the request settings fetch_url takes.
type options struct {
	username, password   string
	hasUsername          bool
	forceBasicAuth       bool
	useNetrc             bool
	useProxy             bool
	followRedirects      string
	unixSocket           string
	timeout              time.Duration
	tls                  *tls.Config
	decompress           bool
	cookies              *cookieJar
	digestRetried        int
	originalAuthorityURL *url.URL
}

type header struct{ name, value string }

// headerMap is a urllib Request header dict: keys capitalize()d, so
// "content-type" and "Content-Type" are the same header.
type headerMap struct {
	keys []string
	vals map[string]string
}

func newHeaderMap() *headerMap { return &headerMap{vals: map[string]string{}} }

func capitalize(name string) string {
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + strings.ToLower(name[1:])
}

func (h *headerMap) set(name, value string) {
	k := capitalize(name)
	if _, ok := h.vals[k]; !ok {
		h.keys = append(h.keys, k)
	}
	h.vals[k] = value
}

func (h *headerMap) get(name string) (string, bool) {
	v, ok := h.vals[capitalize(name)]
	return v, ok
}

func (h *headerMap) del(name string) {
	k := capitalize(name)
	if _, ok := h.vals[k]; !ok {
		return
	}
	delete(h.vals, k)
	for i, key := range h.keys {
		if key == k {
			h.keys = append(h.keys[:i], h.keys[i+1:]...)
			break
		}
	}
}

func (h *headerMap) clone() *headerMap {
	c := newHeaderMap()
	for _, k := range h.keys {
		c.set(k, h.vals[k])
	}
	return c
}

// pyRequest is a urllib.request.Request.
type pyRequest struct {
	method       string
	url          *url.URL
	data         []byte
	hasData      bool
	headers      *headerMap // sent again on redirects
	unredirected *headerMap // not sent on redirects
	redirectDict map[string]int
}

var disallowedURLChar = regexp.MustCompile("[\x00-\x20\x7f]")

// unknownError is reported by fetch_url as "An unknown error occurred".
type unknownError struct{ msg string }

func (e *unknownError) Error() string { return e.msg }

func (r *pyRequest) fullURL() string { return r.url.String() }

// selector is the request target: path and query.
func (r *pyRequest) selector() string {
	s := r.url.EscapedPath()
	if r.url.RawPath == "" && strings.Contains(r.url.Path, " ") {
		s = r.url.Path
	}
	if s == "" {
		s = "/"
	}
	if r.url.RawQuery != "" || r.url.ForceQuery {
		s += "?" + r.url.RawQuery
	}
	return s
}

func (r *pyRequest) hasHeader(name string) bool {
	_, a := r.headers.get(name)
	_, b := r.unredirected.get(name)
	return a || b
}

// response is a final response: success, or the response of an HTTPError.
type response struct {
	code    int
	reason  string
	header  http.Header
	order   []header // header lines as received
	body    []byte
	url     string // final URL (geturl)
	fpNone  bool   // HTTPError raised without a response body (auth failures)
	gzipped bool
	isError bool // the response of an HTTPError
}

// httpError is urllib.error.HTTPError.
type httpError struct {
	resp *response
	msg  string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP Error %d: %s", e.resp.code, e.msg) }

// urlError is urllib.error.URLError: a failure before a response arrived.
type urlError struct{ reason string }

func (e *urlError) Error() string { return "<urlopen error " + e.reason + ">" }

// connFailure is an OSError raised while waiting for the response, which
// fetch_url reports as "Connection failure: ...".
type connFailure struct{ reason string }

func (e *connFailure) Error() string { return e.reason }

// valueError is a ValueError from urllib, reported as the message alone.
type valueError struct{ msg string }

func (e *valueError) Error() string { return e.msg }

// open sends a request through the urllib handler chain.
func (o *options) open(req *pyRequest) (*response, error) {
	if req.url.Scheme != "http" && req.url.Scheme != "https" {
		return nil, &urlError{"unknown url type: " + req.url.Scheme}
	}

	// HTTPCookieProcessor.http_request
	if !req.hasHeader("Cookie") {
		if c := o.cookies.header(req.url); c != "" {
			req.unredirected.set("Cookie", c)
		}
	}
	// AbstractHTTPHandler.do_request_
	if req.hasData {
		if !req.hasHeader("Content-type") {
			req.unredirected.set("Content-type", "application/x-www-form-urlencoded")
		}
		if !req.hasHeader("Content-length") {
			req.unredirected.set("Content-length", fmt.Sprint(len(req.data)))
		}
	}

	resp, err := o.roundTrip(req)
	if err != nil {
		return nil, err
	}
	o.cookies.extract(req.url, resp.header)

	if resp.code >= 200 && resp.code < 300 {
		if o.decompress && strings.EqualFold(resp.header.Get("Content-Encoding"), "gzip") {
			resp.gzipped = true
		}
		return resp, nil
	}

	switch resp.code {
	case 401:
		if r, err, handled := o.handleAuth(req, resp); handled {
			return r, err
		}
	case 301, 302, 303, 307, 308:
		if r, err, handled := o.handleRedirect(req, resp); handled {
			return r, err
		}
	}
	return nil, &httpError{resp, resp.reason}
}

var schemeRealm = regexp.MustCompile(`(?:^|,)[ \t]*([^ \t,]+)[ \t]+realm=(["']?)([^"']*)(["']?)`)

// handleAuth is HTTPDigestAuthHandler then HTTPBasicAuthHandler on a 401.
func (o *options) handleAuth(req *pyRequest, resp *response) (*response, error, bool) {
	if !o.hasUsername || o.forceBasicAuth || !o.authApplies(req.url) {
		return nil, nil, false
	}
	// Digest first: it only reads the first WWW-Authenticate header.
	if challenge := resp.header.Get("WWW-Authenticate"); challenge != "" {
		scheme, rest, _ := strings.Cut(challenge, " ")
		switch strings.ToLower(scheme) {
		case "digest":
			o.digestRetried++
			if o.digestRetried > 5 {
				return nil, &httpError{&response{code: 401, reason: resp.reason, header: resp.header, order: resp.order, url: req.fullURL(), fpNone: true}, "digest auth failed"}, true
			}
			auth, err := o.digestAuthorization(req, rest)
			if err != nil {
				return nil, err, true
			}
			if auth != "" {
				value := "Digest " + auth
				if v, _ := req.headers.get("Authorization"); v == value {
					break
				}
				req.unredirected.set("Authorization", value)
				r, err := o.open(req)
				o.digestRetried = 0
				return r, err, true
			}
		case "basic":
		default:
			return nil, &valueError{fmt.Sprintf("AbstractDigestAuthHandler does not support the following scheme: '%s'", scheme)}, true
		}
	}
	// Basic: any WWW-Authenticate header with a Basic realm.
	unsupported := ""
	for _, challenge := range resp.header.Values("WWW-Authenticate") {
		for _, m := range schemeRealm.FindAllStringSubmatch(challenge, -1) {
			if !strings.EqualFold(m[1], "basic") {
				unsupported = m[1]
				continue
			}
			value := basicAuth(o.username, o.password)
			if v, ok := req.unredirected.get("Authorization"); ok && v == value {
				return nil, nil, false
			}
			req.unredirected.set("Authorization", value)
			r, err := o.open(req)
			return r, err, true
		}
	}
	if unsupported != "" {
		return nil, &valueError{fmt.Sprintf("AbstractBasicAuthHandler does not support the following scheme: '%s'", unsupported)}, true
	}
	return nil, nil, false
}

// authApplies is the password manager lookup: credentials were registered
// for the original URL's host and port.
func (o *options) authApplies(u *url.URL) bool {
	return sameAuthority(o.originalAuthorityURL, u)
}

func sameAuthority(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	pa, pb := a.Port(), b.Port()
	if pa == "" || pb == "" {
		return true // a netloc without a port matches any port, as reduce_uri does
	}
	return pa == pb
}

const infMsg = "The HTTP server returned a redirect error that would lead to an infinite loop.\nThe last 30x error message was:\n"

// handleRedirect is HTTPRedirectHandler.http_error_302 with the module's
// redirect_request for follow_redirects.
func (o *options) handleRedirect(req *pyRequest, resp *response) (*response, error, bool) {
	location := resp.header.Get("Location")
	if location == "" {
		location = resp.header.Get("URI")
	}
	if _, ok := resp.header["Location"]; !ok {
		if _, ok := resp.header["Uri"]; !ok {
			return nil, nil, false
		}
	}
	newURL, err := resolveRedirect(req.url, location)
	if err != nil {
		return nil, &valueError{err.Error()}, true
	}
	if s := newURL.Scheme; s != "http" && s != "https" && s != "ftp" && s != "" {
		return nil, &httpError{resp, fmt.Sprintf("%s - Redirection to url '%s' is not allowed", resp.reason, newURL)}, true
	}

	next, err := o.redirectRequest(req, resp, newURL)
	if err != nil || next == nil {
		return nil, err, true
	}

	key := newURL.String()
	if req.redirectDict != nil {
		next.redirectDict = req.redirectDict
		if next.redirectDict[key] >= 4 || len(next.redirectDict) >= 10 {
			return nil, &httpError{resp, infMsg + resp.reason}, true
		}
	} else {
		next.redirectDict = map[string]int{}
		req.redirectDict = next.redirectDict
	}
	next.redirectDict[key]++
	r, err := o.open(next)
	return r, err, true
}

func (o *options) redirectRequest(req *pyRequest, resp *response, newURL *url.URL) (*pyRequest, error) {
	method := req.method
	code := resp.code
	fail := &httpError{resp, resp.reason}

	switch o.followRedirects {
	case "urllib2", "urllib":
		if !((code == 301 || code == 302 || code == 303 || code == 307 || code == 308) && (method == "GET" || method == "HEAD") ||
			(code == 301 || code == 302 || code == 303) && method == "POST") {
			return nil, fail
		}
		headers := req.headers.clone()
		headers.del("Content-length")
		headers.del("Content-type")
		next := &pyRequest{url: spaceTo20(newURL), headers: headers, unredirected: newHeaderMap(), method: "GET"}
		if method == "HEAD" {
			next.method = "HEAD"
		}
		return next, nil
	case "no", "none":
		return nil, fail
	case "all", "yes":
	case "safe":
		if method != "GET" && method != "HEAD" {
			return nil, fail
		}
	default:
		return nil, fail
	}

	next := &pyRequest{url: spaceTo20(newURL), unredirected: newHeaderMap(), method: method}
	if code == 307 || code == 308 {
		next.headers = req.headers.clone()
		next.data, next.hasData = req.data, req.hasData
	} else {
		next.headers = req.headers.clone()
		next.headers.del("Content-length")
		next.headers.del("Content-type")
		next.headers.del("Transfer-encoding")
		if code == 303 && method != "HEAD" || code == 302 && method != "HEAD" || code == 301 && method == "POST" {
			next.method = "GET"
		}
	}
	return next, nil
}

func spaceTo20(u *url.URL) *url.URL {
	if s := u.String(); strings.Contains(s, " ") {
		if p, err := url.Parse(strings.ReplaceAll(s, " ", "%20")); err == nil {
			return p
		}
	}
	return u
}

// resolveRedirect is http_error_302's handling of the Location value.
func resolveRedirect(base *url.URL, location string) (*url.URL, error) {
	loc, err := url.Parse(quoteRedirect(location))
	if err != nil {
		return nil, err
	}
	if loc.Path == "" && loc.Host != "" {
		loc.Path = "/"
	}
	return base.ResolveReference(loc), nil
}

// quoteRedirect is quote(newurl, encoding="iso-8859-1", safe=string.punctuation).
func quoteRedirect(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80 && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r)):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, "%%%02X", r)
		default:
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
	}
	return b.String()
}

// roundTrip sends one request, as http.client does.
func (o *options) roundTrip(req *pyRequest) (*response, error) {
	// do_open: unredirected headers win over regular ones.
	sent := req.unredirected.clone()
	for _, k := range req.headers.keys {
		if _, ok := sent.get(k); !ok {
			sent.set(k, req.headers.vals[k])
		}
	}
	sent.set("Connection", "close")

	// http.client refuses control characters and spaces in the request target.
	if loc := disallowedURLChar.FindStringIndex(req.selector()); loc != nil {
		return nil, &unknownError{fmt.Sprintf("URL can't contain control characters. %s (found at least %s)",
			pyStrRepr(req.selector()), pyStrRepr(req.selector()[loc[0]:loc[1]]))}
	}

	var body io.Reader
	if req.hasData {
		body = bytes.NewReader(req.data)
	}
	hreq, err := http.NewRequest(req.method, req.url.String(), body)
	if err != nil {
		return nil, &valueError{err.Error()}
	}
	hreq.Header = http.Header{}
	for _, k := range sent.keys {
		v := sent.vals[k]
		switch strings.ToLower(k) {
		case "host":
			hreq.Host = v
		case "content-length":
			if n, err := fmt.Sscan(v, &hreq.ContentLength); n != 1 || err != nil {
				hreq.Header.Set(titleCase(k), v)
			}
		default:
			hreq.Header.Set(titleCase(k), v)
		}
	}
	if hreq.Header.Get("Accept-Encoding") == "" {
		hreq.Header.Set("Accept-Encoding", "identity")
	}
	if !req.hasData && hreq.ContentLength == 0 {
		hreq.Body = nil
	}
	hreq.Close = true

	wrote := false
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote = true }}
	hreq = hreq.WithContext(httptrace.WithClientTrace(context.Background(), trace))

	hresp, err := o.transport(req.url).RoundTrip(hreq)
	if err != nil {
		return nil, o.mapError(err, wrote)
	}
	defer hresp.Body.Close()

	data, err := io.ReadAll(hresp.Body)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, &connFailure{osReason(err)}
	}
	reason := strings.TrimSpace(strings.TrimPrefix(hresp.Status, fmt.Sprint(hresp.StatusCode)))
	if len(hresp.TransferEncoding) > 0 {
		hresp.Header.Set("Transfer-Encoding", strings.Join(hresp.TransferEncoding, ", "))
	}
	resp := &response{code: hresp.StatusCode, reason: reason, header: hresp.Header, body: data, url: req.fullURL()}
	resp.order = headerOrder(hresp)
	return resp, nil
}

// headerOrder lists the response headers in a stable order: Go doesn't
// keep the wire order across names, so sort by name, keeping the order of
// repeated values.
func headerOrder(r *http.Response) []header {
	var out []header
	for _, k := range sortedKeys(r.Header) {
		for _, v := range r.Header[k] {
			out = append(out, header{k, v})
		}
	}
	return out
}

func titleCase(name string) string { return http.CanonicalHeaderKey(name) }

// transport is the connection setup for one request.
func (o *options) transport(u *url.URL) *http.Transport {
	dialer := &net.Dialer{Timeout: o.timeout}
	t := &http.Transport{
		DisableKeepAlives:  true,
		DisableCompression: true,
		TLSClientConfig:    o.tls,
		ForceAttemptHTTP2:  false,
		TLSNextProto:       map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if o.unixSocket != "" {
			if len(o.unixSocket) > 108 { // sizeof(sun_path), checked by Python before connecting
				return nil, &unixSocketError{o.unixSocket, errors.New("AF_UNIX path too long")}
			}
			c, err := dialer.DialContext(ctx, "unix", o.unixSocket)
			if err != nil {
				return nil, &unixSocketError{o.unixSocket, err}
			}
			return &timeoutConn{c, o.timeout}, nil
		}
		c, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &timeoutConn{c, o.timeout}, nil
	}
	t.DialContext = dial
	if o.useProxy && o.unixSocket == "" {
		t.Proxy = pyProxy
	}
	return t
}

type unixSocketError struct {
	path string
	err  error
}

func (e *unixSocketError) Error() string {
	return fmt.Sprintf("Invalid Socket File (%s): %s", e.path, osReason(e.err))
}

// timeoutConn applies the timeout to every read and write, like a Python
// socket timeout, rather than to the whole request.
type timeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *timeoutConn) Read(b []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(b)
}

func (c *timeoutConn) Write(b []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(b)
}

// pyProxy is urllib's proxy selection: <scheme>_proxy from the
// environment (lowercase wins) and no_proxy. Unlike Go's, it proxies
// localhost too.
func pyProxy(r *http.Request) (*url.URL, error) {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		lk := strings.ToLower(k)
		if v != "" && strings.HasSuffix(lk, "_proxy") {
			if _, cgi := os.LookupEnv("REQUEST_METHOD"); cgi && k == "HTTP_PROXY" {
				continue
			}
			if _, seen := env[lk]; !seen || k == lk {
				env[lk] = v
			}
		}
	}
	proxy := env[r.URL.Scheme+"_proxy"]
	if proxy == "" || bypassProxy(r.URL.Host, env["no_proxy"]) {
		return nil, nil
	}
	if !strings.Contains(proxy, "://") {
		proxy = "http://" + proxy
	}
	return url.Parse(proxy)
}

// bypassProxy is urllib.request.proxy_bypass_environment.
func bypassProxy(host, noProxy string) bool {
	if noProxy == "*" {
		return true
	}
	hostOnly := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = h
	}
	for _, name := range strings.Split(noProxy, ",") {
		name = strings.TrimLeft(strings.TrimSpace(name), ".")
		if name == "" {
			continue
		}
		pattern := regexp.MustCompile(`(?i)^(.+\.)?` + regexp.QuoteMeta(name) + `$`)
		if pattern.MatchString(hostOnly) || pattern.MatchString(host) {
			return true
		}
	}
	return false
}

// decodeBody returns the response body, gunzipped when needed.
func (r *response) decodedBody() ([]byte, error) {
	if !r.gzipped {
		return r.body, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(r.body))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}
