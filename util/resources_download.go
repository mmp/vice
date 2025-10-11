// pkg/util/resources_download.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for builds that are expected to fetch resources as needed
// into a local cache from cloud storage.
//go:build downloadresources

package util

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func initResourcesFS() *fs.StatFS {
	fsys, ok := os.DirFS(GetResourcesFolderPath()).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}
	return &fsys
}

func GetResourcesFolderPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		panic(fmt.Sprintf("failed to get user config dir: %v", err))
	}

	return filepath.Join(configDir, "vice", "resources")
}
