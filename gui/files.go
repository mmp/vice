// gui/files.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package gui

import (
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/ncruces/zenity"
)

// DrawDirectoryPicker draws the controls for choosing a directory: the path
// currently chosen, a button that opens the system's directory dialog, and
// one that stops using it, which leaves the directory itself alone. id keeps the buttons distinct from other pickers in
// the same window. It returns whether the choice changed, along with
// whatever the dialog reported; a canceled dialog is neither an error nor a
// change.
func DrawDirectoryPicker(label, id string, dir *string) (bool, error) {
	imgui.Text(label + ": " + util.Select(*dir != "", *dir, "none"))

	imgui.SameLine()
	if imgui.Button("Select##" + id) {
		p, err := zenity.SelectFile(zenity.Title("Select "+label), zenity.Directory())
		if err == zenity.ErrCanceled {
			return false, nil
		} else if err != nil || p == "" {
			return false, err
		}
		*dir = p
		return true, nil
	}

	imgui.SameLine()
	if imgui.Button("Disable##"+id) && *dir != "" {
		*dir = ""
		return true, nil
	}

	return false, nil
}
