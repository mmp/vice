// platform/glfw/cursor.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package glfw

import (
	"fmt"
	"image"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"

	glfw3 "github.com/go-gl/glfw/v3.4/glfw"
)

// cursor implements platform.Cursor. It keeps a reference to the platform
// that created it so that destroying it can clear the platform's references
// to the underlying GLFW cursor.
type cursor struct {
	g      *glfwPlatform
	handle *glfw3.Cursor
}

func (c *cursor) SetOverride() {
	c.g.cursorOverride = c.handle
}

func (c *cursor) Destroy() {
	if c.handle == nil {
		return
	}
	if c.g.cursorOverride == c.handle {
		c.g.cursorOverride = nil
	}
	if c.g.currentCursor == c.handle {
		c.g.currentCursor = nil
	}
	c.handle.Destroy()
	c.handle = nil
}

func (g *glfwPlatform) ClearCursorOverride() {
	g.cursorOverride = nil
}

func (g *glfwPlatform) CreateCursorFromImage(img *image.RGBA, hotspotX, hotspotY int) (platform.Cursor, error) {
	if img == nil {
		return nil, fmt.Errorf("cursor image is nil")
	}
	w := img.Rect.Dx()
	h := img.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("cursor image has invalid size")
	}
	hotspotX = math.Clamp(hotspotX, 0, w-1)
	hotspotY = math.Clamp(hotspotY, 0, h-1)
	handle := glfw3.CreateCursor(img, hotspotX, hotspotY)
	if handle == nil {
		return nil, fmt.Errorf("failed to create cursor")
	}
	return &cursor{g: g, handle: handle}, nil
}
