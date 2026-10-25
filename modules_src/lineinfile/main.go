// Lineinfile equivalent module, written in Go. A close port of
// ansible.builtin.lineinfile; regular expressions use Python syntax,
// translated to Go's RE2 engine (see internal/ansible/pyre.go).
package lineinfile

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/files"
)

type Args struct {
	files.FileArgs
	Path         string  `json:"path"`
	State        string  `json:"state"`
	Regexp       *string `json:"regexp"`
	SearchString *string `json:"search_string"`
	Line         *string `json:"line"`
	Encoding     string  `json:"encoding"`
	InsertAfter  *string `json:"insertafter"`
	InsertBefore *string `json:"insertbefore"`
	Backrefs     bool    `json:"backrefs"`
	Create       bool    `json:"create"`
	Backup       bool    `json:"backup"`
	Firstmatch   bool    `json:"firstmatch"`
	Validate     *string `json:"validate"`
}

const linesep = "\n"

var args Args

// truthy is Python truthiness for an optional string.
func truthy(s *string) bool { return s != nil && *s != "" }

func is(s *string, v string) bool { return s != nil && *s == v }

func rstripNL(s string) string { return strings.TrimRight(s, "\r\n") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func compile(pattern string) *regexp.Regexp {
	re, err := ansible.CompilePython(pattern)
	if err != nil {
		ansible.Crash(err.Error(), "")
	}
	return re
}

func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		ansible.CrashError(err)
	}
	text, err := decode(data, args.Encoding)
	if err != nil {
		ansible.Crash(err.Error(), "")
	}
	return readlines(text)
}

func newDiff(dest string) ansible.Diff {
	return ansible.Diff{
		"before": "", "after": "",
		"before_header": dest + " (content)", "after_header": dest + " (content)",
	}
}

func writeChanges(lines []string, dest string) {
	data, err := encode(strings.Join(lines, ""), args.Encoding)
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
	if err := files.AtomicMove(tmpfile, files.Realpath(dest), args.UnsafeWritesEnabled()); err != nil {
		ansible.CrashError(err)
	}
}

// checkFileAttrs applies the common file options after the content change.
func checkFileAttrs(dest string, changed bool, msg string, diff ansible.Diff) (string, bool) {
	if args.SetFSAttributesIfDifferent(dest, false, diff) {
		if changed {
			msg += " and "
		}
		changed = true
		msg += "ownership, perms or SE linux context changed"
	}
	return msg, changed
}

func present(dest string, regexp, searchString *string, line string, insertafter, insertbefore *string) {
	diff := newDiff(dest)

	var lines []string
	if !exists(dest) {
		if !args.Create {
			ansible.Fail(fmt.Sprintf("Destination %s does not exist !", dest), ansible.Result{"rc": 257})
		}
		destpath := pyDirname(dest)
		if destpath != "" && !exists(destpath) && !ansible.CheckMode() {
			if err := os.MkdirAll(destpath, 0o777); err != nil {
				ansible.Fail(fmt.Sprintf("Error creating %s (%s)", destpath, err), nil)
			}
		}
	} else {
		lines = readLines(dest)
	}

	if ansible.DiffMode() {
		diff["before"] = strings.Join(lines, "")
	}

	var reM, reIns = (*regexpT)(nil), (*regexpT)(nil)
	if regexp != nil {
		reM = compile(*regexp)
	}
	if insertafter != nil && !is(insertafter, "BOF") && !is(insertafter, "EOF") {
		reIns = compile(*insertafter)
	} else if insertbefore != nil && !is(insertbefore, "BOF") {
		reIns = compile(*insertbefore)
	}

	// index[0] is the line where regexp, search_string or line was found;
	// index[1] is where insertafter/insertbefore matched.
	index := [2]int{-1, -1}
	matched := false
	var matchLine string
	var matchLoc []int
	exactLineMatch := false

	// 1. A regexp match wins over insertafter/insertbefore.
	if reM != nil {
		for lineno, cur := range lines {
			if loc := reM.FindStringSubmatchIndex(cur); loc != nil {
				index[0], matched, matchLine, matchLoc = lineno, true, cur, loc
				if args.Firstmatch {
					break
				}
			}
		}
	}
	// 2. So does a search_string match.
	if searchString != nil {
		for lineno, cur := range lines {
			if strings.Contains(cur, *searchString) {
				index[0], matched = lineno, true
				if args.Firstmatch {
					break
				}
			}
		}
	}
	// 3. Otherwise look for the exact line, and where to insert.
	if !matched {
		for lineno, cur := range lines {
			if line == rstripNL(cur) {
				index[0] = lineno
				exactLineMatch = true
			} else if reIns != nil && reIns.MatchString(cur) {
				if truthy(insertafter) {
					index[1] = lineno + 1
					if args.Firstmatch {
						break
					}
				}
				if truthy(insertbefore) {
					index[1] = lineno
					if args.Firstmatch {
						break
					}
				}
			}
		}
	}

	msg := ""
	changed := false
	added := func() { msg, changed = "line added", true }

	switch {
	case index[0] != -1:
		newLine := line
		if args.Backrefs && matched && reM != nil {
			var err error
			if newLine, err = ansible.ExpandPython(reM, line, matchLine, matchLoc); err != nil {
				ansible.Crash(err.Error(), "")
			}
		}
		if !strings.HasSuffix(newLine, linesep) {
			newLine += linesep
		}

		if regexp == nil && searchString == nil && !matched && !exactLineMatch {
			// Unreachable in practice, kept to mirror the Python module.
			if truthy(insertafter) && !is(insertafter, "EOF") {
				if len(lines) > 0 && !endsWithNL(lines[len(lines)-1]) {
					lines[len(lines)-1] += linesep
				}
				if len(lines) == index[1] {
					if rstripNL(lines[index[1]-1]) != line {
						lines = append(lines, line+linesep)
						added()
					}
				} else if rstripNL(lines[index[1]]) != line {
					lines = insert(lines, index[1], line+linesep)
					added()
				}
			} else if truthy(insertbefore) && !is(insertbefore, "BOF") {
				if index[1] <= 0 {
					if rstripNL(lines[max(index[1], 0)]) != line {
						lines = insert(lines, max(index[1], 0), line+linesep)
						added()
					}
				} else if rstripNL(lines[index[1]-1]) != line {
					lines = insert(lines, index[1], line+linesep)
					added()
				}
			}
		} else if lines[index[0]] != newLine {
			lines[index[0]] = newLine
			msg, changed = "line replaced", true
		}
	case args.Backrefs:
		// Do nothing: without a regexp match there is nothing to fill the backrefs with.
	case is(insertbefore, "BOF") || is(insertafter, "BOF"):
		lines = insert(lines, 0, line+linesep)
		added()
	case is(insertafter, "EOF") || index[1] == -1:
		// Make sure the file ends with a newline before adding the line.
		if len(lines) > 0 && !endsWithNL(lines[len(lines)-1]) {
			lines = append(lines, linesep)
		}
		lines = append(lines, line+linesep)
		added()
	case truthy(insertafter) && index[1] != -1:
		// Don't insert the line if it is already there.
		if len(lines) == index[1] {
			if rstripNL(lines[index[1]-1]) != line {
				lines = append(lines, line+linesep)
				added()
			}
		} else if line != rstripNL(lines[index[1]]) {
			lines = insert(lines, index[1], line+linesep)
			added()
		}
	default:
		// insertbefore/insertafter matched, but not regexp or search_string
		lines = insert(lines, index[1], line+linesep)
		added()
	}

	if ansible.DiffMode() {
		diff["after"] = strings.Join(lines, "")
	}

	backupdest := ""
	if changed && !ansible.CheckMode() {
		if args.Backup && exists(dest) {
			backupdest = backup(dest)
		}
		writeChanges(lines, dest)
	}

	if ansible.CheckMode() && !exists(dest) {
		ansible.Exit(ansible.Result{"changed": changed, "msg": msg, "backup": backupdest, "diff": diff})
	}

	attrDiff := ansible.Diff{}
	msg, changed = checkFileAttrs(dest, changed, msg, attrDiff)
	attrDiff["before_header"] = dest + " (file attributes)"
	attrDiff["after_header"] = dest + " (file attributes)"

	ansible.Exit(ansible.Result{"changed": changed, "msg": msg, "backup": backupdest, "diff": []ansible.Diff{diff, attrDiff}})
}

func absent(dest string, regexp, searchString, line *string) {
	if !exists(dest) {
		ansible.Exit(ansible.Result{"changed": false, "msg": "file not present"})
	}

	msg := ""
	diff := newDiff(dest)
	lines := readLines(dest)
	if ansible.DiffMode() {
		diff["before"] = strings.Join(lines, "")
	}

	var re *regexpT
	if regexp != nil {
		re = compile(*regexp)
	}
	found := 0
	kept := lines[:0:0]
	for _, cur := range lines {
		var match bool
		switch {
		case re != nil:
			match = re.MatchString(cur)
		case searchString != nil:
			match = strings.Contains(cur, *searchString)
		default:
			match = line != nil && *line == rstripNL(cur)
		}
		if match {
			found++
		} else {
			kept = append(kept, cur)
		}
	}
	lines = kept
	changed := found > 0

	if ansible.DiffMode() {
		diff["after"] = strings.Join(lines, "")
	}

	backupdest := ""
	if changed && !ansible.CheckMode() {
		if args.Backup {
			backupdest = backup(dest)
		}
		writeChanges(lines, dest)
	}
	if changed {
		msg = fmt.Sprintf("%d line(s) removed", found)
	}

	attrDiff := ansible.Diff{}
	msg, changed = checkFileAttrs(dest, changed, msg, attrDiff)
	attrDiff["before_header"] = dest + " (file attributes)"
	attrDiff["after_header"] = dest + " (file attributes)"

	ansible.Exit(ansible.Result{
		"changed": changed, "found": found, "msg": msg, "backup": backupdest,
		"diff": []ansible.Diff{diff, attrDiff},
	})
}

type regexpT = regexp.Regexp

func backup(dest string) string {
	b, err := files.BackupLocal(dest)
	if err != nil {
		ansible.CrashError(err)
	}
	return b
}

func endsWithNL(s string) bool {
	return strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\r")
}

func insert(lines []string, i int, s string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:i]...)
	out = append(out, s)
	return append(out, lines[i:]...)
}

// pyDirname is os.path.dirname.
func pyDirname(p string) string {
	i := strings.LastIndex(p, "/") + 1
	head := p[:i]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	ansible.ReadArgs(&args)
	if args.State == "" {
		args.State = "present"
	}
	if args.Encoding == "" {
		args.Encoding = "utf-8"
	}
	path := ansible.ExpandPath(args.Path)

	if is(args.Regexp, "") || is(args.SearchString, "") {
		msg := "The %s is an empty string, which will match every line in the file. " +
			"This may have unintended consequences, such as replacing the last line in the file rather than appending."
		name := "search string"
		if is(args.Regexp, "") {
			name = "regular expression"
			msg += " If this is desired, use '^' to match every line in the file and avoid this warning."
		}
		ansible.Warn(fmt.Sprintf(msg, name))
	}

	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		ansible.Fail(fmt.Sprintf("Path %s is a directory !", path), ansible.Result{"rc": 256})
	}

	if args.State == "present" {
		if args.Backrefs && args.Regexp == nil {
			ansible.Fail("regexp is required with backrefs=true", nil)
		}
		if args.Line == nil {
			ansible.Fail("line is required with state=present", nil)
		}
		// Deal with the insertafter default here, as Python does, so it
		// doesn't trip the mutually exclusive check.
		insBef, insAft := args.InsertBefore, args.InsertAfter
		if insBef == nil && insAft == nil {
			eof := "EOF"
			insAft = &eof
		}
		present(path, args.Regexp, args.SearchString, *args.Line, insAft, insBef)
	} else {
		if args.Regexp == nil && args.SearchString == nil && args.Line == nil {
			ansible.Fail("one of line, search_string, or regexp is required with state=absent", nil)
		}
		absent(path, args.Regexp, args.SearchString, args.Line)
	}
}
