// stars/cmdops_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"testing"

	"github.com/mmp/vice/util"
)

// Re-entering a scratchpad's current text restores the text it replaced.
func TestRestorePriorScratchpad(t *testing.T) {
	for _, c := range []struct {
		entry, current, prior, want string
	}{
		{"ABC", "ABC", "XYZ", "XYZ"},
		{"ABC", "ABC", "", ""},
		{"ABC", "XYZ", "DEF", "ABC"},
		{"", "ABC", "XYZ", ""},
	} {
		var entry util.Optional[string]
		entry.Set(c.entry)
		restorePriorScratchpad(&entry, c.current, c.prior)
		if entry.Get() != c.want {
			t.Errorf("%q over %q (prior %q): got %q, want %q", c.entry, c.current, c.prior, entry.Get(), c.want)
		}
	}

	var unset util.Optional[string]
	restorePriorScratchpad(&unset, "", "XYZ")
	if unset.IsSet {
		t.Errorf("an absent entry was set to %q", unset.Get())
	}
}
