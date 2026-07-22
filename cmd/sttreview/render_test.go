package main

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/mmp/vice/stt"
)

// TestRenderNarrowWidths drives render across a range of terminal widths,
// including the ~61-68 band where the correction column width used to go
// negative and panic truncate.
func TestRenderNarrowWidths(t *testing.T) {
	state := &AppState{
		entries: []LogEntry{{
			Transcript: "American Fifty Two Zero Seven heavy field this day or eleven o'clock",
			Callsign:   "AAL5207",
			Command:    "AP/11/12",
			Suggested:  "AAL5207 AP/11/12",
			STTAircraft: map[string]stt.Aircraft{
				"American 52 07": {Callsign: "AAL5207"},
			},
		}},
		disposition:  map[int]Disposition{},
		savedFiles:   map[int]string{},
		focusedField: FocusCorrection,
	}
	state.initFromEntry(0)
	state.updateSearchFilter()

	for _, ctx := range []bool{false, true} {
		state.showContext = ctx
		for w := 20; w <= 200; w++ {
			screen := tcell.NewSimulationScreen("")
			if err := screen.Init(); err != nil {
				t.Fatalf("screen init: %v", err)
			}
			screen.SetSize(w, 40)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("render panicked at width %d (context=%v): %v", w, ctx, r)
					}
				}()
				render(screen, state)
			}()
			screen.Fini()
		}
	}
}
