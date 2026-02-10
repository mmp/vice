// pkg/panes/messages.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

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

	font             *renderer.Font
	events           *sim.EventsSubscription
	messages         []Message
	alertAudioIndex  map[string]int
	shouldAutoScroll bool
}

func NewMessagesPane() *MessagesPane {
	return &MessagesPane{
		FontIdentifier: renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: 16},
	}
}

func (mp *MessagesPane) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	if mp.font = renderer.GetFont(mp.FontIdentifier); mp.font == nil {
		mp.font = renderer.GetDefaultFont()
		mp.FontIdentifier = mp.font.Id
	}
	mp.events = eventStream.Subscribe()

	mp.alertAudioIndex = make(map[string]int)
	for alert, path := range util.SortedMap(audioAlerts) {
		idx, err := p.AddMP3(util.LoadResourceBytes("audio/" + path))
		if err != nil {
			lg.Errorf("Error adding static audio effect: %v", err)
		}
		mp.alertAudioIndex[alert] = idx
	}

	if _, ok := mp.alertAudioIndex[mp.AudioAlertSelection]; !ok { // Not available (or unset)
		// Take the first one alphabetically.
		mp.AudioAlertSelection, _ = util.FirstSortedMapEntry(audioAlerts)
	}
}

func (mp *MessagesPane) ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	mp.messages = nil
}

var _ UIDrawer = (*MessagesPane)(nil)

func (mp *MessagesPane) DisplayName() string { return "Messages" }

func (mp *MessagesPane) DrawUI(p platform.Platform, config *platform.Config) {
	if newFont, changed := renderer.DrawFontSizeSelector(&mp.FontIdentifier); changed {
		mp.font = newFont
	}

	imgui.Separator()
	if imgui.BeginCombo("Audio alert", mp.AudioAlertSelection) {
		for alert := range util.SortedMap(audioAlerts) {
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

func (msg *Message) ImguiColor() imgui.Vec4 {
	c := msg.Color()
	return imgui.Vec4{X: c.R, Y: c.G, Z: c.B, W: 1}
}

func (mp *MessagesPane) DrawWindow(show *bool, c *client.ControlClient, p platform.Platform, lg *log.Logger) {
	mp.processEvents(c, p, lg)

	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 100}, imgui.Vec2{-1, -1})
	if mp.font != nil {
		mp.font.ImguiPush()
	}
	imgui.BeginV("Messages", show, 0)
	for _, msg := range mp.messages {
		color := msg.ImguiColor()
		imgui.PushStyleColorVec4(imgui.ColText, color)
		imgui.TextUnformatted(msg.contents)
		imgui.PopStyleColor()
	}
	// Auto-scroll when new messages arrive
	if mp.shouldAutoScroll {
		imgui.SetScrollHereYV(1.0)
		mp.shouldAutoScroll = false
	}
	imgui.End()
	if mp.font != nil {
		imgui.PopFont()
	}
}

func (mp *MessagesPane) processEvents(c *client.ControlClient, p platform.Platform, lg *log.Logger) {
	for _, event := range mp.events.Get() {
		switch event.Type {
		case sim.RadioTransmissionEvent:
			toUs := c.State.UserControlsPosition(event.ToController)

			priv := c.State.TCWIsPrivileged(c.State.UserTCW)
			if !toUs && !priv {
				break
			}

			prefix := ""
			if priv {
				prefix = "[to " + string(event.ToController) + "] "
			}

			var msg Message
			if event.RadioTransmissionType == av.RadioTransmissionContact {
				msg = Message{contents: prefix + event.WrittenText}
				if mp.ContactTransmissionsAlert {
					p.PlayAudioOnce(mp.alertAudioIndex[mp.AudioAlertSelection])
				}
			} else {
				if len(event.WrittenText) > 0 {
					event.WrittenText = strings.ToUpper(event.WrittenText[:1]) + event.WrittenText[1:]
				}
				msg = Message{
					contents: prefix + event.WrittenText,
					error:    event.RadioTransmissionType == av.RadioTransmissionUnexpected || event.RadioTransmissionType == av.RadioTransmissionMixUp,
				}
				if mp.ReadbackTransmissionsAlert {
					p.PlayAudioOnce(mp.alertAudioIndex[mp.AudioAlertSelection])
				}
			}
			lg.Debug("radio_transmission", slog.String("adsb_callsign", string(event.ADSBCallsign)),
				slog.Any("message", msg))
			mp.messages = append(mp.messages, msg)
			mp.shouldAutoScroll = true

		case sim.GlobalMessageEvent:
			mp.messages = append(mp.messages, Message{contents: event.WrittenText, global: true})
			mp.shouldAutoScroll = true

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
				mp.shouldAutoScroll = true
			}

		case sim.ErrorMessageEvent:
			mp.messages = append(mp.messages,
				Message{
					contents: event.WrittenText,
					error:    true,
				})
			mp.shouldAutoScroll = true

		case sim.STTCommandEvent:
			// Display the controller's STT transcript and resulting command
			if event.STTTranscript != "" || event.STTCommand != "" {
				msg := fmt.Sprintf("STT: %q -> %s", event.STTTranscript, event.STTCommand)
				mp.messages = append(mp.messages,
					Message{
						contents: msg,
						system:   true,
					})
				mp.shouldAutoScroll = true
			}
		}
	}
}
