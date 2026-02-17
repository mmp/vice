package stt

import (
	"fmt"
)

// registerAllCommands registers all STT command templates.
func registerAllCommands() {
	// === ALTITUDE COMMANDS ===
	registerSTTCommand(
		"descend|descended|descending [and] maintain {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("D%d", alt) },
		WithName("descend_maintain"),
		WithPriority(10),
		WithThenVariant("TD%d"),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"descend|descended|descending [and] [to] {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("D%d", alt) },
		WithName("descend"),
		WithPriority(5),
		WithThenVariant("TD%d"),
	)

	registerSTTCommand(
		"climb|climbed|climbing [and] maintain {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("C%d", alt) },
		WithName("climb_maintain"),
		WithPriority(10),
		WithThenVariant("TC%d"),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"climb|climbed|climbing [and] [to] {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("C%d", alt) },
		WithName("climb"),
		WithPriority(5),
		WithThenVariant("TC%d"),
	)

	registerSTTCommand(
		"maintain [at] {altitude}",
		func(alt int) string { return fmt.Sprintf("A%d", alt) },
		WithName("maintain_altitude"),
		WithPriority(3),
	)

	// Standalone altitude - catches cases where the command keyword is garbled
	// but "thousand" or similar altitude indicator is heard.
	// E.g., "decelerating three thousand" where "decelerating" is garbled "descend to"
	// Uses standalone_altitude parser which only matches TokenAltitude (not plain numbers).
	registerSTTCommand(
		"{standalone_altitude}",
		func(alt int) string { return fmt.Sprintf("A%d", alt) },
		WithName("standalone_altitude"),
		WithPriority(1), // Very low priority - only matches if nothing else does
	)

	registerSTTCommand(
		"expedite descent|descend|your",
		func() string { return "ED" },
		WithName("expedite_descent"),
		WithPriority(10),
	)

	registerSTTCommand(
		"expedite climb|your",
		func() string { return "EC" },
		WithName("expedite_climb"),
		WithPriority(10),
	)

	registerSTTCommand(
		"climb via [the] {sid}",
		func(sid string) string { return "CVS" },
		WithName("climb_via_sid"),
		WithPriority(15),
	)

	registerSTTCommand(
		"climb via sid",
		func() string { return "CVS" },
		WithName("climb_via_sid"),
		WithPriority(14),
	)

	registerSTTCommand(
		"descend via [the] {star}",
		func(star string) string { return "DVS" },
		WithName("descend_via_star"),
		WithPriority(15),
	)

	registerSTTCommand(
		"descend via star",
		func() string { return "DVS" },
		WithName("descend_via_star"),
		WithPriority(16),
	)

	// "arrival" is often used as a synonym for "star" in ATC phraseology
	registerSTTCommand(
		"descend via [the] arrival",
		func() string { return "DVS" },
		WithName("descend_via_arrival"),
		WithPriority(16),
	)

	// Pattern for garbled "descend via STAR" - "via the {STAR} arrival" is strong
	// enough context even without a clear "descend" (e.g., "to sin via the boo seven arrival")
	registerSTTCommand(
		"via [the] {star} arrival",
		func(star string) string { return "DVS" },
		WithName("descend_via_star_implicit"),
		WithPriority(14),
	)

	registerSTTCommand(
		"say altitude",
		func() string { return "SA" },
		WithName("say_altitude"),
		WithPriority(10),
	)

	// === HEADING COMMANDS ===
	registerSTTCommand(
		"[turn] [to] left heading {heading}",
		func(hdg int) string { return fmt.Sprintf("L%03d", hdg) },
		WithName("turn_left_heading"),
		WithPriority(10),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"[turn] [to] right heading {heading}",
		func(hdg int) string { return fmt.Sprintf("R%03d", hdg) },
		WithName("turn_right_heading"),
		WithPriority(10),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"[turn] [to] left {heading}",
		func(hdg int) string { return fmt.Sprintf("L%03d", hdg) },
		WithName("turn_left_only"),
		WithPriority(7),
	)

	registerSTTCommand(
		"[turn] [to] right {heading}",
		func(hdg int) string { return fmt.Sprintf("R%03d", hdg) },
		WithName("turn_right_only"),
		WithPriority(7),
	)

	// "fly heading 090" - specific command with "fly" before "heading"
	registerSTTCommand(
		"fly heading {heading}",
		func(hdg int) string { return fmt.Sprintf("H%03d", hdg) },
		WithName("fly_heading"),
		WithPriority(9),
		WithSayAgainOnFail(),
	)

	// "heading 090" - just the heading keyword
	registerSTTCommand(
		"heading {heading}",
		func(hdg int) string { return fmt.Sprintf("H%03d", hdg) },
		WithName("heading_only"),
		WithPriority(5),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"[fly] present heading",
		func() string { return "H" },
		WithName("present_heading"),
		WithPriority(12),
	)

	registerSTTCommand(
		"turn {degrees}",
		func(dr degreesResult) string {
			dir := "L"
			if dr.direction == "right" {
				dir = "R"
			}
			return fmt.Sprintf("T%d%s", dr.degrees, dir)
		},
		WithName("turn_degrees"),
		WithPriority(8),
	)

	// "turn 270" - bare turn + heading when direction/heading keyword is garbled.
	// Only accepts 3-digit headings (100-360) to avoid false positives with
	// leftover callsign numbers (e.g., "turn 934" from garbled "frontier 934").
	// Low priority so patterns with explicit direction or "heading" keyword win.
	registerSTTCommand(
		"turn [to] {num:100-360}",
		func(hdg int) string { return fmt.Sprintf("H%03d", hdg) },
		WithName("turn_heading_bare"),
		WithPriority(3),
	)

	registerSTTCommand(
		"say heading",
		func() string { return "SH" },
		WithName("say_heading"),
		WithPriority(10),
	)

	// Explanation for the vector; discard
	registerSTTCommand(
		"vectors|vector [for] sequence|spacing|final",
		func() string { return "" },
		WithName("vectors_for"),
		WithPriority(5),
	)

	// === SPEED COMMANDS ===
	registerSTTCommand(
		"reduce|slow [speed] [to] {speed}",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("reduce_speed"),
		WithPriority(10),
		WithThenVariant("TS%d"),
	)

	registerSTTCommand(
		"increase [speed] [to] {speed}",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("increase_speed"),
		WithPriority(10),
		WithThenVariant("TS%d"),
	)

	registerSTTCommand(
		"speed [to] {speed}",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("speed_only"),
		WithPriority(5),
		WithThenVariant("TS%d"),
	)

	registerSTTCommand(
		"maintain [speed] {speed}",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("maintain_speed"),
		WithPriority(2),
		WithThenVariant("TS%d"),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"[maintain] slowest|minimum practical|speed|possible [approach] [speed]",
		func() string { return "SMIN" },
		WithName("slowest_practical"),
		WithPriority(12),
	)

	registerSTTCommand(
		"maximum|best forward|speed",
		func() string { return "SMAX" },
		WithName("maximum_speed"),
		WithPriority(12),
	)

	// Bare speed + knots + until: handles "180 knots until 7 mile final" without
	// a preceding command keyword (e.g., after garbled "that is" filler).
	registerSTTCommand(
		"{speed} knots {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s", spd, until.suffix)
		},
		WithName("bare_speed_knots_until"),
		WithPriority(5),
	)

	registerSTTCommand(
		"say speed|airspeed",
		func() string { return "SS" },
		WithName("say_speed"),
		WithPriority(10),
	)
	registerSTTCommand(
		"say indicated [speed|airspeed]",
		func() string { return "SI" },
		WithName("say_indicated"),
		WithPriority(12),
	)
	registerSTTCommand(
		"say mach [number]",
		func() string { return "SM" },
		WithName("say_mach"),
		WithPriority(12),
	)

	registerSTTCommand(
		"cancel speed [restrictions|restriction]",
		func() string { return "S" },
		WithName("cancel_speed_restriction"),
		WithPriority(10),
	)

	registerSTTCommand(
		"resume normal speed",
		func() string { return "S" },
		WithName("resume_normal_speed"),
		WithPriority(12),
	)

	registerSTTCommand(
		"speed [at] [your] discretion",
		func() string { return "S" },
		WithName("speed_your_discretion"),
		WithPriority(12),
	)

	registerSTTCommand(
		"[maintain] present speed",
		func() string { return "SPRES" },
		WithName("maintain_present_speed"),
		WithPriority(12),
	)

	registerSTTCommand(
		"[maintain] {speed} [knots] or greater|better {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d+/U%s", spd, until.suffix)
		},
		WithName("speed_or_greater_until"),
		WithPriority(15),
	)

	registerSTTCommand(
		"[maintain] {speed} or greater|better",
		func(spd int) string { return fmt.Sprintf("S%d+", spd) },
		WithName("speed_or_greater"),
		WithPriority(12),
		WithThenVariant("TS%d+"),
	)

	registerSTTCommand(
		"do not exceed {speed}",
		func(spd int) string { return fmt.Sprintf("S%d-", spd) },
		WithName("do_not_exceed"),
		WithPriority(12),
		WithThenVariant("TS%d-"),
	)

	registerSTTCommand(
		"comply [with] speed restrictions",
		func() string { return "S" },
		WithName("comply_speed_restrictions"),
		WithPriority(12),
	)

	registerSTTCommand(
		"delete speed restrictions",
		func() string { return "S" },
		WithName("delete_speed_restrictions"),
		WithPriority(12),
	)

	registerSTTCommand(
		"no speed restrictions",
		func() string { return "S" },
		WithName("no_speed_restrictions"),
		WithPriority(12),
	)

	registerSTTCommand(
		"reduce [to] final|minimum approach speed",
		func() string { return "SMIN" },
		WithName("final_approach_speed"),
		WithPriority(15),
	)

	// Speed "until advised" and "for now" patterns - these produce regular speed commands
	// without a specific termination point. Higher priority than speed_until to match first.
	// Require at least one speed-related keyword to avoid matching runway numbers.
	registerSTTCommand(
		"reduce|slow|increase|maintain [speed] [to] {speed} until|unto|intel advised|further [notice]",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("speed_until_advised_verb"),
		WithPriority(18),
	)

	registerSTTCommand(
		"speed [to] {speed} until|unto|intel advised|further [notice]",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("speed_until_advised_keyword"),
		WithPriority(18),
	)

	registerSTTCommand(
		"reduce|slow|increase|maintain [speed] [to] {speed} for now",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("speed_for_now_verb"),
		WithPriority(18),
	)

	registerSTTCommand(
		"speed [to] {speed} for now",
		func(spd int) string { return fmt.Sprintf("S%d", spd) },
		WithName("speed_for_now_keyword"),
		WithPriority(18),
	)

	// Speed until commands - higher priority to match before regular speed commands
	registerSTTCommand(
		"reduce|slow [speed] [to] {speed} {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s", spd, until.suffix)
		},
		WithName("reduce_speed_until"),
		WithPriority(15),
	)

	registerSTTCommand(
		"increase [speed] [to] {speed} {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s", spd, until.suffix)
		},
		WithName("increase_speed_until"),
		WithPriority(15),
	)

	registerSTTCommand(
		"speed [to] {speed} {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s", spd, until.suffix)
		},
		WithName("speed_until"),
		WithPriority(12),
	)

	registerSTTCommand(
		"maintain [speed] {speed} {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s", spd, until.suffix)
		},
		WithName("maintain_speed_until"),
		WithPriority(12),
	)

	registerSTTCommand(
		"reduce|slow [speed] [to] mach [point] {mach}",
		func(mach int) string { return fmt.Sprintf("M%d", mach) },
		WithName("reduce_mach"), WithPriority(12),
	)
	registerSTTCommand(
		"increase [speed] [to] mach [point] {mach}",
		func(mach int) string { return fmt.Sprintf("M%d", mach) },
		WithName("increase_mach"), WithPriority(12),
	)
	registerSTTCommand(
		"maintain mach [point] {mach}",
		func(mach int) string { return fmt.Sprintf("M%d", mach) },
		WithName("maintain_mach"), WithPriority(10),
	)
	registerSTTCommand(
		"mach [point] {mach}",
		func(mach int) string { return fmt.Sprintf("M%d", mach) },
		WithName("mach_only"), WithPriority(7),
	)

	// === NAVIGATION COMMANDS ===
	registerSTTCommand(
		"direct|proceed [direct] [to] [at] {fix}",
		func(fix string) string { return fmt.Sprintf("D%s", fix) },
		WithName("direct_fix"),
		WithPriority(10),
	)

	// "cleared direct [fix]" - high priority pattern with SAYAGAIN when fix is garbled
	registerSTTCommand(
		"cleared direct {fix}",
		func(fix string) string { return fmt.Sprintf("D%s", fix) },
		WithName("cleared_direct_fix_explicit"),
		WithPriority(12), // Higher than cleared_approach so "cleared direct [garbled]" becomes SAYAGAIN/FIX
		WithSayAgainOnFail(),
	)

	// "cleared [to/at] {fix}" - lower priority, no SAYAGAIN (for "cleared to KJFK" etc.)
	registerSTTCommand(
		"cleared [to] [at] {fix}",
		func(fix string) string { return fmt.Sprintf("D%s", fix) },
		WithName("cleared_to_fix"),
		WithPriority(7),
	)

	registerSTTCommand(
		"cross {fix} [at] {altitude}",
		func(fix string, alt int) string { return fmt.Sprintf("C%s/A%d", fix, alt) },
		WithName("cross_fix_altitude"),
		WithPriority(10),
	)

	registerSTTCommand(
		"cross {fix} [at] [or] above {altitude}",
		func(fix string, alt int) string { return fmt.Sprintf("C%s/A%d+", fix, alt) },
		WithName("cross_fix_at_or_above"),
		WithPriority(12), // Higher priority for more specific match
	)

	registerSTTCommand(
		"cross {fix} [at] {speed}",
		func(fix string, spd int) string { return fmt.Sprintf("C%s/S%d", fix, spd) },
		WithName("cross_fix_speed"),
		WithPriority(10),
	)

	registerSTTCommand(
		"cross {fix} [at] {speed} or greater|better",
		func(fix string, spd int) string { return fmt.Sprintf("C%s/S%d+", fix, spd) },
		WithName("cross_fix_speed_or_greater"),
		WithPriority(12),
	)

	registerSTTCommand(
		"cross {fix} [at] [or] [and|at] [do] not [to] exceed {speed}",
		func(fix string, spd int) string { return fmt.Sprintf("C%s/S%d-", fix, spd) },
		WithName("cross_fix_do_not_exceed"),
		WithPriority(12),
	)

	registerSTTCommand(
		"cross {fix} [at] mach [point] {mach}",
		func(fix string, mach int) string { return fmt.Sprintf("C%s/M%d", fix, mach) },
		WithName("cross_fix_mach"),
		WithPriority(10),
	)

	registerSTTCommand(
		"depart {fix} [heading] {heading}",
		func(fix string, hdg int) string { return fmt.Sprintf("D%s/H%03d", fix, hdg) },
		WithName("depart_fix_heading"),
		WithPriority(10),
	)

	// The {hold} parser handles both as-published and controller-specified holds
	registerSTTCommand(
		"hold [north|south|east|west|northeast|northwest|southeast|southwest] [of] [at] [as] [published] {hold}",
		func(holdCmd string) string { return holdCmd },
		WithName("hold"),
		WithPriority(15),
	)

	// === APPROACH COMMANDS ===
	registerSTTCommand(
		"at {fix} [cleared] [clear] [for] [approach] {approach}",
		func(fix, appr string) string { return fmt.Sprintf("A%s/C%s", fix, appr) },
		WithName("at_fix_cleared_approach"),
		WithPriority(15),
	)

	// These templates handle "at FIX intercept the localizer" with varying runway info
	registerSTTCommand(
		"at {fix} intercept [the] localizer",
		func(fix string) string { return fmt.Sprintf("A%s/I", fix) },
		WithName("at_fix_intercept_localizer"),
		WithPriority(15),
	)
	// With runway number before localizer
	registerSTTCommand(
		"at {fix} intercept [the] [runway] {num:1-36} [left|right|center] localizer",
		func(fix string, _ int) string { return fmt.Sprintf("A%s/I", fix) },
		WithName("at_fix_intercept_localizer_runway"),
		WithPriority(16), // Higher priority for more specific match
	)

	registerSTTCommand(
		"expect [vectors] [for] [to] [the] {approach_lahso}",
		func(appr string) string { return fmt.Sprintf("E%s", appr) },
		WithName("expect_approach"),
		WithPriority(15),     // Higher than heading commands to match approach context first
		WithSayAgainOnFail(), // "expect [approach]" should ask for clarification if approach unrecognized
	)

	registerSTTCommand(
		"standby [for] [the] approach",
		func() string { return "E" },
		WithName("standby_approach"),
		WithPriority(14),
	)

	registerSTTCommand(
		"expect [the] approach",
		func() string { return "E" },
		WithName("expect_the_approach"),
		WithPriority(14),
	)

	// "vectors {approach}" without SAYAGAIN - "vectors" alone (e.g., "vectors for sequence")
	// is often informational filler, not an actual expect command
	registerSTTCommand(
		"vector|vectors [for] [to] {approach_lahso}",
		func(appr string) string { return fmt.Sprintf("E%s", appr) },
		WithName("vectors_approach"),
		WithPriority(15),
	)

	registerSTTCommand(
		"cleared [the] visual [approach] [runway] {num:1-36} left",
		func(rwy int) string { return fmt.Sprintf("CV%dL", rwy) },
		WithName("cleared_visual_left"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cleared [the] visual [approach] [runway] {num:1-36} right",
		func(rwy int) string { return fmt.Sprintf("CV%dR", rwy) },
		WithName("cleared_visual_right"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cleared [the] visual [approach] [runway] {num:1-36} center",
		func(rwy int) string { return fmt.Sprintf("CV%dC", rwy) },
		WithName("cleared_visual_center"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cleared [the] visual [approach] [runway] {num:1-36}",
		func(rwy int) string { return fmt.Sprintf("CV%d", rwy) },
		WithName("cleared_visual"),
		WithPriority(16),
	)

	registerSTTCommand(
		"cleared [approach] [for] {approach}",
		func(appr string) string { return fmt.Sprintf("C%s", appr) },
		WithName("cleared_approach"),
		WithPriority(8),
		WithSayAgainOnFail(), // Garbled approach clearances should ask for clarification
	)

	registerSTTCommand(
		"clear to|for [approach] {approach}",
		func(appr string) string { return fmt.Sprintf("C%s", appr) },
		WithName("clear_to_approach"),
		WithPriority(8),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"localizer [approach] [acquired] {approach}",
		func(appr string) string { return fmt.Sprintf("C%s", appr) },
		WithName("localizer_approach"),
		WithPriority(7),
	)

	registerSTTCommand(
		"cleared straight [in] {approach}",
		func(appr string) string { return fmt.Sprintf("CSI%s", appr) },
		WithName("cleared_straight_in"),
		WithPriority(12),
	)

	registerSTTCommand(
		"cancel [approach] clearance",
		func() string { return "CAC" },
		WithName("cancel_approach"),
		WithPriority(15),
	)

	// Intercept localizer - multiple patterns from most specific to least
	// Common STT garbage words that appear between "intercept" and "localizer":
	// work, gonna, going, to, load, low, look, for
	// Pattern: "intercept the runway 27 right localizer"
	registerSTTCommand(
		"intercept|join|set [the] [runway] {num:1-36} left|right|center localizer",
		func(_ int) string { return "I" },
		WithName("intercept_localizer_runway_side"),
		WithPriority(14),
	)
	// Pattern: "intercept the runway 27 localizer"
	registerSTTCommand(
		"intercept|join|set [the] [runway] {num:1-36} localizer",
		func(_ int) string { return "I" },
		WithName("intercept_localizer_runway"),
		WithPriority(13),
	)
	// Pattern: "intercept the four right localizer" (runway number and side, no "runway" word)
	registerSTTCommand(
		"intercept|join|set [the] {num:1-36} left|right|center localizer",
		func(_ int) string { return "I" },
		WithName("intercept_localizer_num_side"),
		WithPriority(12),
	)
	// Pattern: "intercept the left localizer"
	registerSTTCommand(
		"intercept|join|set [the] left|right|center localizer",
		func() string { return "I" },
		WithName("intercept_localizer_side"),
		WithPriority(11),
	)
	// Pattern: "intercept the localizer" - basic pattern
	// Note: garbage words between "intercept" and "localizer" are handled by
	// the slack mechanism in literalMatcher, not by enumerating them here.
	registerSTTCommand(
		"intercept|join|set [the] localizer",
		func() string { return "I" },
		WithName("intercept_localizer"),
		WithPriority(10),
	)
	// Pattern: standalone "localizer" without "intercept" keyword.
	// When "localizer" appears alone (e.g., after a heading command), it means
	// intercept the localizer. The word "localizer" is never part of an approach
	// clearance, so this is unambiguous.
	registerSTTCommand(
		"localizer",
		func() string { return "I" },
		WithName("standalone_localizer"),
		WithPriority(5),
	)

	// Absorb "vectors to the localizer" phrasing so the standalone "localizer"
	// template above doesn't fire on it. "Vectors to the localizer" is
	// informational context (e.g., "heading 040 vectors to the localizer"),
	// not an intercept command.
	registerSTTCommand(
		"vectors|vector [to] [the] [for] [through] localizer",
		func() string { return "" },
		WithName("vectors_localizer_absorb"),
		WithPriority(6),
	)

	// === TRANSPONDER COMMANDS ===
	registerSTTCommand(
		"squawk [code] {squawk}",
		func(code string) string { return fmt.Sprintf("SQ%s", code) },
		WithName("squawk_code"),
		WithPriority(10),
	)

	registerSTTCommand(
		"ident",
		func() string { return "ID" },
		WithName("ident_only"),
		WithPriority(10),
	)

	registerSTTCommand(
		"squawk standby",
		func() string { return "SQS" },
		WithName("squawk_standby"),
		WithPriority(12),
	)

	registerSTTCommand(
		"squawk altitude|mode",
		func() string { return "SQA" },
		WithName("squawk_altitude"),
		WithPriority(12),
	)

	registerSTTCommand(
		"transponder on",
		func() string { return "SQON" },
		WithName("squawk_on"),
		WithPriority(12),
	)

	registerSTTCommand(
		"squawk normal",
		func() string { return "SQON" },
		WithName("squawk_normal"),
		WithPriority(12),
	)

	registerSTTCommand(
		"squawk vfr|victor",
		func() string { return "" }, // Ignored - VFR squawk is informational
		WithName("squawk_vfr"),
		WithPriority(12),
	)

	// === HANDOFF COMMANDS ===
	registerSTTCommand(
		"radar contact",
		func() string { return "" }, // Informational only
		WithName("radar_contact_info"),
		WithPriority(20),
	)

	// Contact tower patterns - need to handle "contact <facility> tower"
	registerSTTCommand(
		"contact tower",
		func() string { return "TO" },
		WithName("contact_tower"),
		WithPriority(15),
	)
	// "{facility} tower" (when "contact" is garbled but facility name remains)
	// E.g., "Konak tower", "San Francisco tower"
	// Uses {garbled_word} to require a non-command word before "tower",
	// preventing bare "tower" from matching as noise in transcripts.
	registerSTTCommand(
		"{garbled_word} tower",
		func(_ string) string { return "TO" },
		WithName("facility_tower"),
		WithPriority(5),
	)
	// Pattern: "contact Kennedy Tower" or "contact Lindbergh Tower"
	// The {text} parameter consumes one token (the facility name)
	registerSTTCommand(
		"contact {text} tower",
		func(_ string) string { return "TO" },
		WithName("contact_facility_tower"),
		WithPriority(16),
	)

	// "contact approach/departure/center" produces FC
	// Requires a facility type word after "contact" to avoid matching phrases like
	// "contact clerder" where "clerder" is garbled "cleared"
	// The "radar contact" pattern has higher priority and produces ""
	registerSTTCommand(
		"contact approach|departure|center",
		func() string { return "FC" },
		WithName("frequency_change"),
		WithPriority(3), // Low priority - "radar contact" pattern (priority 20) wins when applicable
	)

	// Pattern: "contact <garbled facility> <frequency>"
	// Handles cases where the facility name is garbled but ends with a frequency.
	// E.g., "contact for ersena one two seven point zero" (Fort Worth Center 127.0)
	registerSTTCommand(
		"contact {contact_frequency}",
		func(_ string) string { return "FC" },
		WithName("frequency_change_with_frequency"),
		WithPriority(4), // Just above the basic "contact facility" pattern
	)

	// Fallback: "contact" + garbled word without frequency pattern → assume tower
	// If we clearly heard "contact" but what follows is too short to be a frequency,
	// it's most likely "contact tower" with a garbled "tower".
	// Uses {garbled_word} to avoid matching command keywords like "climb".
	// Lower priority than FC patterns so those match first when applicable.
	registerSTTCommand(
		"contact {garbled_word}",
		func(_ string) string { return "TO" },
		WithName("contact_garbled_tower"),
		WithPriority(2),
	)

	registerSTTCommand(
		"frequency change approved",
		func() string { return "" }, // Ignored - informational only
		WithName("frequency_change_approved"),
		WithPriority(15),
	)

	// === VFR/MISC COMMANDS ===
	registerSTTCommand(
		"go ahead",
		func() string { return "GA" },
		WithName("go_ahead"),
		WithPriority(15),
	)

	registerSTTCommand(
		"say request",
		func() string { return "GA" },
		WithName("say_request"),
		WithPriority(15),
	)

	registerSTTCommand(
		"radar services terminated",
		func() string { return "RST" },
		WithName("radar_services_terminated"),
		WithPriority(15),
	)

	registerSTTCommand(
		"resume own navigation",
		func() string { return "RON" },
		WithName("resume_own_navigation"),
		WithPriority(15),
	)

	registerSTTCommand(
		"altitude discretion|your",
		func() string { return "A" },
		WithName("vfr_altitude_discretion"),
		WithPriority(10),
	)

	// === TRAFFIC ADVISORY ===
	registerSTTCommand(
		"traffic [at] [your] {traffic}",
		func(tr trafficResult) string {
			return fmt.Sprintf("TRAFFIC/%d/%d/%d", tr.oclock, tr.miles, tr.altitude)
		},
		WithName("traffic_advisory"),
		WithPriority(10),
	)

	registerSTTCommand(
		"maintain visual separation [from] [the] [traffic]",
		func() string { return "VISSEP" },
		WithName("visual_separation"),
		WithPriority(15),
	)

	// === ATIS INFORMATION ===
	registerSTTCommand(
		"information {atis_letter} [is] [current]",
		func(letter string) string { return "ATIS/" + letter },
		WithName("atis_information"),
		WithPriority(15),
	)

	registerSTTCommand(
		"advise [you] have information {atis_letter}",
		func(letter string) string { return "ATIS/" + letter },
		WithName("advise_have_information"),
		WithPriority(15),
	)

	// === FIELD IN SIGHT ===
	registerSTTCommand(
		"[do you] [have the|have] field in sight|[do you] [have the|have] airport in sight",
		func() string { return "FS" },
		WithName("field_in_sight"),
		WithPriority(10),
	)
}
