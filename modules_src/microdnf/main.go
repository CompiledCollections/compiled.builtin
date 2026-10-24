// Microdnf module, written in Go: manages RPM packages with microdnf, the
// package manager of minimal Red Hat, UBI and Fedora images, with the
// options of ansible.builtin.package. There is no builtin module for
// microdnf (ansible.builtin.package picks dnf or dnf5, which need their
// Python bindings), so the results follow ansible.builtin.dnf's.
//
// On Fedora, microdnf is dnf5 under another name; both are handled.
package microdnf

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"compiled/builtin/internal/ansible"
)

type Args struct {
	Name  []string `json:"name"`
	State string   `json:"state"`
	Use   string   `json:"use"`
}

const microdnf = "/usr/bin/microdnf"

var (
	// A section of the transaction table, such as "Installing:" or
	// "Installing dependencies:", and what the results call it.
	sectionRe = regexp.MustCompile(`^(Installing|Upgrading|Downgrading|Reinstalling|Replacing|Obsoleting|Removing)\b[^:]*:\s*$`)
	sections  = map[string]string{
		"Installing": "Installed", "Upgrading": "Upgraded", "Downgrading": "Downgraded",
		"Reinstalling": "Reinstalled", "Replacing": "Replaced", "Obsoleting": "Obsoleted", "Removing": "Removed",
	}
	spaceRe = regexp.MustCompile(`\s+`)
)

func run(argv ...string) (int, string, string) {
	return ansible.RunCommand(argv, ansible.RunOptions{NoExpand: true})
}

// installed maps name.arch to the NEVRA of every installed package.
func installed() map[string]string {
	rc, out, stderr := run("rpm", "-qa", "--qf", "%{NAME}\t%{EPOCH}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\n")
	if rc != 0 {
		ansible.Fail("Failed to list the installed packages: "+strings.TrimSpace(stderr), ansible.Result{"rc": rc, "results": []string{}})
	}
	pkgs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 || f[4] == "(none)" { // gpg-pubkey entries have no arch
			continue
		}
		evr := f[2] + "-" + f[3]
		if f[1] != "(none)" && f[1] != "0" {
			evr = f[1] + ":" + evr
		}
		pkgs[f[0]+"."+f[4]] = f[0] + "-" + evr + "." + f[4]
	}
	return pkgs
}

// changes lists what happened between two package lists.
func changes(before, after map[string]string) []string {
	results := []string{}
	for key, nevra := range after {
		switch old, ok := before[key]; {
		case !ok:
			results = append(results, "Installed: "+nevra)
		case old != nevra:
			results = append(results, "Upgraded: "+nevra)
		}
	}
	for key, nevra := range before {
		if _, ok := after[key]; !ok {
			results = append(results, "Removed: "+nevra)
		}
	}
	sort.Strings(results)
	return results
}

// planned lists what a transaction table (from --assumeno) would do. Classic
// microdnf names each package by its NEVRA; dnf5 has name, arch and version
// columns.
func planned(out string) []string {
	results := []string{}
	verb := ""
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := sectionRe.FindStringSubmatch(trimmed); m != nil {
			verb = sections[m[1]]
			continue
		}
		if verb == "" || !strings.HasPrefix(line, " ") || trimmed == "" {
			verb = ""
			continue
		}
		f := spaceRe.Split(trimmed, -1)
		pkg := f[0]
		if len(f) >= 4 && !strings.Contains(f[0], ".") && strings.Contains(f[2], "-") {
			pkg = f[0] + "-" + f[2] + "." + f[1] // dnf5: name arch version
		}
		results = append(results, verb+": "+pkg)
	}
	sort.Strings(results)
	return results
}

func failed(argv []string, rc int, stdout, stderr string) {
	var lines []string
	for _, l := range strings.Split(stderr+"\n"+stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "librhsm-WARNING") {
			lines = append(lines, l)
		}
	}
	msg := strings.Join(lines, "\n")
	ansible.Fail(msg, ansible.Result{"rc": rc, "results": []string{}, "stdout": stdout, "stderr": stderr, "cmd": argv})
}

// Main runs the module; the multi-call binary in modules_src/main.go calls it.
func Main() {
	var args Args
	ansible.ReadArgs(&args)
	if args.Use != "" && args.Use != "auto" && args.Use != "microdnf" {
		ansible.Fail(fmt.Sprintf("use must be auto or microdnf, not %s: compiled.builtin.microdnf only manages packages with microdnf", args.Use), nil)
	}
	if fi, err := os.Stat(microdnf); err != nil || fi.IsDir() {
		ansible.Fail("microdnf was not found on the target: compiled.builtin.microdnf manages packages with "+microdnf, ansible.Result{"rc": 127, "results": []string{}})
	}

	var names []string
	for _, n := range args.Name {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	nothing := ansible.Result{"changed": false, "msg": "Nothing to do", "rc": 0, "results": []string{}}
	if len(names) == 0 {
		ansible.Exit(nothing)
	}

	var commands [][]string
	switch args.State {
	case "present", "installed":
		commands = [][]string{append([]string{microdnf, "install"}, names...)}
	case "latest":
		// install adds what's missing; upgrade updates what's there.
		commands = [][]string{append([]string{microdnf, "install"}, names...), append([]string{microdnf, "upgrade"}, names...)}
	case "absent", "removed":
		// Classic microdnf fails to remove what isn't installed.
		var present []string
		for _, n := range names {
			if rc, _, _ := run("rpm", "-q", n); rc == 0 {
				present = append(present, n)
			}
		}
		if len(present) == 0 {
			ansible.Exit(nothing)
		}
		commands = [][]string{append([]string{microdnf, "remove"}, present...)}
	default:
		ansible.Fail(fmt.Sprintf("value of state must be one of: present, installed, latest, absent, removed, got: %s", args.State), nil)
	}

	if ansible.CheckMode() {
		var results []string
		for _, argv := range commands {
			argv = append(argv[:2:2], append([]string{"--assumeno"}, argv[2:]...)...)
			rc, stdout, stderr := run(argv...)
			plan := planned(stdout)
			// --assumeno aborts the transaction, which dnf5 reports with rc 1.
			if rc != 0 && len(plan) == 0 {
				failed(argv, rc, stdout, stderr)
			}
			results = append(results, plan...)
		}
		if len(results) == 0 {
			ansible.Exit(nothing)
		}
		ansible.Exit(ansible.Result{"changed": true, "rc": 0, "results": results,
			"msg": "Check mode: No changes made, but would have if not in check mode"})
	}

	before := installed()
	for _, argv := range commands {
		argv = append(argv[:2:2], append([]string{"-y"}, argv[2:]...)...)
		if rc, stdout, stderr := run(argv...); rc != 0 {
			failed(argv, rc, stdout, stderr)
		}
	}
	results := changes(before, installed())
	if len(results) == 0 {
		ansible.Exit(nothing)
	}
	ansible.Exit(ansible.Result{"changed": true, "rc": 0, "results": results, "msg": ""})
}
