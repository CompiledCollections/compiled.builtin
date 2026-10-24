// Find equivalent module, written in Go. A close port of
// ansible.builtin.find; regular expressions use Python syntax, translated
// to Go's RE2 engine (see internal/ansible/pyre.go), and the directory walk
// is os.walk's (see internal/files/walk.go).
package find

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/codecs"
	"compiled/builtin/internal/files"
)

type Args struct {
	Paths             []string        `json:"paths"`
	Patterns          []string        `json:"patterns"`
	Excludes          []string        `json:"excludes"`
	Contains          *string         `json:"contains"`
	ReadWholeFile     bool            `json:"read_whole_file"`
	FileType          string          `json:"file_type"`
	Age               *string         `json:"age"`
	AgeStamp          string          `json:"age_stamp"`
	Size              *string         `json:"size"`
	Recurse           bool            `json:"recurse"`
	Hidden            bool            `json:"hidden"`
	Follow            bool            `json:"follow"`
	GetChecksum       bool            `json:"get_checksum"`
	ChecksumAlgorithm string          `json:"checksum_algorithm"`
	UseRegex          bool            `json:"use_regex"`
	Depth             *int            `json:"depth"`
	Mode              json.RawMessage `json:"mode"`
	ExactMode         bool            `json:"exact_mode"`
	Encoding          *string         `json:"encoding"`
	Limit             *int            `json:"limit"`
}

var args Args

// pyError is an exception raised inside the loop over paths, which the
// module turns into a skipped path and a warning.
type pyError struct{ msg string }

func (e *pyError) Error() string { return e.msg }

func raise(format string, a ...any) { panic(&pyError{fmt.Sprintf(format, a...)}) }

var regexps = map[string]*regexp.Regexp{}

// compile is re.compile. Patterns Python can't compile raise; patterns
// using what RE2 can't do fail the task, as they would match differently.
func compile(pattern string) *regexp.Regexp {
	if re, ok := regexps[pattern]; ok {
		return re
	}
	re, err := ansible.CompilePython(pattern)
	var unsupported *ansible.UnsupportedError
	if errors.As(err, &unsupported) {
		ansible.Crash(err.Error(), "")
	} else if err != nil {
		raise("%s", err.Error())
	}
	regexps[pattern] = re
	return re
}

// match is re.match: a match starting at the beginning of s.
func match(re *regexp.Regexp, s string) bool {
	loc := re.FindStringIndex(s)
	return loc != nil && loc[0] == 0
}

func pfilter(f string, patterns, excludes []string) bool {
	if len(patterns) == 0 && len(excludes) == 0 {
		return true
	}
	matches := func(f, p string) bool {
		if args.UseRegex {
			return match(compile(p), f)
		}
		return ansible.Fnmatch(f, p)
	}
	for _, p := range patterns {
		if matches(f, p) {
			for _, e := range excludes {
				if matches(f, e) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func stTime(st *syscall.Stat_t, stamp string) float64 {
	t := statTimes(st)[stamp]
	return float64(t)
}

func agefilter(st *syscall.Stat_t, now float64, age *int64) bool {
	if age == nil {
		return true
	}
	a := float64(*age)
	elapsed := now - stTime(st, args.AgeStamp)
	if *age >= 0 {
		return elapsed >= a
	}
	return elapsed <= -a
}

func sizefilter(st *syscall.Stat_t, size *int64) bool {
	if size == nil {
		return true
	}
	if *size >= 0 {
		return st.Size >= *size
	}
	return st.Size <= -*size
}

const chunkSize = 8192 // io.TextIOWrapper's _CHUNK_SIZE

// universalNewlines is what text mode reads: \r\n and \r become \n.
func universalNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func contentfilter(fsname string, pattern *string) bool {
	if pattern == nil {
		return true
	}
	prog := compile(*pattern)

	encoding := "utf-8" // locale.getpreferredencoding(), UTF-8 under Ansible
	named := args.Encoding != nil
	if named {
		encoding = *args.Encoding
	}
	f, err := os.Open(fsname)
	if err != nil {
		return false // any other error: the file just doesn't match
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return false
	}
	if _, err := codecs.Lookup(encoding); err != nil {
		raise("%s", err.Error())
	}
	text, err := codecs.Decode(data, encoding)
	if err == nil {
		text = universalNewlines(text)
		if args.ReadWholeFile {
			return prog.MatchString(text)
		}
		for _, line := range readlines(text) {
			if match(prog, line) {
				return true
			}
		}
		return false
	}
	// Python decodes a chunk at a time, so lines in the chunks before the
	// one that fails to decode are matched first.
	if !args.ReadWholeFile {
		good := ""
		for end := chunkSize; end < len(data); end += chunkSize {
			t, err := codecs.Decode(trimIncomplete(data[:end], encoding), encoding)
			if err != nil {
				break
			}
			good = t
		}
		for _, line := range readlines(universalNewlines(strings.TrimSuffix(good, "\r"))) {
			if strings.HasSuffix(line, "\n") && match(prog, line) {
				return true
			}
		}
	}
	if !named {
		encoding += " (Python default)"
	}
	raise("Failed to decode the file %s with encoding: %s", ansible.PyRepr(fsname), encoding)
	return false
}

// trimIncomplete drops a UTF-8 sequence cut off at the end of a chunk,
// which an incremental decoder keeps for the next one.
func trimIncomplete(b []byte, encoding string) []byte {
	if c, _ := codecs.Lookup(encoding); c != "utf-8" && c != "utf-8-sig" {
		return b
	}
	for n := 1; n <= 3 && n <= len(b); n++ {
		c := b[len(b)-n]
		if c&0xc0 != 0x80 { // a start byte or ASCII
			if size := seqLen(c); size > n {
				return b[:len(b)-n]
			}
			break
		}
	}
	return b
}

func seqLen(c byte) int {
	switch {
	case c >= 0xf0:
		return 4
	case c >= 0xe0:
		return 3
	case c >= 0xc0:
		return 2
	}
	return 1
}

// readlines splits text after each \n, as iterating over a text file does.
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

// zeroStat is the _Object(st_mode=0) the builtin gives symbolic modes.
type zeroStat struct{}

func (zeroStat) Name() string       { return "" }
func (zeroStat) Size() int64        { return 0 }
func (zeroStat) Mode() os.FileMode  { return 0 }
func (zeroStat) ModTime() time.Time { return time.Time{} }
func (zeroStat) IsDir() bool        { return false }
func (zeroStat) Sys() any           { return &syscall.Stat_t{} }

func modeFilter(st *syscall.Stat_t, mode string) bool {
	if mode == "" {
		return true
	}
	stMode := int64(st.Mode) & 0o7777
	m, ok := files.ParsePythonOctal(mode)
	if !ok {
		var err error
		if m, err = files.SymbolicModeToOctal(zeroStat{}, mode); err != nil {
			raise("%s", err.Error())
		}
	}
	m &= 0o7777
	if args.ExactMode {
		return stMode == m
	}
	return stMode&m != 0
}

func statinfo(st *syscall.Stat_t) map[string]any {
	pwName, _ := ansible.UserName(int(st.Uid))
	grName, _ := ansible.GroupName(int(st.Gid))
	mode := uint32(st.Mode)
	ft := mode & syscall.S_IFMT
	out := map[string]any{
		"mode":  fmt.Sprintf("%04o", mode&0o7777),
		"isdir": ft == syscall.S_IFDIR, "ischr": ft == syscall.S_IFCHR, "isblk": ft == syscall.S_IFBLK,
		"isreg": ft == syscall.S_IFREG, "isfifo": ft == syscall.S_IFIFO, "islnk": ft == syscall.S_IFLNK,
		"issock": ft == syscall.S_IFSOCK,
		"uid":    int(st.Uid), "gid": int(st.Gid), "size": st.Size, "inode": st.Ino, "dev": st.Dev,
		"nlink":   st.Nlink,
		"gr_name": grName, "pw_name": pwName,
		"wusr": mode&0o200 != 0, "rusr": mode&0o400 != 0, "xusr": mode&0o100 != 0,
		"wgrp": mode&0o020 != 0, "rgrp": mode&0o040 != 0, "xgrp": mode&0o010 != 0,
		"woth": mode&0o002 != 0, "roth": mode&0o004 != 0, "xoth": mode&0o001 != 0,
		"isuid": mode&0o4000 != 0, "isgid": mode&0o2000 != 0,
	}
	for k, v := range statTimes(st) {
		out[k] = v
	}
	platformFields(st, out)
	return out
}

// digest is AnsibleModule.digest_from_file.
func digest(path string) any {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var h hash.Hash
	switch args.ChecksumAlgorithm {
	case "md5":
		h = md5.New()
	case "", "sha1":
		h = sha1.New()
	case "sha224":
		h = sha256.New224()
	case "sha256":
		h = sha256.New()
	case "sha384":
		h = sha512.New384()
	case "sha512":
		h = sha512.New()
	}
	f, err := os.Open(files.Realpath(path))
	if err != nil {
		raise("%s", ansible.OSErrorMessage(err, true))
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		raise("%s", ansible.OSErrorMessage(err, true))
	}
	return hex.EncodeToString(h.Sum(nil))
}

var (
	ageRe  = &ansible.LazyRegexp{Expr: `^(\p{Nd}+|-\p{Nd}+)([smhdw])?\n?\z`}
	sizeRe = &ansible.LazyRegexp{Expr: `^(\p{Nd}+|-\p{Nd}+)([bkmgt])?\n?\z`}
)

// parseUnits converts an age or size with an optional unit, as the
// builtin's re.match and int() do (int accepts any Unicode digits).
func parseUnits(re *ansible.LazyRegexp, value string, units map[string]int64) (int64, bool) {
	m := re.Get().FindStringSubmatch(strings.ToLower(value))
	if m == nil {
		return 0, false
	}
	var n int64
	for _, r := range strings.TrimPrefix(m[1], "-") {
		n = n*10 + int64(digitValue(r))
	}
	if strings.HasPrefix(m[1], "-") {
		n = -n
	}
	unit := int64(1)
	if m[2] != "" {
		unit = units[m[2]]
	}
	return n * unit, true
}

// digitValue is the value of a Unicode decimal digit: they come in runs of
// ten, from zero to nine.
func digitValue(r rune) int {
	start := r
	for unicode.IsDigit(start - 1) {
		start--
	}
	return int(r-start) % 10
}

// rawMode returns the mode option as a string, failing as the builtin does
// for other types. Falsy values mean no mode.
func rawMode() string {
	var v any
	if len(args.Mode) == 0 || json.Unmarshal(args.Mode, &v) != nil || v == nil {
		return ""
	}
	switch m := v.(type) {
	case string:
		return m
	case bool:
		if m {
			ansible.Fail("argument 'mode' is not a string and conversion is not allowed, value is of type bool", nil)
		}
	case float64:
		if m != 0 {
			kind := "float"
			if !strings.ContainsAny(string(args.Mode), ".eE") {
				kind = "int"
			}
			ansible.Fail("argument 'mode' is not a string and conversion is not allowed, value is of type "+kind, nil)
		}
	case []any:
		if len(m) > 0 {
			ansible.Fail("argument 'mode' is not a string and conversion is not allowed, value is of type list", nil)
		}
	case map[string]any:
		if len(m) > 0 {
			ansible.Fail("argument 'mode' is not a string and conversion is not allowed, value is of type dict", nil)
		}
	}
	return ""
}

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	ansible.ReadArgs(&args)
	mode := rawMode()

	patterns := args.Patterns
	if len(patterns) == 0 {
		if args.UseRegex {
			patterns = []string{".*"}
		} else {
			patterns = []string{"*"}
		}
	}

	var age, size *int64
	if args.Age != nil {
		a, ok := parseUnits(ageRe, *args.Age, map[string]int64{"s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800})
		if !ok {
			ansible.Fail("failed to process age", ansible.Result{"age": *args.Age})
		}
		age = &a
	}
	if args.Size != nil {
		s, ok := parseUnits(sizeRe, *args.Size, map[string]int64{"b": 1, "k": 1024, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40})
		if !ok {
			ansible.Fail("failed to process size", ansible.Result{"size": *args.Size})
		}
		size = &s
	}
	if args.Limit != nil && *args.Limit <= 0 {
		ansible.Fail(fmt.Sprintf("limit cannot be %d (use None for unlimited)", *args.Limit), nil)
	}

	now := float64(time.Now().UnixNano()) / 1e9
	msg := "All paths examined"
	looked := 0
	hasWarnings := false
	filelist := []map[string]any{}
	skipped := map[string]string{}
	limitReached := func() bool { return args.Limit != nil && len(filelist) == *args.Limit }

	for _, raw := range args.Paths {
		npath := ansible.ExpandPath(raw)
		func() {
			defer func() {
				if r := recover(); r != nil {
					e, ok := r.(*pyError)
					if !ok {
						panic(r)
					}
					skipped[npath] = e.msg
					ansible.Warn(fmt.Sprintf("Skipped '%s' path due to this access issue: %s\n", npath, e.msg))
					hasWarnings = true
				}
			}()
			if fi, err := os.Stat(npath); err != nil || !fi.IsDir() {
				raise("'%s' is not a directory", npath)
			}
			onerror := func(err error) error {
				switch ansible.Errno(err) {
				case syscall.EPERM, syscall.EACCES, syscall.ENOENT:
					var pe *os.PathError
					if errors.As(err, &pe) {
						skipped[pe.Path] = ansible.OSErrorMessage(err, false)
					}
					return nil
				}
				return err
			}
			err := files.WalkTopDown(npath, args.Follow, onerror, func(root string, dirs, nondirs []string) ([]string, bool) {
				looked += len(nondirs) + len(dirs)
				keep := dirs
				for _, fsobj := range append(append([]string{}, nondirs...), dirs...) {
					fsname := ansible.PyNormpath(files.PyJoin(root, fsobj))
					if args.Depth != nil && *args.Depth != 0 {
						wpath := strings.TrimRight(npath, "/") + "/"
						depth := strings.Count(fsname, "/") - strings.Count(wpath, "/") + 1
						if depth > *args.Depth {
							keep = nil
							continue
						}
					}
					if strings.HasPrefix(files.PyBasename(fsname), ".") && !args.Hidden {
						continue
					}
					var st syscall.Stat_t
					if err := syscall.Lstat(fsname, &st); err != nil {
						msg := ansible.OSErrorMessage(&os.PathError{Op: "lstat", Path: fsname, Err: err}, false)
						ansible.Warn(fmt.Sprintf("Skipped entry %s due to access issue: %s", ansible.PyRepr(fsname), msg))
						skipped[fsname] = msg
						hasWarnings = true
						continue
					}
					ft := uint32(st.Mode) & syscall.S_IFMT
					common := func() bool {
						return pfilter(fsobj, patterns, args.Excludes) && agefilter(&st, now, age)
					}
					r := map[string]any{"path": fsname}
					switch {
					case args.FileType == "any":
						if common() && modeFilter(&st, mode) {
							for k, v := range statinfo(&st) {
								r[k] = v
							}
							if ft == syscall.S_IFREG && args.GetChecksum {
								r["checksum"] = digest(fsname)
							}
							if ft != syscall.S_IFREG || sizefilter(&st, size) {
								filelist = append(filelist, r)
							}
						}
					case ft == syscall.S_IFDIR && args.FileType == "directory",
						ft == syscall.S_IFLNK && args.FileType == "link":
						if common() && modeFilter(&st, mode) {
							for k, v := range statinfo(&st) {
								r[k] = v
							}
							filelist = append(filelist, r)
						}
					case ft == syscall.S_IFREG && args.FileType == "file":
						if common() && sizefilter(&st, size) && contentfilter(fsname, args.Contains) && modeFilter(&st, mode) {
							for k, v := range statinfo(&st) {
								r[k] = v
							}
							if args.GetChecksum {
								r["checksum"] = digest(fsname)
							}
							filelist = append(filelist, r)
						}
					}
					if limitReached() {
						msg = "Limit of matches reached"
						break
					}
				}
				return keep, !args.Recurse || limitReached()
			})
			if err != nil {
				raise("%s", ansible.OSErrorMessage(err, false))
			}
		}()
	}

	if hasWarnings {
		msg = "Not all paths examined, check warnings for details"
	}
	ansible.Exit(ansible.Result{
		"files": filelist, "changed": false, "msg": msg, "matched": len(filelist),
		"examined": looked, "skipped_paths": skipped,
	})
}
