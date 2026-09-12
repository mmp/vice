// pkg/log/logdir_local.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !release

package log

// serverLogDir returns the log directory for server processes in local
// builds: a directory in the working directory, which keeps a server's logs
// next to the tree or deployment it was launched from.
func serverLogDir() string {
	return "vice-logs"
}
