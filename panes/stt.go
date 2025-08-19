// panes/stt.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"fmt"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"

	"github.com/AllenDang/cimgui-go/imgui"
)

type STTPane struct {
	// Settings
	PushToTalkKey      imgui.Key
	SelectedMicrophone string

	// State
	PushToTalkRecording bool
	LastTranscription   string
	RecordingPTTKey     bool      // Whether we're recording a new PTT key
	CapturedKey         imgui.Key // Temporarily captured key during recording

	// Display settings
	FontSize      int
	ShowOnStartup bool
}

func init() {
	RegisterUnmarshalPane("STTPane", func(d []byte) (Pane, error) {
		var p STTPane
		err := json.Unmarshal(d, &p)
		// Set defaults
		if p.FontSize == 0 {
			p.FontSize = 14
		}
		return &p, err
	})
}

func (sp *STTPane) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	// Nothing to initialize
}

func (sp *STTPane) LoadedSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// Nothing to initialize
}

func (sp *STTPane) ResetSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// Nothing to initialize
}

func (sp *STTPane) Deactivate() {
	// Nothing to clean up
}

func (sp *STTPane) CanTakeKeyboardFocus() bool { return true }

func (sp *STTPane) Hide() bool { return false }

func (sp *STTPane) Name() string { return "Speech to Text" }

func (sp *STTPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	// Handle keyboard input
	sp.processKeyboardInput(ctx)

	// Draw the UI window
	displaySize := ctx.Platform.DisplaySize()
	imgui.SetNextWindowPosV(imgui.Vec2{displaySize[0] - 400, 50}, imgui.CondFirstUseEver, imgui.Vec2{})
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 200}, imgui.Vec2{600, 400})

	if imgui.BeginV("Speech to Text", nil, imgui.WindowFlagsAlwaysAutoResize) {
		// Push-to-talk key recording
		keyName := "None"
		if sp.PushToTalkKey != imgui.KeyNone {
			keyName = GetKeyName(sp.PushToTalkKey)
		}

		imgui.Text("Push-to-Talk Key: ")
		imgui.SameLine()
		imgui.TextColored(imgui.Vec4{0, 1, 1, 1}, keyName)

		if sp.RecordingPTTKey {
			imgui.TextColored(imgui.Vec4{1, 1, 0, 1}, "Press any key for Push-to-Talk...")

			// Check for any key press
			if sp.CapturedKey != imgui.KeyNone {
				sp.PushToTalkKey = sp.CapturedKey
				sp.RecordingPTTKey = false
				sp.CapturedKey = imgui.KeyNone
			}
		} else {
			imgui.SameLine()
			if imgui.Button("Change Key") {
				sp.RecordingPTTKey = true
				sp.CapturedKey = imgui.KeyNone
			}
			imgui.SameLine()
			if imgui.Button("Clear") {
				sp.PushToTalkKey = imgui.KeyNone
			}
		}

		imgui.Separator()

		// Microphone selection
		imgui.Text("Microphone:")
		imgui.SameLine()
		micName := sp.SelectedMicrophone
		if micName == "" {
			micName = "Default"
		}
		if imgui.BeginComboV("##microphone", micName, 0) {
			if imgui.SelectableBoolV("Default", sp.SelectedMicrophone == "", 0, imgui.Vec2{}) {
				sp.SelectedMicrophone = ""
			}

			// Get available microphones
			mics := ctx.Platform.GetAudioInputDevices()
			for _, mic := range mics {
				if imgui.SelectableBoolV(mic, mic == sp.SelectedMicrophone, 0, imgui.Vec2{}) {
					sp.SelectedMicrophone = mic
				}
			}
			imgui.EndCombo()
		}

		imgui.Separator()

		// Push-to-talk status
		if sp.PushToTalkRecording {
			imgui.TextColored(imgui.Vec4{1, 0, 0, 1}, "Recording...")
		} else if sp.LastTranscription != "" {
			imgui.Text("Last transcription:")
			imgui.TextWrapped(sp.LastTranscription)
		}

		imgui.End()
	}
}

func (sp *STTPane) processKeyboardInput(ctx *Context) {
	if !ctx.HaveFocus || ctx.Keyboard == nil {
		return
	}

	// If we're recording a PTT key, capture any key press
	if sp.RecordingPTTKey {
		for key := range ctx.Keyboard.Pressed {
			// Ignore modifier keys
			if key != imgui.KeyLeftShift && key != imgui.KeyRightShift &&
				key != imgui.KeyLeftCtrl && key != imgui.KeyRightCtrl &&
				key != imgui.KeyLeftAlt && key != imgui.KeyRightAlt &&
				key != imgui.KeyLeftSuper && key != imgui.KeyRightSuper {
				sp.CapturedKey = key
				return // Don't process normal key commands while recording
			}
		}
		return
	}

	// Handle push-to-talk
	for key := range ctx.Keyboard.Pressed {
		if key == sp.PushToTalkKey && sp.PushToTalkKey != imgui.KeyNone {
			if !sp.PushToTalkRecording && !ctx.Platform.IsAudioRecording() {
				// Start recording with selected microphone
				if err := ctx.Platform.StartAudioRecordingWithDevice(sp.SelectedMicrophone); err != nil {
					ctx.Lg.Errorf("Failed to start audio recording: %v", err)
				} else {
					sp.PushToTalkRecording = true
					fmt.Println("Push-to-talk: Started recording")
				}
			}
		}
	}

	// Check for push-to-talk key release
	if sp.PushToTalkRecording && ctx.Keyboard != nil {
		if sp.PushToTalkKey != imgui.KeyNone && !imgui.IsKeyDown(sp.PushToTalkKey) {
			if ctx.Platform.IsAudioRecording() {
				// Stop recording and transcribe
				audioData, err := ctx.Platform.StopAudioRecording()
				if err != nil {
					ctx.Lg.Errorf("Failed to stop audio recording: %v", err)
				} else {
					fmt.Println("Push-to-talk: Stopped recording, transcribing...")

					// Transcribe in a goroutine to avoid blocking
					go func(data []int16) {
						// Convert to STT format and transcribe
						audio := &stt.AudioData{
							SampleRate: platform.AudioSampleRate,
							Channels:   1,
							Data:       data,
						}

						transcription, err := stt.Transcribe(audio)
						if err != nil {
							fmt.Printf("Push-to-talk: Transcription error: %v\n", err)
						} else {
							sp.LastTranscription = transcription
							fmt.Printf("Push-to-talk: Transcription: %s\n", transcription)
						}
					}(audioData)
				}
			} else {
				// Not recording at platform level; reset flag
				sp.PushToTalkRecording = false
			}
			sp.PushToTalkRecording = false
		}
	}
}

func GetKeyName(key imgui.Key) string {
	switch key {
	case imgui.KeyA:
		return "A"
	case imgui.KeyB:
		return "B"
	case imgui.KeyC:
		return "C"
	case imgui.KeyD:
		return "D"
	case imgui.KeyE:
		return "E"
	case imgui.KeyF:
		return "F"
	case imgui.KeyG:
		return "G"
	case imgui.KeyH:
		return "H"
	case imgui.KeyI:
		return "I"
	case imgui.KeyJ:
		return "J"
	case imgui.KeyK:
		return "K"
	case imgui.KeyL:
		return "L"
	case imgui.KeyM:
		return "M"
	case imgui.KeyN:
		return "N"
	case imgui.KeyO:
		return "O"
	case imgui.KeyP:
		return "P"
	case imgui.KeyQ:
		return "Q"
	case imgui.KeyR:
		return "R"
	case imgui.KeyS:
		return "S"
	case imgui.KeyT:
		return "T"
	case imgui.KeyU:
		return "U"
	case imgui.KeyV:
		return "V"
	case imgui.KeyW:
		return "W"
	case imgui.KeyX:
		return "X"
	case imgui.KeyY:
		return "Y"
	case imgui.KeyZ:
		return "Z"
	case imgui.Key0:
		return "0"
	case imgui.Key1:
		return "1"
	case imgui.Key2:
		return "2"
	case imgui.Key3:
		return "3"
	case imgui.Key4:
		return "4"
	case imgui.Key5:
		return "5"
	case imgui.Key6:
		return "6"
	case imgui.Key7:
		return "7"
	case imgui.Key8:
		return "8"
	case imgui.Key9:
		return "9"
	case imgui.KeyF1:
		return "F1"
	case imgui.KeyF2:
		return "F2"
	case imgui.KeyF3:
		return "F3"
	case imgui.KeyF4:
		return "F4"
	case imgui.KeyF5:
		return "F5"
	case imgui.KeyF6:
		return "F6"
	case imgui.KeyF7:
		return "F7"
	case imgui.KeyF8:
		return "F8"
	case imgui.KeyF9:
		return "F9"
	case imgui.KeyF10:
		return "F10"
	case imgui.KeyF11:
		return "F11"
	case imgui.KeyF12:
		return "F12"
	case imgui.KeySpace:
		return "Space"
	case imgui.KeyTab:
		return "Tab"
	case imgui.KeyCapsLock:
		return "CapsLock"
	case imgui.KeyEnter:
		return "Enter"
	case imgui.KeyBackspace:
		return "Backspace"
	case imgui.KeyInsert:
		return "Insert"
	case imgui.KeyDelete:
		return "Delete"
	case imgui.KeyHome:
		return "Home"
	case imgui.KeyEnd:
		return "End"
	case imgui.KeyPageUp:
		return "PageUp"
	case imgui.KeyPageDown:
		return "PageDown"
	case imgui.KeyLeftArrow:
		return "Left"
	case imgui.KeyRightArrow:
		return "Right"
	case imgui.KeyUpArrow:
		return "Up"
	case imgui.KeyDownArrow:
		return "Down"
	case imgui.KeyEscape:
		return "Escape"
	case imgui.KeyGraveAccent:
		return "`"
	case imgui.KeyMinus:
		return "-"
	case imgui.KeyEqual:
		return "="
	case imgui.KeyLeftBracket:
		return "["
	case imgui.KeyRightBracket:
		return "]"
	case imgui.KeyBackslash:
		return "\\"
	case imgui.KeySemicolon:
		return ";"
	case imgui.KeyApostrophe:
		return "'"
	case imgui.KeyComma:
		return ","
	case imgui.KeyPeriod:
		return "."
	case imgui.KeySlash:
		return "/"
	default:
		return "Unknown"
	}
}
