// Package ansible holds the plumbing shared by every binary module: reading
// the arguments Ansible passes in and writing the JSON result back, the way
// AnsibleModule does for Python modules.
//
// Arguments are validated and normalized (types, aliases, defaults) on the
// controller by the action plugin, so modules can decode them straight into
// typed structs.
package ansible

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Result is a module result, serialized as the JSON object Ansible reads.
type Result map[string]any

// Internal holds the _ansible_* arguments the controller adds to every module call.
type Internal struct {
	CheckMode        bool     `json:"_ansible_check_mode"`
	Diff             bool     `json:"_ansible_diff"`
	NoLog            bool     `json:"_ansible_no_log"`
	ShellExecutable  string   `json:"_ansible_shell_executable"`
	SelinuxSpecialFS []string `json:"_ansible_selinux_special_fs"`
	Tmpdir           *string  `json:"_ansible_tmpdir"`
	RemoteTmp        string   `json:"_ansible_remote_tmp"`
	KeepRemoteFiles  bool     `json:"_ansible_keep_remote_files"`
	Version          string   `json:"_ansible_version"`
}

// legacyErrors reports whether the controller predates ansible-core 2.19,
// which changed how modules report exceptions: before it, exception is a
// traceback string and a crashing module just exits with output on stderr.
func legacyErrors() bool {
	var major, minor int
	if _, err := fmt.Sscanf(Params.Version, "%d.%d", &major, &minor); err != nil {
		return false
	}
	return major < 2 || major == 2 && minor < 19
}

var (
	// Params are the internal arguments of the running module.
	Params       Internal
	warnings     []string
	deprecations []any
	cleanup      []string
)

// ReadArgs decodes the module arguments into v, and the internal arguments
// into Params. Ansible passes binary modules the path to a JSON args file as
// the first argument; stdin is a fallback for running a module by hand.
func ReadArgs(v any) {
	var (
		data []byte
		err  error
	)
	if len(os.Args) > 1 {
		data, err = os.ReadFile(os.Args[1])
	} else {
		data, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		Fail(fmt.Sprintf("failed to read input: %v", err), nil)
	}
	if len(data) == 0 {
		data = []byte("{}")
	}
	if err := json.Unmarshal(data, &Params); err != nil {
		Fail(fmt.Sprintf("failed to parse input JSON: %v", err), nil)
	}
	if err := json.Unmarshal(data, v); err != nil {
		Fail(fmt.Sprintf("failed to parse input JSON: %v", err), nil)
	}
	if Params.ShellExecutable == "" {
		Params.ShellExecutable = "/bin/sh"
	}
}

// CheckMode reports whether the task runs in check mode.
func CheckMode() bool { return Params.CheckMode }

// DiffMode reports whether the task runs in diff mode.
func DiffMode() bool { return Params.Diff }

// Warn adds a warning to the result, like AnsibleModule.warn.
func Warn(msg string) { warnings = append(warnings, msg) }

// Deprecate adds a deprecation to the result, like AnsibleModule.deprecate.
// collection is the collection whose version `version` refers to; module is
// the module's full name.
func Deprecate(msg, version, collection, module string) {
	if legacyErrors() {
		// Before 2.19 ansible-core's own modules deprecate without naming a
		// collection, and so do these, to report the same.
		deprecations = append(deprecations, map[string]any{
			"msg": msg, "version": version, "collection_name": nil,
		})
		return
	}
	deprecations = append(deprecations, map[string]any{
		"__ansible_type": "DeprecationSummary",
		"event":          map[string]any{"__ansible_type": "Event", "msg": msg},
		"deprecator": map[string]any{
			"__ansible_type": "PluginInfo",
			"resolved_name":  module,
			"type":           map[string]any{"__ansible_type": "PluginType", "value": "module"},
		},
		"version": version,
	})
}

// Tmpdir returns a directory for temporary files, like AnsibleModule.tmpdir:
// the task's remote tmpdir when the controller made one, else a new directory
// under remote_tmp that is removed when the module exits.
func Tmpdir() string {
	if Params.Tmpdir != nil && *Params.Tmpdir != "" {
		if fi, err := os.Stat(*Params.Tmpdir); err == nil && fi.IsDir() {
			return *Params.Tmpdir
		}
	}
	base := ExpandPath(Params.RemoteTmp)
	if base == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		base = os.TempDir()
	}
	// Named like Python's: ansible-moduletmp-<time.time()>-<random>.
	now := PyFloat(float64(time.Now().UnixNano()) / 1e9)
	dir, err := os.MkdirTemp(base, "ansible-moduletmp-"+now.String()+"-")
	if err != nil {
		Fail(fmt.Sprintf("Failed to create remote module tmp path at dir %s: %v", base, err), nil)
	}
	if !Params.KeepRemoteFiles {
		cleanup = append(cleanup, dir)
	}
	Params.Tmpdir = &dir
	return dir
}

var resultHooks []func(Result)

// OnResult registers a hook that can add to every result before it is
// written, such as the files package's add_path_info.
func OnResult(hook func(Result)) { resultHooks = append(resultHooks, hook) }

// Exit writes r as the module result and exits successfully.
func Exit(r Result) {
	write(r)
	os.Exit(0)
}

// Fail writes a failed result carrying msg, plus any extra keys, and exits
// non-zero, like AnsibleModule.fail_json.
func Fail(msg string, extra Result) {
	r := Result{}
	for k, v := range extra {
		r[k] = v
	}
	r["failed"] = true
	r["msg"] = msg
	write(r)
	os.Exit(1)
}

func write(r Result) {
	for _, dir := range cleanup {
		os.RemoveAll(dir)
	}
	for _, hook := range resultHooks {
		hook(r)
	}
	if len(warnings) > 0 {
		r["warnings"] = warnings
	}
	if len(deprecations) > 0 {
		r["deprecations"] = deprecations
	}
	data, err := json.Marshal(r)
	if err != nil {
		fmt.Println(`{"failed":true,"msg":"internal error: failed to marshal module result"}`)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// Diff is a diff entry in a result: before/after content or attributes, with headers.
type Diff map[string]any

// Set records key's before and after values in d, like the diff dicts
// AnsibleModule's set_*_if_different helpers fill in.
func (d Diff) Set(key string, before, after any) {
	if d == nil {
		return
	}
	b, ok := d["before"].(map[string]any)
	if !ok {
		b = map[string]any{}
		d["before"] = b
	}
	a, ok := d["after"].(map[string]any)
	if !ok {
		a = map[string]any{}
		d["after"] = a
	}
	b[key] = before
	a[key] = after
}

// Abs is filepath.Abs that falls back to the path itself.
func Abs(path string) string {
	if p, err := filepath.Abs(path); err == nil {
		return p
	}
	return path
}

// CausedError is an error raised from another, like Python's
// `raise Exception(msg) from cause`.
type CausedError struct {
	Msg   string
	Cause error
}

func (e *CausedError) Error() string { return e.Msg + ": " + OSErrorMessage(e.Cause, false) }

func (e *CausedError) Unwrap() error { return e.Cause }

// FailCause is fail_json(msg, exception=cause): the result's msg stays
// msg, and the controller shows the cause in its error message.
func FailCause(msg, cause string, extra Result) {
	r := Result{}
	for k, v := range extra {
		r[k] = v
	}
	if legacyErrors() {
		r["exception"] = cause + "\n"
		Fail(msg, r)
	}
	r["exception"] = map[string]any{
		"__ansible_type": "ErrorSummary",
		"event": map[string]any{
			"__ansible_type": "Event",
			"msg":            msg,
			"chain": map[string]any{
				"__ansible_type":   "EventChain",
				"msg_reason":       "<<< caused by >>>",
				"traceback_reason": "The above exception was the direct cause of the following error:",
				"follow":           true,
				"event":            map[string]any{"__ansible_type": "Event", "msg": cause},
			},
		},
	}
	Fail(msg, r)
}

// FailError fails with err, keeping a CausedError's message and cause apart.
func FailError(err error, extra Result) {
	var ce *CausedError
	if errors.As(err, &ce) {
		FailCause(ce.Msg, OSErrorMessage(ce.Cause, false), extra)
	}
	Fail(OSErrorMessage(err, false), extra)
}

// Crash ends the module the way an uncaught Python exception does. From
// ansible-core 2.19 that is a result with only the exception, which the
// controller turns into "Module failed: <message>"; cause, if not empty, is
// the exception's cause.
func Crash(message, cause string) {
	if legacyErrors() {
		// What a Python module that raised looks like to older controllers:
		// nothing on stdout, the error on stderr. The controller reports
		// "MODULE FAILURE" with the error in module_stderr.
		for _, dir := range cleanup {
			os.RemoveAll(dir)
		}
		if cause != "" {
			fmt.Fprintln(os.Stderr, cause)
			fmt.Fprintln(os.Stderr, "\nThe above exception was the direct cause of the following exception:")
		}
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
	event := map[string]any{"__ansible_type": "Event", "msg": message}
	if cause != "" {
		event["chain"] = map[string]any{
			"__ansible_type":   "EventChain",
			"msg_reason":       "<<< caused by >>>",
			"traceback_reason": "The above exception was the direct cause of the following error:",
			"follow":           true,
			"event":            map[string]any{"__ansible_type": "Event", "msg": cause},
		}
	}
	write(Result{
		"failed":    true,
		"exception": map[string]any{"__ansible_type": "ErrorSummary", "event": event},
	})
	os.Exit(1)
}

// CrashError is Crash for err, keeping a CausedError's cause.
func CrashError(err error) {
	var ce *CausedError
	if errors.As(err, &ce) {
		Crash(ce.Msg, OSErrorMessage(ce.Cause, true))
	}
	Crash(OSErrorMessage(err, true), "")
}
