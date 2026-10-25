// The multi-call binary: every module in one executable, which runs the one
// the action plugin names in _compiled_module. Go puts its runtime and the
// shared internal packages in every binary, so one binary for all modules is
// a fraction of the size of one per module.
package main

import (
	"compiled/builtin/internal/ansible"
	"compiled/builtin/modules_src/apk"
	"compiled/builtin/modules_src/blockinfile"
	"compiled/builtin/modules_src/command"
	"compiled/builtin/modules_src/copy"
	"compiled/builtin/modules_src/file"
	"compiled/builtin/modules_src/find"
	"compiled/builtin/modules_src/lineinfile"
	"compiled/builtin/modules_src/microdnf"
	"compiled/builtin/modules_src/ping"
	"compiled/builtin/modules_src/replace"
	"compiled/builtin/modules_src/slurp"
	"compiled/builtin/modules_src/stat"
	"compiled/builtin/modules_src/uri"
)

// modules maps each module directory in modules_src to its Main. shell runs
// command's, and get_url uri's.
var modules = map[string]func(){
	"apk":         apk.Main,
	"blockinfile": blockinfile.Main,
	"command":     command.Main,
	"copy":        copy.Main,
	"file":        file.Main,
	"find":        find.Main,
	"lineinfile":  lineinfile.Main,
	"microdnf":    microdnf.Main,
	"ping":        ping.Main,
	"replace":     replace.Main,
	"slurp":       slurp.Main,
	"stat":        stat.Main,
	"uri":         uri.Main,
}

func main() { ansible.Dispatch(modules) }
