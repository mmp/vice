// pkg/util/resources_local.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for local builds (e.g. for regular development) where we just want to
// grab the resources from resources/
//go:build !downloadresources

package util

import (
	"io/fs"
)

func initResourcesFS() *fs.StatFS {
	return ExecutableResourcesFS()
}
