// stars/fonts.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"C"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)
import "maps"

func (sp *STARSPane) initializeFonts(r renderer.Renderer, p platform.Platform) {
	fonts := createFontAtlas(r, p)
	get := func(name string, size int) *renderer.Font {
		idx := slices.IndexFunc(fonts, func(f *renderer.Font) bool { return f.Id.Name == name && f.Id.Size == size })
		if idx == -1 {
			panic(name + " size " + strconv.Itoa(size) + " not found in STARS fonts")
		}
		return fonts[idx]
	}

	sp.systemFontA[0] = get("sddCharFontSetASize0", 9)
	sp.systemFontA[1] = get("sddCharFontSetASize1", 14)
	sp.systemFontA[2] = get("sddCharFontSetASize2", 17)
	sp.systemFontA[3] = get("sddCharFontSetASize3", 20)
	sp.systemFontA[4] = get("sddCharFontSetASize4", 21)
	sp.systemFontA[5] = get("sddCharFontSetASize5", 23)
	sp.systemOutlineFontA[0] = get("sddCharOutlineFontSetASize0", 9)
	sp.systemOutlineFontA[1] = get("sddCharOutlineFontSetASize1", 14)
	sp.systemOutlineFontA[2] = get("sddCharOutlineFontSetASize2", 17)
	sp.systemOutlineFontA[3] = get("sddCharOutlineFontSetASize3", 20)
	sp.systemOutlineFontA[4] = get("sddCharOutlineFontSetASize4", 21)
	sp.systemOutlineFontA[5] = get("sddCharOutlineFontSetASize5", 23)
	sp.dcbFontA[0] = get("sddCharFontSetASize0", 9)
	sp.dcbFontA[1] = get("sddCharFontSetASize1", 14)
	sp.dcbFontA[2] = get("sddCharFontSetASize2", 17)

	sp.systemFontB[0] = get("sddCharFontSetBSize0", 11)
	sp.systemFontB[1] = get("sddCharFontSetBSize1", 12)
	sp.systemFontB[2] = get("sddCharFontSetBSize2", 15)
	sp.systemFontB[3] = get("sddCharFontSetBSize3", 16)
	sp.systemFontB[4] = get("sddCharFontSetBSize4", 18)
	sp.systemFontB[5] = get("sddCharFontSetBSize5", 19)
	sp.systemOutlineFontB[0] = get("sddCharOutlineFontSetBSize0", 11)
	sp.systemOutlineFontB[1] = get("sddCharOutlineFontSetBSize1", 12)
	sp.systemOutlineFontB[2] = get("sddCharOutlineFontSetBSize2", 15)
	sp.systemOutlineFontB[3] = get("sddCharOutlineFontSetBSize3", 16)
	sp.systemOutlineFontB[4] = get("sddCharOutlineFontSetBSize4", 18)
	sp.systemOutlineFontB[5] = get("sddCharOutlineFontSetBSize5", 19)
	sp.dcbFontB[0] = get("sddCharFontSetBSize0", 11)
	sp.dcbFontB[1] = get("sddCharFontSetBSize1", 12)
	sp.dcbFontB[2] = get("sddCharFontSetBSize2", 15)

	sp.cursorsFont = get("STARS cursors", 30)
}

func (sp *STARSPane) systemFont(ctx *panes.Context, idx int) *renderer.Font {
	if sp.FontSelection == fontLegacy {
		return sp.systemFontA[idx]
	} else if sp.FontSelection == fontARTS {
		return sp.systemFontB[idx]
	} else if ctx.FacilityAdaptation.UseLegacyFont {
		return sp.systemFontA[idx]
	} else {
		return sp.systemFontB[idx]
	}
}

func (sp *STARSPane) systemOutlineFont(ctx *panes.Context, idx int) *renderer.Font {
	if sp.FontSelection == fontLegacy {
		return sp.systemOutlineFontA[idx]
	} else if sp.FontSelection == fontARTS {
		return sp.systemOutlineFontB[idx]
	} else if ctx.FacilityAdaptation.UseLegacyFont {
		return sp.systemOutlineFontA[idx]
	} else {
		return sp.systemOutlineFontB[idx]
	}
}

func (sp *STARSPane) dcbFont(ctx *panes.Context, idx int) *renderer.Font {
	if sp.FontSelection == fontLegacy {
		return sp.dcbFontA[idx]
	} else if sp.FontSelection == fontARTS {
		return sp.dcbFontB[idx]
	} else if ctx.FacilityAdaptation.UseLegacyFont {
		return sp.dcbFontA[idx]
	} else {
		return sp.dcbFontB[idx]
	}
}

// The ∆ character in the STARS font isn't at the regular ∆ unicode rune,
// so patch it up.
func rewriteDelta(s string) string {
	s = strings.ReplaceAll(s, "Δ", STARSTriangleCharacter)
	return strings.ReplaceAll(s, "∆", STARSTriangleCharacter)
}

func createFontAtlas(r renderer.Renderer, p platform.Platform) []*renderer.Font {
	// Patch up the cursors (which are missing Offset values) so that they
	// are centered at the point where they are drawn.
	for i := range starsCursors.Glyphs {
		g := &starsCursors.Glyphs[i]
		g.Offset[0] = -(g.Bounds[0] + 1) / 2
		g.Offset[1] = starsCursors.Height - (g.Bounds[1]+1)/2
	}

	seq := util.Seq2Concat(maps.All(starsFonts), util.Seq2Single("STARS cursors", starsCursors))
	return renderer.CreateBitmapFontAtlas(r, p, seq)
}
