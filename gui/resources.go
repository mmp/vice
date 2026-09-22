// gui/resources.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package gui

import (
	"fmt"
	"os"

	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

var _ util.SyncUI = (*SyncUI)(nil)

// SyncUI implements util.SyncUI with modal dialog boxes, for the resource
// sync that vice's GUI applications run before they read any resources.
type SyncUI struct {
	plat     platform.Platform
	font     *Font
	progress *ModalDialog
	client   *syncProgressClient
}

// NewSyncUI returns a SyncUI that draws into the given platform's window.
// The font is what its dialogs draw their text with; the sync runs before
// the application's main loop, so it renders its own frames.
func NewSyncUI(p platform.Platform, font *Font) *SyncUI {
	return &SyncUI{plat: p, font: font}
}

func (u *SyncUI) PromptModifiedFiles(files []string) util.SyncChoice {
	client := &modifiedFilesClient{files: files}
	d := NewModalDialog(client, u.plat)
	RunDialogEventLoop(u.plat, u.font, d, func() bool { return client.done })
	return client.choice
}

func (u *SyncUI) BackupFailed(err error) util.SyncChoice {
	client := &backupFailedClient{errMsg: err.Error()}
	d := NewModalDialog(client, u.plat)
	RunDialogEventLoop(u.plat, u.font, d, func() bool { return client.done })
	return client.choice
}

func (u *SyncUI) BackedUp(dir string) {
	client := &backupCompleteClient{dir: dir}
	d := NewModalDialog(client, u.plat)
	RunDialogEventLoop(u.plat, u.font, d, func() bool { return client.done })
}

func (u *SyncUI) Update(st util.SyncStatus) {
	if u.progress == nil {
		u.client = &syncProgressClient{}
		u.progress = NewModalDialog(u.client, u.plat)
	}
	u.client.status = st
	DrawDialogFrame(u.plat, u.font, u.progress)
}

// syncProgressClient shows the progress of the files being downloaded.
type syncProgressClient struct {
	status util.SyncStatus
}

func (r *syncProgressClient) Title() string {
	return "Downloading Resources"
}

func (r *syncProgressClient) Opening() {}

func (r *syncProgressClient) FixedSize() [2]float32 {
	return [2]float32{450, 150}
}

func (r *syncProgressClient) Buttons() []DialogButton {
	// The download loop has no cancellation point to return to, so the
	// only thing Cancel can do is end the program.
	return []DialogButton{{Text: "Cancel",
		Action: func() bool {
			os.Exit(1)
			return true
		}}}
}

func (r *syncProgressClient) Draw() int {
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

// modifiedFilesClient warns the user about resource files they have
// modified that the sync would overwrite.
type modifiedFilesClient struct {
	files  []string
	choice util.SyncChoice
	done   bool
}

func (m *modifiedFilesClient) Title() string {
	return "Modified Resource Files Detected"
}

func (m *modifiedFilesClient) Opening() {}

func (m *modifiedFilesClient) Buttons() []DialogButton {
	return []DialogButton{
		{
			Text: "Back Up and Update",
			Action: func() bool {
				m.choice = util.SyncBackupAndContinue
				m.done = true
				return true
			},
		},
		{
			Text: "Overwrite All",
			Action: func() bool {
				m.choice = util.SyncOverwriteAll
				m.done = true
				return true
			},
		},
		{
			Text: "Quit",
			Action: func() bool {
				m.choice = util.SyncQuit
				m.done = true
				return true
			},
		},
	}
}

func (m *modifiedFilesClient) Draw() int {
	imgui.Text("The following resource files have been modified locally and will be\noverwritten by the update:\n")
	for _, f := range m.files {
		imgui.BulletText(f)
	}
	imgui.Text("")
	return -1
}

// backupCompleteClient tells the user where their modified files were
// copied to; without it the backup is of little use to them.
type backupCompleteClient struct {
	dir  string
	done bool
}

func (b *backupCompleteClient) Title() string {
	return "Modified Files Backed Up"
}

func (b *backupCompleteClient) Opening() {}

func (b *backupCompleteClient) Buttons() []DialogButton {
	return []DialogButton{
		{
			Text: "Ok",
			Action: func() bool {
				b.done = true
				return true
			},
		},
	}
}

func (b *backupCompleteClient) Draw() int {
	imgui.Text("Your modified resource files were copied to:\n")
	imgui.Text(b.dir)
	imgui.Text("")
	return -1
}

// backupFailedClient is shown when backing up modified files fails.
type backupFailedClient struct {
	errMsg string
	choice util.SyncChoice
	done   bool
}

func (b *backupFailedClient) Title() string {
	return "Backup Failed"
}

func (b *backupFailedClient) Opening() {}

func (b *backupFailedClient) Buttons() []DialogButton {
	return []DialogButton{
		{
			Text: "Overwrite All",
			Action: func() bool {
				b.choice = util.SyncOverwriteAll
				b.done = true
				return true
			},
		},
		{
			Text: "Quit",
			Action: func() bool {
				b.choice = util.SyncQuit
				b.done = true
				return true
			},
		},
	}
}

func (b *backupFailedClient) Draw() int {
	imgui.Text("Failed to back up modified files:\n")
	imgui.Text(b.errMsg)
	imgui.Text("")
	return -1
}
