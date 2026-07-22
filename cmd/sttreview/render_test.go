package main

import (
	"strings"
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
			Reason:     "the airport advisory is at the eleven o'clock, so AP/11/12 is the right reading of the garbled field call",
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

	// Both the compact view (bottom note) and the context pane (inline note)
	// must render the reason without panicking at any width.
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
			screen.Show()
			if w == 120 && !screenContains(screen, "Why:") {
				t.Errorf("reason note not rendered at width %d (context=%v)", w, ctx)
			}
			screen.Fini()
		}
	}
}

// TestSaveEntryCarriesReason checks that the diagnosis rationale is written
// into a saved test only when the reviewer accepted the suggestion verbatim.
func TestSaveEntryCarriesReason(t *testing.T) {
	dir := t.TempDir()
	base := LogEntry{
		Transcript: "test transcript alpha bravo",
		Callsign:   "AAL14",
		Command:    "D40",
		Suggested:  "AAL312 D40 ED",
		Reason:     "[proposed fix] garbled American three twelve, not American fourteen",
		STTAircraft: map[string]stt.Aircraft{
			"a": {Callsign: "AAL312"},
			"b": {Callsign: "AAL14"},
		},
	}

	// Accepted: correction equals the suggestion → reason carried through,
	// with the suggested output installed.
	p, err := saveEntry(base, base.Suggested, dir)
	if err != nil {
		t.Fatal(err)
	}
	tf, err := stt.LoadTestFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if tf.Reason == "" {
		t.Error("accepted suggestion should carry the reason into the test")
	}
	if tf.Callsign != "AAL312" || tf.Command != "D40 ED" {
		t.Errorf("saved output = %q %q, want AAL312 D40 ED", tf.Callsign, tf.Command)
	}
	if tf.Suggested != "" {
		t.Errorf("suggested should be stripped, got %q", tf.Suggested)
	}

	// Overridden: reviewer typed a different correction → reason dropped so a
	// later pass isn't misled by a rationale for the rejected answer.
	p2, err := saveEntry(base, "AAL14 D40", dir)
	if err != nil {
		t.Fatal(err)
	}
	tf2, err := stt.LoadTestFile(p2)
	if err != nil {
		t.Fatal(err)
	}
	if tf2.Reason != "" {
		t.Errorf("overridden correction should drop the reason, got %q", tf2.Reason)
	}
}

// screenContains reports whether the simulation screen's cells contain sub.
func screenContains(screen tcell.SimulationScreen, sub string) bool {
	cells, w, h := screen.GetContents()
	var b []rune
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b = append(b, cells[y*w+x].Runes...)
		}
		b = append(b, '\n')
	}
	return strings.Contains(string(b), sub)
}
