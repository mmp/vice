package wx

import (
	"testing"
)

func TestLevelIndexInverse(t *testing.T) {
	for i := range NumSampleLevels {
		id := IdFromLevelIndex(i)
		idx := LevelIndexFromId([]byte(id))
		if idx != i {
			t.Errorf("Inverse check failed for index %d: got ID %q, converted back to %d", i, id, idx)
		}
	}

	testCases := []struct {
		index int
		id    string
	}{
		{0, "1013.2 mb"},
		{1, "1000 mb"},
		{39, "50 mb"},
	}

	for _, tc := range testCases {
		gotId := IdFromLevelIndex(tc.index)
		if gotId != tc.id {
			t.Errorf("IdFromLevelIndex(%d) = %q, want %q", tc.index, gotId, tc.id)
		}

		gotIndex := LevelIndexFromId([]byte(tc.id))
		if gotIndex != tc.index {
			t.Errorf("LevelIndexFromId(%q) = %d, want %d", tc.id, gotIndex, tc.index)
		}
	}
}
