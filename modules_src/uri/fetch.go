package uri

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"compiled/builtin/internal/ansible"
)

// sortedKeys returns a header map's names in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fetchURL is module_utils.urls.fetch_url with Request.open: it returns the
// response (nil on connection failures) and the info dict. For an HTTP
// error status the response is the error's, and info carries its body.
func fetchURL(rawURL, method string, data []byte, hasData bool, headers *orderedMap, timeout int, lastMod string) (*response, map[string]any) {
	info := map[string]any{"url": maskURL(rawURL), "status": -1}

	resp, err := openURL(rawURL, method, data, hasData, headers, timeout, lastMod)
	if err == nil {
		lower := map[string]string{}
		for _, h := range resp.order {
			name := strings.ToLower(h.name)
			if prev, ok := lower[name]; ok {
				lower[name] = prev + ", " + h.value
			} else {
				lower[name] = h.value
			}
		}
		for k, v := range lower {
			info[k] = v
		}
		cookies := map[string]any{}
		var list []string
		for _, c := range jar.sorted() {
			cookies[c.name] = c.value
			list = append(list, c.name+"="+c.value)
		}
		info["cookies_string"] = strings.Join(list, "; ")
		info["cookies"] = cookies
		length := resp.header.Get("Content-Length")
		if length == "" {
			length = "unknown"
		}
		info["msg"] = fmt.Sprintf("OK (%s bytes)", length)
		info["url"] = maskURL(resp.url)
		info["status"] = resp.code
		return resp, info
	}

	var (
		herr *httpError
		uerr *urlError
		cerr *connFailure
		verr *valueError
		nerr *unsupportedError
	)
	switch {
	case errors.As(err, &verr):
		info["msg"] = verr.msg
		ansible.Fail(verr.msg, info)
	case errors.As(err, &nerr):
		ansible.Fail(nerr.msg, info)
	case errors.As(err, &herr):
		r := herr.resp
		r.isError = true
		// e.info().items() into a dict: the last of repeated headers wins.
		for _, h := range r.order {
			info[strings.ToLower(h.name)] = h.value
		}
		body := []byte{}
		if !r.fpNone {
			body = r.body
		}
		info["msg"] = herr.Error()
		info["body"] = body
		info["status"] = r.code
		return r, info
	case errors.As(err, &uerr):
		info["msg"] = "Request failed: " + uerr.Error()
	case errors.As(err, &cerr):
		info["msg"] = "Connection failure: " + cerr.reason
	default:
		info["msg"] = "An unknown error occurred: " + err.Error()
	}
	return nil, info
}

var jar = &cookieJar{}

// moduleName is the module this binary runs as, for messages.
var moduleName = "compiled.builtin.uri"

// openURL is Request.open with _configure_auth and make_context.
func openURL(rawURL, method string, data []byte, hasData bool, userHeaders *orderedMap, timeout int, lastMod string) (*response, error) {
	o := &options{
		username:        str(args.URLUsername),
		password:        str(args.URLPassword),
		forceBasicAuth:  args.ForceBasicAuth,
		useNetrc:        boolOr(args.UseNetrc, true),
		useProxy:        boolOr(args.UseProxy, true),
		followRedirects: args.FollowRedirects,
		timeout:         time.Duration(timeout) * time.Second,
		decompress:      boolOr(args.Decompress, true),
		cookies:         jar,
	}
	if o.followRedirects == "" {
		o.followRedirects = "safe"
	}
	if p := pathOpt(args.UnixSocket); p != nil {
		o.unixSocket = *p
	}
	httpAgent := "ansible-httpget"
	if args.HTTPAgent != nil {
		httpAgent = *args.HTTPAgent
	}

	// The headers to send: the task's, plus those auth adds.
	headers := &orderedMap{vals: map[string]any{}}
	for _, k := range userHeaders.keys {
		headers.keys = append(headers.keys, k)
		headers.vals[k] = userHeaders.vals[k]
	}
	setHeader := func(name, value string) {
		if _, ok := headers.vals[name]; !ok {
			headers.keys = append(headers.keys, name)
		}
		headers.vals[name] = value
	}

	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		return nil, &valueError{"unknown url type: " + pyStrRepr(rawURL)}
	}
	if s := strings.ToLower(u.Scheme); s == "file" || s == "ftp" {
		// urllib reads these itself; only HTTP and HTTPS have a Go equivalent here.
		return nil, &unsupportedError{s + ":// URLs are not supported by " + moduleName + ": only http and https are"}
	}

	// _configure_auth
	if o.username != "" {
		o.hasUsername = true
	} else if u.User != nil {
		o.username = u.User.Username()
		o.password, _ = u.User.Password()
		o.hasUsername = o.username != ""
		u.User = nil
	}
	switch {
	case args.UseGSSAPI:
		return nil, &unsupportedError{"use_gssapi is not supported by " + moduleName + ": GSSAPI/Kerberos authentication needs the Python gssapi library"}
	case o.hasUsername && !o.forceBasicAuth:
		o.originalAuthorityURL = &url.URL{Host: u.Host}
	case o.hasUsername && o.forceBasicAuth:
		setHeader("Authorization", basicAuth(o.username, o.password))
	case o.useNetrc:
		if login, password, ok := netrcLogin(u.Hostname()); ok && login != "" && password != "" {
			setHeader("Authorization", basicAuth(login, password))
		}
	}

	if u.Host == "" {
		return nil, &urlError{"no host given"}
	}

	o.tls, err = buildTLS(boolOr(args.ValidateCerts, true), str(pathOpt(args.CAPath)), args.Ciphers,
		str(pathOpt(args.ClientCert)), str(pathOpt(args.ClientKey)))
	if err != nil {
		return nil, err
	}

	req := &pyRequest{method: method, url: u, data: data, hasData: hasData, headers: newHeaderMap(), unredirected: newHeaderMap()}
	if httpAgent != "" {
		req.headers.set("User-agent", httpAgent)
	}
	if args.Force {
		req.headers.set("cache-control", "no-cache")
	} else if lastMod != "" {
		req.headers.set("If-Modified-Since", lastMod)
	}
	unredirected := map[string]bool{}
	for _, h := range args.UnredirectedHeaders {
		unredirected[strings.ToLower(h)] = true
	}
	for _, k := range headers.keys {
		value := pyStr(headers.vals[k])
		if unredirected[strings.ToLower(k)] {
			req.unredirected.set(k, value)
		} else {
			req.headers.set(k, value)
		}
	}
	return o.open(req)
}
