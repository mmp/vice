// cmd/backshop/dialogs.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"

	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/pkg/browser"
)

// showDialog queues up a modal dialog; they are shown one at a time, in the
// order they were added.
func (a *app) showDialog(c gui.DialogClient) {
	a.dialogs = append(a.dialogs, gui.NewModalDialog(c, a.plat))
}

// drawDialogs draws the first queued dialog that the user hasn't dismissed.
func (a *app) drawDialogs() {
	for len(a.dialogs) > 0 {
		if d := a.dialogs[0]; !d.Closed() {
			d.Draw()
			return
		}
		a.dialogs = a.dialogs[1:]
	}
}

// checkForNewRelease sends along a release newer than the running build if
// there is one. It runs in a goroutine since it makes a network request
// that may take some time (or may even time out, etc.)
func checkForNewRelease(ch chan *util.Release, lg *log.Logger) {
	defer close(ch)

	if release := util.NewerRelease(lg); release != nil {
		ch <- release
	}
}

// pollNewRelease puts up the new release dialog once the check that
// newApp started has something to say.
func (a *app) pollNewRelease() {
	if a.newReleaseChan == nil {
		return
	}
	select {
	case release, ok := <-a.newReleaseChan:
		if ok {
			a.showDialog(&newReleaseDialog{app: a, version: release.TagName})
		} else {
			a.newReleaseChan = nil
		}
	default:
		// Nothing there yet and the check is still running.
	}
}

// newReleaseDialog offers to quit and open the download page.
type newReleaseDialog struct {
	app     *app
	version string
}

func (nr *newReleaseDialog) Title() string { return "A new backshop release is available" }

func (nr *newReleaseDialog) Opening() {}

func (nr *newReleaseDialog) Buttons() []gui.DialogButton {
	return []gui.DialogButton{
		{
			Text: "Quit and update",
			Action: func() bool {
				browser.OpenURL("https://pharr.org/vice/facility-engineering.html#fe-backshop")
				nr.app.quit = true
				return true
			},
		},
		{Text: "Update later"},
	}
}

func (nr *newReleaseDialog) Draw() int {
	imgui.Text(fmt.Sprintf("backshop version %s is the latest version", nr.version))
	imgui.Text("Would you like to quit and open the backshop download page?")
	return -1
}

// whatsNewDialog lists the entries the user hasn't seen yet.
type whatsNewDialog struct {
	config *Config
}

func (wn *whatsNewDialog) Title() string { return "What's new in this version of backshop" }

func (wn *whatsNewDialog) Opening() {}

func (wn *whatsNewDialog) Buttons() []gui.DialogButton {
	return []gui.DialogButton{
		{
			Text: "View Release Notes",
			Action: func() bool {
				browser.OpenURL("https://pharr.org/vice/index.html#releases")
				return false
			},
		},
		{
			Text: "Ok",
			Action: func() bool {
				wn.config.WhatsNewIndex = len(whatsNew)
				return true
			},
		},
	}
}

func (wn *whatsNewDialog) Draw() int {
	for i := wn.config.WhatsNewIndex; i < len(whatsNew); i++ {
		imgui.Text(gui.Icons.Square + " " + whatsNew[i])
	}
	return -1
}
