// pkg/platform/keymouse.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

import (
	"github.com/AllenDang/cimgui-go/imgui"
)

type MouseState struct {
	Pos           [2]float32
	DeltaPos      [2]float32
	Down          [MouseButtonCount]bool
	Clicked       [MouseButtonCount]bool
	Released      [MouseButtonCount]bool
	DoubleClicked [MouseButtonCount]bool
	Dragging      [MouseButtonCount]bool
	DragDelta     [2]float32
	Wheel         [2]float32
}

const (
	MouseButtonPrimary imgui.MouseButton = iota
	MouseButtonSecondary
	MouseButtonTertiary
	MouseButtonCount
)

func (ms *MouseState) SetCursor(id imgui.MouseCursor) {
	imgui.SetMouseCursor(id)
}

type KeyboardState struct {
	Input string
	// A key shows up here once each time it is pressed (though repeatedly
	// if key repeat kicks in.)
	Pressed   map[imgui.Key]interface{}
	HeldFKeys map[imgui.Key]interface{}
}

func (k *KeyboardState) KeyShift() bool {
	return imgui.CurrentIO().KeyShift()
}
func (k *KeyboardState) KeyControl() bool {
	return imgui.CurrentIO().KeyCtrl()
}
func (k *KeyboardState) KeyAlt() bool {
	return imgui.CurrentIO().KeyAlt()
}
func (k *KeyboardState) KeySuper() bool {
	return imgui.CurrentIO().KeySuper()
}

func (k *KeyboardState) WasPressed(key imgui.Key) bool {
	_, ok := k.Pressed[key]
	return ok
}

func (k *KeyboardState) IsFKeyHeld(key imgui.Key) bool {
	_, ok := k.HeldFKeys[key]
	return ok
}
