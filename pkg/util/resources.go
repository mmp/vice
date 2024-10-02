// pkg/util/resources.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

func initResourcesFS() *fs.StatFS {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}

	dir := filepath.Dir(path)
	if runtime.GOOS == "darwin" {
		dir = filepath.Clean(filepath.Join(dir, "..", "Resources"))
	} else {
		dir = filepath.Join(dir, "resources")
	}

	fsys, ok := os.DirFS(dir).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}

	check := func(fs fs.StatFS) bool {
		_, errv := fsys.Stat("videomaps")
		_, errs := fsys.Stat("scenarios")
		return errv == nil && errs == nil
	}

	if check(fsys) {
		return &fsys
	}

	// Try CWD as well as CWD/../..; these are useful for development and
	// debugging but shouldn't be needed for release builds.
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for _, alts := range []string{".", "../.."} {
		dir = filepath.Join(wd, alts, "resources")

		fsys, ok = os.DirFS(dir).(fs.StatFS)
		if !ok {
			panic("FS from DirFS is not a StatFS?")
		}

		if check(fsys) {
			return &fsys
		}
	}
	panic("unable to find videomaps in CWD")
}

var resourcesFS *fs.StatFS

func init() {
	resourcesFS = initResourcesFS()
}

func GetResourcesFS() fs.StatFS {
	return *resourcesFS
}

// LoadResource loads the specified file from the resources directory, decompressing it if
// it is zstd compressed. It panics if the file is not found; missing resources are pretty
// much impossible to recover from.
func LoadResource(path string) []byte {
	b := LoadRawResource(path)

	if filepath.Ext(path) == ".zst" {
		s, err := DecompressZstd(string(b))
		if err != nil {
			panic(err)
		}
		return []byte(s)
	}

	return b
}

func LoadRawResource(path string) []byte {
	b, err := fs.ReadFile(*resourcesFS, path)
	if err != nil {
		panic(err)
	}

	return b
}

func GetResourceReader(path string) (io.ReadCloser, error) {
	if r, err := (*resourcesFS).Open(path); err == nil {
		return r.(io.ReadCloser), nil
	} else {
		return nil, err
	}
}

func WalkResources(root string, fn func(path string, d fs.DirEntry, filesystem fs.FS, err error) error) error {
	return fs.WalkDir(*resourcesFS, root,
		func(path string, d fs.DirEntry, err error) error {
			return fn(path, d, *resourcesFS, err)
		})
}
