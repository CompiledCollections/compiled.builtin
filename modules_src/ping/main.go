// Ping equivalent module, written in Go
package main

import "compiled/builtin/internal/ansible"

type Args struct {
	Data *string `json:"data"`
}

func main() {
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
