// pkg/util/resources_local.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for local builds (e.g. for regular development) where we just want to
// grab the resources from resources/
//go:build !downloadresources

package util

import (
	"io/fs"
	"os"
	"path/filepath"
)

func initResourcesFS() *fs.StatFS {
	dir := GetResourcesFolderPath()
	fsys, ok := os.DirFS(dir).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}
	return &fsys
}

func GetResourcesFolderPath() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	// Try CWD as well the two directories above it.
	for range 3 {
		resourcesPath := filepath.Join(dir, "resources")

		// Check if this directory contains the expected subdirectories
		if _, err := os.Stat(filepath.Join(resourcesPath, "videomaps")); err == nil {
			if _, err := os.Stat(filepath.Join(resourcesPath, "scenarios")); err == nil {
				return resourcesPath
			}
		}

		dir = filepath.Join(dir, "..")
	}
	panic("unable to find resources directory with videomaps and scenarios")
}
