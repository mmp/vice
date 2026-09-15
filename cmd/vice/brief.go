// cmd/vice/brief.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"

	"github.com/AllenDang/cimgui-go/imgui"
)

// drawScenarioBriefSection draws the scenario brief as a collapsing header section inside the
// scenario info window.
func drawScenarioBriefSection(mgr *client.ConnectionManager, config *Config, c *client.ControlClient,
	p platform.Platform, lg *log.Logger) {
	if c.State.ScenarioBrief == "" {
		return
	}

	if !imgui.CollapsingHeaderBoolPtr(c.State.Facility+" Procedures", nil) {
		return
	}

	imgui.Indent()

	// Check for Control-R to reload the brief.
	if imgui.IsKeyPressedBool(imgui.KeyR) && imgui.CurrentIO().KeyCtrl() {
		if err := c.ReloadScenarioBrief(); err != nil {
			lg.Errorf("Failed to reload scenario brief: %v", err)
		} else {
			ui.brief.Invalidate()
		}
	}

	ui.brief.Draw(c.State.ScenarioBrief, p, config.UIFontSize, &c.State, c, lg)

	imgui.Unindent()
}
