// eram/eram_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"image"
	"testing"

	"github.com/mmp/vice/platform"
)

type fakeCursor struct {
	img       *image.RGBA
	destroyed bool
}

func (c *fakeCursor) SetOverride() {}
func (c *fakeCursor) Destroy()     { c.destroyed = true }

// cursorPlatform records the cursors made with it.
type cursorPlatform struct {
	platform.Platform
	cursors []*fakeCursor
}

func (p *cursorPlatform) CreateCursorFromImage(img *image.RGBA, _, _ int) (platform.Cursor, error) {
	c := &fakeCursor{img: img}
	p.cursors = append(p.cursors, c)
	return c, nil
}

// The cursors are made again at the BRIGHT menu's CURSOR brightness, which
// scales their colors but not their transparency.
func TestCursorBrightness(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	copy(src.Pix, []uint8{200, 100, 50, 255})
	ep := &Scope{cursorImages: map[string]cursorImage{"Eram1": {img: src}}}
	pl := &cursorPlatform{}

	ep.makeCursors(pl, 100)
	ep.makeCursors(pl, 50)

	if got := pl.cursors[1].img.Pix; [4]uint8(got) != [4]uint8{100, 50, 25, 255} {
		t.Errorf("cursor at brightness 50 is %v", got)
	}
	if [4]uint8(src.Pix) != [4]uint8{200, 100, 50, 255} {
		t.Errorf("making a dimmer cursor changed its image to %v", src.Pix)
	}
	if !pl.cursors[0].destroyed || ep.cursorBrightness != 50 {
		t.Errorf("the cursor it replaced wasn't destroyed, or the brightness %v wasn't recorded", ep.cursorBrightness)
	}
}
