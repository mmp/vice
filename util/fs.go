// pkg/util/fs.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"io/fs"
	"os"
)

type RootFS struct{}

func (r RootFS) Open(filename string) (fs.File, error) {
	return os.Open(filename)
}
