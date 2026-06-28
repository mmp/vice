// Verify that every non-UI command binary stays free of UI / renderer /
// platform dependencies. This both prevents accidental imports that would
// break the -race build of viceserver (cimgui-go inlined generics crash
// the Go linker under -race) and replaces the older pre-commit hook that
// did the same check externally.
package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNoUIDeps(t *testing.T) {
	// vice is the only cmd that's allowed to pull in UI packages.
	uiCmds := map[string]bool{
		"github.com/mmp/vice/cmd/vice": true,
	}

	forbidden := []string{
		"github.com/mmp/vice/renderer",
		"github.com/mmp/vice/platform",
		"github.com/mmp/vice/panes",
		"github.com/mmp/vice/stars",
		"github.com/mmp/vice/eram",
		"github.com/mmp/vice/client",
		"github.com/AllenDang/cimgui-go",
		"github.com/go-gl/gl",
		"github.com/go-gl/glfw",
		"github.com/veandco/go-sdl2",
	}

	pkgsOut, err := exec.Command("go", "list", "github.com/mmp/vice/cmd/...").Output()
	if err != nil {
		t.Fatalf("go list ./cmd/...: %v", err)
	}
	cmds := strings.Fields(string(pkgsOut))

	for _, cmd := range cmds {
		if uiCmds[cmd] {
			continue
		}
		out, err := exec.Command("go", "list", "-deps", cmd).Output()
		if err != nil {
			t.Errorf("go list -deps %s: %v", cmd, err)
			continue
		}
		deps := make(map[string]struct{})
		for _, d := range strings.Fields(string(out)) {
			deps[d] = struct{}{}
		}
		for _, f := range forbidden {
			for d := range deps {
				if d == f || strings.HasPrefix(d, f+"/") {
					t.Errorf("%s pulls in forbidden UI dependency %q (via %q)", cmd, f, d)
					break
				}
			}
		}
	}
}
