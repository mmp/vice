// resources_download.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for builds that are expected to fetch resources as needed
// into a local cache from cloud storage.
//go:build release

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// SyncResources brings the local resources directory up to date with the
// manifest this build was made with. It presents the sync in the GUI when
// plat is non-nil and on the terminal otherwise.
func SyncResources(plat platform.Platform) error {
	var sync util.SyncUI
	if plat != nil {
		sync = &imguiSyncUI{plat: plat}
	} else {
		sync = &util.TextSyncUI{}
	}

	if err := util.SyncResources(sync); err != nil {
		if errors.Is(err, util.ErrSyncCanceled) {
			os.Exit(0)
		}
		return err
	}
	return nil
}

// imguiSyncUI implements util.SyncUI using modal dialog boxes.
type imguiSyncUI struct {
	plat   platform.Platform
	dialog *ModalDialogBox
	client *ResourcesDownloadModalClient
}

func (u *imguiSyncUI) PromptModifiedFiles(files []string) util.SyncChoice {
	client := &ModifiedFilesWarningModalClient{files: files}
	d := NewModalDialogBox(client, u.plat)
	runModalEventLoop(u.plat, d, func() bool { return client.done })
	return client.choice
}

func (u *imguiSyncUI) BackupFailed(err error) util.SyncChoice {
	client := &BackupErrorModalClient{errMsg: err.Error()}
	d := NewModalDialogBox(client, u.plat)
	runModalEventLoop(u.plat, d, func() bool { return client.done })
	return client.choice
}

func (u *imguiSyncUI) BackedUp(dir string) {
	client := &BackupCompleteModalClient{dir: dir}
	d := NewModalDialogBox(client, u.plat)
	runModalEventLoop(u.plat, d, func() bool { return client.done })
}

func (u *imguiSyncUI) Update(st util.SyncStatus) {
	if u.dialog == nil {
		u.client = &ResourcesDownloadModalClient{}
		u.dialog = NewModalDialogBox(u.client, u.plat)
	}
	u.client.status = st
	drawModalFrame(u.plat, u.dialog)
}

// ResourcesDownloadModalClient implements ModalDialogClient to show the
// progress of the files being downloaded.
type ResourcesDownloadModalClient struct {
	status util.SyncStatus
}

func (r *ResourcesDownloadModalClient) Title() string {
	return "Downloading Resources"
}

func (r *ResourcesDownloadModalClient) Opening() {}

func (r *ResourcesDownloadModalClient) FixedSize() [2]float32 {
	return [2]float32{450, 150}
}

func (r *ResourcesDownloadModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{{text: "Cancel",
		action: func() bool {
			os.Exit(1)
			return true
		}}}
}

func (r *ResourcesDownloadModalClient) Draw() int {
	imgui.Text(fmt.Sprintf("Downloaded file %d of %d", r.status.CompletedFiles, r.status.TotalFiles))

	if r.status.CurrentFile != "" {
		imgui.Text(fmt.Sprintf("Downloading: %s", r.status.CurrentFile))
	} else {
		imgui.Text("\n")
	}

	imgui.Spacing()

	if r.status.TotalBytes > 0 {
		const mb = 1024 * 1024
		progress := float32(r.status.DownloadedBytes) / float32(r.status.TotalBytes)

		// Progress bar fills available width (-1 for width)
		imgui.ProgressBarV(progress, imgui.Vec2{-1, 0}, fmt.Sprintf("%.1f MB / %.1f MB",
			float64(r.status.DownloadedBytes)/mb, float64(r.status.TotalBytes)/mb))
	}

	return -1
}

// ModifiedFilesWarningModalClient implements ModalDialogClient to warn the
// user about resource files they have modified that will be overwritten.
type ModifiedFilesWarningModalClient struct {
	files  []string
	choice util.SyncChoice
	done   bool
}

func (m *ModifiedFilesWarningModalClient) Title() string {
	return "Modified Resource Files Detected"
}

func (m *ModifiedFilesWarningModalClient) Opening() {}

func (m *ModifiedFilesWarningModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		{
			text: "Back Up and Update",
			action: func() bool {
				m.choice = util.SyncBackupAndContinue
				m.done = true
				return true
			},
		},
		{
			text: "Overwrite All",
			action: func() bool {
				m.choice = util.SyncOverwriteAll
				m.done = true
				return true
			},
		},
		{
			text: "Quit",
			action: func() bool {
				m.choice = util.SyncQuit
				m.done = true
				return true
			},
		},
	}
}

func (m *ModifiedFilesWarningModalClient) Draw() int {
	imgui.Text("The following resource files have been modified locally and will be\noverwritten by the update:\n")
	for _, f := range m.files {
		imgui.BulletText(f)
	}
	imgui.Text("")
	return -1
}

// BackupCompleteModalClient tells the user where their modified files were
// copied to; without it the backup is of little use to them.
type BackupCompleteModalClient struct {
	dir  string
	done bool
}

func (b *BackupCompleteModalClient) Title() string {
	return "Modified Files Backed Up"
}

func (b *BackupCompleteModalClient) Opening() {}

func (b *BackupCompleteModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		{
			text: "Ok",
			action: func() bool {
				b.done = true
				return true
			},
		},
	}
}

func (b *BackupCompleteModalClient) Draw() int {
	imgui.Text("Your modified resource files were copied to:\n")
	imgui.Text(b.dir)
	imgui.Text("")
	return -1
}

// BackupErrorModalClient is shown when backing up modified files fails.
type BackupErrorModalClient struct {
	errMsg string
	choice util.SyncChoice
	done   bool
}

func (b *BackupErrorModalClient) Title() string {
	return "Backup Failed"
}

func (b *BackupErrorModalClient) Opening() {}

func (b *BackupErrorModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		{
			text: "Overwrite All",
			action: func() bool {
				b.choice = util.SyncOverwriteAll
				b.done = true
				return true
			},
		},
		{
			text: "Quit",
			action: func() bool {
				b.choice = util.SyncQuit
				b.done = true
				return true
			},
		},
	}
}

func (b *BackupErrorModalClient) Draw() int {
	imgui.Text("Failed to back up modified files:\n")
	imgui.Text(b.errMsg)
	imgui.Text("")
	return -1
}
