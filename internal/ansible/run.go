package ansible

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// RunOptions are the run_command options the modules use.
type RunOptions struct {
	// Shell runs the command string through a shell (use_unsafe_shell).
	Shell bool
	// Executable is the shell to use with Shell.
	Executable string
	// Data is written to the command's stdin; a newline is added unless BinaryData.
	Data       *string
	BinaryData bool
	// NoExpand turns off expansion of ~ and $VARS in arguments.
	NoExpand bool
}

// RunCommand runs args (a []string, or a string to split) like
// AnsibleModule.run_command with its default handle_exceptions=True: a
// command that cannot be started fails the module.
func RunCommand(args any, opts RunOptions) (rc int, stdout, stderr string) {
	var argv []string
	switch a := args.(type) {
	case []string:
		argv = a
	case string:
		if opts.Shell {
			argv = []string{a}
		} else {
			var err error
			if argv, err = ShlexSplit(a); err != nil {
				Fail("Invalid command syntax in run_command: "+err.Error(), nil)
			}
		}
	default:
		Fail("Argument 'args' to run_command must be list or string", Result{"rc": 257, "cmd": args})
	}

	if opts.Shell {
		var script string
		if s, ok := args.(string); ok {
			script = s
		} else {
			quoted := make([]string, len(argv))
			for i, a := range argv {
				quoted[i] = ShlexQuote(a)
			}
			script = strings.Join(quoted, " ")
		}
		switch {
		case opts.Executable != "":
			argv = []string{opts.Executable, "-c", script}
		case Params.ShellExecutable != "" && Params.ShellExecutable != "/bin/sh":
			argv = []string{Params.ShellExecutable, "-c", script}
		default:
			argv = []string{"/bin/sh", "-c", script}
		}
	} else if !opts.NoExpand {
		expanded := make([]string, len(argv))
		for i, a := range argv {
			expanded[i] = ExpandPath(a)
		}
		argv = expanded
	}

	if len(argv) == 0 {
		Fail("Error executing command.", Result{"rc": 257, "stdout": "", "stderr": "", "cmd": ""})
	}

	r, out, errOut, err := run(argv, opts)
	if err != nil {
		rc := 257
		if errno := Errno(err); errno != 0 {
			rc = int(errno)
		}
		FailCause("Error executing command.", OSErrorMessage(err, true), Result{
			"rc": rc, "stdout": "", "stderr": "", "cmd": CleanArgs(argv),
		})
	}
	return r, out, errOut
}

func run(argv []string, opts RunOptions) (int, string, string, error) {
	env := cleanEnv()
	path, err := lookPath(argv[0], env)
	if err != nil {
		return 0, "", "", err
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env}

	if opts.Data != nil && *opts.Data != "" {
		data := *opts.Data
		if !opts.BinaryData {
			data += "\n"
		}
		cmd.Stdin = strings.NewReader(data)
	} else {
		cmd.Stdin = os.Stdin
	}

	// Pipes we read ourselves, so we can stop once the command has exited
	// even if something it started keeps them open (as Python does).
	outR, outW, err := os.Pipe()
	if err != nil {
		return 0, "", "", err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return 0, "", "", err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		outW.Close()
		errW.Close()
		outR.Close()
		errR.Close()
		return 0, "", "", unwrapExecError(err)
	}
	outW.Close()
	errW.Close()

	var (
		mu       sync.Mutex
		stdout   []byte
		stderr   []byte
		lastRead = time.Now()
		wg       sync.WaitGroup
	)
	drain := func(r *os.File, dst *[]byte) {
		defer wg.Done()
		buf := make([]byte, 32768)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				mu.Lock()
				*dst = append(*dst, buf[:n]...)
				lastRead = time.Now()
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go drain(outR, &stdout)
	go drain(errR, &stderr)

	waitErr := cmd.Wait()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-done:
		case <-time.After(time.Second):
			mu.Lock()
			idle := time.Since(lastRead) >= time.Second
			mu.Unlock()
			if !idle {
				continue
			}
			// Something still holds the pipes open; stop reading like Python.
			outR.Close()
			errR.Close()
		}
		break
	}
	mu.Lock()
	defer mu.Unlock()

	rc := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return 0, "", "", waitErr
		}
		rc = exitErr.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			rc = -int(ws.Signal())
		}
	}
	return rc, string(stdout), string(stderr), nil
}

// cleanEnv is the module's environment without the PYTHONPATH entries
// AnsiballZ adds, as run_command does.
func cleanEnv() []string {
	env := os.Environ()
	for i, kv := range env {
		if !strings.HasPrefix(kv, "PYTHONPATH=") {
			continue
		}
		var keep []string
		for _, p := range strings.Split(strings.TrimPrefix(kv, "PYTHONPATH="), ":") {
			if p != "" && !strings.HasSuffix(p, "/ansible_modlib.zip") && !strings.HasSuffix(p, "/debug_dir") {
				keep = append(keep, p)
			}
		}
		if len(keep) > 0 {
			env[i] = "PYTHONPATH=" + strings.Join(keep, ":")
		}
	}
	return env
}

// lookPath resolves name like Python's os.execvpe: a name containing a
// slash is used as is; otherwise each PATH entry is tried, and a file found
// but not executable is reported as EACCES rather than skipped.
func lookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	path := "/bin:/usr/bin"
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	var saved error
	for _, dir := range strings.Split(path, ":") {
		if dir == "" {
			dir = "."
		}
		p := dir + "/" + name
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() || fi.Mode()&0o111 == 0 || syscall.Access(p, 1) != nil {
			if saved == nil {
				saved = &os.PathError{Op: "exec", Path: name, Err: syscall.EACCES}
			}
			continue
		}
		return p, nil
	}
	if saved != nil {
		return "", saved
	}
	return "", &os.PathError{Op: "exec", Path: name, Err: syscall.ENOENT}
}

func unwrapExecError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, io.EOF) {
		return syscall.EIO
	}
	return err
}

var passwdArgRe = &LazyRegexp{Expr: `^-{0,2}pass-?(word|wd)?`}

// CleanArgs is AnsibleModule._clean_args: the command line as a quoted
// string, with password arguments masked.
func CleanArgs(argv []string) string {
	clean := make([]string, 0, len(argv))
	isPasswd := false
	for _, a := range argv {
		if isPasswd {
			isPasswd = false
			clean = append(clean, "********")
			continue
		}
		if passwdArgRe.Get().MatchString(a) {
			if i := strings.Index(a, "="); i > -1 {
				clean = append(clean, a[:i]+"=********")
				continue
			}
			isPasswd = true
		}
		clean = append(clean, a)
	}
	for i, a := range clean {
		clean[i] = ShlexQuote(a)
	}
	return strings.Join(clean, " ")
}
