// log/release.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build release

package log

// ReleaseBuild is true for builds made with the release tag (i.e. official
// builds), as opposed to ones developers build locally.
const ReleaseBuild = true
