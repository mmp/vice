// util/cache.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"compress/flate"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func fullCachePath(path string) (string, error) {
	cd, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cd, "Vice", path), nil
}

func CacheStoreObject(path string, obj any) error {
	path, err := fullCachePath(path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fw, err := flate.NewWriter(f, flate.BestSpeed)
	if err != nil {
		return err
	}

	if err := msgpack.NewEncoder(fw).Encode(obj); err != nil {
		return err
	}
	return fw.Close()
}

func CacheRetrieveObject(path string, obj any) (time.Time, error) {
	path, err := fullCachePath(path)
	if err != nil {
		return time.Time{}, err
	}

	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}

	fr := flate.NewReader(f)
	defer fr.Close()

	return fi.ModTime(), msgpack.NewDecoder(fr).Decode(obj)
}

func CacheCullObjects(maxBytes int64) error {
	cacheDir, err := fullCachePath("")
	if err != nil {
		return err
	}

	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return nil // Nothing to cull
	}

	type fileInfo struct {
		path    string
		size    int64
		modTime time.Time
	}
	var files []fileInfo
	var totalSize int64

	err = filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			files = append(files, fileInfo{
				path:    path,
				size:    info.Size(),
				modTime: info.ModTime(),
			})
			totalSize += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Sort files by modification time, oldest first
	slices.SortFunc(files, func(a, b fileInfo) int {
		return a.modTime.Compare(b.modTime)
	})

	// Remove files oldest to newest until we're under the limit
	for len(files) > 0 && totalSize > maxBytes {
		f := files[0]
		if err := os.Remove(f.path); err == nil {
			totalSize -= f.size
		}
		files = files[1:]
	}

	return nil
}
