package main

// get_url: compiled.builtin.get_url runs this binary with _get_url set, as
// shell runs command's. It is a port of ansible.builtin.get_url, sending its
// requests the way uri does (fetch_url).

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/codecs"
	"compiled/builtin/internal/files"
)

// The algorithms AnsibleModule.digest_from_file knows, in its order.
var hashAlgorithms = []string{"md5", "sha1", "sha224", "sha256", "sha384", "sha512"}

func newHash(algorithm string) hash.Hash {
	switch algorithm {
	case "md5":
		return md5.New()
	case "sha1":
		return sha1.New()
	case "sha224":
		return sha256.New224()
	case "sha256":
		return sha256.New()
	case "sha384":
		return sha512.New384()
	case "sha512":
		return sha512.New()
	}
	return nil
}

// digestFromFile is AnsibleModule.digest_from_file: nil for a missing file.
func digestFromFile(filename, algorithm string) any {
	if _, err := os.Stat(filename); err != nil {
		return nil
	}
	if fi, err := os.Stat(filename); err == nil && fi.IsDir() {
		ansible.Fail("attempted to take checksum of directory: "+filename, nil)
	}
	h := newHash(algorithm)
	if h == nil {
		ansible.Fail(fmt.Sprintf("Could not hash file '%s' with algorithm '%s'. Available algorithms: %s",
			filename, algorithm, strings.Join(hashAlgorithms, ", ")), nil)
	}
	f, err := os.Open(files.Realpath(filename))
	if err != nil {
		ansible.CrashError(err)
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		ansible.CrashError(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// urlFilename is get_url's url_filename: the last part of the URL's path,
// still quoted, or index.html.
func urlFilename(rawURL string) string {
	p := rawURL
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
		if j := strings.IndexAny(p, "/?#"); j >= 0 {
			p = p[j:]
		} else {
			p = ""
		}
	}
	if j := strings.IndexAny(p, "?#"); j >= 0 {
		p = p[:j]
	}
	if fn := files.PyBasename(p); fn != "" {
		return fn
	}
	return "index.html"
}

// urlScheme is urlsplit(url).scheme: lowercase, empty when not a valid scheme.
func urlScheme(s string) string {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return ""
	}
	for k, r := range s[:i] {
		if !(r < unicode.MaxASCII && (unicode.IsLetter(r) || k > 0 && (unicode.IsDigit(r) || strings.ContainsRune("+-.", r)))) {
			return ""
		}
	}
	return strings.ToLower(s[:i])
}

// emailFilenameParam is the filename parameter of a Content-Disposition
// header, as email.message.Message.get_param finds and unquotes it.
func emailFilenameParam(header string) string {
	parts := splitParams(header)
	if len(parts) == 0 {
		return ""
	}
	for _, p := range parts[1:] {
		name, value, ok := strings.Cut(p, "=")
		if !ok || strings.ToLower(strings.TrimSpace(name)) != "filename" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(value[1 : len(value)-1])
		} else if len(value) >= 2 && value[0] == '<' && value[len(value)-1] == '>' {
			value = value[1 : len(value)-1]
		}
		return value
	}
	return ""
}

// splitParams is email.message._parseparam: ;-separated, minding quotes.
func splitParams(s string) []string {
	var out []string
	s = ";" + s
	for strings.HasPrefix(s, ";") {
		s = s[1:]
		end := strings.IndexByte(s, ';')
		for end > 0 && (strings.Count(s[:end], `"`)-strings.Count(s[:end], `\"`))%2 != 0 {
			next := strings.IndexByte(s[end+1:], ';')
			if next < 0 {
				end = -1
				break
			}
			end += 1 + next
		}
		if end < 0 {
			end = len(s)
		}
		f := s[:end]
		if i := strings.IndexByte(f, '='); i >= 0 {
			f = strings.ToLower(strings.TrimSpace(f[:i])) + "=" + strings.TrimSpace(f[i+1:])
		}
		out = append(out, strings.TrimSpace(f))
		s = s[end:]
	}
	return out
}

// parseDigestLines is get_url's parse_digest_lines.
func parseDigestLines(filename string, lines []string) [][2]string {
	if len(lines) == 1 && len(strings.Fields(lines[0])) == 1 {
		return [][2]string{{lines[0], filename}}
	}
	bsd, _ := ansible.CompilePython(`^(\w+) ?\((?P<path>.+)\) ?= (?P<digest>[\w.]+)$`)
	gnu, _ := ansible.CompilePython(`^(?P<digest>[\w.]+)\s+(\*|\.\/|\.)?(?P<path>.+)$`)
	var out [][2]string
	for _, line := range lines {
		if m := bsd.FindStringSubmatch(line); m != nil {
			out = append(out, [2]string{m[bsd.SubexpIndex("digest")], m[bsd.SubexpIndex("path")]})
		} else if m := gnu.FindStringSubmatch(line); m != nil {
			out = append(out, [2]string{m[gnu.SubexpIndex("digest")], strings.TrimLeft(m[gnu.SubexpIndex("path")], "./")})
		}
	}
	return out
}

// isHex is whether int(s, 16) parses: an optional 0x, then hex digits with
// single underscores between them (and any Unicode decimal digit).
func isHex(s string) bool {
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = strings.TrimPrefix(s[2:], "_")
	}
	if s == "" || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
		return false
	}
	for _, r := range s {
		if !(r == '_' || unicode.IsDigit(r) || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

var nonWord, _ = ansible.CompilePython(`\W+`)

// urlGet is get_url's url_get: it downloads url into a temporary file.
func urlGet(rawURL, dest string, lastMod string, method string, tmpDest *string, timeout int, headers *orderedMap) (string, map[string]any) {
	start := time.Now()
	resp, info := fetchURL(rawURL, method, nil, false, headers, timeout, lastMod)
	elapsed := int(time.Since(start).Seconds())

	status, _ := info["status"].(int)
	if status == 304 {
		ansible.Exit(ansible.Result{"url": maskURL(rawURL), "dest": dest, "changed": false,
			"msg": infoString(info, "msg"), "status_code": status, "elapsed": elapsed})
	}
	if status == -1 {
		ansible.Fail(infoString(info, "msg"), ansible.Result{"url": maskURL(rawURL), "dest": dest, "elapsed": elapsed})
	}
	if status != 200 && !strings.HasPrefix(rawURL, "file:/") && !(strings.HasPrefix(rawURL, "ftp:/") && strings.HasPrefix(infoString(info, "msg"), "OK")) {
		ansible.Fail("Request failed", ansible.Result{"status_code": status, "response": info["msg"],
			"url": maskURL(rawURL), "dest": dest, "elapsed": elapsed})
	}

	dir := ""
	if tmpDest != nil && *tmpDest != "" {
		dir = ansible.ExpandPath(*tmpDest)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			if err == nil {
				ansible.Fail(dir+" is a file but should be a directory.", ansible.Result{"elapsed": elapsed})
			}
			ansible.Fail(dir+" directory does not exist.", ansible.Result{"elapsed": elapsed})
		}
	} else {
		dir = ansible.Tmpdir()
	}
	tmp, err := os.CreateTemp(dir, "tmp")
	if err != nil {
		ansible.CrashError(err)
	}
	body, err := resp.decodedBody()
	if err == nil {
		_, err = tmp.Write(body)
	}
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		ansible.Fail("failed to create temporary content file: "+err.Error(), ansible.Result{"elapsed": elapsed})
	}
	return tmp.Name(), info
}

func infoString(info map[string]any, key string) string {
	s, _ := info[key].(string)
	return s
}

func access(p string, mode uint32) bool { return syscall.Access(p, mode) == nil }

func getURL() {
	moduleName = "compiled.builtin.get_url"
	// fetch_url's defaults for options get_url doesn't have.
	args.FollowRedirects = "urllib2"
	args.UnixSocket, args.CAPath = nil, nil

	rawURL := args.URL
	dest := ansible.ExpandPath(str(args.Dest))
	timeout := 10
	if args.Timeout != nil {
		timeout = *args.Timeout
	}
	headers := &orderedMap{vals: map[string]any{}}
	if len(args.Headers) > 0 && string(args.Headers) != "null" {
		if h, err := decodeOrdered(args.Headers); err == nil {
			if m, ok := h.(*orderedMap); ok {
				headers = m
			}
		}
	}

	result := ansible.Result{
		"changed": false, "checksum_dest": nil, "checksum_src": nil,
		"dest": dest, "elapsed": 0, "url": maskURL(rawURL),
	}
	fail := func(msg string) { ansible.Fail(msg, result) }

	fi, err := os.Stat(dest)
	destIsDir := err == nil && fi.IsDir()
	lastMod := ""

	checksum, algorithm := args.Checksum, ""
	if checksum != "" {
		var ok bool
		if algorithm, checksum, ok = strings.Cut(checksum, ":"); !ok {
			fail("The checksum parameter has to be in format <algorithm>:<checksum>")
		}
		switch urlScheme(checksum) {
		case "http", "https", "ftp", "file":
			checksumURL := checksum
			tmpsrc, _ := urlGet(checksumURL, dest, lastMod, "GET", args.TmpDest, timeout, headers)
			data, err := os.ReadFile(tmpsrc)
			os.Remove(tmpsrc)
			if err != nil {
				ansible.CrashError(err)
			}
			text, err := codecs.Decode(data, "utf-8")
			if err != nil {
				ansible.Crash(err.Error(), "")
			}
			text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
			var lines []string
			for _, line := range strings.SplitAfter(text, "\n") {
				if line != "" {
					lines = append(lines, strings.TrimRight(line, "\n"))
				}
			}
			filename := urlFilename(rawURL)
			found := false
			for _, pair := range parseDigestLines(filename, lines) {
				if pair[1] == filename {
					checksum, found = pair[0], true
					break
				}
			}
			if !found {
				ansible.Fail(fmt.Sprintf("Unable to find a checksum for file '%s' in '%s'", filename, maskURL(checksumURL)), nil)
			}
		}
		// Drop everything but word characters, such as a zero-width space.
		checksum = strings.ToLower(nonWord.ReplaceAllString(checksum, ""))
		if !isHex(checksum) {
			fail("The checksum format is invalid")
		}
	}

	if _, err := os.Stat(dest); !destIsDir && err == nil {
		mismatch := false
		if !args.Force && checksum != "" {
			if digestFromFile(dest, algorithm) != checksum {
				mismatch = true
			}
		}
		if !args.Force && checksum != "" && !mismatch {
			result["changed"] = args.FileArgs.SetFSAttributesIfDifferent(dest, false, nil)
			if result["changed"] == true {
				result["msg"] = "file already exists but file attributes changed"
			} else {
				result["msg"] = "file already exists"
			}
			ansible.Exit(result)
		}
		if mfi, err := os.Stat(dest); err == nil {
			lastMod = mfi.ModTime().UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT"
		}
		if mismatch {
			args.Force = true
		}
	}

	start := time.Now()
	method := "GET"
	if ansible.CheckMode() {
		method = "HEAD"
	}
	tmpsrc, info := urlGet(rawURL, dest, lastMod, method, args.TmpDest, timeout, headers)
	result["elapsed"] = int(time.Since(start).Seconds())
	result["src"] = tmpsrc
	remove := func() { os.Remove(tmpsrc) }

	if destIsDir {
		filename := files.PyBasename(emailFilenameParam(headerValue(info, "content-disposition")))
		if filename == "" {
			filename = urlFilename(infoString(info, "url"))
		}
		dest = files.PyJoin(dest, filename)
		result["dest"] = dest
	}

	result["checksum_src"] = digestFromFile(tmpsrc, "sha1")

	if _, err := os.Stat(dest); err == nil {
		if !access(dest, 2) {
			remove()
			fail(fmt.Sprintf("Destination %s is not writable", dest))
		}
		if !access(dest, 4) {
			remove()
			fail(fmt.Sprintf("Destination %s is not readable", dest))
		}
		result["checksum_dest"] = digestFromFile(dest, "sha1")
	} else {
		dir := files.PyDirname(dest)
		if _, err := os.Stat(dir); err != nil {
			remove()
			fail(fmt.Sprintf("Destination %s does not exist", dir))
		}
		if !access(dir, 2) {
			remove()
			fail(fmt.Sprintf("Destination %s is not writable", dir))
		}
	}

	if ansible.CheckMode() {
		remove()
		result["changed"] = result["checksum_src"] != result["checksum_dest"]
		result["msg"] = infoString(info, "msg")
		ansible.Exit(result)
	}

	if checksum != "" {
		if got := digestFromFile(tmpsrc, algorithm); got != checksum {
			remove()
			fail(fmt.Sprintf("The checksum for %s did not match %s; it was %v.", tmpsrc, checksum, got))
		}
	}

	var backupFile string
	if result["checksum_src"] != result["checksum_dest"] {
		if args.Backup {
			if _, err := os.Stat(dest); err == nil {
				b, err := files.BackupLocal(dest)
				if err != nil {
					remove()
					fail(fmt.Sprintf("failed to copy %s to %s: %s", tmpsrc, dest, err))
				}
				backupFile = b
			}
		}
		if err := files.AtomicMove(tmpsrc, dest, args.FileArgs.UnsafeWritesEnabled()); err != nil {
			remove()
			msg := err.Error()
			if ce, ok := err.(*ansible.CausedError); ok {
				msg = ce.Msg
			}
			fail(fmt.Sprintf("failed to copy %s to %s: %s", tmpsrc, dest, msg))
		}
		result["changed"] = true
	} else {
		result["changed"] = false
		remove()
	}

	result["changed"] = args.FileArgs.SetFSAttributesIfDifferent(dest, result["changed"] == true, nil)
	result["md5sum"] = digestFromFile(dest, "md5")
	if backupFile != "" {
		result["backup_file"] = backupFile
	}
	result["msg"] = infoString(info, "msg")
	if s, ok := info["status"]; ok {
		result["status_code"] = s
	} else {
		result["status_code"] = ""
	}
	ansible.Exit(result)
}

func headerValue(info map[string]any, name string) string {
	s, _ := info[name].(string)
	return s
}
