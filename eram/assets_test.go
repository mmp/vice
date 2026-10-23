// eram/assets_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/platform/audio"
)

type fakeCursor struct {
	img       *image.RGBA
	destroyed bool
}

func (c *fakeCursor) SetOverride() {}
func (c *fakeCursor) Destroy()     { c.destroyed = true }

// cursorPlatform records the cursors made with it, or fails to make them.
type cursorPlatform struct {
	platform.Platform
	cursors []*fakeCursor
	fail    bool
}

func (p *cursorPlatform) CreateCursorFromImage(img *image.RGBA, _, _ int) (platform.Cursor, error) {
	if p.fail {
		return nil, errors.New("no cursor")
	}
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

	ep.makeCursors(pl, 100, nil)
	ep.makeCursors(pl, 50, nil)

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

// A cursor that can't be made at a new brightness keeps the one made
// before.
func TestCursorCreationFailure(t *testing.T) {
	ep := &Scope{cursorImages: map[string]cursorImage{"Eram1": {img: image.NewRGBA(image.Rect(0, 0, 1, 1))}}}
	pl := &cursorPlatform{}
	ep.makeCursors(pl, 100, nil)
	pl.fail = true
	ep.makeCursors(pl, 50, nil)

	if c := ep.eramCursors["Eram1"]; c != pl.cursors[0] || pl.cursors[0].destroyed {
		t.Errorf("after a failure the cursor is %v, destroyed %v; want the one made before", c, pl.cursors[0].destroyed)
	}
}

// The alarm tone lasts half a second, peaks in proportion to its volume,
// and fades in and out rather than starting and stopping abruptly.
func TestAlarmTone(t *testing.T) {
	samples := func(pcm []byte) []int {
		s := make([]int, len(pcm)/2)
		for i := range s {
			s[i] = int(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		}
		return s
	}
	peak := func(s []int) int {
		p := 0
		for _, v := range s {
			p = max(p, v, -v)
		}
		return p
	}

	loud, quiet := samples(makeAlarmTone(1)), samples(makeAlarmTone(0.2))
	if len(loud) != audio.SampleRate/2 {
		t.Errorf("tone is %d samples long, want %d", len(loud), audio.SampleRate/2)
	}
	if p := peak(loud); p < alarmPeakAmplitude*99/100 || p > alarmPeakAmplitude {
		t.Errorf("loudest tone peaks at %d, want %d", p, alarmPeakAmplitude)
	}
	if p, want := peak(quiet), alarmPeakAmplitude/5; p < want*99/100 || p > want {
		t.Errorf("quietest tone peaks at %d, want %d", p, want)
	}
	for _, i := range []int{0, len(loud) - 1} {
		if loud[i] != 0 {
			t.Errorf("sample %d is %d, want the tone faded to 0", i, loud[i])
		}
	}
}
