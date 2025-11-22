// pkg/renderer/renderer_windows.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build windows

package renderer

import "github.com/mmp/vice/log"

// NewRenderer creates a new DirectX 9 renderer for Windows.
func NewRenderer(hwnd uintptr, lg *log.Logger) (Renderer, error) {
	return NewDirectX9Renderer(hwnd, lg)
}
