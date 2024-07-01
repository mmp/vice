// util.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////

func getResourcesFS() fs.StatFS {
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
		lg.Infof("%s: resources directory", dir)
		return fsys
	}

	// Try CWD (this is useful for development and debugging but shouldn't
	// be needed for release builds.
	lg.Infof("Trying CWD for resources FS")

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	dir = filepath.Join(wd, "resources")

	fsys, ok = os.DirFS(dir).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}

	if check(fsys) {
		return fsys
	}
	panic("unable to find videomaps in CWD")
}

// LoadResource loads the specified file from the resources directory, decompressing it if
// it is zstd compressed. It panics if the file is not found; missing resources are pretty
// much impossible to recover from.
func LoadResource(path string) []byte {
	b, err := fs.ReadFile(resourcesFS, path)
	if err != nil {
		panic(err)
	}

	if filepath.Ext(path) == ".zst" {
		s, err := util.DecompressZstd(string(b))
		if err != nil {
			panic(err)
		}
		return []byte(s)
	}

	return b
}
