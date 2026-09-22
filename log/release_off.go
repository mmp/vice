// log/release_off.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !release

package log

// ReleaseBuild is false for builds developers make locally, as opposed to
// official builds made with the release tag.
const ReleaseBuild = false
