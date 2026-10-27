// util/fs.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
)

type RootFS struct{}

func (r RootFS) Open(filename string) (fs.File, error) {
	return os.Open(filename)
}

// IsDir reports whether path names an existing directory.
func IsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// overlayFS presents the files in overlay along with the ones in base. A
// file in overlay takes the place of the one in base at the same path.
// Paths are compared without regard to case, so that the outcome doesn't
// depend on whether the operating system's file system ignores case. A
// replaced file keeps base's spelling of its name so that references to it
// still find it.
type overlayFS struct {
	base, overlay fs.FS
	// opened records the paths of the files in overlay that are opened.
	opened *pathSet
}

func (o overlayFS) Open(name string) (fs.File, error) {
	if p, ok := findFold(o.overlay, name); ok {
		o.opened.add(p)
		return o.overlay.Open(p)
	}
	return o.base.Open(name)
}

func (o overlayFS) Stat(name string) (fs.FileInfo, error) {
	if p, ok := findFold(o.overlay, name); ok {
		fi, err := fs.Stat(o.overlay, p)
		if err != nil {
			return nil, err
		}
		return renamedFileInfo{FileInfo: fi, name: path.Base(name)}, nil
	}
	return fs.Stat(o.base, name)
}

func (o overlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	base, err := fs.ReadDir(o.base, name)
	p, ok := findFold(o.overlay, name)
	if !ok {
		return base, err
	}
	entries, err := fs.ReadDir(o.overlay, p)
	if err != nil {
		return nil, err
	}

	n := len(entries)
	for _, b := range base {
		if i := indexFold(entries[:n], b.Name()); i == -1 {
			entries = append(entries, b)
		} else if entries[i].Name() != b.Name() {
			fi, err := entries[i].Info()
			if err != nil {
				return nil, err
			}
			entries[i] = fs.FileInfoToDirEntry(renamedFileInfo{FileInfo: fi, name: b.Name()})
		}
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// pathSet is a set of paths that is safe for concurrent use.
type pathSet struct {
	mu    sync.Mutex
	paths map[string]bool
}

func (s *pathSet) add(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths == nil {
		s.paths = make(map[string]bool)
	}
	s.paths[p] = true
}

func (s *pathSet) contains(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[p]
}

// renamedFileInfo is the FileInfo of a file that overlayFS presents under a
// name spelled differently than the one it has on disk.
type renamedFileInfo struct {
	fs.FileInfo
	name string
}

func (fi renamedFileInfo) Name() string { return fi.name }

// findFold returns the path of the file in fsys that name refers to when
// the case of each of its elements is ignored.
func findFold(fsys fs.FS, name string) (string, bool) {
	if name == "." {
		_, err := fs.Stat(fsys, name)
		return name, err == nil
	}

	p := "."
	for elem := range strings.SplitSeq(name, "/") {
		entries, err := fs.ReadDir(fsys, p)
		if err != nil {
			return "", false
		}
		i := indexFold(entries, elem)
		if i == -1 {
			return "", false
		}
		p = path.Join(p, entries[i].Name())
	}
	return p, true
}

// indexFold returns the index of the entry with the given name, ignoring
// case, or -1 if there is none.
func indexFold(entries []fs.DirEntry, name string) int {
	return slices.IndexFunc(entries, func(e fs.DirEntry) bool { return strings.EqualFold(e.Name(), name) })
}
