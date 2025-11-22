// pkg/renderer/renderer_other.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !windows

package renderer

import "github.com/mmp/vice/log"

// NewRenderer creates a new OpenGL 2 renderer for non-Windows platforms.
func NewRenderer(_ uintptr, lg *log.Logger) (Renderer, error) {
	return NewOpenGL2Renderer(lg)
}
