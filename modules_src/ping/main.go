// Ping equivalent module, written in Go
package ping

import "compiled/builtin/internal/ansible"

type Args struct {
	Data *string `json:"data"`
}

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	var args Args
	ansible.ReadArgs(&args)

	data := "pong"
	if args.Data != nil {
		data = *args.Data
	}
	// Like ansible.builtin.ping, data=crash makes the module raise on purpose.
	if data == "crash" {
		ansible.Crash("boom", "")
	}
	ansible.Exit(ansible.Result{"ping": data})
}
