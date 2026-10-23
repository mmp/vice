// eram/assets.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"encoding/binary"
	"fmt"
	"image"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/platform/audio"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// The platform resources the scope loads when it is activated: its bitmap
// fonts, its cursors, and its alarm tone.

///////////////////////////////////////////////////////////////////////////
// Fonts

// maxERAMFontSize is the largest size ERAMFont has; larger ones are drawn in
// it.
const maxERAMFontSize = 4

func (ep *Scope) ERAMFont(size int) *renderer.Font {
	// Clamp before indexing: a view whose Font preference was never
	// initialized passes 0, which would otherwise index off the front of
	// systemFont.
	size = math.Clamp(size, 1, maxERAMFontSize)
	if runtime.GOOS == "darwin" {
		if size == 1 {
			return ep.systemFont[10] // Smaller size for macOS
		}
		return ep.systemFont[size-1]
	}
	if size == 4 {
		size = 3 // Missing one font, so skip it for now
	}
	return ep.systemFont[size-1]
}

// fdbLine4Font returns the font of FDB line 4, which FONT's LINE 4 makes up
// to two sizes smaller than the FDB font.
func (ep *Scope) fdbLine4Font() *renderer.Font {
	ps := ep.currentPrefs()
	return ep.ERAMFont(min(ps.FDBSize, maxERAMFontSize) + ps.Line4Size)
}

// fdbPortalFont returns the font of the FDB fields outside the portal
// fence, which FONT's PORTAL makes up to two sizes smaller than the FDB
// font.
func (ep *Scope) fdbPortalFont() *renderer.Font {
	ps := ep.currentPrefs()
	return ep.ERAMFont(min(ps.FDBSize, maxERAMFontSize) + ps.PortalSize)
}

// ERAMToolbarFont returns the font of the toolbars' buttons and panels, in
// the size that FONT's TOOLBAR sets: 1 or 2.
func (ep *Scope) ERAMToolbarFont() *renderer.Font {
	return ep.systemFont[ep.currentPrefs().ToolbarSize-1]
}

func (ep *Scope) ERAMInputFont() *renderer.Font {
	return ep.systemFont[1]
}

func (ep *Scope) initializeFonts(r renderer.Renderer, p platform.Platform) {
	fonts := scope.CreateERAMFonts(r, p.DPIScale())
	get := func(name string, size int) *renderer.Font {
		return scope.FindERAMFont(fonts, name, size)
	}

	// TODO: Find the fifth ERAM text size.
	ep.systemFont[0] = get("EramText-9.pcf", 11)
	ep.systemFont[1] = get("EramText-11.pcf", 13)
	ep.systemFont[2] = get("EramText-14.pcf", 17)
	ep.systemFont[3] = get("EramText-16.pcf", 18)
	ep.systemFont[4] = get("EramText-16.pcf", 18)

	ep.systemFont[5] = get("EramTargets-16.pcf", 15)
	ep.systemFont[6] = get("EramGeomap-16.pcf", 15)
	ep.systemFont[7] = get("EramGeomap-18.pcf", 17)
	ep.systemFont[8] = get("EramGeomap-20.pcf", 19)
	ep.systemFont[9] = get("EramTracks-16.pcf", 15)
	ep.systemFont[10] = get("EramText-8.pcf", 11)

}

// ERAMGeomapFont returns the EramGeomap bitmap font that holds the
// navigational symbol glyphs (VOR / TACAN / airport / etc.) at one of three
// pixel sizes, selected by the videomaps.Symbol Size byte typically stored in
// video maps (1, 2, 3).
func (ep *Scope) ERAMGeomapFont(size int) *renderer.Font {
	switch {
	case size <= 1:
		return ep.systemFont[6]
	case size == 2:
		return ep.systemFont[7]
	default:
		return ep.systemFont[8]
	}
}

// MapSymbolFont and MapLabelFont implement scope.MapFonts so that video map
// features are drawn with the ERAM scope's own fonts.
func (ep *Scope) MapSymbolFont(size int) *renderer.Font { return ep.ERAMGeomapFont(size) }

func (ep *Scope) MapLabelFont(size int) *renderer.Font { return ep.ERAMFont(size) }

///////////////////////////////////////////////////////////////////////////
// Cursors

// cursorImage is a decoded ERAM cursor, kept so that the cursor can be made
// again at another brightness.
type cursorImage struct {
	img     *image.RGBA
	hotspot [2]int
}

// loadCursors loads all .cur files from the eram-cursors resource directory
// and makes the cursors from them at full brightness. Panics if a cursor
// file is missing or malformed: that is a build/install problem, not
// something the user can recover from.
func (ep *Scope) loadCursors(pl platform.Platform, lg *log.Logger) {
	ep.cursorImages = make(map[string]cursorImage)
	cursorSize := int(32*pl.DPIScale() + 0.5)
	err := util.WalkResources("eram-cursors", func(path string, d fs.DirEntry, _ fs.FS, err error) error {
		if err != nil {
			panic(err)
		}
		if d.IsDir() || filepath.Ext(path) != ".cur" {
			return nil
		}
		img, hotspot, err := platform.DecodeCUR(util.LoadResourceBytes(path), cursorSize)
		if err != nil {
			panic(fmt.Sprintf("%s: %v", path, err))
		}
		ep.cursorImages[strings.TrimSuffix(d.Name(), ".cur")] = cursorImage{img: img, hotspot: hotspot}
		return nil
	})
	if err != nil {
		panic(err)
	}
	ep.makeCursors(pl, 100, lg)
}

// makeCursors makes the cursors at the given brightness, which the BRIGHT
// menu's CURSOR sets, replacing those made before. A cursor that can't be
// made keeps its previous one, if there is one.
func (ep *Scope) makeCursors(pl platform.Platform, b scope.Brightness, lg *log.Logger) {
	if ep.eramCursors == nil {
		ep.eramCursors = make(map[string]platform.Cursor)
	}
	for name, ci := range ep.cursorImages {
		// The pixels' alpha is premultiplied, so scaling the colors alone
		// dims them.
		img := &image.RGBA{Pix: slices.Clone(ci.img.Pix), Stride: ci.img.Stride, Rect: ci.img.Rect}
		for i := 0; i < len(img.Pix); i += 4 {
			for c := range 3 {
				img.Pix[i+c] = uint8(int(img.Pix[i+c]) * int(b) / 100)
			}
		}
		cursor, err := pl.CreateCursorFromImage(img, ci.hotspot[0], ci.hotspot[1])
		if err != nil {
			lg.Warnf("%s: %v", name, err)
			continue
		}
		if old := ep.eramCursors[name]; old != nil {
			old.Destroy()
		}
		ep.eramCursors[name] = cursor
	}
	ep.cursorBrightness = b
}

// updateCursorOverride applies the current ERAM cursor to the platform.
// The cursor is the temporary override (if any), else the base cursor chosen
// in the CURSOR menu. When a timed override expires, the rollback cursor (if
// set) becomes the new override; otherwise we fall back to the base cursor.
func (ep *Scope) updateCursorOverride(ctx *scope.Context) {
	if ep.prefSet == nil || ctx.Mouse == nil {
		return
	}
	if b := ep.currentPrefs().Brightness.Cursor; b != ep.cursorBrightness {
		ep.makeCursors(ctx.Platform, b, ctx.Lg)
	}

	if ep.cursorOverrideSelection != "" && !ep.cursorOverrideUntil.IsZero() &&
		time.Now().After(ep.cursorOverrideUntil) {
		ep.cursorOverrideSelection = ep.cursorRollbackSelection
		ep.cursorOverrideUntil = time.Time{}
		ep.cursorRollbackSelection = ""
	}

	name := ep.cursorOverrideSelection
	if name == "" {
		if sz := ep.currentPrefs().CursorSize; sz > 0 {
			name = fmt.Sprintf("Eram%d", sz)
		}
	}

	if c := ep.eramCursors[name]; c != nil {
		c.SetOverride()
	} else {
		ctx.Platform.ClearCursorOverride()
	}
}

// SetTemporaryCursor displays a cursor type for the given number of seconds.
func (ep *Scope) SetTemporaryCursor(cursorType string, seconds float64, rollbackCursor string) {
	cursorType = strings.TrimSpace(cursorType)
	if cursorType == "" || seconds == 0 || seconds < -1 {
		ep.ClearTemporaryCursor()
		return
	}

	if size, err := strconv.Atoi(cursorType); err == nil {
		cursorType = fmt.Sprintf("Eram%d", size)
	}

	ep.cursorOverrideSelection = cursorType
	rollback := strings.TrimSpace(rollbackCursor)
	if rollback != "" {
		if size, err := strconv.Atoi(rollback); err == nil {
			rollback = fmt.Sprintf("Eram%d", size)
		}
		ep.cursorRollbackSelection = rollback
	} else {
		ep.cursorRollbackSelection = ""
	}
	if seconds == -1 {
		ep.cursorOverrideUntil = time.Time{}
	} else {
		ep.cursorOverrideUntil = time.Now().Add(time.Duration(seconds * float64(time.Second)))
	}
}

// ClearTemporaryCursor removes any temporary cursor override and rollback cursor.
func (ep *Scope) ClearTemporaryCursor() {
	ep.cursorOverrideSelection = ""
	ep.cursorOverrideUntil = time.Time{}
	ep.cursorRollbackSelection = ""
}

///////////////////////////////////////////////////////////////////////////
// Alarm

// The audible alarm is a 700 Hz tone of half a second that sounds for an
// input error or a value at its limit (1.4). The CURSOR menu's VOLUME
// button sets its volume from 1 to 5 and mutes it at 0 (3.5.3).
const (
	alarmToneHz       = 700
	alarmToneDuration = 500 * time.Millisecond
	maxAlarmVolume    = 5
	// alarmPeakAmplitude is the tone's peak sample at the loudest volume:
	// half of full scale, leaving room for speech playing at the same time.
	alarmPeakAmplitude = 16384
)

// alarm holds the alarm tone as registered with the audio engine at each
// volume. The engine's own volume also applies to the pilots' speech, so
// the tone's volume is set by its amplitude instead.
type alarm struct {
	engine  audio.Engine
	tones   [maxAlarmVolume]int
	sounded time.Time
}

func (ep *Scope) loadAlarm(e audio.Engine, lg *log.Logger) {
	if ep.alarm.engine != nil {
		return
	}
	for v := range maxAlarmVolume {
		id, err := e.AddPCM(makeAlarmTone(float32(v+1)/maxAlarmVolume), audio.SampleRate)
		if err != nil {
			lg.Errorf("alarm tone: %v", err)
			return
		}
		ep.alarm.tones[v] = id
	}
	ep.alarm.engine = e
}

// makeAlarmTone returns the alarm tone at the given fraction of its peak
// amplitude as 16-bit little-endian PCM. It fades in and out over a few
// milliseconds so that it starts and stops without a click.
func makeAlarmTone(gain float32) []byte {
	n := int(alarmToneDuration.Seconds() * audio.SampleRate)
	const fade = audio.SampleRate / 200
	pcm := make([]byte, 2*n)
	for i := range n {
		envelope := min(1, float32(i)/fade, float32(n-1-i)/fade)
		phase := 2 * math.Pi * alarmToneHz * float32(i) / audio.SampleRate
		s := int16(gain * envelope * alarmPeakAmplitude * math.Sin(phase))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(s))
	}
	return pcm
}

// soundAlarm sounds the alarm at the current volume. It doesn't sound again
// until the tone has finished, so that a button held at its limit doesn't
// queue up one tone after another.
func (ep *Scope) soundAlarm() {
	if ep.alarm.engine == nil || time.Since(ep.alarm.sounded) < alarmToneDuration {
		return
	}
	if v := ep.currentPrefs().AlarmVolume; v > 0 {
		ep.alarm.engine.PlayAudioOnce(ep.alarm.tones[v-1])
		ep.alarm.sounded = time.Now()
	}
}

// clickAlarmVolume adjusts the alarm volume and sounds the alarm at the new
// volume so that it can be heard (3.5.3), cutting off a tone still playing
// at the old one.
func clickAlarmVolume(ep *Scope, c toolbarClick) error {
	if err := adjust(&ep.currentPrefs().AlarmVolume, c, 0, maxAlarmVolume, 1); err != nil {
		return err
	}
	if ep.alarm.engine != nil {
		for _, id := range ep.alarm.tones {
			ep.alarm.engine.StopPlayAudio(id)
		}
	}
	ep.alarm.sounded = time.Time{}
	ep.soundAlarm()
	return nil
}
