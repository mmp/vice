// wx/store_release.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build release

package wx

import "github.com/mmp/vice/log"

// gcsStore returns nil: release builds get their weather from the vice server,
// so they leave out the GCS client and its oauth2 dependency entirely.
func gcsStore(*log.Logger) ObjectStore { return nil }
