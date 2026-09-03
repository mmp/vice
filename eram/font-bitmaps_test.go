package eram

import "testing"

// TestBitmapFontDigitWidths checks that the digits of each text font are
// drawn to a common width.  The fonts use tabular figures so that columns of
// numbers line up, and a digit that is narrower than its neighbours means the
// PCF conversion produced a malformed glyph: the 8 in EramText-9.pcf was two
// pixels too narrow, which made data block lines ending in 8 look truncated.
// 1 is excluded since it is legitimately narrow in every size.
func TestBitmapFontDigitWidths(t *testing.T) {
	for name, bf := range eramFonts {
		widths := make(map[int][]string)
		for ch := '0'; ch <= '9'; ch++ {
			if ch != '1' && int(ch) < len(bf.Glyphs) && bf.Glyphs[ch].StepX != 0 {
				w := bf.Glyphs[ch].Bounds[0]
				widths[w] = append(widths[w], string(ch))
			}
		}
		if len(widths) > 1 {
			t.Errorf("%s: digits are drawn to %d different widths: %v", name, len(widths), widths)
		}
	}
}
