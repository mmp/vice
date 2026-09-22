// util/resources_download.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for builds that are expected to fetch resources as needed
// into a local cache from cloud storage.
//go:build release

package util

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mmp/vice/log"
)

// configResourcesDir returns the directory the resource files are
// downloaded to.
func configResourcesDir() (string, error) {
	dir, err := log.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "resources"), nil
}

func initResourcesFS() *fs.StatFS {
	dir, err := configResourcesDir()
	if err != nil {
		panic(err)
	}
	resourcesBasePath = dir

	fsys, ok := os.DirFS(resourcesBasePath).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}
	return &fsys
}
