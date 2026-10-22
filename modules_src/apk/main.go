// Apk module, written in Go: manages Alpine packages with apk, with the
// options of ansible.builtin.package. There is no builtin module for apk
// (ansible.builtin.package hands it to community.general.apk, which needs
// Python), so the results follow ansible.builtin.dnf's.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"compiled/builtin/internal/ansible"
)

type Args struct {
	Name  []string `json:"name"`
	State string   `json:"state"`
	Use   string   `json:"use"`
}

// What apk prints for each package a transaction changes, as in
// "(1/2) Installing tar (1.35-r3)", and what the results call it.
var (
	stepRe  = regexp.MustCompile(`(?m)^\(\d+/\d+\) (Installing|Upgrading|Downgrading|Reinstalling|Replacing|Purging|Removing) (\S+) \(([^)]*)\)`)
	actions = map[string]string{
		"Installing": "Installed", "Upgrading": "Upgraded", "Downgrading": "Downgraded",
		"Reinstalling": "Reinstalled", "Replacing": "Replaced", "Purging": "Removed", "Removing": "Removed",
	}
	// A package name with an optional version constraint: tar, tar=1.35-r3, tar>1.
	constraintRe = regexp.MustCompile(`[<>=~]`)
)

func apkBinary() string {
	for _, p := range []string{"/sbin/apk", "/usr/sbin/apk", "/bin/apk", "/usr/bin/apk"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	ansible.Fail("apk was not found on the target: compiled.builtin.apk manages Alpine Linux packages", ansible.Result{"rc": 127, "results": []string{}})
	return ""
}

// steps lists what a transaction did (or would do) from apk's output.
func steps(out string) []string {
	results := []string{}
	for _, m := range stepRe.FindAllStringSubmatch(out, -1) {
		results = append(results, fmt.Sprintf("%s: %s (%s)", actions[m[1]], m[2], m[3]))
	}
	return results
}

func run(argv []string) (int, string, string) {
	return ansible.RunCommand(argv, ansible.RunOptions{NoExpand: true})
}

func main() {
	var args Args
	ansible.ReadArgs(&args)
	if args.Use != "" && args.Use != "auto" && args.Use != "apk" {
		ansible.Fail(fmt.Sprintf("use must be auto or apk, not %s: compiled.builtin.apk only manages packages with apk", args.Use), nil)
	}
	apk := apkBinary()

	var names []string
	for _, n := range args.Name {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		ansible.Exit(ansible.Result{"changed": false, "msg": "Nothing to do", "rc": 0, "results": []string{}})
	}

	var argv []string
	switch args.State {
	case "present", "installed":
		argv = append([]string{apk, "add"}, names...)
	case "latest":
		argv = append([]string{apk, "add", "--upgrade"}, names...)
	case "absent", "removed":
		// apk fails to remove what isn't installed, so leave those out.
		var installed []string
		for _, n := range names {
			base := n
			if loc := constraintRe.FindStringIndex(n); loc != nil {
				base = n[:loc[0]]
			}
			if rc, _, _ := run([]string{apk, "info", "-e", base}); rc == 0 {
				installed = append(installed, base)
			}
		}
		if len(installed) == 0 {
			ansible.Exit(ansible.Result{"changed": false, "msg": "Nothing to do", "rc": 0, "results": []string{}})
		}
		argv = append([]string{apk, "del"}, installed...)
	default:
		ansible.Fail(fmt.Sprintf("value of state must be one of: present, installed, latest, absent, removed, got: %s", args.State), nil)
	}
	if ansible.CheckMode() {
		// --simulate reads repository indexes but won't fetch them;
		// --no-cache fetches them into memory without writing the cache.
		argv = append(argv[:2:2], append([]string{"--simulate", "--no-cache"}, argv[2:]...)...)
	}

	rc, stdout, stderr := run(argv)
	results := steps(stdout)
	if rc != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		ansible.Fail(msg, ansible.Result{"rc": rc, "results": results, "stdout": stdout, "stderr": stderr, "cmd": argv})
	}

	res := ansible.Result{"changed": len(results) > 0, "rc": 0, "results": results, "msg": ""}
	switch {
	case len(results) == 0:
		res["msg"] = "Nothing to do"
	case ansible.CheckMode():
		res["msg"] = "Check mode: No changes made, but would have if not in check mode"
	}
	ansible.Exit(res)
}
