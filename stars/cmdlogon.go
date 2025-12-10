// stars/cmdlogon.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 3 of the TCW Operator Manual
package stars

func init() {
	// 3.11.1 Basic consolidation of inactive and future flights (p. 3-22)
	//(CommandModeMultiFunc, "C"+STARSTriangleCharacter+"[TCP]", unimplementedCommand),
	//(CommandModeMultiFunc, "C[TCP][TCP]", unimplementedCommand),

	// 3.11.2 Limited consolidation of inactive and future flights assigned by fix pairs
	//(CommandModeMultiFunc, "C"+STARSTriangleCharacter+"[TCP]/", unimplementedCommand),
	//(CommandModeMultiFunc, "C[TCP][TCP]/", unimplementedCommand),

	// 3.11.3 Full consolidation of active, inactive, and future flights (p. 3-28)
	//(CommandModeMultiFunc, "C"+STARSTriangleCharacter+"[TCP]+", unimplementedCommand),
	//(CommandModeMultiFunc, "C[TCP][TCP]+", unimplementedCommand),

	// 3.11.4 Deconsolidate inactive and future flights (p. 3-32)
	//(CommandModeMultiFunc, "C", unimplementedCommand),
	//(CommandModeMultiFunc, "C[TCP]", unimplementedCommand),

	// 3.11.5 Limited deconsolidation (p. 3-34)
	//(CommandModeMultiFunc, "C/", unimplementedCommand),

	// 3.11.6 Perform partial resectorization by fix pair ID (p. 3-36)
	//(CommandModeMultiFunc, "C.[TCP][FIELD:1]", unimplementedCommand),

	// 3.11.7 Perform partial resectorization by fix names
	//(CommandModeMultiFunc, "C.[TCP][FIELD:3]", unimplementedCommand),
	//(CommandModeMultiFunc, "C.[TCP][FIELD:3]*[FIELD:3]", unimplementedCommand),

	// 3.11.8 Print out current consolidation or sectorization data (p. 3-38)
	//(CommandModeMultiFunc, "CP1", unimplementedCommand),
	//(CommandModeMultiFunc, "CP2", unimplementedCommand),

	// 3.11.9 Display consolidated positions in Preview area (p. 3-43)
	//(CommandModeMultiFunc, "D+", unimplementedCommand),
}
