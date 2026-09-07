// util/resources_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"io/fs"
	"testing"
)

// TestLoadResourceBytesDoesNotOverallocate guards the uncompressed path
// against going back through io.ReadAll, whose doubling buffer holds a second
// copy of the file and leaves slack in the result. That costs over a gigabyte
// of transient heap for the speech models.
func TestLoadResourceBytesDoesNotOverallocate(t *testing.T) {
	// Big enough that io.ReadAll's doubling overshoots by kilobytes;
	// a couple-KB file lands within a few bytes either way.
	const path = "airport_artccs.json"

	want, err := fs.Stat(GetResourcesFS(), path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	b := LoadResourceBytes(path)
	if int64(len(b)) != want.Size() {
		t.Errorf("%s: got %d bytes, want %d", path, len(b), want.Size())
	}
	// fs.ReadFile allocates one spare byte to see EOF; a growing buffer
	// overshoots by a fraction of the file size, which for a model is
	// tens of megabytes.
	if cap(b) > len(b)+64 {
		t.Errorf("%s: cap %d exceeds len %d; the file was copied through a growing buffer",
			path, cap(b), len(b))
	}
}
