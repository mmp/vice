// eram/errors_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"testing"

	"github.com/mmp/vice/client"
)

// Every error ERAM has a code for is one a controller can provoke, so it must
// survive being reduced to its message in an RPC reply and come back as the
// same value; otherwise the scope falls back to FORMAT.
func TestERAMErrorsRoundTripOverRPC(t *testing.T) {
	if len(eramErrorRemap) == 0 {
		t.Fatal("no errors to check")
	}
	for err := range eramErrorRemap {
		if decoded := client.DecodeErrorMessage(err.Error()); decoded != err {
			t.Errorf("%q: decoded to %v, not the original error", err, decoded)
		}
	}
}
