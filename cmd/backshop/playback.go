// cmd/backshop/playback.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// playback is the flights the Launch tab has recorded. Launching an aircraft
// runs the sim through the whole of its flight at once, so what the scope shows
// afterwards is a recording being played rather than a sim being watched.
//
// Every flight carries its own clock rather than sharing one. They all run at
// the same rate while playback is going, so left alone they play as they were
// flown; scrubbing one on its own is what lets two flights that never shared
// the sky be put side by side and measured against each other.
type playback struct {
	flights []recordedFlight

	playing bool
	rate    float32

	// recording is set while the sim is flying the launched aircraft; the
	// answer comes back after however many minutes of flight it takes.
	recording bool
	truncated bool

	// advanced is when the clocks were last moved forward, so that playback
	// runs at the wall-clock rate however fast frames come.
	advanced time.Time
}

// recordedFlight is one recorded flight and where its own scrubber sits. A
// scrubber that has been run off either end of the flight leaves the aircraft
// off the scope, which is how one is set aside without being forgotten.
type recordedFlight struct {
	sim.FlightRecording
	at    sim.Time
	drawn bool
}

// position is where the aircraft was at the flight's own moment, or false if
// the scrubber is off either end of it. Samples are a second apart and playback
// is smooth, so the position between two of them is interpolated.
func (f *recordedFlight) position() (sim.FlightSample, bool) {
	if len(f.Samples) == 0 || f.at.Before(f.Start) || f.at.After(f.End()) {
		return sim.FlightSample{}, false
	}
	seconds := f.at.Sub(f.Start).Seconds()
	i := min(int(seconds), len(f.Samples)-1)
	s := f.Samples[i]
	if i+1 == len(f.Samples) {
		return s, true
	}
	next := f.Samples[i+1]
	t := float32(seconds - float64(i))
	s.Location = math.Lerp2f(t, s.Location, next.Location)
	s.Altitude = math.Lerp(t, s.Altitude, next.Altitude)
	s.Groundspeed = math.Lerp(t, s.Groundspeed, next.Groundspeed)
	return s, true
}

// flown splits the recorded track at the aircraft: what it has flown and what
// it has yet to fly, sharing the sample under it so the two draw as one line. A
// scrubber run off either end of the flight puts the whole track on one side.
func (f *recordedFlight) flown() (past, future []sim.FlightSample) {
	i := math.Clamp(int(f.at.Sub(f.Start).Seconds()), 0, len(f.Samples)-1)
	return f.Samples[:i+1], f.Samples[i:]
}

// finished reports whether the scrubber has run off the end of the flight,
// which is where playback leaves it.
func (f *recordedFlight) finished() bool { return !f.at.Before(f.End()) }

func (p *playback) init() { p.rate = 1 }

func (p *playback) reset() {
	p.flights, p.playing, p.truncated = nil, false, false
}

func (p *playback) empty() bool { return len(p.flights) == 0 }

// launch flies one aircraft: the sim creates it, then runs through the rest of
// every flight it has going, which is what there is to play back.
func (p *playback) launch(a *app, flight sim.LaunchFlight) {
	p.recording = true
	a.cc.LaunchAircraft(flight, func(err error) {
		if err != nil {
			p.recording = false
			a.status = "launch failed: " + err.Error()
			return
		}
		a.cc.RecordFlights(func(recordings sim.FlightRecordings, err error) {
			p.recording = false
			if err != nil {
				a.status = "recording failed: " + err.Error()
				return
			}
			p.add(recordings)
		})
	})
}

// add takes in a run's recordings, each starting at its own beginning and
// playing from there.
func (p *playback) add(recordings sim.FlightRecordings) {
	p.truncated = p.truncated || recordings.Truncated
	for _, r := range recordings.Recordings {
		if len(r.Samples) > 0 {
			p.flights = append(p.flights, recordedFlight{FlightRecording: r, at: r.Start, drawn: true})
		}
	}
	p.playing, p.advanced = true, time.Now()
}

// advance moves every flight's scrubber along while playback runs, stopping
// once they have all reached their ends. It runs every frame rather than with
// the tab, so that playback keeps going while another tab is in front.
func (p *playback) advance() {
	if !p.playing || p.empty() {
		return
	}
	now := time.Now()
	elapsed := time.Duration(float32(now.Sub(p.advanced)) * p.rate)
	p.advanced = now

	done := true
	for i := range p.flights {
		f := &p.flights[i]
		if f.finished() {
			continue
		}
		f.at = f.at.Add(elapsed)
		if f.finished() {
			f.at = f.End()
		} else {
			done = false
		}
	}
	p.playing = !done
}

// drawUI is the transport that every flight's clock follows.
func (p *playback) drawUI(a *app) {
	if p.recording {
		imgui.Text("Flying the launched aircraft...")
		return
	}
	if p.empty() {
		imgui.TextDisabled("Launch an aircraft to record a flight.")
		return
	}

	if imgui.Button(util.Select(p.playing, "Pause", "Play")) {
		p.playing, p.advanced = !p.playing, time.Now()
		p.rewindFinished()
	}
	imgui.SameLine()
	imgui.SetNextItemWidth(140)
	imgui.SliderFloatV("Speed", &p.rate, 0.25, 60, "%.2fx", imgui.SliderFlagsLogarithmic)
	imgui.SameLine()
	if imgui.Button("Restart") {
		for i := range p.flights {
			p.flights[i].at = p.flights[i].Start
		}
		p.playing, p.advanced = true, time.Now()
	}
	imgui.SameLine()
	if imgui.Button("Clear") {
		p.reset()
		return
	}

	if p.truncated {
		imgui.TextColored(warningColor,
			"An aircraft was still flying after four hours; its recording stops there.")
	}
}

// rewindFinished sends the flights that have played out back to their starts,
// so that pressing play at the end replays rather than doing nothing.
func (p *playback) rewindFinished() {
	if !p.playing {
		return
	}
	for i := range p.flights {
		if p.flights[i].finished() {
			p.flights[i].at = p.flights[i].Start
		}
	}
}

// clockText is a length of time the way a stopwatch shows it.
func clockText(d time.Duration) string {
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// drawFlights lists what has been recorded, each with the scrubber for its own
// flight. The scrubber reaches a second past either end of the recording, where
// the aircraft leaves the scope.
func (p *playback) drawFlights(a *app) {
	flags, size := tableSize(len(p.flights), 10)
	if !imgui.BeginTableV("recordings", 7, flags, size, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Callsign", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("Type", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumnV("From", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumnV("To", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumnV("Procedure", imgui.TableColumnFlagsWidthFixed, 75, 0)
	imgui.TableSetupColumn("Scrub")
	imgui.TableHeadersRow()

	for i := range p.flights {
		f := &p.flights[i]
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Checkbox("##draw", &f.drawn)
		imgui.TableNextColumn()
		if imgui.SelectableBool(string(f.Callsign)) {
			f.at = f.Start
			a.scope.center = f.Samples[0].Location
		}
		tooltip(f.Route + "\nClick to go to this flight's start and center the map on it")
		imgui.TableNextColumn()
		imgui.Text(f.AircraftType)
		imgui.TableNextColumn()
		imgui.Text(string(f.DepartureAirport))
		imgui.TableNextColumn()
		imgui.Text(string(f.ArrivalAirport))
		imgui.TableNextColumn()
		imgui.Text(util.Select(f.SID != "", f.SID, f.STAR))
		imgui.TableNextColumn()
		p.drawScrubber(f)
		imgui.PopID()
	}
	imgui.EndTable()
}

func (p *playback) drawScrubber(f *recordedFlight) {
	flown := f.End().Sub(f.Start)
	// A second past either end is off the recording, and the aircraft with it.
	elapsed := float32(f.at.Sub(f.Start).Seconds())
	label := clockText(f.at.Sub(f.Start)) + " of " + clockText(flown)
	if _, ok := f.position(); !ok {
		label = "not shown"
	}

	imgui.SetNextItemWidth(-1)
	if imgui.SliderFloatV("##scrub", &elapsed, -1, float32(flown.Seconds())+1, label, 0) {
		f.at = f.Start.Add(time.Duration(elapsed) * time.Second)
		p.playing = false
	}
	// Letting go of the scrubber runs the recording on from wherever it was
	// put down, which is what someone dragging it there wanted to see.
	if imgui.IsItemDeactivatedAfterEdit() {
		p.playing, p.advanced = true, time.Now()
	}
}

// visible is the recordings the scope is drawing.
func (p *playback) visible() []*recordedFlight {
	var flights []*recordedFlight
	for i := range p.flights {
		if f := &p.flights[i]; f.drawn {
			flights = append(flights, f)
		}
	}
	return flights
}

// datablock is the readout drawn beside a target: what it is, how high, how
// fast, and which procedure it is flying.
func datablock(f *recordedFlight, sample sim.FlightSample) string {
	text := string(f.Callsign)
	if f.AircraftType != "" {
		text += " " + f.AircraftType
	}
	text += fmt.Sprintf("\n%03d %03d", int(sample.Altitude+50)/100, int(sample.Groundspeed+0.5))
	if proc := util.Select(f.SID != "", f.SID, f.STAR); proc != "" {
		text += " " + proc
	}
	return text
}
