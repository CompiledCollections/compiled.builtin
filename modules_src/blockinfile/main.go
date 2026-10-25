// Blockinfile equivalent module, written in Go. A close port of
// ansible.builtin.blockinfile; insertafter and insertbefore use Python
// regular expression syntax, translated to Go's RE2 engine (see
// internal/ansible/pyre.go).
package blockinfile

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/codecs"
	"compiled/builtin/internal/files"
)

type Args struct {
	files.FileArgs
	Path           string  `json:"path"`
	State          string  `json:"state"`
	Marker         string  `json:"marker"`
	Block          string  `json:"block"`
	InsertAfter    *string `json:"insertafter"`
	InsertBefore   *string `json:"insertbefore"`
	Create         bool    `json:"create"`
	Backup         bool    `json:"backup"`
	Validate       *string `json:"validate"`
	MarkerBegin    string  `json:"marker_begin"`
	MarkerEnd      string  `json:"marker_end"`
	AppendNewline  bool    `json:"append_newline"`
	PrependNewline bool    `json:"prepend_newline"`
	Encoding       string  `json:"encoding"`
}

const lineSeparator = "\n"

var args Args

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func is(s *string, v string) bool { return s != nil && *s == v }

// markerFor is re.sub(r'{mark}', mark, marker): mark is a replacement
// template, so its backslash escapes are processed, and bad ones raise.
var markRe = regexp.MustCompile(`\{mark\}`)

func markerFor(mark string) string {
	if strings.Contains(mark, `\`) {
		expanded, err := ansible.ExpandPython(markRe, mark, "", []int{0, 0})
		if err != nil {
			ansible.Crash(err.Error(), "")
		}
		mark = expanded
	}
	return strings.ReplaceAll(args.Marker, "{mark}", mark) + lineSeparator
}

// at is lines[i] for an index Python would check, failing as IndexError does.
func at(lines []string, i int) string {
	if i < 0 {
		i += len(lines)
	}
	if i < 0 || i >= len(lines) {
		ansible.Crash("list index out of range", "")
	}
	return lines[i]
}

// insert is lines[i:i] = add, with Python's slice clamping.
func insert(lines []string, i int, add ...string) []string {
	i = min(max(i, 0), len(lines))
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:i]...)
	out = append(out, add...)
	return append(out, lines[i:]...)
}

func writeChanges(contents, path string) {
	data, err := codecs.EncodeString(contents, args.Encoding)
	if err != nil {
		ansible.Crash(err.Error(), "")
	}
	tmp, err := os.CreateTemp(ansible.Tmpdir(), "tmp")
	if err != nil {
		ansible.CrashError(err)
	}
	tmpfile := tmp.Name()
	defer os.Remove(tmpfile)
	if _, err := tmp.Write(data); err != nil {
		ansible.CrashError(err)
	}
	tmp.Close()

	if args.Validate != nil && *args.Validate != "" {
		if !strings.Contains(*args.Validate, "%s") {
			ansible.Fail("validate must contain %s: "+*args.Validate, nil)
		}
		cmd, err := ansible.PercentFormat(*args.Validate, tmpfile)
		if err != nil {
			ansible.Crash(err.Error(), "")
		}
		rc, _, stderr := ansible.RunCommand(cmd, ansible.RunOptions{})
		if rc != 0 {
			ansible.Fail(fmt.Sprintf("failed to validate: rc:%d error:%s", rc, stderr), nil)
		}
	}
	if err := files.AtomicMove(tmpfile, path, args.UnsafeWritesEnabled()); err != nil {
		ansible.CrashError(err)
	}
}

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	ansible.ReadArgs(&args)
	path := ansible.ExpandPath(args.Path)

	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		ansible.Fail(fmt.Sprintf("Path %s is a directory !", path), ansible.Result{"rc": 256})
	}

	pathExists := exists(path)
	var original *string
	var lines []string
	if !pathExists {
		if !args.Create {
			ansible.Fail(fmt.Sprintf("Path %s does not exist !", path), ansible.Result{"rc": 257})
		}
		destpath := files.PyDirname(path)
		if destpath != "" && !exists(destpath) && !ansible.CheckMode() {
			if err := os.MkdirAll(destpath, 0o777); err != nil {
				ansible.Fail(fmt.Sprintf("Error creating %s Error code: %d Error description: %s",
					destpath, int(ansible.Errno(err)), ansible.Strerror(err)), nil)
			}
		}
	} else {
		data, err := os.ReadFile(path)
		if err != nil {
			ansible.CrashError(err)
		}
		text, err := codecs.Decode(data, args.Encoding)
		if err != nil {
			ansible.Crash(err.Error(), "")
		}
		original = &text
		lines = ansible.SplitLines(text)
	}

	diff := ansible.Diff{
		"before": "", "after": "",
		"before_header": path + " (content)", "after_header": path + " (content)",
	}
	if ansible.DiffMode() && original != nil && *original != "" {
		diff["before"] = *original
	}

	present := args.State == "present"
	if !present && !pathExists {
		ansible.Exit(ansible.Result{"changed": false, "msg": fmt.Sprintf("File %s not present", path)})
	}

	insertbefore, insertafter := args.InsertBefore, args.InsertAfter
	if insertbefore == nil && insertafter == nil {
		eof := "EOF"
		insertafter = &eof
	}

	var insertre *regexp.Regexp
	var insertPattern string
	switch {
	case insertafter != nil && *insertafter != "EOF":
		insertPattern = *insertafter
	case insertbefore != nil && *insertbefore != "BOF":
		insertPattern = *insertbefore
	}
	if insertafter != nil && *insertafter != "EOF" || insertbefore != nil && *insertbefore != "BOF" {
		var err error
		if insertre, err = ansible.CompilePython(insertPattern); err != nil {
			ansible.Crash(err.Error(), "")
		}
	}

	marker0 := markerFor(args.MarkerBegin)
	marker1 := markerFor(args.MarkerEnd)

	var blocklines []string
	if present && args.Block != "" {
		block := args.Block
		if !strings.HasSuffix(block, lineSeparator) {
			block += lineSeparator
		}
		blocklines = append([]string{marker0}, ansible.SplitLines(block)...)
		blocklines = append(blocklines, marker1)
	}

	n0, n1 := -1, -1
	for i, line := range lines {
		if line == marker0 {
			n0 = i
		}
		if line == marker1 {
			n1 = i
		}
	}

	switch {
	case n0 == -1 || n1 == -1:
		n0 = -1
		switch {
		case insertre != nil:
			if strings.ContainsRune(ansible.PythonGlobalFlags(insertPattern), 'm') {
				if original == nil {
					ansible.Crash("expected string or bytes-like object, got 'NoneType'", "")
				}
				if loc := insertre.FindStringIndex(*original); loc != nil {
					if insertafter != nil && *insertafter != "" {
						n0 = strings.Count((*original)[:loc[1]], "\n")
					} else if insertbefore != nil && *insertbefore != "" {
						n0 = strings.Count((*original)[:loc[0]], "\n")
					}
				}
			} else {
				for i, line := range lines {
					if insertre.MatchString(line) {
						n0 = i
					}
				}
			}
			if n0 == -1 {
				n0 = len(lines)
			} else if insertafter != nil {
				n0++
			}
		case insertbefore != nil:
			n0 = 0 // insertbefore=BOF
		default:
			n0 = len(lines) // insertafter=EOF
		}
	case n0 < n1:
		lines = append(lines[:n0:n0], lines[n1+1:]...)
	default:
		lines = append(lines[:n1:n1], lines[n0+1:]...)
		n0 = n1
	}

	// Make sure the line before the block ends with a line separator.
	if n0 > 0 {
		if prev := at(lines, n0-1); !strings.HasSuffix(prev, lineSeparator) {
			lines[n0-1] = prev + lineSeparator
		}
	}

	// A blank line before the block, unless at the start of the file or
	// after a blank line already.
	if args.PrependNewline && present {
		if n0 != 0 && at(lines, n0-1) != lineSeparator {
			lines = insert(lines, n0, lineSeparator)
			n0++
		}
	}

	lines = insert(lines, n0, blocklines...)

	// A blank line after the block, unless at the end of the file or
	// before a blank line already.
	if args.AppendNewline && present {
		after := n0 + len(blocklines)
		if after < len(lines) && lines[after] != lineSeparator {
			lines = insert(lines, after, lineSeparator)
		}
	}

	result := strings.Join(lines, "")
	if ansible.DiffMode() {
		diff["after"] = result
	}

	var msg string
	changed := true
	switch {
	case original != nil && *original == result:
		msg, changed = "", false
	case original == nil:
		msg = "File created"
	case len(blocklines) == 0:
		msg = "Block removed"
	default:
		msg = "Block inserted"
	}

	var backupFile *string
	if changed && !ansible.CheckMode() {
		if args.Backup && pathExists {
			b, err := files.BackupLocal(path)
			if err != nil {
				ansible.CrashError(err)
			}
			backupFile = &b
		}
		// Always follow symlinks, to change the real file.
		writeChanges(result, files.Realpath(path))
	}

	if ansible.CheckMode() && !pathExists {
		ansible.Exit(ansible.Result{"changed": changed, "msg": msg, "diff": diff})
	}

	attrDiff := ansible.Diff{}
	if args.SetFSAttributesIfDifferent(path, false, attrDiff) {
		if changed {
			msg += " and "
		}
		changed = true
		msg += "ownership, perms or SE linux context changed"
	}
	attrDiff["before_header"] = path + " (file attributes)"
	attrDiff["after_header"] = path + " (file attributes)"

	res := ansible.Result{"changed": changed, "msg": msg, "diff": []ansible.Diff{diff, attrDiff}}
	if backupFile != nil {
		res["backup_file"] = *backupFile
	}
	ansible.Exit(res)
}
