package stt

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

func TestFrequencyValueParser(t *testing.T) {
	p := &frequencyValueParser{}
	tests := []struct {
		name     string
		tokens   []Token
		expected av.Frequency
		ok       bool
	}{
		{
			name: "two-digit decimal",
			tokens: []Token{
				{Text: "123", Type: TokenNumber, Value: 123},
				{Text: "point", Type: TokenWord, Value: -1},
				{Text: "45", Type: TokenNumber, Value: 45},
			},
			expected: av.NewFrequency(123.45),
			ok:       true,
		},
		{
			name: "one-digit decimal",
			tokens: []Token{
				{Text: "118", Type: TokenNumber, Value: 118},
				{Text: "point", Type: TokenWord, Value: -1},
				{Text: "9", Type: TokenNumber, Value: 9},
			},
			expected: av.NewFrequency(118.9),
			ok:       true,
		},
		{
			name: "zero decimal",
			tokens: []Token{
				{Text: "120", Type: TokenNumber, Value: 120},
				{Text: "point", Type: TokenWord, Value: -1},
				{Text: "0", Type: TokenNumber, Value: 0},
			},
			expected: av.NewFrequency(120.0),
			ok:       true,
		},
		{
			name: "leading-zero decimal preserved",
			tokens: []Token{
				{Text: "118", Type: TokenNumber, Value: 118},
				{Text: "point", Type: TokenWord, Value: -1},
				{Text: "09", Type: TokenNumber, Value: 9},
			},
			expected: av.NewFrequency(118.09),
			ok:       true,
		},
		{
			name: "whole out of range",
			tokens: []Token{
				{Text: "12", Type: TokenNumber, Value: 12},
				{Text: "point", Type: TokenWord, Value: -1},
				{Text: "5", Type: TokenNumber, Value: 5},
			},
			ok: false,
		},
		{
			name: "missing point",
			tokens: []Token{
				{Text: "123", Type: TokenNumber, Value: 123},
				{Text: "45", Type: TokenNumber, Value: 45},
				{Text: "foo", Type: TokenWord, Value: -1},
			},
			ok: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, consumed, _ := p.parse(tt.tokens, 0, Aircraft{})
			if tt.ok {
				if consumed != 3 {
					t.Fatalf("consumed = %d, want 3", consumed)
				}
				got, ok := value.(av.Frequency)
				if !ok {
					t.Fatalf("value type = %T, want av.Frequency", value)
				}
				if got != tt.expected {
					t.Fatalf("got %d (%s), want %d (%s)", got, got, tt.expected, tt.expected)
				}
			} else {
				if value != nil || consumed != 0 {
					t.Fatalf("expected no match, got value=%v consumed=%d", value, consumed)
				}
			}
		})
	}
}

func TestCompassDirParser(t *testing.T) {
	p := &compassDirParser{}
	tests := []struct {
		name     string
		token    Token
		expected string
		ok       bool
	}{
		{name: "cardinal", token: Token{Text: "west", Type: TokenWord}, expected: "W", ok: true},
		{name: "ordinal", token: Token{Text: "southwest", Type: TokenWord}, expected: "SW", ok: true},
		{name: "invalid", token: Token{Text: "detgy", Type: TokenWord}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, consumed, _ := p.parse([]Token{tt.token}, 0, Aircraft{})
			if tt.ok {
				if consumed != 1 {
					t.Fatalf("consumed = %d, want 1", consumed)
				}
				got, ok := value.(string)
				if !ok {
					t.Fatalf("value type = %T, want string", value)
				}
				if got != tt.expected {
					t.Fatalf("got %q, want %q", got, tt.expected)
				}
			} else {
				if value != nil || consumed != 0 {
					t.Fatalf("expected no match, got value=%v consumed=%d", value, consumed)
				}
			}
		})
	}
}

func TestExtractFixFromCrossCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		expected string
	}{
		{"CROSLY/A60", "ROSLY"},
		{"CJOBAS/A57+", "JOBAS"},
		{"CROSLY/S250", "ROSLY"},
		{"CROSLY/M80", "ROSLY"},
		{"CDETGY/5W/A30", "DETGY"},
		// Not cross-fix commands
		{"C90", ""},    // climb command (no slash)
		{"CI9L", ""},   // cleared approach (no slash)
		{"D30", ""},    // wrong prefix
		{"DROSLY", ""}, // wrong prefix
		{"S210", ""},   // wrong prefix
		{"", ""},       // empty
		{"C", ""},      // too short
		{"C/A60", ""},  // slash at position 1 (no fix)
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := extractFixFromCrossCommand(tt.cmd)
			if got != tt.expected {
				t.Errorf("extractFixFromCrossCommand(%q) = %q, want %q", tt.cmd, got, tt.expected)
			}
		})
	}
}

func TestExtractCrossAltitude(t *testing.T) {
	tests := []struct {
		cmd      string
		expected int
	}{
		{"CROSLY/A60", 60},
		{"CJOBAS/A57+", 57},
		{"CROSLY/A30-", 30},
		{"CDETGY/5W/A30", 30},
		{"CROSLY/S250", 0}, // speed, not altitude
		{"CROSLY/M80", 0},  // mach, not altitude
		{"C90", 0},         // no slash
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := extractCrossAltitude(tt.cmd)
			if got != tt.expected {
				t.Errorf("extractCrossAltitude(%q) = %d, want %d", tt.cmd, got, tt.expected)
			}
		})
	}
}

func TestTransformToAfterFix(t *testing.T) {
	tests := []struct {
		name     string
		fix      string
		cmd      string
		crossAlt int
		expected string
	}{
		{"descend", "ROSLY", "D30", 60, "AROSLY/D30"},
		{"climb", "ROSLY", "C90", 60, "AROSLY/C90"},
		{"maintain below cross alt is descend", "ROSLY", "A30", 60, "AROSLY/D30"},
		{"maintain above cross alt is climb", "ROSLY", "A90", 60, "AROSLY/C90"},
		{"maintain with no cross alt defaults descend", "ROSLY", "A60", 0, "AROSLY/D60"},
		{"then descend", "ROSLY", "TD30", 60, "AROSLY/D30"},
		{"then climb", "ROSLY", "TC90", 60, "AROSLY/C90"},
		{"then maintain below", "ROSLY", "TA30", 60, "AROSLY/D30"},
		{"then maintain above", "ROSLY", "TA90", 60, "AROSLY/C90"},
		// Should not transform
		{"direct to fix", "ROSLY", "DROSLY", 60, ""},
		{"speed command", "ROSLY", "S210", 60, ""},
		{"heading command", "ROSLY", "H270", 60, ""},
		{"cleared approach", "ROSLY", "CI9L", 60, ""},
		{"too short", "ROSLY", "D", 60, ""},
		{"empty", "ROSLY", "", 60, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transformToAfterFix(tt.fix, tt.cmd, tt.crossAlt)
			if got != tt.expected {
				t.Errorf("transformToAfterFix(%q, %q, %d) = %q, want %q", tt.fix, tt.cmd, tt.crossAlt, got, tt.expected)
			}
		})
	}
}

func TestCoalesceAfterFixAltitudes(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "cross fix then descend",
			input:    []string{"CROSLY/A60", "D30"},
			expected: []string{"CROSLY/A60", "AROSLY/D30"},
		},
		{
			name:     "cross fix then climb",
			input:    []string{"CROSLY/A60", "C90"},
			expected: []string{"CROSLY/A60", "AROSLY/C90"},
		},
		{
			name:     "cross fix then maintain below is descend",
			input:    []string{"CROSLY/A60", "A50"},
			expected: []string{"CROSLY/A60", "AROSLY/D50"},
		},
		{
			name:     "cross fix then maintain above is climb",
			input:    []string{"CROSLY/A60", "A90"},
			expected: []string{"CROSLY/A60", "AROSLY/C90"},
		},
		{
			name:     "cross fix then then-descend",
			input:    []string{"CROSLY/A60", "TD30"},
			expected: []string{"CROSLY/A60", "AROSLY/D30"},
		},
		{
			name:     "standalone descend unchanged",
			input:    []string{"D30"},
			expected: []string{"D30"},
		},
		{
			name:     "cross fix then speed unchanged",
			input:    []string{"CROSLY/A60", "S210"},
			expected: []string{"CROSLY/A60", "S210"},
		},
		{
			name:     "cross fix then direct-to-fix unchanged",
			input:    []string{"CROSLY/A60", "DROSLY"},
			expected: []string{"CROSLY/A60", "DROSLY"},
		},
		{
			name:     "non-cross C command unchanged",
			input:    []string{"C90", "D30"},
			expected: []string{"C90", "D30"},
		},
		{
			name:     "cleared approach unchanged",
			input:    []string{"CI9L", "D30"},
			expected: []string{"CI9L", "D30"},
		},
		{
			name:     "cross fix at or above then descend",
			input:    []string{"CJOBAS/A57+", "D30"},
			expected: []string{"CJOBAS/A57+", "AJOBAS/D30"},
		},
		{
			name:     "cross fix at or below then climb",
			input:    []string{"CJOBAS/A57-", "C90"},
			expected: []string{"CJOBAS/A57-", "AJOBAS/C90"},
		},
		{
			name:     "only transforms immediately following command",
			input:    []string{"CROSLY/A60", "D30", "D20"},
			expected: []string{"CROSLY/A60", "AROSLY/D30", "D20"},
		},
		{
			name:     "multiple cross-fix commands",
			input:    []string{"CROSLY/A60", "D30", "CJOBAS/A57+", "D20"},
			expected: []string{"CROSLY/A60", "AROSLY/D30", "CJOBAS/A57+", "AJOBAS/D20"},
		},
		{
			name:     "single command unchanged",
			input:    []string{"CROSLY/A60"},
			expected: []string{"CROSLY/A60"},
		},
		{
			name:     "cross distance then descend unchanged",
			input:    []string{"CDETGY/5W/A30", "D20"},
			expected: []string{"CDETGY/5W/A30", "D20"},
		},
		{
			name:     "cross fix altitude and speed still coalesces",
			input:    []string{"CROSLY/A60/S250", "D20"},
			expected: []string{"CROSLY/A60/S250", "AROSLY/D20"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy so we don't modify test data
			input := make([]string, len(tt.input))
			copy(input, tt.input)
			got := coalesceAfterFixAltitudes(input)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %v, want %v", got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %v, want %v", got, tt.expected)
					break
				}
			}
		})
	}
}
