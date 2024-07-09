// pkg/platform/keymouse.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

import (
	"strings"

	"github.com/mmp/imgui-go/v4"
)

type MouseState struct {
	Pos           [2]float32
	Down          [MouseButtonCount]bool
	Clicked       [MouseButtonCount]bool
	Released      [MouseButtonCount]bool
	DoubleClicked [MouseButtonCount]bool
	Dragging      [MouseButtonCount]bool
	DragDelta     [2]float32
	Wheel         [2]float32
}

const (
	MouseButtonPrimary   = 0
	MouseButtonSecondary = 1
	MouseButtonTertiary  = 2
	MouseButtonCount     = 3
)

func (ms *MouseState) SetCursor(id imgui.MouseCursorID) {
	imgui.SetMouseCursor(id)
}

func (g *glfwPlatform) GetMouse() *MouseState {
	io := imgui.CurrentIO()
	pos := imgui.MousePos()
	wx, wy := io.MouseWheel()

	m := &MouseState{
		Pos:   [2]float32{pos.X, pos.Y},
		Wheel: [2]float32{wx, wy},
	}

	for b := 0; b < MouseButtonCount; b++ {
		m.Down[b] = imgui.IsMouseDown(b)
		m.Released[b] = imgui.IsMouseReleased(b)
		m.Clicked[b] = imgui.IsMouseClicked(b)
		m.DoubleClicked[b] = imgui.IsMouseDoubleClicked(b)
		m.Dragging[b] = imgui.IsMouseDragging(b, 0)
		if m.Dragging[b] {
			delta := imgui.MouseDragDelta(b, 0.)
			m.DragDelta = [2]float32{delta.X, delta.Y}
			imgui.ResetMouseDragDelta(b)
		}
	}

	return m
}

type Key int

const (
	KeyEnter = iota
	KeyUpArrow
	KeyDownArrow
	KeyLeftArrow
	KeyRightArrow
	KeyHome
	KeyEnd
	KeyBackspace
	KeyDelete
	KeyEscape
	KeyTab
	KeyPageUp
	KeyPageDown
	KeyShift
	KeyControl
	KeyAlt
	KeySuper
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
	KeyV
)

type KeyboardState struct {
	Input   string
	Pressed map[Key]interface{}
}

func (g *glfwPlatform) GetKeyboard() *KeyboardState {
	keyboard := &KeyboardState{Pressed: make(map[Key]interface{})}

	keyboard.Input = g.InputCharacters()

	// Map \ to END for laptops (hacky...)
	if strings.Contains(keyboard.Input, `\`) {
		keyboard.Input = strings.ReplaceAll(keyboard.Input, `\`, "")
		keyboard.Pressed[KeyEnd] = nil
	}

	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnter)) {
		keyboard.Pressed[KeyEnter] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDownArrow)) {
		keyboard.Pressed[KeyDownArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyUpArrow)) {
		keyboard.Pressed[KeyUpArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyLeftArrow)) {
		keyboard.Pressed[KeyLeftArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyRightArrow)) {
		keyboard.Pressed[KeyRightArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyHome)) {
		keyboard.Pressed[KeyHome] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnd)) {
		keyboard.Pressed[KeyEnd] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyBackspace)) {
		keyboard.Pressed[KeyBackspace] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDelete)) {
		keyboard.Pressed[KeyDelete] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEscape)) {
		keyboard.Pressed[KeyEscape] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyTab)) {
		keyboard.Pressed[KeyTab] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageUp)) {
		keyboard.Pressed[KeyPageUp] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageDown)) {
		keyboard.Pressed[KeyPageDown] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyV)) {
		keyboard.Pressed[KeyV] = nil
	}
	const ImguiF1 = 290
	for i := 0; i < 12; i++ {
		if imgui.IsKeyPressed(ImguiF1 + i) {
			keyboard.Pressed[Key(int(KeyF1)+i)] = nil
		}
	}
	io := imgui.CurrentIO()
	if io.KeyShiftPressed() {
		keyboard.Pressed[KeyShift] = nil
	}
	if io.KeyCtrlPressed() {
		keyboard.Pressed[KeyControl] = nil
	}
	if io.KeyAltPressed() {
		keyboard.Pressed[KeyAlt] = nil
	}
	if io.KeySuperPressed() {
		keyboard.Pressed[KeySuper] = nil
	}

	return keyboard
}

func (k *KeyboardState) IsPressed(key Key) bool {
	_, ok := k.Pressed[key]
	return ok
}
