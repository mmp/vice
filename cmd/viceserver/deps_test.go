// cmd/viceserver/deps_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Verify that every non-UI command binary stays free of UI / renderer /
// platform dependencies. This both prevents accidental imports that would
// break the -race build of viceserver (cimgui-go inlined generics crash
// the Go linker under -race) and replaces the older pre-commit hook that
// did the same check externally.
package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestNoUIDeps(t *testing.T) {
	// vice and backshop are the GUI binaries and so are the only cmds
	// allowed to pull in UI packages.
	uiCmds := map[string]bool{
		"github.com/mmp/vice/cmd/vice":     true,
		"github.com/mmp/vice/cmd/backshop": true,
	}

	forbidden := []string{
		"github.com/mmp/vice/renderer",
		"github.com/mmp/vice/platform/glfw",
		"github.com/mmp/vice/platform/sdl2",
		"github.com/mmp/vice/scope",
		"github.com/mmp/vice/gui",
		"github.com/mmp/vice/stars",
		"github.com/mmp/vice/eram",
		"github.com/mmp/vice/client",
		"github.com/AllenDang/cimgui-go",
		"github.com/go-gl/gl",
		"github.com/go-gl/glfw",
		"github.com/veandco/go-sdl2",
	}

	// One go list gives every cmd's full transitive dependency set; a
	// separate invocation per package dominated this package's test time.
	out, err := exec.Command("go", "list", "-f",
		"{{.ImportPath}}{{range .Deps}} {{.}}{{end}}",
		"github.com/mmp/vice/cmd/...").Output()
	if err != nil {
		t.Fatalf("go list ./cmd/...: %v", err)
	}

	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 || uiCmds[fields[0]] {
			continue
		}
		cmd, deps := fields[0], fields[1:]

		for _, f := range forbidden {
			if i := slices.IndexFunc(deps, func(d string) bool {
				return d == f || strings.HasPrefix(d, f+"/")
			}); i != -1 {
				t.Errorf("%s pulls in forbidden UI dependency %q (via %q)", cmd, f, deps[i])
			}
		}
	}
}
