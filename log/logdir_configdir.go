// log/logdir_configdir.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build release

package log

// serverLogDir returns the log directory for server processes in installed
// builds. An installed viceserver runs from a read-only location such as
// C:\Program Files\vice, so its logs and crash reports go alongside the
// client's in the user's configuration directory.
func serverLogDir() string {
	return configDir()
}
