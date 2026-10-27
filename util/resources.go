// util/resources.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"
)

var resourcesFS *fs.StatFS

var resourcesOnce sync.Once

func InitResources() {
	resourcesOnce.Do(func() {
		resourcesFS = initResourcesFS()
	})
}

// resourcesOverlay is the overlay directory set with SetResourcesOverlay
// along with the files in it that have been opened.
type resourcesOverlay struct {
	dir    string
	opened pathSet
}

var currentResourcesOverlay atomic.Pointer[resourcesOverlay]

// SetResourcesOverlay makes the files in dir take the place of the resource
// files at the same paths and adds the others to the resources. An empty dir
// removes the overlay. Reads of the resources that start after it returns
// see the new overlay.
func SetResourcesOverlay(dir string) {
	if dir == "" {
		currentResourcesOverlay.Store(nil)
	} else {
		currentResourcesOverlay.Store(&resourcesOverlay{dir: dir})
	}
}

// GetResourcesFS returns the file system that holds vice's resources: the
// resources directory with the overlay directory, if any, on top of it.
func GetResourcesFS() fs.StatFS {
	InitResources()
	if o := currentResourcesOverlay.Load(); o != nil {
		return overlayFS{base: *resourcesFS, overlay: os.DirFS(o.dir), opened: &o.opened}
	}
	return *resourcesFS
}

// UnreadOverlayFiles returns the files in the overlay directory that
// haven't been read since it was set. Once the resources have been loaded,
// these are likely misnamed or misplaced, since vice reads everything
// facility engineering involves at startup. Hidden files and editor backups
// are skipped.
func UnreadOverlayFiles() ([]string, error) {
	o := currentResourcesOverlay.Load()
	if o == nil {
		return nil, nil
	}

	var unread []string
	err := fs.WalkDir(os.DirFS(o.dir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || strings.HasPrefix(name, "#") || strings.HasSuffix(name, "~") {
			return nil
		}
		if !o.opened.contains(p) {
			unread = append(unread, p)
		}
		return nil
	})
	return unread, err
}

// MirrorResourceDirs creates each of the resources directory's directories
// in dir, so that someone filling in an overlay directory can see where its
// files go. dir itself must already exist. The models directory isn't
// mirrored: the speech models are read from their paths on disk, not
// through the overlay.
func MirrorResourceDirs(dir string) error {
	InitResources()
	base, overlay := *resourcesFS, os.DirFS(dir)

	return fs.WalkDir(base, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == "." {
			return err
		}
		if p == "models" {
			return fs.SkipDir
		}
		if _, ok := findFold(overlay, p); ok {
			return nil
		}
		// Make it inside its parent as the overlay spells the parent's
		// name. WalkDir visits the parent first, so it is missing only if
		// dir is.
		parent, ok := findFold(overlay, path.Dir(p))
		if !ok {
			return &fs.PathError{Op: "stat", Path: dir, Err: fs.ErrNotExist}
		}
		return os.Mkdir(filepath.Join(dir, filepath.FromSlash(path.Join(parent, path.Base(p)))), 0o755)
	})
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
	f, err := fs.ReadFile(GetResourcesFS(), path)
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

// LoadResourceBytes returns the contents of the specified resource file,
// decompressing it if it is zstd compressed. It panics if the file is not
// found.
func LoadResourceBytes(path string) []byte {
	if filepath.Ext(path) != ".zst" {
		// Read it directly rather than going through LoadResource and
		// io.ReadAll, which would make a second copy of the whole file with
		// a doubling buffer; that costs over a gigabyte for the models.
		b, err := fs.ReadFile(GetResourcesFS(), path)
		if err != nil {
			panic(err)
		}
		return b
	}

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
	_, err := GetResourcesFS().Stat(path)
	return err == nil
}

func WalkResources(root string, fn func(path string, d fs.DirEntry, filesystem fs.FS, err error) error) error {
	rfs := GetResourcesFS()
	return fs.WalkDir(rfs, root,
		func(path string, d fs.DirEntry, err error) error {
			return fn(path, d, rfs, err)
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
