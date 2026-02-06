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
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(0))
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

// ResourceExists returns true if the specified resource file exists.
func ResourceExists(path string) bool {
	_, err := (*resourcesFS).Stat(path)
	return err == nil
}

func WalkResources(root string, fn func(path string, d fs.DirEntry, filesystem fs.FS, err error) error) error {
	return fs.WalkDir(*resourcesFS, root,
		func(path string, d fs.DirEntry, err error) error {
			return fn(path, d, *resourcesFS, err)
		})
}

// resourcesBasePath caches the base path to resources directory
var resourcesBasePath string

// findResourcesBasePath locates the resources directory by checking
// CWD and up to two parent directories.
func findResourcesBasePath() string {
	if resourcesBasePath != "" {
		return resourcesBasePath
	}

	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	// Try CWD as well as the two directories above it
	for range 3 {
		candidate := filepath.Join(dir, "resources")
		if _, err := os.Stat(filepath.Join(candidate, "videomaps")); err == nil {
			resourcesBasePath = candidate
			return resourcesBasePath
		}
		dir = filepath.Join(dir, "..")
	}

	panic("unable to find resources directory")
}

// GetResourcePath returns the absolute filesystem path to a resource.
// This is needed for libraries that require file paths rather than embedded data.
// Panics if the resources directory cannot be found.
func GetResourcePath(path string) string {
	return filepath.Join(findResourcesBasePath(), path)
}

func localResourcesFS() *fs.StatFS {
	basePath := findResourcesBasePath()
	fsys, ok := os.DirFS(basePath).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}
	return &fsys
}
