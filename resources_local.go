// resources_local.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for local builds (e.g. for regular development) where we just want to
// grab the resources from resources/
//go:build !downloadresources

package main

import (
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
)

func SyncResources(plat platform.Platform, r renderer.Renderer, lg *log.Logger) {
	// Nothing to do!
}
