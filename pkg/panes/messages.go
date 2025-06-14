// pkg/panes/messages.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/mmp/vice/pkg/client"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/speech"
	"github.com/mmp/vice/pkg/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

type Message struct {
	contents string
	system   bool
	error    bool
	global   bool
}

var audioAlerts map[string]string = map[string]string{
	"Aircraft Check In": "Aircraft_Check_In.mp3",
	"Radio Static":      "fm-radio-static-82334_2.mp3",
}

type MessagesPane struct {
	FontIdentifier             renderer.FontIdentifier
	AudioAlertSelection        string
	ContactTransmissionsAlert  bool
	ReadbackTransmissionsAlert bool

	font            *renderer.Font
	scrollbar       *ScrollBar
	events          *sim.EventsSubscription
	messages        []Message
	alertAudioIndex map[string]int
}

func init() {
	RegisterUnmarshalPane("MessagesPane", func(d []byte) (Pane, error) {
		var p MessagesPane
		err := json.Unmarshal(d, &p)
		return &p, err
	})
}

func NewMessagesPane() *MessagesPane {
	return &MessagesPane{
		FontIdentifier: renderer.FontIdentifier{Name: "Inconsolata Condensed Regular", Size: 16},
	}
}

func (mp *MessagesPane) DisplayName() string { return "Messages" }

func (mp *MessagesPane) Hide() bool { return false }

func (mp *MessagesPane) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	if mp.font = renderer.GetFont(mp.FontIdentifier); mp.font == nil {
		mp.font = renderer.GetDefaultFont()
		mp.FontIdentifier = mp.font.Id
	}
	if mp.scrollbar == nil {
		mp.scrollbar = NewVerticalScrollBar(4, true)
	}
	mp.events = eventStream.Subscribe()

	mp.alertAudioIndex = make(map[string]int)
	for _, alert := range util.SortedMapKeys(audioAlerts) {
		idx, err := p.AddMP3(util.LoadResourceBytes("audio/" + audioAlerts[alert]))
		if err != nil {
			lg.Error("Error adding static audio effect: %v", err)
		}
		mp.alertAudioIndex[alert] = idx
	}

	if _, ok := mp.alertAudioIndex[mp.AudioAlertSelection]; !ok { // Not available (or unset)
		// Take the first one alphabetically.
		for _, alert := range util.SortedMapKeys(audioAlerts) {
			mp.AudioAlertSelection = alert
			break
		}
	}
}

func (mp *MessagesPane) LoadedSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
}

func (mp *MessagesPane) ResetSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	mp.messages = nil
}

func (mp *MessagesPane) CanTakeKeyboardFocus() bool { return false }

func (mp *MessagesPane) Upgrade(prev, current int) {
}

func (mp *MessagesPane) DrawUI(p platform.Platform, config *platform.Config) {
	if newFont, changed := renderer.DrawFontPicker(&mp.FontIdentifier, "Font"); changed {
		mp.font = newFont
	}

	imgui.Separator()
	if imgui.BeginCombo("Audio alert", mp.AudioAlertSelection) {
		for _, alert := range util.SortedMapKeys(audioAlerts) {
			if imgui.SelectableBoolV(alert, alert == mp.AudioAlertSelection, 0, imgui.Vec2{}) {
				mp.AudioAlertSelection = alert
				p.PlayAudioOnce(mp.alertAudioIndex[alert])
			}
		}
		imgui.EndCombo()
	}
	imgui.Checkbox("Play audio alert after pilot initial contact transmissions", &mp.ContactTransmissionsAlert)
	imgui.Checkbox("Play audio alert after pilot readback transmissions", &mp.ReadbackTransmissionsAlert)
}

func (mp *MessagesPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	mp.processEvents(ctx)

	nLines := len(mp.messages) + 1 /* prompt */
	lineHeight := float32(mp.font.Size + 1)
	visibleLines := int(ctx.PaneExtent.Height() / lineHeight)
	mp.scrollbar.Update(nLines, visibleLines, ctx)

	drawWidth := ctx.PaneExtent.Width()
	if mp.scrollbar.Visible() {
		drawWidth -= float32(mp.scrollbar.PixelExtent())
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	indent := float32(2)

	scrollOffset := mp.scrollbar.Offset()
	y := lineHeight

	for i := scrollOffset; i < min(len(mp.messages), visibleLines+scrollOffset+1); i++ {
		// TODO? wrap text
		msg := mp.messages[len(mp.messages)-1-i]

		s := renderer.TextStyle{Font: mp.font, Color: msg.Color()}
		td.AddText(msg.contents, [2]float32{indent, y}, s)
		y += lineHeight
	}

	ctx.SetWindowCoordinateMatrices(cb)
	if ctx.HaveFocus {
		// Yellow border around the edges
		ld := renderer.GetLinesDrawBuilder()
		defer renderer.ReturnLinesDrawBuilder(ld)

		w, h := ctx.PaneExtent.Width(), ctx.PaneExtent.Height()
		ld.AddLineLoop([][2]float32{{0, 0}, {w, 0}, {w, h}, {0, h}})
		cb.SetRGB(renderer.RGB{1, 1, 0}) // yellow
		ld.GenerateCommands(cb)
	}
	mp.scrollbar.Draw(ctx, cb)
	td.GenerateCommands(cb)
}

func (msg *Message) Color() renderer.RGB {
	switch {
	case msg.error:
		return renderer.RGB{.9, .1, .1}
	case msg.global, msg.system:
		return renderer.RGB{0.012, 0.78, 0.016}
	default:
		return renderer.RGB{1, 1, 1}
	}
}

func (mp *MessagesPane) processEvents(ctx *Context) {
	for _, event := range mp.events.Get() {
		switch event.Type {
		case sim.RadioTransmissionEvent:
			toUs := event.ToController == ctx.UserTCP

			if !toUs && !ctx.Client.State.AreInstructorOrRPO(ctx.UserTCP) {
				break
			}

			prefix := ""
			if ctx.Client.State.AreInstructorOrRPO(ctx.UserTCP) {
				prefix = "[to " + event.ToController + "] "
			}

			var msg Message
			if event.RadioTransmissionType == speech.RadioTransmissionContact {
				msg = Message{contents: prefix + event.WrittenText}
				if mp.ContactTransmissionsAlert {
					ctx.Platform.PlayAudioOnce(mp.alertAudioIndex[mp.AudioAlertSelection])
				}
			} else {
				if len(event.WrittenText) > 0 {
					event.WrittenText = strings.ToUpper(event.WrittenText[:1]) + event.WrittenText[1:]
				}
				msg = Message{contents: prefix + event.WrittenText,
					error: event.Type == speech.RadioTransmissionUnexpected,
				}
				if mp.ReadbackTransmissionsAlert {
					ctx.Platform.PlayAudioOnce(mp.alertAudioIndex[mp.AudioAlertSelection])
				}
			}
			ctx.Lg.Debug("radio_transmission", slog.String("adsb_callsign", string(event.ADSBCallsign)),
				slog.Any("message", msg))
			mp.messages = append(mp.messages, msg)

		case sim.GlobalMessageEvent:
			if event.FromController != ctx.UserTCP {
				for _, line := range strings.Split(event.WrittenText, "\n") {
					mp.messages = append(mp.messages, Message{contents: line, global: true})
				}
			}

		case sim.StatusMessageEvent:
			// Don't spam the same message repeatedly; look in the most recent 5.
			n := len(mp.messages)
			start := max(0, n-5)
			if !slices.ContainsFunc(mp.messages[start:],
				func(m Message) bool { return m.contents == event.WrittenText }) {
				mp.messages = append(mp.messages,
					Message{
						contents: event.WrittenText,
						system:   true,
					})
			}
		}
	}
}
