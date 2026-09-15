// gui/dialog.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package gui

import (
	"fmt"
	"runtime"

	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	implogl3 "github.com/AllenDang/cimgui-go/impl/opengl3"
)

// ModalDialog is a modal dialog box: while one is up, it is the only thing the
// user can interact with. Its contents and buttons come from its DialogClient.
type ModalDialog struct {
	closed, isOpen bool
	client         DialogClient
	platform       platform.Platform
}

// DialogButton is one of the buttons along the bottom of a dialog. Action
// returns whether the dialog should close; a nil Action closes it.
type DialogButton struct {
	Text     string
	Disabled bool
	Action   func() bool
}

// DialogClient supplies a dialog's title, contents, and buttons; a dialog is
// defined by implementing it.
type DialogClient interface {
	Title() string
	Opening()
	Buttons() []DialogButton
	// Draw returns the index of an equivalently-clicked button (out of range if
	// none). For dialogs with no buttons, returning a non-negative value closes
	// the dialog.
	Draw() int
}

// FixedSizeDialogClient is an optional interface that dialog clients can implement
// to specify a fixed window size instead of auto-resizing based on content.
type FixedSizeDialogClient interface {
	FixedSize() [2]float32 // Returns [width, height] in pixels (before DPI scaling)
}

// NewModalDialog returns a dialog that draws the given client's contents. It is
// not shown until something draws it, either in the application's own frame
// or via DrawDialogFrame.
func NewModalDialog(c DialogClient, p platform.Platform) *ModalDialog {
	return &ModalDialog{client: c, platform: p}
}

// Closed reports whether the user has dismissed the dialog, after which
// drawing it does nothing.
func (m *ModalDialog) Closed() bool { return m.closed }

// Draw draws the dialog in the current imgui frame.
func (m *ModalDialog) Draw() {
	if m.closed {
		return
	}

	title := fmt.Sprintf("%s##%p", m.client.Title(), m)
	imgui.OpenPopupStr(title)

	dpiScale := util.Select(runtime.GOOS == "windows", m.platform.DPIScale(), float32(1))

	// Use the main viewport for positioning and sizing so that dialogs
	// are centered correctly when multi-viewport is enabled (imgui uses
	// screen-space coordinates with viewports).
	mainVP := imgui.MainViewport()
	vpPos := mainVP.Pos()
	vpSize := mainVP.Size()

	// Check if client wants a fixed size window
	var flags imgui.WindowFlags
	if fixedSize, ok := m.client.(FixedSizeDialogClient); ok {
		// Fixed size dialog - don't auto-resize
		flags = imgui.WindowFlagsNoResize | imgui.WindowFlagsNoSavedSettings | imgui.WindowFlagsNoScrollbar
		size := fixedSize.FixedSize()
		imgui.SetNextWindowSize(imgui.Vec2{dpiScale * size[0], dpiScale * size[1]})
	} else {
		// Auto-resize dialog with constraints
		flags = imgui.WindowFlagsNoResize | imgui.WindowFlagsAlwaysAutoResize | imgui.WindowFlagsNoSavedSettings
		maxHeight := vpSize.Y * 19 / 20
		imgui.SetNextWindowSizeConstraints(imgui.Vec2{dpiScale * 850, dpiScale * 100}, imgui.Vec2{-1, maxHeight})
	}

	// Center the dialog on the main viewport, near the top.
	topMargin := vpSize.Y * 0.05
	imgui.SetNextWindowPosV(imgui.Vec2{vpPos.X + vpSize.X/2, vpPos.Y + topMargin}, imgui.CondAlways, imgui.Vec2{0.5, 0})

	// Force the modal into the main viewport so it doesn't become a
	// separate OS window (which can end up behind the main window when
	// ConfigViewportsNoAutoMerge is enabled).
	imgui.SetNextWindowViewport(mainVP.ID())

	if imgui.BeginPopupModalV(title, nil, flags) {
		if !m.isOpen {
			imgui.SetKeyboardFocusHere()
			m.client.Opening()
			m.isOpen = true
		}

		selIndex := m.client.Draw()
		imgui.Text("\n") // spacing

		buttons := m.client.Buttons()

		// Only position buttons if we have any
		if len(buttons) > 0 {
			// First, figure out where to start drawing so the buttons end up right-justified.
			// https://github.com/ocornut/imgui/discussions/3862
			var allButtonText []string
			for _, b := range buttons {
				allButtonText = append(allButtonText, b.Text)
			}
			setCursorForRightButtons(allButtonText)
		}

		for i, b := range buttons {
			if b.Disabled {
				imgui.BeginDisabled()
			}
			if i > 0 {
				imgui.SameLine()
			}
			if (imgui.Button(b.Text) || i == selIndex) && !b.Disabled {
				if b.Action == nil || b.Action() {
					imgui.CloseCurrentPopup()
					m.closed = true
					m.isOpen = false
				}
			}
			if b.Disabled {
				imgui.EndDisabled()
			}
		}
		if len(buttons) == 0 && selIndex >= 0 {
			imgui.CloseCurrentPopup()
			m.closed = true
			m.isOpen = false
		}
		imgui.EndPopup()
	}
}

// RunDialogEventLoop runs a blocking event loop that renders the given dialog
// each frame until done() returns true. All pre-main-loop modal dialogs
// (fatal errors, resource warnings, whisper benchmark, etc.) use this to
// avoid duplicating the imgui frame/render boilerplate.
func RunDialogEventLoop(p platform.Platform, font *renderer.Font, d *ModalDialog, done func() bool) {
	for !done() {
		DrawDialogFrame(p, font, d)
	}
}

// DrawDialogFrame renders a single frame with the given dialog box drawn in it.
func DrawDialogFrame(p platform.Platform, font *renderer.Font, d *ModalDialog) {
	p.ProcessEvents()
	p.NewFrame()
	imgui.NewFrame()
	font.ImguiPush()
	d.Draw()
	imgui.PopFont()

	imgui.Render()
	implogl3.RenderDrawData(imgui.CurrentDrawData())

	if imgui.CurrentIO().ConfigFlags()&imgui.ConfigFlagsViewportsEnable != 0 {
		imgui.UpdatePlatformWindows()
		imgui.RenderPlatformWindowsDefault()
		p.MakeContextCurrent()
	}

	p.PostRender()
}

// setCursorForRightButtons leaves the cursor where drawing the given
// buttons will end them flush with the right edge of the dialog.
func setCursorForRightButtons(text []string) {
	style := imgui.CurrentStyle()
	width := float32(0)

	for i, t := range text {
		width += imgui.CalcTextSize(t).X + 2*style.FramePadding().X
		if i > 0 {
			// space between buttons
			width += style.ItemSpacing().X
		}
	}
	offset := imgui.ContentRegionAvail().X - width
	imgui.SetCursorPos(imgui.Vec2{offset, imgui.CursorPosY()})
}
