// Slurp equivalent module, written in Go. Mirrors ansible.builtin.slurp.
package slurp

import (
	"encoding/base64"
	"errors"
	"os"
	"syscall"

	"compiled/builtin/internal/ansible"
)

type Args struct {
	Src   string `json:"src"`
	Armor *bool  `json:"armor"`
}

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	var args Args
	ansible.ReadArgs(&args)

	source := ansible.ExpandPath(args.Src)
	armor := args.Armor == nil || *args.Armor

	data, err := os.ReadFile(source)
	if err != nil {
		var msg string
		switch {
		case errors.Is(err, syscall.ENOENT):
			msg = "File not found: " + source
		case errors.Is(err, syscall.EACCES):
			msg = "File is not readable: " + source
		case errors.Is(err, syscall.EISDIR):
			msg = "Source is a directory and must be a file: " + source
		default:
			// As in the builtin since ansible-core 2.19, where this message isn't
			// an f-string: the placeholder is printed literally.
			msg = "Unable to slurp file: {source}"
		}
		ansible.FailCause(msg, ansible.OSErrorMessage(err, false), nil)
	}

	if armor {
		ansible.Exit(ansible.Result{"content": base64.StdEncoding.EncodeToString(data), "source": source, "encoding": "base64"})
	}
	// Not the file's encoding, but what it is once sent as JSON.
	ansible.Exit(ansible.Result{"content": ansible.SurrogateEscaped(data), "source": source, "encoding": "utf-8"})
}
