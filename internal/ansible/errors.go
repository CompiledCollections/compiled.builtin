package ansible

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// Errno returns the errno behind err, or 0.
func Errno(err error) syscall.Errno {
	var errno syscall.Errno
	errors.As(err, &errno)
	return errno
}

// OSErrorMessage formats an OS error the way Python's OSError prints:
// [Errno N] message: 'path'. bytesPath prints the path as b'path', as
// Python does for paths it handled as bytes.
func OSErrorMessage(err error, bytesPath bool) string {
	var pe *os.PathError
	var le *os.LinkError
	var errno syscall.Errno
	if errors.As(err, &errno) {
		msg := fmt.Sprintf("[Errno %d] %s", int(errno), pythonStrerror(errno))
		prefix := ""
		if bytesPath {
			prefix = "b"
		}
		if errors.As(err, &pe) {
			msg += fmt.Sprintf(": %s'%s'", prefix, pe.Path)
		} else if errors.As(err, &le) {
			// Two-path calls (symlink, link, rename) name both: 'src' -> 'dst'.
			msg += fmt.Sprintf(": %s'%s' -> %s'%s'", prefix, le.Old, prefix, le.New)
		}
		return msg
	}
	return err.Error()
}

// pythonStrerror capitalizes like glibc's strerror, which Python uses.
func pythonStrerror(errno syscall.Errno) string {
	s := errno.Error()
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
