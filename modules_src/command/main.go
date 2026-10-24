// Command equivalent module, written in Go. Mirrors ansible.builtin.command,
// including the _uses_shell/executable handling the shell action relies on.
package command

import (
	"fmt"
	"os"
	"strings"
	"time"

	"compiled/builtin/internal/ansible"
)

type Args struct {
	RawParams          *string  `json:"_raw_params"`
	UsesShell          bool     `json:"_uses_shell"`
	Cmd                *string  `json:"cmd"`
	Argv               []string `json:"argv"`
	Chdir              *string  `json:"chdir"`
	Executable         *string  `json:"executable"`
	ExpandArgumentVars *bool    `json:"expand_argument_vars"`
	Creates            *string  `json:"creates"`
	Removes            *string  `json:"removes"`
	Stdin              *string  `json:"stdin"`
	StdinAddNewline    *bool    `json:"stdin_add_newline"`
	StripEmptyEnds     *bool    `json:"strip_empty_ends"`
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

func path(p *string) *string {
	if p == nil {
		return nil
	}
	v := ansible.ExpandPath(*p)
	return &v
}

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	var a Args
	ansible.ReadArgs(&a)

	shell := a.UsesShell
	chdir := path(a.Chdir)
	executable := str(a.Executable)
	creates := path(a.Creates)
	removes := path(a.Removes)
	strip := boolOr(a.StripEmptyEnds, true)

	// We promised these in 'always'; *_lines are added by the controller.
	r := ansible.Result{
		"changed": false, "stdout": "", "stderr": "", "rc": nil, "cmd": nil,
		"start": nil, "end": nil, "delta": nil, "msg": "",
	}

	if !shell && executable != "" {
		ansible.Warn(fmt.Sprintf("As of Ansible 2.4, the parameter 'executable' is no longer supported with the 'command' module. Not using '%s'.", executable))
		executable = ""
	}

	// args is the command string, or after splitting the argument list.
	var args any
	command := str(a.RawParams)
	if command == "" {
		command = str(a.Cmd)
	}
	switch {
	case command != "" && !shell:
		split, err := ansible.ShlexSplit(command)
		if err != nil {
			ansible.Crash(err.Error(), "")
		}
		if len(split) > 0 {
			args = split
		}
	case command != "":
		args = command
	}
	if args == nil && a.Argv != nil {
		args = a.Argv
	}
	r["cmd"] = args

	if chdir != nil && *chdir != "" {
		if err := os.Chdir(*chdir); err != nil {
			r["msg"] = "Unable to change directory before execution."
			ansible.FailCause(r["msg"].(string), ansible.OSErrorMessage(err, true), r)
		}
	}

	shoulda := "Did"
	if ansible.CheckMode() {
		shoulda = "Would"
	}

	// Skips for idempotence if the file exists (assumes command creates)
	if creates != nil && *creates != "" && len(ansible.Glob(*creates)) > 0 {
		r["msg"] = fmt.Sprintf("%s not run command since '%s' exists", shoulda, *creates)
		r["stdout"] = fmt.Sprintf("skipped, since %s exists", *creates)
		r["rc"] = 0
	}
	// or if the file does not exist (assumes command removes)
	if r["msg"] == "" && removes != nil && *removes != "" && len(ansible.Glob(*removes)) == 0 {
		r["msg"] = fmt.Sprintf("%s not run command since '%s' does not exist", shoulda, *removes)
		r["stdout"] = fmt.Sprintf("skipped, since %s does not exist", *removes)
		r["rc"] = 0
	}
	if r["msg"] != "" {
		ansible.Exit(r)
	}

	r["changed"] = true

	var stdout, stderr string
	if !ansible.CheckMode() {
		if args == nil {
			ansible.Fail("Argument 'args' to run_command must be list or string", ansible.Result{"rc": 257, "cmd": nil})
		}
		start := time.Now()
		rc, out, errOut := ansible.RunCommand(args, ansible.RunOptions{
			Shell:      shell,
			Executable: executable,
			Data:       a.Stdin,
			BinaryData: !boolOr(a.StdinAddNewline, true),
			NoExpand:   !boolOr(a.ExpandArgumentVars, true),
		})
		end := time.Now()
		r["rc"], stdout, stderr = rc, out, errOut
		r["start"] = ansible.PyDatetime(start)
		r["end"] = ansible.PyDatetime(end)
		r["delta"] = ansible.PyTimedelta(end.Sub(start))
	} else {
		// Partial check mode support: only creates/removes can be checked.
		r["rc"] = 0
		r["msg"] = "Command would have run if not in check mode"
		if creates == nil && removes == nil {
			r["skipped"] = true
			// skipped=True and changed=True are mutually exclusive
			r["changed"] = false
		}
	}

	if strip {
		stdout = strings.TrimRight(stdout, "\r\n")
		stderr = strings.TrimRight(stderr, "\r\n")
	}
	r["stdout"], r["stderr"] = stdout, stderr

	if r["rc"] != 0 {
		r["msg"] = "The command exited with a non-zero return code."
		ansible.Fail(r["msg"].(string), r)
	}
	ansible.Exit(r)
}
