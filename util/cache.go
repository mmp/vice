// util/cache.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

func fullCachePath(path string) (string, error) {
	cd, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cd, "Vice", path), nil
}

var (
	// Pool a limited number of them to keep memory use under control.
	zstdEncoders     chan *zstd.Encoder
	zstdEncodersOnce sync.Once
)

func initZstdEncoders(n int) error {
	zstdEncoders = make(chan *zstd.Encoder, n)
	for range n {
		ze, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1))
		if err != nil {
			return err
		}
		zstdEncoders <- ze
	}
	return nil
}

func CacheStoreObject(path string, obj any) error {
	var err error
	zstdEncodersOnce.Do(func() { err = initZstdEncoders(2) })
	if err != nil {
		return err
	}

	path, err = fullCachePath(path)
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

	zw := <-zstdEncoders
	defer func() { zstdEncoders <- zw }()
	zw.Reset(f)

	if err := msgpack.NewEncoder(zw).Encode(obj); err != nil {
		f.Close()
		return err
	} else if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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

	zr, err := zstd.NewReader(f)
	if err != nil {
		return time.Time{}, err
	}
	defer zr.Close()

	return fi.ModTime(), msgpack.NewDecoder(zr).Decode(obj)
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
