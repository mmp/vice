// cmd/backshop/tts.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"slices"
	"strconv"
	"strings"
	"sync"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/tts"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// speakingColor marks the word that is being spoken.
var speakingColor = imgui.Vec4{X: 1, Y: 0.8, Z: 0.3, W: 1}

// ttsRadioSeed picks the radio characteristics the radio effect gives a
// transmission. In a sim it is derived from the callsign so that an
// aircraft sounds like itself; here there is no aircraft, and any of them
// would do.
const ttsRadioSeed = 1

// ttsTab hears what the sim will say. Every name vice speaks a pilot or a
// controller reads out of the pronunciation files in resources/, and the
// only way to tell whether an entry there is right is to listen to it; this
// is where that happens without flying a sim to make an aircraft say it.
type ttsTab struct {
	voice string
	// radio puts the speech through the VHF radio effect, which is how it
	// is heard in a sim. It is off to start with: the effect is meant to
	// make speech harder to make out, and hearing the pronunciation is the
	// point here.
	radio bool

	// What the pronounce-this entries hold.
	sid, star, fix string

	// procedures is what each airport with IFR traffic flies, built from
	// the aviation database the first time the tab is drawn for a sim.
	procedures map[av.ICAOAirportCode][]procedure
	// words counts the words built so far, which is where their ids come
	// from.
	words int

	speech speech

	// preloaded records that the TTS model has been asked for. Loading it
	// costs time and memory, so it waits until the tab is opened rather
	// than happening in every backshop session.
	preloaded bool
}

// spokenWord is one thing the tab can say: a fix, a procedure's name, or
// something typed into one of the entries. The id is unique among the words
// on screen and is how the one being spoken is picked out.
type spokenWord struct {
	id      string
	written string
	spoken  string
}

// procedure is one SID, STAR, or approach as the tab lists it: its name and
// every fix it is flown over, each named once however many of its
// transitions go by it.
type procedure struct {
	kind  string
	name  spokenWord
	fixes []spokenWord
}

// fixesPerLine is how many fixes a procedure shows before wrapping to the
// next line, which is about as many as can be read across at once.
const fixesPerLine = 12

func (t *ttsTab) init(config *Config) {
	t.voice = config.TTSVoice
	if !slices.Contains(ttsVoices(), t.voice) {
		t.voice = defaultTTSVoice
	}
}

func (t *ttsTab) reset() {
	// The procedures are the ones a scenario that is going away flies, and
	// anything being read from them is on its way out with them.
	t.speech.cancel()
	t.procedures = nil
	t.words = 0
}

// defaultTTSVoice is the voice the tab starts out speaking with.
const defaultTTSVoice = "af_heart"

// ttsVoices are the voices offered. They are the American English ones the
// sim gives to airlines it has nothing more specific for, which are the
// voices most of a scenario's traffic is heard in.
func ttsVoices() []string { return sim.AirlineVoices["default"] }

func (in *inspector) drawTTSTab(a *app) {
	if !imgui.BeginTabItem("TTS") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	t := &in.tts
	if !t.preloaded {
		t.preloaded = true
		tts.PreloadTTSModel(a.lg, nil, platform.AudioSampleRate)
	}

	t.drawStatus(a)
	t.drawControls(a)
	imgui.Separator()
	t.drawEntries(a)
	imgui.Separator()
	t.drawProcedures(a)
}

func (t *ttsTab) drawStatus(a *app) {
	if err := a.plat.AudioPlaybackError(); err != nil {
		imgui.TextColored(warningColor, "Audio playback is unavailable: "+err.Error())
		return
	}
	if err, done := tts.CheckTTSLoadError(); !done {
		imgui.Text("Loading the speech model...")
	} else if err != nil {
		imgui.TextColored(warningColor, "Speech synthesis is unavailable: "+err.Error())
	}
	if err := t.speech.error(); err != "" {
		imgui.TextColored(warningColor, err)
	}
}

func (t *ttsTab) drawControls(a *app) {
	imgui.SetNextItemWidth(140)
	if imgui.BeginCombo("Voice", t.voice) {
		for _, voice := range ttsVoices() {
			if imgui.SelectableBool(voice) {
				t.voice, a.config.TTSVoice = voice, voice
			}
		}
		imgui.EndCombo()
	}
	imgui.SameLine()
	imgui.Checkbox("Radio effect", &t.radio)
	itemTooltip("Speak through the VHF radio effect, as it is heard in a sim")
	imgui.SameLine()
	imgui.BeginDisabledV(!t.speech.playing())
	if imgui.Button(renderer.FontAwesomeIconStopCircle) {
		t.speech.stop(a.plat)
	}
	imgui.EndDisabled()
	itemTooltip("Stop reading")
}

// drawEntries is where a name that isn't in any of the listings below can be
// typed to hear how it comes out.
func (t *ttsTab) drawEntries(a *app) {
	if !imgui.BeginTableV("say", 3, tableFlags, imgui.Vec2{}, 0) {
		return
	}
	defer imgui.EndTable()
	imgui.TableSetupColumnV("Item", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumnV("Name", imgui.TableColumnFlagsWidthFixed, 200, 0)
	imgui.TableSetupColumn("Spoken")
	imgui.TableHeadersRow()

	row := func(label string, text *string, telephony func(string) string) {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(label)
		imgui.TableNextColumn()
		imgui.SetNextItemWidth(-1)
		entered := imgui.InputTextWithHint("##"+label, strings.ToLower(label), text,
			imgui.InputTextFlagsCharsUppercase|imgui.InputTextFlagsEnterReturnsTrue, nil)
		imgui.TableNextColumn()

		name := strings.TrimSpace(*text)
		if name == "" {
			return
		}
		word := spokenWord{id: label, written: name, spoken: telephony(name)}
		if imgui.SmallButton(renderer.FontAwesomeIconPlayCircle) || entered {
			t.play(a, []spokenWord{word})
		}
		imgui.SameLine()
		t.drawWord(a, word)
	}

	row("SID", &t.sid, av.GetSIDTelephony)
	row("STAR", &t.star, av.GetSTARTelephony)
	row("Fix", &t.fix, av.GetFixTelephony)
}

// drawProcedures lists what the scenario's airports fly, an airport to a
// header.
func (t *ttsTab) drawProcedures(a *app) {
	if t.procedures == nil {
		t.build(ifrAirports(&a.cc.State))
	}

	// A procedure with a lot of fixes runs off the right edge; the lines are
	// meant to be read across, so they scroll rather than wrap.
	if !imgui.BeginChildStrV("procedures", imgui.Vec2{}, imgui.ChildFlagsBorders,
		imgui.WindowFlagsHorizontalScrollbar) {
		imgui.EndChild()
		return
	}
	defer imgui.EndChild()

	for _, icao := range util.SortedMapKeys(t.procedures) {
		if !imgui.CollapsingHeaderBoolPtr(string(icao), nil) {
			continue
		}
		imgui.PushIDStr(string(icao))
		for _, p := range t.procedures[icao] {
			t.drawProcedure(a, p)
		}
		imgui.PopID()
	}
}

func (t *ttsTab) drawProcedure(a *app, p procedure) {
	imgui.PushIDStr(p.name.id)
	defer imgui.PopID()

	if imgui.SmallButton(renderer.FontAwesomeIconPlayCircle) {
		t.play(a, p.script())
	}
	itemTooltip("Read the procedure's name and then its fixes")
	imgui.SameLine()
	imgui.TextDisabled(p.kind)
	imgui.SameLine()
	t.drawWord(a, p.name)

	imgui.SameLine()
	// Where the fixes start, which is where each further line of them picks
	// up again.
	x := imgui.CursorPosX()
	for i, w := range p.fixes {
		if i > 0 {
			if i%fixesPerLine == 0 {
				imgui.SetCursorPosX(x)
			} else {
				imgui.SameLine()
			}
		}
		t.drawWord(a, w)
	}
}

// drawWord draws one word, lit up if it is the one being spoken. Clicking it
// says just it.
func (t *ttsTab) drawWord(a *app, w spokenWord) {
	if t.speech.speaking(w.id) {
		imgui.TextColored(speakingColor, w.written)
	} else {
		imgui.Text(w.written)
	}
	itemTooltip(w.spoken + "\nClick to hear it")
	if imgui.IsItemHovered() && imgui.IsMouseClickedBool(imgui.MouseButton(0)) {
		t.play(a, []spokenWord{w})
	}
}

// script is what the procedure's play button reads: its name and then its
// fixes, in order.
func (p procedure) script() []spokenWord {
	return append([]spokenWord{p.name}, p.fixes...)
}

func (t *ttsTab) play(a *app, words []spokenWord) {
	t.speech.play(a.plat, t.voice, t.radio, words)
}

///////////////////////////////////////////////////////////////////////////
// Building the listing

func (t *ttsTab) build(airports []av.ICAOAirportCode) {
	t.procedures = make(map[av.ICAOAirportCode][]procedure)

	for _, icao := range airports {
		ap, ok := av.DB.Airports[icao]
		if !ok {
			continue
		}

		var procedures []procedure
		for name, sid := range util.SortedMap(ap.SIDs) {
			routes := transitionRoutes(sid.RunwayTransitions)
			routes = append(routes, sid.Common)
			routes = append(routes, transitionRoutes(sid.EnrouteTransitions)...)
			procedures = append(procedures, procedure{kind: "SID",
				name: t.word(name, av.GetSIDTelephony(name)), fixes: t.routeWords(routes)})
		}
		for name, star := range util.SortedMap(ap.STARs) {
			routes := append(transitionRoutes(star.Transitions), transitionRoutes(star.RunwayWaypoints)...)
			procedures = append(procedures, procedure{kind: "STAR",
				name: t.word(name, av.GetSTARTelephony(name)), fixes: t.routeWords(routes)})
		}
		for name, appr := range util.SortedMap(ap.Approaches) {
			procedures = append(procedures, procedure{kind: "APPR",
				name:  t.word(name, av.GetApproachTelephony(appr.DefaultFullName())),
				fixes: t.routeWords(appr.Waypoints)})
		}

		t.procedures[icao] = procedures
	}
}

func (t *ttsTab) word(written, spoken string) spokenWord {
	t.words++
	return spokenWord{id: strconv.Itoa(t.words), written: written, spoken: spoken}
}

// transitionRoutes are a procedure's transitions, in the order they are
// named in.
func transitionRoutes(transitions map[string]av.WaypointArray) []av.WaypointArray {
	var routes []av.WaypointArray
	for _, wps := range util.SortedMap(transitions) {
		routes = append(routes, wps)
	}
	return routes
}

// routeWords are the fixes the given routes are flown over, each named once:
// a procedure's transitions overlap heavily, and a name is no more
// interesting the second time it comes up.
//
// Fixes vice made up are left out. The CIFP codes routes that start at a
// point of its own--the departure end of a runway, the place a heading leg
// ends--whose identifier is internal and is never read out.
func (t *ttsTab) routeWords(routes []av.WaypointArray) []spokenWord {
	var fixes []string
	for _, wps := range routes {
		for _, wp := range wps {
			if fix := wp.Fix; fix != "" && !strings.HasPrefix(fix, "_") &&
				!strings.Contains(fix, "-") && !slices.Contains(fixes, fix) {
				fixes = append(fixes, fix)
			}
		}
	}
	return util.MapSlice(fixes, func(fix string) spokenWord {
		return t.word(fix, av.GetFixTelephony(fix))
	})
}

///////////////////////////////////////////////////////////////////////////
// Playback

// speech reads a sequence of words, one after the next, and tracks which of
// them is being said so that it can be shown.
type speech struct {
	mu sync.Mutex
	// seq is the sequence being read; it is closed to stop it, and a
	// goroutine that finds it replaced knows it has been superseded.
	seq     chan struct{}
	current string
	err     string
}

// synthesized is a word with the audio to say it, handed from the goroutine
// that generates it to the one that plays it.
type synthesized struct {
	word spokenWord
	pcm  []int16
	err  error
}

func (s *speech) play(plat platform.Platform, voice string, radio bool, words []spokenWord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancelLocked()
	// Whatever was playing is no longer wanted; without this the new
	// sequence would have to wait behind it.
	plat.StopSpeech()

	seq := make(chan struct{})
	s.seq, s.err = seq, ""
	go s.read(plat, voice, radio, words, seq)
}

func (s *speech) stop(plat platform.Platform) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelLocked()
	plat.StopSpeech()
}

// cancel ends the sequence being read but lets the word that is already
// playing finish, for when there is no platform at hand to stop it.
func (s *speech) cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelLocked()
}

func (s *speech) cancelLocked() {
	if s.seq != nil {
		close(s.seq)
		s.seq = nil
	}
	s.current = ""
}

func (s *speech) playing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq != nil
}

func (s *speech) speaking(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current == id
}

func (s *speech) error() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *speech) read(plat platform.Platform, voice string, radio bool, words []spokenWord, seq chan struct{}) {
	defer s.finish(seq)

	// Generating a word takes long enough to hear, so the ones to come are
	// generated while the current one plays.
	audio := make(chan synthesized, 2)
	go func() {
		defer close(audio)
		for _, word := range words {
			pcm, err := synthesize(word.spoken, voice, radio)
			select {
			case audio <- synthesized{word: word, pcm: pcm, err: err}:
			case <-seq:
				return
			}
		}
	}()

	for a := range audio {
		done, ok := s.speak(plat, seq, a)
		if !ok {
			return
		}
		select {
		case <-done:
		case <-seq:
			return
		}
	}
}

// speak starts one word if its sequence is still the one being read,
// returning a channel that is closed once it has been said.
func (s *speech) speak(plat platform.Platform, seq chan struct{}, a synthesized) (<-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seq != seq {
		return nil, false
	}
	if a.err != nil {
		s.err = a.err.Error()
		return nil, false
	}

	s.current = a.word.id
	done := make(chan struct{})
	if err := plat.TryEnqueueSpeechPCM(a.pcm, func() { close(done) }); err != nil {
		s.err = err.Error()
		return nil, false
	}
	return done, true
}

func (s *speech) finish(seq chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq == seq {
		close(seq)
		s.seq = nil
		s.current = ""
	}
}

func synthesize(text, voice string, radio bool) ([]int16, error) {
	if radio {
		return tts.SynthesizeReadbackTTS(text, voice, ttsRadioSeed)
	}
	return tts.SynthesizePlainTTS(text, voice)
}
