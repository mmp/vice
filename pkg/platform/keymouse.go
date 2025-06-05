// pkg/platform/keymouse.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

import (
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/pkg/util"
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

func (g *glfwPlatform) GetMouse() *MouseState {
	io := imgui.CurrentIO()
	pos := imgui.MousePos()
	wx, wy := io.MouseWheelH(), io.MouseWheel()

	m := &MouseState{
		Pos:      [2]float32{pos.X, pos.Y},
		DeltaPos: util.Select(g.mouseDeltaMode, g.mouseDelta, [2]float32{}),
		Wheel:    [2]float32{wx, wy},
	}

	for b := MouseButtonPrimary; b < MouseButtonCount; b++ {
		m.Down[b] = imgui.IsMouseDown(b)
		m.Released[b] = imgui.IsMouseReleased(b)
		m.Clicked[b] = imgui.IsMouseClickedBool(b)
		m.DoubleClicked[b] = imgui.IsMouseDoubleClicked(b)
		m.Dragging[b] = imgui.IsMouseDraggingV(b, 0)
		if m.Dragging[b] {
			delta := imgui.MouseDragDeltaV(b, 0.)
			m.DragDelta = [2]float32{delta.X, delta.Y}
			imgui.ResetMouseDragDeltaV(b)
		}
	}

	return m
}

type KeyboardState struct {
	Input string
	// A key shows up here once each time it is pressed (though repeatedly
	// if key repeat kicks in.)
	Pressed   map[imgui.Key]interface{}
	HeldFKeys map[imgui.Key]interface{}
}

func (g *glfwPlatform) GetKeyboard() *KeyboardState {
	keyboard := &KeyboardState{
		Pressed:   make(map[imgui.Key]interface{}),
		HeldFKeys: g.heldFKeys,
	}

	keyboard.Input = g.InputCharacters()

	// Map \ to END for laptops (hacky...)
	if strings.Contains(keyboard.Input, `\`) {
		keyboard.Input = strings.ReplaceAll(keyboard.Input, `\`, "")
		keyboard.Pressed[imgui.KeyEnd] = nil
	}

	if imgui.IsKeyPressedBool(imgui.KeyEnter) ||
		imgui.IsKeyPressedBool(imgui.KeyKeypadEnter) {
		keyboard.Pressed[imgui.KeyEnter] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyDownArrow) {
		keyboard.Pressed[imgui.KeyDownArrow] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyUpArrow) {
		keyboard.Pressed[imgui.KeyUpArrow] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyLeftArrow) {
		keyboard.Pressed[imgui.KeyLeftArrow] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyRightArrow) {
		keyboard.Pressed[imgui.KeyRightArrow] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyHome) {
		keyboard.Pressed[imgui.KeyHome] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyEnd) {
		keyboard.Pressed[imgui.KeyEnd] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyBackspace) {
		keyboard.Pressed[imgui.KeyBackspace] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyDelete) {
		keyboard.Pressed[imgui.KeyDelete] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyEscape) {
		keyboard.Pressed[imgui.KeyEscape] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyTab) {
		keyboard.Pressed[imgui.KeyTab] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyPageUp) {
		keyboard.Pressed[imgui.KeyPageUp] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyPageDown) {
		keyboard.Pressed[imgui.KeyPageDown] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyV) {
		keyboard.Pressed[imgui.KeyV] = nil
	}
	if imgui.IsKeyPressedBool(imgui.KeyInsert) {
		keyboard.Pressed[imgui.KeyInsert] = nil
	}
	for i := 0; i < 16; i++ { // 16 f-keys on the STARS keyboard
		k := imgui.KeyF1 + imgui.Key(i)
		if imgui.IsKeyPressedBool(k) {
			keyboard.Pressed[k] = nil
		}
	}

	return keyboard
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
