// stars/commands_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

type inputPlatform struct{ platform.Platform }

func (inputPlatform) EndCaptureMouse()    {}
func (inputPlatform) StopMouseDeltaMode() {}

// trackSelectionHarness drives Scope.consumeMouseEvents with a [SLEW]
// handler installed so that tests can see which track a click selects.
type trackSelectionHarness struct {
	sp         *Scope
	ctx        *scope.Context
	transforms scope.Transformations
	selected   []av.ADSBCallsign
}

// makeTrackSelectionHarness places a track at each of the given window
// positions; the tracks are named by their index: "T0", "T1", ...
func makeTrackSelectionHarness(dwell DwellMode, positions ...[2]float32) *trackSelectionHarness {
	ctx := &scope.Context{
		DrawExtent:     math.Extent2D{P1: [2]float32{1000, 1000}},
		DrawPixelScale: 1,
		Platform:       inputPlatform{},
		Mouse:          &platform.MouseState{},
		Client:         &client.ControlClient{},
	}
	h := &trackSelectionHarness{
		sp:         &Scope{TrackState: make(map[av.ADSBCallsign]*TrackState), prefSet: &PreferenceSet{}},
		ctx:        ctx,
		transforms: scope.GetTransformations(ctx.DrawExtent, 45, math.Point2LL{-73, 40}, 50, 0),
	}
	h.sp.prefSet.Current.DwellMode = dwell

	for i, p := range positions {
		cs := av.ADSBCallsign("T" + string(rune('0'+i)))
		h.sp.visibleTracks = append(h.sp.visibleTracks, sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: cs}})
		h.sp.TrackState[cs] = &TrackState{track: av.RadarTrack{Location: h.transforms.LatLongFromWindowP(p)}}
	}
	return h
}

// moveTo moves the cursor without clicking.
func (h *trackSelectionHarness) moveTo(p [2]float32) {
	*h.ctx.Mouse = platform.MouseState{Pos: p}
	h.sp.consumeMouseEvents(h.ctx, nil, h.transforms, nil)
}

func (h *trackSelectionHarness) clickAt(p [2]float32) {
	if len(h.sp.transientCommandHandlers) == 0 {
		h.sp.installCommandHandlers(makeCommandHandlers("[SLEW]", func(sp *Scope, trk *sim.Track) {
			h.selected = append(h.selected, trk.ADSBCallsign)
		}))
	}
	*h.ctx.Mouse = platform.MouseState{Pos: p}
	h.ctx.Mouse.Released[platform.MouseButtonPrimary] = true
	h.sp.consumeMouseEvents(h.ctx, nil, h.transforms, nil)
}

func (h *trackSelectionHarness) expectSelected(t *testing.T, want ...av.ADSBCallsign) {
	t.Helper()
	if !slices.Equal(h.selected, want) {
		t.Fatalf("selected %v, want %v", h.selected, want)
	}
}

func TestMultiTrackSelection(t *testing.T) {
	t0, t1 := [2]float32{500, 500}, [2]float32{510, 500}

	t.Run("dwell on selects the closest track", func(t *testing.T) {
		h := makeTrackSelectionHarness(DwellModeOn, t0, t1)
		h.clickAt([2]float32{507, 500})
		h.expectSelected(t, "T1")
	})

	t.Run("dwell off with one nearby track selects it", func(t *testing.T) {
		h := makeTrackSelectionHarness(DwellModeOff, t0, [2]float32{600, 500})
		h.clickAt([2]float32{503, 500})
		h.expectSelected(t, "T0")
	})

	t.Run("dwell off with closely spaced tracks", func(t *testing.T) {
		h := makeTrackSelectionHarness(DwellModeOff, t0, t1)

		h.clickAt([2]float32{503, 500})
		h.expectSelected(t)
		if h.sp.previewAreaOutput != "MULTIPLE TRK" {
			t.Errorf("preview area output %q, want MULTIPLE TRK", h.sp.previewAreaOutput)
		}
		if h.sp.multiTrackHighlight != "T0" {
			t.Errorf("highlighted %q, want T0", h.sp.multiTrackHighlight)
		}

		h.moveTo([2]float32{508, 500})
		if h.sp.multiTrackHighlight != "T1" {
			t.Errorf("after moving the cursor highlighted %q, want T1", h.sp.multiTrackHighlight)
		}

		h.clickAt([2]float32{508, 500})
		h.expectSelected(t, "T1")
		if h.sp.multiTrackSelection || h.sp.multiTrackHighlight != "" {
			t.Errorf("multi-track selection still engaged after selecting a track")
		}

		// The next ambiguous click starts over.
		h.clickAt([2]float32{503, 500})
		h.expectSelected(t, "T1")
		if !h.sp.multiTrackSelection {
			t.Errorf("multi-track selection not engaged by second ambiguous click")
		}
	})

	t.Run("clear disengages multi-track selection", func(t *testing.T) {
		h := makeTrackSelectionHarness(DwellModeOff, t0, t1)
		h.clickAt([2]float32{503, 500})
		h.sp.resetInputState(h.ctx.Platform)
		if h.sp.multiTrackSelection || h.sp.multiTrackHighlight != "" {
			t.Errorf("multi-track selection still engaged after reset")
		}
	})
}
