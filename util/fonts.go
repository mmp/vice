// util/fonts.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var fontsFS fs.StatFS

var fontsOnce sync.Once

func initFontsFS() {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}

	dir := filepath.Dir(path)
	if runtime.GOOS == "darwin" {
		dir = filepath.Clean(filepath.Join(dir, "..", "Resources"))
	}

	// Is there a "fonts" directory in the FS?
	check := func(fs fs.StatFS) bool {
		info, err := fs.Stat("fonts")
		return err == nil && info.IsDir()
	}

	fsys := os.DirFS(dir).(fs.StatFS)
	if check(fsys) {
		fontsFS = fsys
		return
	}

	dir, err = os.Getwd()
	if err != nil {
		panic(err)
	}

	// Try CWD as well the two directories above it.
	for range 3 {
		fsys, ok := os.DirFS(dir).(fs.StatFS)
		if !ok {
			panic("FS from DirFS is not a StatFS?")
		}

		if _, err := fsys.Stat("fonts"); err == nil { // got it
			fontsFS = fsys
			return
		}

		dir = filepath.Join(dir, "..")
	}

	panic("unable to find fonts")
}

// LoadFontBytes returns the contents of a file in the fonts directory,
// decompressing it. It panics if the file is not there: the fonts are part
// of the distribution, so a missing one is a broken install.
func LoadFontBytes(name string) []byte {
	fontsOnce.Do(initFontsFS)

	b, err := fs.ReadFile(fontsFS, "fonts/"+name)
	if err != nil {
		panic(err)
	}

	zr, err := zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderConcurrency(0))
	if err != nil {
		panic(err)
	}

	b, err = io.ReadAll(zr)
	if err != nil {
		panic(err)
	}

	zr.Close()

	return b
}
