// cmd/backshop/videomaps.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
	"github.com/mmp/vice/videomaps/crc"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/ncruces/zenity"
)

// videoMapsTab converts a CRC installation's video maps and lists what a
// video map library holds: the pieces that were the crc2vice command and a
// command-line flag.
type videoMapsTab struct {
	mappackPath string
	mappack     string
	mappackErr  string

	crc crcConversion
}

func (in *inspector) drawVideoMapsTab(a *app) {
	if !imgui.BeginTabItem("Video maps") {
		return
	}
	defer imgui.EndTabItem()

	v := &in.videoMaps
	v.drawCRCUI(a)

	imgui.Separator()

	imgui.TextWrapped("Lists what a video map library holds, so that one can be inspected without " +
		"a scenario that refers to it.")
	imgui.SetNextItemWidth(300)
	imgui.InputTextWithHint("##mappack", "videomaps/ZNY-N90.mappack", &v.mappackPath, 0, nil)
	imgui.SameLine()
	if imgui.Button("List") {
		v.listMappack()
	}
	if v.mappackErr != "" {
		imgui.TextColored(warningColor, v.mappackErr)
	}
	if v.mappack != "" {
		imgui.SameLine()
		a.copyButton("mappack", v.mappack)
		if imgui.BeginChildStrV("mappacktext", imgui.Vec2{Y: 260}, imgui.ChildFlagsBorders, 0) {
			imgui.Text(v.mappack)
		}
		imgui.EndChild()
	}
}

// listMappack renders a video map library the way `vice -listmaps` prints it.
func (v *videoMapsTab) listMappack() {
	v.mappack, v.mappackErr = "", ""

	lib, err := videomaps.LoadLibrary(strings.TrimSpace(v.mappackPath))
	if err != nil {
		v.mappackErr = err.Error()
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d STARS maps\n", len(lib.Maps))
	for name, m := range util.SortedMap(lib.Maps) {
		fmt.Fprintf(&b, "  %4d %-10s %-40s %s\n", m.Id, m.Label, name, categoryName(m.Category, false))
	}
	if len(lib.ERAMMapGroups) > 0 {
		fmt.Fprintf(&b, "\n%d ERAM map groups\n", len(lib.ERAMMapGroups))
		for name, g := range util.SortedMap(lib.ERAMMapGroups) {
			fmt.Fprintf(&b, "  %-30s %d maps\n", name, len(g.Maps))
		}
	}
	v.mappack = b.String()
}

///////////////////////////////////////////////////////////////////////////
// CRC video map conversion

// crcConversion is the tab's view of a CRC installation: the ARTCCs
// its directory holds, which of them are to be converted, and the
// conversion that is running or was last run.
type crcConversion struct {
	artccs   []string
	selected map[string]bool

	// scanned is the directory artccs was read from, so that a new one the
	// user types or picks is noticed.
	scanned string
	scanErr string

	run *crcRun
	// running is cleared once the finished run's result has been reported,
	// which is what keeps it from being reported on every frame.
	running bool
}

// scan lists the ARTCCs in a CRC directory, doing so again whenever the
// directory changes.
func (c *crcConversion) scan(crcDir string) {
	if crcDir == c.scanned {
		return
	}
	c.scanned = crcDir
	c.artccs, c.scanErr, c.selected = nil, "", make(map[string]bool)

	artccs, err := crc.ListARTCCs(crcDir)
	if err != nil {
		c.scanErr = err.Error()
		return
	}
	c.artccs = artccs
}

// pollCRC reports a finished conversion. It runs every frame rather than
// with the rest of the tab, which is only drawn while the tab is the one in
// front: a conversion started and then left behind still has to report.
func (v *videoMapsTab) pollCRC(a *app) {
	if v.crc.running && v.crc.run.finished() {
		v.crc.running = false
		a.status = v.crc.run.summary()
	}
}

// drawCRCUI converts a CRC installation's video maps to vice video map
// libraries: what running crc2vice does, without leaving the tool.
func (v *videoMapsTab) drawCRCUI(a *app) {
	if a.config.CRCDir == "" {
		a.config.CRCDir = crc.DefaultDirectory()
	}

	imgui.TextWrapped("Converts a CRC installation's video maps: one .mappack per STARS facility " +
		"of each selected ARTCC, plus one for the ARTCC's ERAM maps.")

	directory := func(id, label string, dir *string) {
		imgui.SetNextItemWidth(340)
		imgui.InputTextWithHint("##"+id, label, dir, 0, nil)
		imgui.SameLine()
		if imgui.Button("Browse##" + id) {
			if p, err := zenity.SelectFile(zenity.Title("Select "+label), zenity.Directory()); err == nil {
				*dir = p
			} else if err != zenity.ErrCanceled {
				a.status = err.Error()
			}
		}
		imgui.SameLine()
		imgui.Text(label)
	}
	directory("crcdir", "CRC directory", &a.config.CRCDir)
	directory("mappackdir", "Output directory", &a.config.MapPackDir)

	v.crc.scan(a.config.CRCDir)
	if v.crc.scanErr != "" {
		imgui.TextColored(warningColor, v.crc.scanErr)
		return
	}
	if len(v.crc.artccs) == 0 {
		imgui.Text("No ARTCCs in that directory.")
		return
	}

	if imgui.SmallButton("All") {
		for _, id := range v.crc.artccs {
			v.crc.selected[id] = true
		}
	}
	imgui.SameLine()
	if imgui.SmallButton("None") {
		clear(v.crc.selected)
	}

	if imgui.BeginChildStrV("artccs", imgui.Vec2{Y: 100}, imgui.ChildFlagsBorders, 0) {
		const cols = 6
		for i, id := range v.crc.artccs {
			if i%cols > 0 {
				imgui.SameLine()
			}
			sel := v.crc.selected[id]
			if imgui.Checkbox(id, &sel) {
				v.crc.selected[id] = sel
			}
		}
	}
	imgui.EndChild()

	if a.config.MapPackDir == "" {
		imgui.TextDisabled("Choose a directory to write the generated .mappack files into.")
	}

	selected := util.FilterSlice(v.crc.artccs, func(id string) bool { return v.crc.selected[id] })
	imgui.BeginDisabledV(v.crc.running || len(selected) == 0 || a.config.MapPackDir == "")
	if imgui.Button("Convert") {
		v.crc.run = startCRCConversion(a.config.CRCDir, a.config.MapPackDir, selected)
		v.crc.running = true
		a.status = fmt.Sprintf("converting %d %s...", len(selected), artccNoun(len(selected)))
	}
	imgui.EndDisabled()

	if v.crc.run == nil {
		return
	}
	lines := v.crc.run.text()
	imgui.SameLine()
	a.copyButton("crc", lines)
	if imgui.BeginChildStrV("crcoutput", imgui.Vec2{Y: 260}, imgui.ChildFlagsBorders, 0) {
		imgui.Text(lines)
		if v.crc.running {
			imgui.SetScrollHereYV(1)
		}
	}
	imgui.EndChild()
}

// crcRun is a conversion running on its own goroutine so that the frame
// loop isn't blocked for the many seconds an ARTCC takes; the UI polls
// finished and shows the lines that have come back so far.
type crcRun struct {
	artccs []string

	mu       sync.Mutex
	lines    []string
	failures int

	done chan struct{}
}

func startCRCConversion(crcDir, outDir string, artccs []string) *crcRun {
	r := &crcRun{artccs: artccs, done: make(chan struct{})}

	go func() {
		defer close(r.done)
		for _, id := range artccs {
			if err := crc.Convert(crcDir, id, outDir, r.addLine); err != nil {
				r.addError(id, err)
			}
		}
	}()

	return r
}

// addLine is the crc.Report the conversion is given, so it is called from
// the goroutines crc.ConvertCRC runs its work in as well as from the one above.
func (r *crcRun) addLine(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *crcRun) addError(artcc string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf("%s: %v", artcc, err))
	r.failures++
}

func (r *crcRun) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *crcRun) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// summary is the one line the menu bar gets when a conversion finishes.
func (r *crcRun) summary() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failures > 0 {
		return fmt.Sprintf("%d of %d ARTCCs failed to convert", r.failures, len(r.artccs))
	}
	return fmt.Sprintf("converted %d %s", len(r.artccs), artccNoun(len(r.artccs)))
}

func artccNoun(n int) string {
	return util.Select(n == 1, "ARTCC", "ARTCCs")
}
