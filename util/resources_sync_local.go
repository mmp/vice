// util/resources_sync_local.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for local builds (e.g. for regular development) where the
// resources come from resources/ in the source tree and so never need syncing.
//go:build !release

package util

// SyncResources is a no-op in local builds.
func SyncResources(ui SyncUI) error {
	return nil
}
