// pkg/util/resources.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/klauspost/compress/zstd"
)

var resourcesFS *fs.StatFS

func init() {
	resourcesFS = initResourcesFS()
}

func GetResourcesFS() fs.StatFS {
	return *resourcesFS
}

// Unfortunately, unlike io.ReadCloser, the zstd Decoder's Close() method
// doesn't return an error, so we need to make our own custom ReadCloser
// interface.
type ResourceReadCloser interface {
	io.Reader
	Close()
}

type bytesReadCloser struct {
	*bytes.Reader
}

func (bytesReadCloser) Close() {}

// LoadResource provides a ResourceReadCloser to access the specified file from
// the resources directory; if it's zstd compressed, the Reader will
// handle decompression transparently. It panics if the file is not found
// since missing resources are pretty much impossible to recover from.
func LoadResource(path string) ResourceReadCloser {
	f, err := fs.ReadFile(*resourcesFS, path)
	if err != nil {
		panic(err)
	}
	br := bytesReadCloser{bytes.NewReader(f)}

	if filepath.Ext(path) == ".zst" {
		zr, err := zstd.NewReader(br)
		if err != nil {
			panic(err)
		}
		return zr
	}

	return br
}

func LoadResourceBytes(path string) []byte {
	r := LoadResource(path)
	defer r.Close()

	b, err := io.ReadAll(r)
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

func ExecutableResourcesFS() *fs.StatFS {
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
