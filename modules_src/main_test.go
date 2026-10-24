package main

import (
	"os"
	"testing"
)

// Every module directory must be in modules, or the action plugin would run
// a binary that doesn't have it.
func TestEveryModuleIsDispatched(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			dirs[e.Name()] = true
			if modules[e.Name()] == nil {
				t.Errorf("modules_src/%s isn't in modules", e.Name())
			}
		}
	}
	for name := range modules {
		if !dirs[name] {
			t.Errorf("modules has %s, which isn't a directory in modules_src", name)
		}
	}
}
