// stars/colors.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
)

type MonitorColors struct {
	// Tracks/datablocks
	OwnedDatablock    renderer.RGB // Also handoff attention, ...
	UnownedDatablock  renderer.RGB
	AlertDatablock    renderer.RGB
	CautionDatablock  renderer.RGB
	GhostDatablock    renderer.RGB
	SelectedDatablock renderer.RGB // Middle-button - highlight
	TrackGeometry     renderer.RGB // Search target extent / fused-track symbol
	TrackHistory      [5]renderer.RGB

	// Lists / text
	List        renderer.RGB
	ListFrame   renderer.RGB
	TextAlert   renderer.RGB
	TextWarning renderer.RGB // caution

	// UI
	Cursor     renderer.RGB
	Background renderer.RGB // at 100% contrast

	// Tools
	RangeBearingLine renderer.RGB
	PTL              renderer.RGB
	Compass          renderer.RGB
	RangeRing        renderer.RGB
	JRingCone        renderer.RGB // TPA
	ATPAWarning      renderer.RGB
	ATPAAlert        renderer.RGB

	// Maps / restriction areas
	MapA                [8]renderer.RGB
	MapB                [8]renderer.RGB
	RestrictionAreaText renderer.RGB
	RestrictionAreaGeom [8]renderer.RGB

	// WX
	WX             [scope.NumWxLevels]renderer.RGB
	WXStipple      renderer.RGB
	WXLevelStipple [scope.NumWxLevels]int // 0=none, 1=light, 2=dense

	// DCB
	DCBButton            renderer.RGB
	DCBActiveButton      renderer.RGB
	DCBText              renderer.RGB
	DCBTextSelected      renderer.RGB
	DCBUnsupportedButton renderer.RGB
	DCBUnsupportedText   renderer.RGB
	DCBDisabledButton    renderer.RGB
	DCBDisabledText      renderer.RGB
	DCBBackground        renderer.RGB
	DCBTopBevel          renderer.RGB
	DCBBottomBevel       renderer.RGB
	DCBWXButton          renderer.RGB
	DCBActiveWXButton    renderer.RGB
}

var monitorColorSets = map[string]MonitorColors{
	"legacy": {
		OwnedDatablock:    renderer.RGBFromUInt8(255, 255, 255),
		UnownedDatablock:  renderer.RGBFromUInt8(0, 255, 0),
		AlertDatablock:    renderer.RGBFromUInt8(255, 0, 0),
		CautionDatablock:  renderer.RGBFromUInt8(255, 255, 0),
		GhostDatablock:    renderer.RGBFromUInt8(255, 255, 0),
		SelectedDatablock: renderer.RGBFromUInt8(0, 255, 255),
		TrackGeometry:     renderer.RGBFromUInt8(30, 120, 255),
		TrackHistory: [5]renderer.RGB{
			renderer.RGBFromUInt8(30, 80, 200),
			renderer.RGBFromUInt8(70, 70, 170),
			renderer.RGBFromUInt8(50, 50, 130),
			renderer.RGBFromUInt8(40, 40, 110),
			renderer.RGBFromUInt8(30, 30, 90),
		},

		List:        renderer.RGBFromUInt8(0, 255, 0),
		ListFrame:   renderer.RGBFromUInt8(0, 255, 0),
		TextAlert:   renderer.RGBFromUInt8(255, 0, 0),
		TextWarning: renderer.RGBFromUInt8(255, 255, 0),

		Cursor:     renderer.RGBFromUInt8(255, 255, 255),
		Background: renderer.RGBFromUInt8(50, 50, 50),

		RangeBearingLine: renderer.RGBFromUInt8(255, 255, 255),
		PTL:              renderer.RGBFromUInt8(255, 255, 255),
		Compass:          renderer.RGBFromUInt8(140, 140, 140),
		RangeRing:        renderer.RGBFromUInt8(140, 140, 140),
		JRingCone:        renderer.RGBFromUInt8(128, 128, 255),
		ATPAWarning:      renderer.RGBFromUInt8(255, 255, 0),
		ATPAAlert:        renderer.RGBFromUInt8(255, 55, 0),

		MapA: [8]renderer.RGB{
			renderer.RGBFromUInt8(140, 140, 140),
			renderer.RGBFromUInt8(0, 255, 255),
			renderer.RGBFromUInt8(255, 0, 255),
			renderer.RGBFromUInt8(238, 201, 0),
			renderer.RGBFromUInt8(238, 106, 80),
			renderer.RGBFromUInt8(162, 205, 90),
			renderer.RGBFromUInt8(218, 165, 32),
			renderer.RGBFromUInt8(72, 118, 255),
		},
		MapB: [8]renderer.RGB{
			renderer.RGBFromUInt8(140, 140, 140),
			renderer.RGBFromUInt8(132, 112, 255),
			renderer.RGBFromUInt8(118, 238, 198),
			renderer.RGBFromUInt8(237, 145, 33),
			renderer.RGBFromUInt8(218, 112, 214),
			renderer.RGBFromUInt8(238, 180, 180),
			renderer.RGBFromUInt8(50, 205, 50),
			renderer.RGBFromUInt8(255, 106, 106),
		},
		RestrictionAreaText: renderer.RGBFromUInt8(255, 255, 0),
		RestrictionAreaGeom: [8]renderer.RGB{
			renderer.RGBFromUInt8(255, 255, 0),
			renderer.RGBFromUInt8(0, 255, 255),
			renderer.RGBFromUInt8(255, 0, 255),
			renderer.RGBFromUInt8(238, 201, 0),
			renderer.RGBFromUInt8(238, 106, 80),
			renderer.RGBFromUInt8(132, 112, 255),
			renderer.RGBFromUInt8(118, 238, 198),
			renderer.RGBFromUInt8(50, 205, 50),
		},

		WX: [scope.NumWxLevels]renderer.RGB{
			renderer.RGBFromUInt8(38, 77, 77),
			renderer.RGBFromUInt8(38, 77, 77),
			renderer.RGBFromUInt8(38, 77, 77),
			renderer.RGBFromUInt8(100, 100, 51),
			renderer.RGBFromUInt8(100, 100, 51),
			renderer.RGBFromUInt8(100, 100, 51),
		},
		WXLevelStipple: [scope.NumWxLevels]int{0, 1, 2, 0, 1, 2},
		WXStipple:      renderer.RGBFromUInt8(255, 255, 255),

		DCBButton:            renderer.RGBFromUInt8(0, 44, 0),
		DCBActiveButton:      renderer.RGBFromUInt8(0, 78, 0),
		DCBText:              renderer.RGBFromUInt8(255, 255, 255),
		DCBTextSelected:      renderer.RGBFromUInt8(255, 255, 0),
		DCBUnsupportedButton: renderer.RGBFromUInt8(100, 100, 100),
		DCBUnsupportedText:   renderer.RGBFromUInt8(200, 200, 200),
		DCBDisabledButton:    renderer.RGBFromUInt8(0, 22, 0),
		DCBDisabledText:      renderer.RGBFromUInt8(128, 128, 128),
		DCBBackground:        renderer.RGBFromUInt8(0, 12, 0),
		DCBTopBevel:          renderer.RGBFromUInt8(50, 50, 50),
		DCBBottomBevel:       renderer.RGBFromUInt8(0, 0, 0),
		DCBWXButton:          renderer.RGBFromUInt8(83, 83, 162),   // 50,50,100
		DCBActiveWXButton:    renderer.RGBFromUInt8(116, 116, 162), // 70,70,100
	},
	"mdm3": {
		OwnedDatablock:    renderer.RGBFromUInt8(254, 236, 237),
		UnownedDatablock:  renderer.RGBFromUInt8(106, 218, 88),
		AlertDatablock:    renderer.RGBFromUInt8(255, 56, 24),
		CautionDatablock:  renderer.RGBFromUInt8(254, 255, 50),
		GhostDatablock:    renderer.RGBFromUInt8(254, 255, 50),
		SelectedDatablock: renderer.RGBFromUInt8(96, 196, 192),
		TrackGeometry:     renderer.RGBFromUInt8(139, 163, 253),
		TrackHistory: [5]renderer.RGB{
			renderer.RGBFromUInt8(146, 172, 255),
			renderer.RGBFromUInt8(123, 145, 208),
			renderer.RGBFromUInt8(100, 115, 161),
			renderer.RGBFromUInt8(87, 85, 114),
			renderer.RGBFromUInt8(64, 55, 67),
		},

		List:        renderer.RGBFromUInt8(106, 218, 88),
		ListFrame:   renderer.RGBFromUInt8(106, 218, 88),
		TextAlert:   renderer.RGBFromUInt8(255, 56, 24),
		TextWarning: renderer.RGBFromUInt8(254, 255, 50),

		Cursor:     renderer.RGBFromUInt8(254, 236, 237),
		Background: renderer.RGBFromUInt8(50, 50, 50),

		RangeBearingLine: renderer.RGBFromUInt8(254, 236, 237),
		PTL:              renderer.RGBFromUInt8(254, 236, 237),
		Compass:          renderer.RGBFromUInt8(203, 187, 159),
		RangeRing:        renderer.RGBFromUInt8(203, 187, 159),
		JRingCone:        renderer.RGBFromUInt8(139, 163, 253),
		ATPAWarning:      renderer.RGBFromUInt8(254, 255, 50),
		ATPAAlert:        renderer.RGBFromUInt8(255, 56, 24),

		MapA: [8]renderer.RGB{
			renderer.RGBFromUInt8(203, 187, 159),
			renderer.RGBFromUInt8(96, 196, 192),
			renderer.RGBFromUInt8(216, 62, 208),
			renderer.RGBFromUInt8(254, 147, 14),
			renderer.RGBFromUInt8(255, 135, 173),
			renderer.RGBFromUInt8(108, 224, 90),
			renderer.RGBFromUInt8(254, 147, 14),
			renderer.RGBFromUInt8(139, 163, 253),
		},
		MapB: [8]renderer.RGB{
			renderer.RGBFromUInt8(203, 187, 159),
			renderer.RGBFromUInt8(139, 163, 253),
			renderer.RGBFromUInt8(109, 236, 230),
			renderer.RGBFromUInt8(254, 147, 14),
			renderer.RGBFromUInt8(253, 64, 249),
			renderer.RGBFromUInt8(252, 134, 171),
			renderer.RGBFromUInt8(102, 208, 85),
			renderer.RGBFromUInt8(255, 177, 99),
		},
		RestrictionAreaText: renderer.RGBFromUInt8(254, 255, 50),
		RestrictionAreaGeom: [8]renderer.RGB{
			renderer.RGBFromUInt8(254, 255, 50),
			renderer.RGBFromUInt8(96, 196, 192),
			renderer.RGBFromUInt8(216, 62, 208),
			renderer.RGBFromUInt8(254, 147, 14),
			renderer.RGBFromUInt8(255, 135, 173),
			renderer.RGBFromUInt8(139, 163, 253),
			renderer.RGBFromUInt8(109, 236, 230),
			renderer.RGBFromUInt8(102, 208, 85),
		},

		WX: [scope.NumWxLevels]renderer.RGB{
			renderer.RGBFromUInt8(57, 73, 51),
			renderer.RGBFromUInt8(57, 73, 51),
			renderer.RGBFromUInt8(107, 86, 19),
			renderer.RGBFromUInt8(107, 86, 19),
			renderer.RGBFromUInt8(107, 67, 84),
			renderer.RGBFromUInt8(107, 67, 84),
		},
		WXLevelStipple: [scope.NumWxLevels]int{0, 2, 0, 2, 0, 2},
		WXStipple:      renderer.RGBFromUInt8(185, 172, 147),

		DCBButton:            renderer.RGBFromUInt8(19, 47, 0),
		DCBActiveButton:      renderer.RGBFromUInt8(57, 94, 40),
		DCBText:              renderer.RGBFromUInt8(254, 236, 237),
		DCBTextSelected:      renderer.RGBFromUInt8(254, 255, 50),
		DCBUnsupportedButton: renderer.RGBFromUInt8(100, 100, 100), // ???
		DCBUnsupportedText:   renderer.RGBFromUInt8(0, 0, 0),       // ???
		DCBDisabledButton:    renderer.RGBFromUInt8(3, 39, 0),
		DCBDisabledText:      renderer.RGBFromUInt8(25, 51, 8),
		DCBBackground:        renderer.RGBFromUInt8(0, 10, 0),
		DCBTopBevel:          renderer.RGBFromUInt8(152, 135, 108),
		DCBBottomBevel:       renderer.RGBFromUInt8(0, 0, 0),
		DCBWXButton:          renderer.RGBFromUInt8(44, 81, 73),
		DCBActiveWXButton:    renderer.RGBFromUInt8(58, 99, 95),
	},
	"mdm4": {
		OwnedDatablock:    renderer.RGBFromUInt8(253, 241, 237),
		UnownedDatablock:  renderer.RGBFromUInt8(41, 202, 48),
		AlertDatablock:    renderer.RGBFromUInt8(255, 20, 14),
		CautionDatablock:  renderer.RGBFromUInt8(253, 255, 32),
		GhostDatablock:    renderer.RGBFromUInt8(253, 255, 32),
		SelectedDatablock: renderer.RGBFromUInt8(16, 183, 133),
		TrackGeometry:     renderer.RGBFromUInt8(51, 115, 140),
		TrackHistory: [5]renderer.RGB{
			renderer.RGBFromUInt8(80, 200, 236),
			renderer.RGBFromUInt8(65, 161, 191),
			renderer.RGBFromUInt8(50, 122, 146),
			renderer.RGBFromUInt8(35, 83, 101),
			renderer.RGBFromUInt8(20, 44, 56),
		},

		List:        renderer.RGBFromUInt8(41, 202, 48),
		ListFrame:   renderer.RGBFromUInt8(41, 202, 48),
		TextAlert:   renderer.RGBFromUInt8(255, 20, 14),
		TextWarning: renderer.RGBFromUInt8(253, 255, 32),

		Cursor:     renderer.RGBFromUInt8(253, 241, 237),
		Background: renderer.RGBFromUInt8(50, 50, 50),

		RangeBearingLine: renderer.RGBFromUInt8(253, 241, 237),
		PTL:              renderer.RGBFromUInt8(253, 241, 237),
		Compass:          renderer.RGBFromUInt8(177, 167, 99),
		RangeRing:        renderer.RGBFromUInt8(177, 167, 99),
		JRingCone:        renderer.RGBFromUInt8(51, 115, 140),
		ATPAWarning:      renderer.RGBFromUInt8(253, 255, 32),
		ATPAAlert:        renderer.RGBFromUInt8(255, 20, 14),

		MapA: [8]renderer.RGB{
			renderer.RGBFromUInt8(177, 167, 99),
			renderer.RGBFromUInt8(16, 183, 133),
			renderer.RGBFromUInt8(194, 45, 141),
			renderer.RGBFromUInt8(252, 98, 16),
			renderer.RGBFromUInt8(252, 100, 108),
			renderer.RGBFromUInt8(42, 210, 49),
			renderer.RGBFromUInt8(252, 98, 16),
			renderer.RGBFromUInt8(89, 199, 230),
		},
		MapB: [8]renderer.RGB{
			renderer.RGBFromUInt8(177, 167, 99),
			renderer.RGBFromUInt8(97, 219, 246),
			renderer.RGBFromUInt8(19, 236, 158),
			renderer.RGBFromUInt8(252, 98, 16),
			renderer.RGBFromUInt8(249, 56, 170),
			renderer.RGBFromUInt8(252, 100, 108),
			renderer.RGBFromUInt8(39, 187, 44),
			renderer.RGBFromUInt8(255, 149, 54),
		},
		RestrictionAreaText: renderer.RGBFromUInt8(253, 255, 32),
		RestrictionAreaGeom: [8]renderer.RGB{
			renderer.RGBFromUInt8(253, 255, 32),
			renderer.RGBFromUInt8(16, 183, 133),
			renderer.RGBFromUInt8(194, 45, 141),
			renderer.RGBFromUInt8(252, 98, 16),
			renderer.RGBFromUInt8(252, 100, 108),
			renderer.RGBFromUInt8(97, 219, 246),
			renderer.RGBFromUInt8(19, 236, 158),
			renderer.RGBFromUInt8(39, 187, 44),
		},

		WX: [scope.NumWxLevels]renderer.RGB{
			renderer.RGBFromUInt8(57, 73, 51),
			renderer.RGBFromUInt8(57, 73, 51),
			renderer.RGBFromUInt8(107, 86, 19),
			renderer.RGBFromUInt8(107, 86, 19),
			renderer.RGBFromUInt8(107, 67, 84),
			renderer.RGBFromUInt8(107, 67, 84),
		},
		WXLevelStipple: [scope.NumWxLevels]int{0, 2, 0, 2, 0, 2},
		WXStipple:      renderer.RGBFromUInt8(153, 144, 89),

		DCBButton:            renderer.RGBFromUInt8(2, 20, 4),
		DCBActiveButton:      renderer.RGBFromUInt8(7, 37, 12),
		DCBText:              renderer.RGBFromUInt8(253, 241, 237),
		DCBTextSelected:      renderer.RGBFromUInt8(253, 255, 32),
		DCBUnsupportedButton: renderer.RGBFromUInt8(100, 100, 100), // ???
		DCBUnsupportedText:   renderer.RGBFromUInt8(200, 200, 200), // ???
		DCBDisabledButton:    renderer.RGBFromUInt8(0, 6, 0),
		DCBDisabledText:      renderer.RGBFromUInt8(85, 73, 76),
		DCBBackground:        renderer.RGBFromUInt8(0, 1, 0),
		DCBTopBevel:          renderer.RGBFromUInt8(158, 134, 139),
		DCBBottomBevel:       renderer.RGBFromUInt8(0, 0, 0),
		DCBWXButton:          renderer.RGBFromUInt8(0, 16, 20),
		DCBActiveWXButton:    renderer.RGBFromUInt8(1, 26, 33),
	},
}
