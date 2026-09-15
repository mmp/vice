// gui/files.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package gui

import (
	"fmt"
	"slices"

	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/ncruces/zenity"
)

// DrawFilePicker draws the controls for choosing one work-in-progress file
// to load alongside the installed resources: the path currently chosen, a
// button that opens a file dialog, and one that clears it. id keeps the
// buttons distinct from other pickers in the same window. It returns whether
// the choice changed, along with whatever the file dialog reported; a
// canceled dialog is neither an error nor a change.
func DrawFilePicker(label, id string, path *string, patterns []string) (bool, error) {
	imgui.Text(label + ": " + util.Select(*path != "", *path, "none"))

	imgui.SameLine()
	if imgui.Button("Select##" + id) {
		p, err := selectFile(label, patterns)
		if err != nil || p == "" {
			return false, err
		}
		*path = p
		return true, nil
	}

	imgui.SameLine()
	if imgui.Button("Clear##"+id) && *path != "" {
		*path = ""
		return true, nil
	}

	return false, nil
}

// DrawFileListPicker is DrawFilePicker for a kind of file that may be given
// more than once, as facility configurations are: a TRACON's and its ARTCC's
// may both be overridden at the same time.
func DrawFileListPicker(label, id string, paths *[]string, patterns []string) (bool, error) {
	if len(*paths) == 0 {
		imgui.Text(label + ": none")
	}

	remove := -1
	for i, path := range *paths {
		imgui.Text(label + ": " + path)
		imgui.SameLine()
		if imgui.Button(fmt.Sprintf("Clear##%s%d", id, i)) {
			remove = i
		}
	}
	if remove != -1 {
		*paths = slices.Delete(*paths, remove, remove+1)
		return true, nil
	}

	if imgui.Button("Add##" + id) {
		p, err := selectFile(label, patterns)
		if err != nil || p == "" {
			return false, err
		}
		*paths = append(*paths, p)
		return true, nil
	}

	return false, nil
}

// selectFile puts up the system's file dialog, returning an empty path if
// the user canceled it.
func selectFile(label string, patterns []string) (string, error) {
	path, err := zenity.SelectFile(zenity.Title("Select "+label+" File"),
		zenity.FileFilters{{Name: label, Patterns: patterns}})
	if err == zenity.ErrCanceled {
		return "", nil
	}
	return path, err
}
