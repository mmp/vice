// resources_local.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for local builds (e.g. for regular development) where we just want to
// grab the resources from resources/
//go:build !downloadresources

package main

import (
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
)

func SyncResources(plat platform.Platform, r renderer.Renderer, lg *log.Logger) error {
	// Nothing to do!
	return nil
}
