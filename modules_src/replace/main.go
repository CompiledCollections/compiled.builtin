// Replace equivalent module, written in Go. A close port of
// ansible.builtin.replace; regexp, after and before use Python regular
// expression syntax, translated to Go's RE2 engine, and replace is a Python
// replacement template (see internal/ansible/pyre.go).
package replace

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"compiled/builtin/internal/ansible"
	"compiled/builtin/internal/codecs"
	"compiled/builtin/internal/files"
)

type Args struct {
	files.FileArgs
	Path     string  `json:"path"`
	Regexp   string  `json:"regexp"`
	Replace  string  `json:"replace"`
	After    *string `json:"after"`
	Before   *string `json:"before"`
	Backup   bool    `json:"backup"`
	Validate *string `json:"validate"`
	Encoding string  `json:"encoding"`
}

var args Args

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func truthy(s *string) bool { return s != nil && *s != "" }

func compile(pattern string) *ansible.PythonRegexp {
	re, err := ansible.CompilePythonRegexp(pattern)
	if err != nil {
		ansible.Crash(err.Error(), "")
	}
	return re
}

// read is open(path, 'r', encoding=...).read(): decoded strictly, with
// universal newlines (\r\n and \r become \n).
func read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		ansible.Crash(fmt.Sprintf("Unable to read the contents of %s.", ansible.PyRepr(path)), ansible.OSErrorMessage(err, false))
	}
	text, err := codecs.Decode(data, args.Encoding)
	if err != nil {
		ansible.Crash(err.Error(), "")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
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
	res := ansible.Result{"rc": 0}

	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		ansible.Fail(fmt.Sprintf("Path %s is a directory !", path), ansible.Result{"rc": 256})
	}
	if !exists(path) {
		ansible.Fail(fmt.Sprintf("Path %s does not exist !", path), ansible.Result{"rc": 257})
	}
	contents := read(path)

	pattern := ""
	switch {
	case truthy(args.After) && truthy(args.Before):
		pattern = fmt.Sprintf("%s(?P<subsection>.*?)%s", *args.After, *args.Before)
	case truthy(args.After):
		pattern = fmt.Sprintf("%s(?P<subsection>.*)", *args.After)
	case truthy(args.Before):
		pattern = fmt.Sprintf("(?P<subsection>.*)%s", *args.Before)
	}

	section := contents
	var indices [2]int
	if pattern != "" {
		sectionRe := compile("(?s)" + pattern)
		loc := sectionRe.FindStringSubmatchIndex(contents)
		if loc == nil {
			res["msg"] = "Pattern for before/after params did not match the given file: " + pattern
			res["changed"] = false
			ansible.Exit(res)
		}
		g := sectionRe.SubexpIndex("subsection")
		indices = [2]int{loc[2*g], loc[2*g+1]}
		section = contents[indices[0]:indices[1]]
	}

	mre := compile("(?m)" + args.Regexp)
	template, err := ansible.ParseTemplate(mre.Regexp, args.Replace)
	var unknownGroup *ansible.UnknownGroupError
	if errors.As(err, &unknownGroup) {
		ansible.Crash(err.Error(), "")
	} else if err != nil {
		ansible.Fail("Unable to process replace due to error: "+err.Error(), nil)
	}
	result, count := mre.SubnPython(template, section)

	msg := ""
	changed := false
	if count > 0 && section != result {
		if pattern != "" {
			result = contents[:indices[0]] + result + contents[indices[1]:]
		}
		msg = fmt.Sprintf("%d replacements made", count)
		changed = true
		if ansible.DiffMode() {
			res["diff"] = ansible.Diff{
				"before_header": path, "before": contents,
				"after_header": path, "after": result,
			}
		}
	}

	if changed && !ansible.CheckMode() {
		if args.Backup && exists(path) {
			backup, err := files.BackupLocal(path)
			if err != nil {
				ansible.CrashError(err)
			}
			res["backup_file"] = backup
		}
		// Always follow symlinks, to change the real file.
		writeChanges(result, files.Realpath(path))
	}

	if args.SetFSAttributesIfDifferent(path, false, nil) {
		if changed {
			msg += " and "
		}
		changed = true
		msg += "ownership, perms or SE linux context changed"
	}
	res["msg"], res["changed"] = msg, changed
	ansible.Exit(res)
}
