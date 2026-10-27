// util/util_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestObjectArena(t *testing.T) {
	var a ObjectArena[int]

	for range 10 {
		seen := make(map[*int]any)
		for i := range 100 {
			p := a.AllocClear()
			if _, ok := seen[p]; ok {
				t.Errorf("%p: pointer returned twice!", p)
			}
			seen[p] = nil

			if *p != 0 {
				t.Errorf("%p = %d, expected 0", p, *p)
			}
			*p = i
		}

		if a.Cap() > 200 {
			t.Errorf("Capacity growing too fast: now %d", a.Cap())
		}

		a.Reset()
	}
}

func TestFindDuplicateJSONKeys(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected []DuplicateJSONKey
	}{
		{
			name:     "no duplicates",
			json:     `{"a": 1, "b": 2, "c": 3}`,
			expected: nil,
		},
		{
			name: "simple duplicate at root",
			json: `{"a": 1, "b": 2, "a": 3}`,
			expected: []DuplicateJSONKey{
				{Path: "", Key: "a"},
			},
		},
		{
			name: "duplicate in nested object",
			json: `{"outer": {"inner": 1, "inner": 2}}`,
			expected: []DuplicateJSONKey{
				{Path: "outer", Key: "inner"},
			},
		},
		{
			name: "multiple duplicates at different levels",
			json: `{"a": 1, "a": 2, "nested": {"b": 1, "b": 2}}`,
			expected: []DuplicateJSONKey{
				{Path: "", Key: "a"},
				{Path: "nested", Key: "b"},
			},
		},
		{
			name:     "array with objects no duplicates",
			json:     `{"items": [{"x": 1}, {"x": 2}]}`,
			expected: nil,
		},
		{
			name: "duplicate inside array element",
			json: `{"items": [{"x": 1, "x": 2}]}`,
			expected: []DuplicateJSONKey{
				{Path: "items", Key: "x"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindDuplicateJSONKeys([]byte(tt.json))

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d duplicates, got %d", len(tt.expected), len(result))
				return
			}

			for i, exp := range tt.expected {
				if result[i].Path != exp.Path || result[i].Key != exp.Key {
					t.Errorf("duplicate %d: expected {Path: %q, Key: %q}, got {Path: %q, Key: %q}",
						i, exp.Path, exp.Key, result[i].Path, result[i].Key)
				}
			}
		})
	}
}

// TestLoadResourceBytesDoesNotOverallocate guards the uncompressed path
// against going back through io.ReadAll, whose doubling buffer holds a second
// copy of the file and leaves slack in the result. That costs over a gigabyte
// of transient heap for the speech models.
func TestLoadResourceBytesDoesNotOverallocate(t *testing.T) {
	// Big enough that io.ReadAll's doubling overshoots by kilobytes;
	// a couple-KB file lands within a few bytes either way.
	const path = "airport_artccs.json"

	want, err := fs.Stat(GetResourcesFS(), path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	b := LoadResourceBytes(path)
	if int64(len(b)) != want.Size() {
		t.Errorf("%s: got %d bytes, want %d", path, len(b), want.Size())
	}
	// fs.ReadFile allocates one spare byte to see EOF; a growing buffer
	// overshoots by a fraction of the file size, which for a model is
	// tens of megabytes.
	if cap(b) > len(b)+64 {
		t.Errorf("%s: cap %d exceeds len %d; the file was copied through a growing buffer",
			path, cap(b), len(b))
	}
}

func TestOverlayFS(t *testing.T) {
	o := overlayFS{
		base: fstest.MapFS{
			"scenarios/abe.json":          {Data: []byte("base abe")},
			"scenarios/n90.json":          {Data: []byte("base n90")},
			"configurations/ZNY/N90.json": {Data: []byte("base N90 config")},
		},
		overlay: fstest.MapFS{
			"scenarios/N90.json":          {Data: []byte("overlay n90")},
			"scenarios/new.json":          {Data: []byte("overlay new")},
			"configurations/zny/n90.json": {Data: []byte("overlay N90 config")},
		},
		opened: &pathSet{},
	}

	for path, want := range map[string]string{
		"scenarios/abe.json":          "base abe",
		"scenarios/n90.json":          "overlay n90",
		"scenarios/N90.json":          "overlay n90",
		"scenarios/new.json":          "overlay new",
		"configurations/ZNY/N90.json": "overlay N90 config",
	} {
		if b, err := fs.ReadFile(o, path); err != nil {
			t.Errorf("%s: %v", path, err)
		} else if string(b) != want {
			t.Errorf("%s: got %q, want %q", path, b, want)
		}
	}

	// Replaced files and directories keep the base's spelling.
	var paths []string
	err := fs.WalkDir(o, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"configurations/ZNY/N90.json", "scenarios/abe.json", "scenarios/n90.json", "scenarios/new.json"}
	if !slices.Equal(paths, want) {
		t.Errorf("walk found %v, want %v", paths, want)
	}

	fi, err := o.Stat("scenarios/n90.json")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Name() != "n90.json" || fi.Size() != int64(len("overlay n90")) {
		t.Errorf("Stat gave %q with size %d, want the overlay's n90.json", fi.Name(), fi.Size())
	}

	if _, err := o.Open("scenarios/missing.json"); err == nil {
		t.Error("opened a file in neither file system")
	}
}

func TestMirrorResourceDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Configurations"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := MirrorResourceDirs(dir); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{"videomaps", "briefs/ZNY", "traffic/timetables", "Configurations/ZNY"} {
		if !IsDir(filepath.Join(dir, filepath.FromSlash(d))) {
			t.Errorf("%s wasn't created", d)
		}
	}
	if IsDir(filepath.Join(dir, "models")) {
		t.Error("models was mirrored")
	}

	// A directory that already exists with its name in a different case is
	// used rather than having a second copy made.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(FilterSlice(entries, func(e os.DirEntry) bool { return strings.EqualFold(e.Name(), "configurations") })); n != 1 {
		t.Errorf("%d configurations directories, want 1", n)
	}

	// An overlay directory that has been removed isn't made again.
	gone := filepath.Join(t.TempDir(), "gone")
	if err := MirrorResourceDirs(gone); err == nil {
		t.Error("no error for a missing overlay directory")
	}
	if _, err := os.Stat(gone); err == nil {
		t.Error("the missing overlay directory was made")
	}
}

func TestUnreadOverlayFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"scenarios/abe.json", "scenarios/n90.jsn", "scenario/n90.json",
		"scenarios/abe.json~", "scenarios/#abe.json#", ".DS_Store", ".git/config"} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	SetResourcesOverlay(dir)
	t.Cleanup(func() { SetResourcesOverlay("") })

	if _, err := fs.ReadFile(GetResourcesFS(), "scenarios/ABE.json"); err != nil {
		t.Fatal(err)
	}
	// Listing a directory doesn't read the files in it.
	if _, err := fs.ReadDir(GetResourcesFS(), "scenarios"); err != nil {
		t.Fatal(err)
	}

	unread, err := UnreadOverlayFiles()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"scenario/n90.json", "scenarios/n90.jsn"}; !slices.Equal(unread, want) {
		t.Errorf("unread files %v, want %v", unread, want)
	}
}
