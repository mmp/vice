package stt

import (
	"fmt"

	av "github.com/mmp/vice/aviation"
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
		WithThenVariant("TA%d"),
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

	// "{altitude} until established [on] [the] [localizer|glide|slope|glideslope]"
	registerSTTCommand(
		"{standalone_altitude} until established|establishing [on] [the] [localizer|glide|slope|glideslope]",
		func(alt int) string { return fmt.Sprintf("A%d", alt) },
		WithName("altitude_until_established"),
		WithPriority(12),
	)

	// Absorb "expect further clearance" so it doesn't trigger "expect approach"
	registerSTTCommand(
		"expect further clearance",
		func() string { return "" },
		WithName("expect_further_clearance"),
		WithPriority(25),
	)

	// Expedite through/to altitude (descent) - higher priority than plain expedite.
	// Two patterns: one with explicit "through", one without (handles "to" stripped as filler).
	registerSTTCommand(
		"expedite descent|descend through {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("ED%d", alt) },
		WithName("expedite_descent_through"),
		WithPriority(15),
	)
	registerSTTCommand(
		"expedite descent|descend {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("ED%d", alt) },
		WithName("expedite_descent_to"),
		WithPriority(15),
	)

	// Expedite through/to altitude (climb)
	registerSTTCommand(
		"expedite climb through {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("EC%d", alt) },
		WithName("expedite_climb_through"),
		WithPriority(15),
	)
	registerSTTCommand(
		"expedite climb {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("EC%d", alt) },
		WithName("expedite_climb_to"),
		WithPriority(15),
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

	// Best rate = synonym for expedite (no altitude)
	registerSTTCommand(
		"best rate descent|descend",
		func() string { return "ED" },
		WithName("best_rate_descent"),
		WithPriority(10),
	)

	registerSTTCommand(
		"best rate climb",
		func() string { return "EC" },
		WithName("best_rate_climb"),
		WithPriority(10),
	)

	// Best rate through/to altitude (descent)
	registerSTTCommand(
		"best rate descent|descend through {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("ED%d", alt) },
		WithName("best_rate_descent_through"),
		WithPriority(15),
	)
	registerSTTCommand(
		"best rate descent|descend {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("ED%d", alt) },
		WithName("best_rate_descent_to"),
		WithPriority(15),
	)

	// Best rate through/to altitude (climb)
	registerSTTCommand(
		"best rate climb through {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("EC%d", alt) },
		WithName("best_rate_climb_through"),
		WithPriority(15),
	)
	registerSTTCommand(
		"best rate climb {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("EC%d", alt) },
		WithName("best_rate_climb_to"),
		WithPriority(15),
	)

	// Good rate without altitude (direction explicit)
	registerSTTCommand(
		"good rate [of] descent|descend",
		func() string { return "GRD" },
		WithName("good_rate_descent"),
		WithPriority(10),
	)
	registerSTTCommand(
		"good rate [of] climb",
		func() string { return "GRC" },
		WithName("good_rate_climb"),
		WithPriority(10),
	)

	// Good rate through/to altitude (direction inferred)
	registerSTTCommand(
		"good rate through {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("GR%d", alt) },
		WithName("good_rate_through"),
		WithPriority(15),
	)
	registerSTTCommand(
		"good rate {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("GR%d", alt) },
		WithName("good_rate_to"),
		WithPriority(15),
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

	// Fallback for cut-off transcripts like "fly present hap-" where STT loses
	// the "heading" token. Requires literal "fly" so it can't fire on phrases
	// like "maintain present speed".
	registerSTTCommand(
		"fly present",
		func() string { return "H" },
		WithName("fly_present_bare"),
		WithPriority(11),
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

	// An explicit "level" reads as flight level even without "flight"
	// ("maintain level two eight zero"); outranks the speed reading of
	// the same number.
	registerSTTCommand(
		"maintain level {altitude_fl}",
		func(alt int) string { return fmt.Sprintf("A%d", alt) },
		WithName("maintain_flight_level"),
		WithPriority(4),
	)

	registerSTTCommand(
		"[maintain] slowest|minimum practical|speed|possible [approach] [speed]",
		func() string { return "SMIN" },
		WithName("slowest_practical"),
		WithPriority(12),
	)

	registerSTTCommand(
		"[maintain] maximum|best forward|speed",
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
		"[maintain] {speed} [knots] or less {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d-/U%s", spd, until.suffix)
		},
		WithName("speed_or_less_until"),
		WithPriority(15),
	)

	registerSTTCommand(
		"[maintain] {speed} [knots] or less",
		func(spd int) string { return fmt.Sprintf("S%d-", spd) },
		WithName("speed_or_less"),
		WithPriority(12),
		WithThenVariant("TS%d-"),
	)

	registerSTTCommand(
		"speed [to] {speed} [knots] or less {speed_until}",
		func(spd int, until speedUntilResult) string {
			return fmt.Sprintf("S%d-/U%s", spd, until.suffix)
		},
		WithName("speed_keyword_or_less_until"),
		WithPriority(15),
	)

	registerSTTCommand(
		"speed [to] {speed} [knots] or less",
		func(spd int) string { return fmt.Sprintf("S%d-", spd) },
		WithName("speed_keyword_or_less"),
		WithPriority(12),
		WithThenVariant("TS%d-"),
	)

	registerSTTCommand(
		"reduce|slow [speed] [to] {speed} [knots] or less",
		func(spd int) string { return fmt.Sprintf("S%d-", spd) },
		WithName("reduce_speed_or_less"),
		WithPriority(12),
		WithThenVariant("TS%d-"),
	)

	registerSTTCommand(
		"increase [speed] [to] {speed} [knots] or less",
		func(spd int) string { return fmt.Sprintf("S%d-", spd) },
		WithName("increase_speed_or_less"),
		WithPriority(12),
		WithThenVariant("TS%d-"),
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
		"reduce|slow [to] final|minimum [approach] [speed]",
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

	// "speed X to FIX" — pilot omits "until". Treated as speed-until-fix.
	// "to" is required (not optional) so bare "speed X" or "speed X at FIX" doesn't
	// grab an unrelated trailing fix via fuzzy match. Lower priority than the
	// explicit "until" patterns so those still win when "until" is spoken.
	registerSTTCommand(
		"reduce|slow [speed] [to] {speed} [knots] to {fix}",
		func(spd int, fix string) string {
			return fmt.Sprintf("S%d/U%s", spd, fix)
		},
		WithName("reduce_speed_to_fix"),
		WithPriority(13),
	)
	registerSTTCommand(
		"increase [speed] [to] {speed} [knots] to {fix}",
		func(spd int, fix string) string {
			return fmt.Sprintf("S%d/U%s", spd, fix)
		},
		WithName("increase_speed_to_fix"),
		WithPriority(13),
	)
	registerSTTCommand(
		"speed [to] {speed} [knots] to {fix}",
		func(spd int, fix string) string {
			return fmt.Sprintf("S%d/U%s", spd, fix)
		},
		WithName("speed_to_fix"),
		WithPriority(11),
	)
	registerSTTCommand(
		"maintain [speed] {speed} [knots] to {fix}",
		func(spd int, fix string) string {
			return fmt.Sprintf("S%d/U%s", spd, fix)
		},
		WithName("maintain_speed_to_fix"),
		WithPriority(11),
	)

	// === COMPOUND SPEED COMMANDS ===
	// 2-segment: speed until fix, then speed (open-ended)
	registerSTTCommand(
		"speed [to] {speed} until {fix} [,] [then] [speed [to]] {speed} [knots]",
		func(spd1 int, fix string, spd2 int) string {
			return fmt.Sprintf("S%d/U%s/%d", spd1, fix, spd2)
		},
		WithName("compound_speed_2seg"), WithPriority(16),
	)
	registerSTTCommand(
		"reduce|slow [speed] [to] {speed} until {fix} [,] [then] [speed [to]] {speed} [knots]",
		func(spd1 int, fix string, spd2 int) string {
			return fmt.Sprintf("S%d/U%s/%d", spd1, fix, spd2)
		},
		WithName("compound_reduce_speed_2seg"), WithPriority(16),
	)
	registerSTTCommand(
		"maintain [speed] {speed} until {fix} [,] [then] [speed [to]] {speed} [knots]",
		func(spd1 int, fix string, spd2 int) string {
			return fmt.Sprintf("S%d/U%s/%d", spd1, fix, spd2)
		},
		WithName("compound_maintain_speed_2seg"), WithPriority(16),
	)

	// 2-segment: speed until fix, speed until fix
	registerSTTCommand(
		"speed [to] {speed} until {fix} [,] [then] [speed [to]] {speed} {speed_until}",
		func(spd1 int, fix string, spd2 int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s/%d/U%s", spd1, fix, spd2, until.suffix)
		},
		WithName("compound_speed_2seg_until"), WithPriority(16),
	)
	registerSTTCommand(
		"reduce|slow [speed] [to] {speed} until {fix} [,] [then] [speed [to]] {speed} {speed_until}",
		func(spd1 int, fix string, spd2 int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s/%d/U%s", spd1, fix, spd2, until.suffix)
		},
		WithName("compound_reduce_speed_2seg_until"), WithPriority(16),
	)
	registerSTTCommand(
		"maintain [speed] {speed} until {fix} [,] [then] [speed [to]] {speed} {speed_until}",
		func(spd1 int, fix string, spd2 int, until speedUntilResult) string {
			return fmt.Sprintf("S%d/U%s/%d/U%s", spd1, fix, spd2, until.suffix)
		},
		WithName("compound_maintain_speed_2seg_until"), WithPriority(16),
	)

	// 3-segment: speed until fix, speed until fix, then speed
	registerSTTCommand(
		"speed [to] {speed} until {fix} [,] [speed [to]] {speed} until {fix} [,] [then] [speed [to]] {speed} [knots]",
		func(spd1 int, fix1 string, spd2 int, fix2 string, spd3 int) string {
			return fmt.Sprintf("S%d/U%s/%d/U%s/%d", spd1, fix1, spd2, fix2, spd3)
		},
		WithName("compound_speed_3seg"), WithPriority(18),
	)
	registerSTTCommand(
		"reduce|slow [speed] [to] {speed} until {fix} [,] [speed [to]] {speed} until {fix} [,] [then] [speed [to]] {speed} [knots]",
		func(spd1 int, fix1 string, spd2 int, fix2 string, spd3 int) string {
			return fmt.Sprintf("S%d/U%s/%d/U%s/%d", spd1, fix1, spd2, fix2, spd3)
		},
		WithName("compound_reduce_speed_3seg"), WithPriority(18),
	)
	registerSTTCommand(
		"maintain [speed] {speed} until {fix} [,] [speed [to]] {speed} until {fix} [,] [then] [speed [to]] {speed} [knots]",
		func(spd1 int, fix1 string, spd2 int, fix2 string, spd3 int) string {
			return fmt.Sprintf("S%d/U%s/%d/U%s/%d", spd1, fix1, spd2, fix2, spd3)
		},
		WithName("compound_maintain_speed_3seg"), WithPriority(18),
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
		"[proceed] left [turn] direct [to] [at] {fix}",
		func(fix string) string { return fmt.Sprintf("LD%s", fix) },
		WithName("left_direct_fix"),
		WithPriority(11),
	)
	registerSTTCommand(
		"[proceed] right [turn] direct [to] [at] {fix}",
		func(fix string) string { return fmt.Sprintf("RD%s", fix) },
		WithName("right_direct_fix"),
		WithPriority(11),
	)
	registerSTTCommand(
		"direct|proceed [direct] [to] [at] {fix}",
		func(fix string) string { return fmt.Sprintf("D%s", fix) },
		WithName("direct_fix"),
		WithPriority(10),
	)

	// "cleared direct [fix]" - SAYAGAIN when fix is garbled.
	registerSTTCommand(
		"cleared direct {fix}",
		func(fix string) string { return fmt.Sprintf("D%s", fix) },
		WithName("cleared_direct_fix_explicit"),
		WithPriority(12),
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
		"expect [to] [go] direct [to] {fix}",
		func(fix string) string { return fmt.Sprintf("EXPDIR%s", fix) },
		WithName("expect_direct_fix"),
		WithPriority(14),
	)
	registerSTTCommand(
		"expect [to] rejoin|resume [the] arrival [at] {fix}",
		func(fix string) string { return fmt.Sprintf("EXPDIR%s", fix) },
		WithName("expect_rejoin_arrival"),
		WithPriority(16),
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
		"cross {fix} [at] [or] below {altitude}",
		func(fix string, alt int) string { return fmt.Sprintf("C%s/A%d-", fix, alt) },
		WithName("cross_fix_at_or_below"),
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

	// Combined altitude + speed crossing restrictions: all combinations of
	// (plain | at-or-above) altitude × (plain | or-greater | do-not-exceed)
	// speed, plus mach variants, in both word orders. Outputs match the
	// keyboard /A.../S.../M... grammar accepted by sim/command_parser.go.
	// Priority follows the +2-per-modifier ladder used by single-constraint
	// handlers (15 base) so more-specific patterns win over less-specific
	// ones for inputs containing modifier keywords like "above" / "or
	// greater" / "do not exceed".

	// Altitude-first: plain altitude × speed variants
	registerSTTCommand(
		"cross {fix} [at] {altitude} [and] [at] {speed}",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d/S%d", fix, alt, spd)
		},
		WithName("cross_fix_altitude_speed"),
		WithPriority(15),
	)
	registerSTTCommand(
		"cross {fix} [at] {altitude} [and] [at] {speed} or greater|better",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d/S%d+", fix, alt, spd)
		},
		WithName("cross_fix_altitude_speed_or_greater"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] {altitude} [and] [at] [do] not [to] exceed {speed}",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d/S%d-", fix, alt, spd)
		},
		WithName("cross_fix_altitude_do_not_exceed"),
		WithPriority(17),
	)

	// Altitude-first: at-or-above altitude × speed variants
	registerSTTCommand(
		"cross {fix} [at] [or] above {altitude} [and] [at] {speed}",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d+/S%d", fix, alt, spd)
		},
		WithName("cross_fix_at_or_above_speed"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] [or] above {altitude} [and] [at] {speed} or greater|better",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d+/S%d+", fix, alt, spd)
		},
		WithName("cross_fix_at_or_above_speed_or_greater"),
		WithPriority(19),
	)
	registerSTTCommand(
		"cross {fix} [at] [or] above {altitude} [and] [at] [do] not [to] exceed {speed}",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d+/S%d-", fix, alt, spd)
		},
		WithName("cross_fix_at_or_above_do_not_exceed"),
		WithPriority(19),
	)

	// Altitude-first: at-or-below altitude × speed variants
	registerSTTCommand(
		"cross {fix} [at] [or] below {altitude} [and] [at] {speed}",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d-/S%d", fix, alt, spd)
		},
		WithName("cross_fix_at_or_below_speed"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] [or] below {altitude} [and] [at] {speed} or greater|better",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d-/S%d+", fix, alt, spd)
		},
		WithName("cross_fix_at_or_below_speed_or_greater"),
		WithPriority(19),
	)
	registerSTTCommand(
		"cross {fix} [at] [or] below {altitude} [and] [at] [do] not [to] exceed {speed}",
		func(fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/A%d-/S%d-", fix, alt, spd)
		},
		WithName("cross_fix_at_or_below_do_not_exceed"),
		WithPriority(19),
	)

	// Speed-first: speed variants × plain altitude
	registerSTTCommand(
		"cross {fix} [at] {speed} [and] [at] {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d/S%d", fix, alt, spd)
		},
		WithName("cross_fix_speed_altitude"),
		WithPriority(15),
	)
	registerSTTCommand(
		"cross {fix} [at] {speed} or greater|better [and] [at] {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d/S%d+", fix, alt, spd)
		},
		WithName("cross_fix_speed_or_greater_altitude"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] [do] not [to] exceed {speed} [and] [at] {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d/S%d-", fix, alt, spd)
		},
		WithName("cross_fix_do_not_exceed_altitude"),
		WithPriority(17),
	)

	// Speed-first: speed variants × at-or-above altitude
	registerSTTCommand(
		"cross {fix} [at] {speed} [and] [at] [or] above {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d+/S%d", fix, alt, spd)
		},
		WithName("cross_fix_speed_at_or_above"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] {speed} or greater|better [and] [at] [or] above {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d+/S%d+", fix, alt, spd)
		},
		WithName("cross_fix_speed_or_greater_at_or_above"),
		WithPriority(19),
	)
	registerSTTCommand(
		"cross {fix} [at] [do] not [to] exceed {speed} [and] [at] [or] above {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d+/S%d-", fix, alt, spd)
		},
		WithName("cross_fix_do_not_exceed_at_or_above"),
		WithPriority(19),
	)

	// Speed-first: speed variants × at-or-below altitude
	registerSTTCommand(
		"cross {fix} [at] {speed} [and] [at] [or] below {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d-/S%d", fix, alt, spd)
		},
		WithName("cross_fix_speed_at_or_below"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] {speed} or greater|better [and] [at] [or] below {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d-/S%d+", fix, alt, spd)
		},
		WithName("cross_fix_speed_or_greater_at_or_below"),
		WithPriority(19),
	)
	registerSTTCommand(
		"cross {fix} [at] [do] not [to] exceed {speed} [and] [at] [or] below {altitude}",
		func(fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/A%d-/S%d-", fix, alt, spd)
		},
		WithName("cross_fix_do_not_exceed_at_or_below"),
		WithPriority(19),
	)

	// Mach combinations (no spd-modifier — mach IS the speed type)
	registerSTTCommand(
		"cross {fix} [at] {altitude} [and] [at] mach [point] {mach}",
		func(fix string, alt int, mach int) string {
			return fmt.Sprintf("C%s/A%d/M%d", fix, alt, mach)
		},
		WithName("cross_fix_altitude_mach"),
		WithPriority(15),
	)
	registerSTTCommand(
		"cross {fix} [at] [or] above {altitude} [and] [at] mach [point] {mach}",
		func(fix string, alt int, mach int) string {
			return fmt.Sprintf("C%s/A%d+/M%d", fix, alt, mach)
		},
		WithName("cross_fix_at_or_above_mach"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] [or] below {altitude} [and] [at] mach [point] {mach}",
		func(fix string, alt int, mach int) string {
			return fmt.Sprintf("C%s/A%d-/M%d", fix, alt, mach)
		},
		WithName("cross_fix_at_or_below_mach"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] mach [point] {mach} [and] [at] {altitude}",
		func(fix string, mach int, alt int) string {
			return fmt.Sprintf("C%s/A%d/M%d", fix, alt, mach)
		},
		WithName("cross_fix_mach_altitude"),
		WithPriority(15),
	)
	registerSTTCommand(
		"cross {fix} [at] mach [point] {mach} [and] [at] [or] above {altitude}",
		func(fix string, mach int, alt int) string {
			return fmt.Sprintf("C%s/A%d+/M%d", fix, alt, mach)
		},
		WithName("cross_fix_mach_at_or_above"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cross {fix} [at] mach [point] {mach} [and] [at] [or] below {altitude}",
		func(fix string, mach int, alt int) string {
			return fmt.Sprintf("C%s/A%d-/M%d", fix, alt, mach)
		},
		WithName("cross_fix_mach_at_or_below"),
		WithPriority(17),
	)

	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] [and] [maintain] {altitude_fl}",
		func(dist int, dir string, fix string, alt int) string {
			return fmt.Sprintf("C%s/%d%s/A%d", fix, dist, dir, alt)
		},
		WithName("cross_distance_direction_fix_altitude"),
		WithPriority(14),
	)

	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] {speed}",
		func(dist int, dir string, fix string, spd int) string {
			return fmt.Sprintf("C%s/%d%s/S%d", fix, dist, dir, spd)
		},
		WithName("cross_distance_direction_fix_speed"),
		WithPriority(14),
	)

	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] {speed} or greater|better",
		func(dist int, dir string, fix string, spd int) string {
			return fmt.Sprintf("C%s/%d%s/S%d+", fix, dist, dir, spd)
		},
		WithName("cross_distance_direction_fix_speed_or_greater"),
		WithPriority(16),
	)

	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] [or] [and|at] [do] not [to] exceed {speed}",
		func(dist int, dir string, fix string, spd int) string {
			return fmt.Sprintf("C%s/%d%s/S%d-", fix, dist, dir, spd)
		},
		WithName("cross_distance_direction_fix_do_not_exceed"),
		WithPriority(16),
	)

	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] mach [point] {mach}",
		func(dist int, dir string, fix string, mach int) string {
			return fmt.Sprintf("C%s/%d%s/M%d", fix, dist, dir, mach)
		},
		WithName("cross_distance_direction_fix_mach"),
		WithPriority(14),
	)

	// Combined dist/dir altitude + speed crossing restrictions. Mirrors the
	// single-constraint dist/dir handlers (no at-or-above-altitude variant
	// exists for dist/dir, so combined dist/dir uses plain altitude only).
	// Priority +2 per speed modifier above the 18 base.

	// Altitude-first
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] [and] [maintain] {altitude_fl} [and] [at] {speed}",
		func(dist int, dir string, fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/S%d", fix, dist, dir, alt, spd)
		},
		WithName("cross_distance_direction_fix_altitude_speed"),
		WithPriority(18),
	)
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] [and] [maintain] {altitude_fl} [and] [at] {speed} or greater|better",
		func(dist int, dir string, fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/S%d+", fix, dist, dir, alt, spd)
		},
		WithName("cross_distance_direction_fix_altitude_speed_or_greater"),
		WithPriority(20),
	)
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] [and] [maintain] {altitude_fl} [and] [at] [do] not [to] exceed {speed}",
		func(dist int, dir string, fix string, alt int, spd int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/S%d-", fix, dist, dir, alt, spd)
		},
		WithName("cross_distance_direction_fix_altitude_do_not_exceed"),
		WithPriority(20),
	)
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] [and] [maintain] {altitude_fl} [and] [at] mach [point] {mach}",
		func(dist int, dir string, fix string, alt int, mach int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/M%d", fix, dist, dir, alt, mach)
		},
		WithName("cross_distance_direction_fix_altitude_mach"),
		WithPriority(18),
	)

	// Speed-first
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] {speed} [and] [at] {altitude_fl}",
		func(dist int, dir string, fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/S%d", fix, dist, dir, alt, spd)
		},
		WithName("cross_distance_direction_fix_speed_altitude"),
		WithPriority(18),
	)
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] {speed} or greater|better [and] [at] {altitude_fl}",
		func(dist int, dir string, fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/S%d+", fix, dist, dir, alt, spd)
		},
		WithName("cross_distance_direction_fix_speed_or_greater_altitude"),
		WithPriority(20),
	)
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] [do] not [to] exceed {speed} [and] [at] {altitude_fl}",
		func(dist int, dir string, fix string, spd int, alt int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/S%d-", fix, dist, dir, alt, spd)
		},
		WithName("cross_distance_direction_fix_do_not_exceed_altitude"),
		WithPriority(20),
	)
	registerSTTCommand(
		"cross {num:1-99} miles|mile {compass_dir} [of] {fix} [at] mach [point] {mach} [and] [at] {altitude_fl}",
		func(dist int, dir string, fix string, mach int, alt int) string {
			return fmt.Sprintf("C%s/%d%s/A%d/M%d", fix, dist, dir, alt, mach)
		},
		WithName("cross_distance_direction_fix_mach_altitude"),
		WithPriority(18),
	)

	// Cross N DME commands. These apply to a cleared visual approach; the
	// runway threshold is the DME reference. The compound variants fire the
	// visual approach clearance first so the approach is set up before the
	// crossing restriction is applied.
	registerSTTCommand(
		"cross {dme} [at] [and] [maintain] {altitude}",
		func(dist int, alt int) string { return fmt.Sprintf("CDME%d/A%d", dist, alt) },
		WithName("cross_dme_altitude"),
		WithPriority(14),
	)
	registerSTTCommand(
		"cross {dme} [at] [or] above {altitude}",
		func(dist int, alt int) string { return fmt.Sprintf("CDME%d/A%d+", dist, alt) },
		WithName("cross_dme_at_or_above"),
		WithPriority(16),
	)
	registerSTTCommand(
		"cross {dme} [at] [or] below {altitude}",
		func(dist int, alt int) string { return fmt.Sprintf("CDME%d/A%d-", dist, alt) },
		WithName("cross_dme_at_or_below"),
		WithPriority(16),
	)

	// Compound: "cross N DME ..., cleared visual approach RWY" — emit CVA first so
	// the visual approach is established before the crossing restriction.
	registerSTTCommand(
		"cross {dme} [at] [and] [maintain] {altitude} [,] cleared [the] {visual_approach_lahso}",
		func(dist int, alt int, appr string) string {
			return fmt.Sprintf("CVA%s CDME%d/A%d", appr, dist, alt)
		},
		WithName("cross_dme_altitude_cleared_visual"),
		WithPriority(18),
		WithSayAgainOnFail(),
	)
	registerSTTCommand(
		"cross {dme} [at] [or] above {altitude} [,] cleared [the] {visual_approach_lahso}",
		func(dist int, alt int, appr string) string {
			return fmt.Sprintf("CVA%s CDME%d/A%d+", appr, dist, alt)
		},
		WithName("cross_dme_at_or_above_cleared_visual"),
		WithPriority(19),
		WithSayAgainOnFail(),
	)
	registerSTTCommand(
		"cross {dme} [at] [or] below {altitude} [,] cleared [the] {visual_approach_lahso}",
		func(dist int, alt int, appr string) string {
			return fmt.Sprintf("CVA%s CDME%d/A%d-", appr, dist, alt)
		},
		WithName("cross_dme_at_or_below_cleared_visual"),
		WithPriority(19),
		WithSayAgainOnFail(),
	)

	// Compound: "cleared visual approach RWY, cross N DME ..." — same result.
	registerSTTCommand(
		"cleared [the] {visual_approach_lahso} [,] cross {dme} [at] [and] [maintain] {altitude}",
		func(appr string, dist int, alt int) string {
			return fmt.Sprintf("CVA%s CDME%d/A%d", appr, dist, alt)
		},
		WithName("cleared_visual_cross_dme_altitude"),
		WithPriority(18),
		WithSayAgainOnFail(),
	)
	registerSTTCommand(
		"cleared [the] {visual_approach_lahso} [,] cross {dme} [at] [or] above {altitude}",
		func(appr string, dist int, alt int) string {
			return fmt.Sprintf("CVA%s CDME%d/A%d+", appr, dist, alt)
		},
		WithName("cleared_visual_cross_dme_at_or_above"),
		WithPriority(19),
		WithSayAgainOnFail(),
	)
	registerSTTCommand(
		"cleared [the] {visual_approach_lahso} [,] cross {dme} [at] [or] below {altitude}",
		func(appr string, dist int, alt int) string {
			return fmt.Sprintf("CVA%s CDME%d/A%d-", appr, dist, alt)
		},
		WithName("cleared_visual_cross_dme_at_or_below"),
		WithPriority(19),
		WithSayAgainOnFail(),
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

	// === AFTER FIX SPEED COMMANDS ===
	registerSTTCommand(
		"after|at {fix} [,] maintain [speed] {speed} [knots]",
		func(fix string, spd int) string { return fmt.Sprintf("A%s/S%d", fix, spd) },
		WithName("after_fix_maintain_speed"),
		WithPriority(15),
	)
	registerSTTCommand(
		"after|at {fix} [,] [maintain] [speed] {speed} [knots] or greater|better",
		func(fix string, spd int) string { return fmt.Sprintf("A%s/S%d+", fix, spd) },
		WithName("after_fix_speed_or_greater"),
		WithPriority(17),
	)
	registerSTTCommand(
		"after|at {fix} [,] [maintain] [speed] {speed} [knots] or less",
		func(fix string, spd int) string { return fmt.Sprintf("A%s/S%d-", fix, spd) },
		WithName("after_fix_speed_or_less"),
		WithPriority(17),
	)
	registerSTTCommand(
		"after|at {fix} [,] reduce|slow|increase [speed] [to] {speed} [knots]",
		func(fix string, spd int) string { return fmt.Sprintf("A%s/S%d", fix, spd) },
		WithName("after_fix_reduce_speed"),
		WithPriority(15),
	)
	registerSTTCommand(
		"after|at {fix} [,] [speed] {speed} [knots]",
		func(fix string, spd int) string { return fmt.Sprintf("A%s/S%d", fix, spd) },
		WithName("after_fix_speed_bare"),
		WithPriority(14),
	)

	// === AFTER FIX ALTITUDE COMMANDS ===
	registerSTTCommand(
		"after|at {fix} [,] climb [and] maintain {altitude_fl}",
		func(fix string, alt int) string { return fmt.Sprintf("A%s/C%d", fix, alt) },
		WithName("after_fix_climb_maintain"),
		WithPriority(15),
	)
	registerSTTCommand(
		"after|at {fix} [,] descend [and] maintain {altitude_fl}",
		func(fix string, alt int) string { return fmt.Sprintf("A%s/D%d", fix, alt) },
		WithName("after_fix_descend_maintain"),
		WithPriority(15),
	)

	// === APPROACH COMMANDS ===
	registerSTTCommand(
		"at {fix} [cleared] [clear] straight [in] [for] [approach] {approach}",
		func(fix, appr string) string { return fmt.Sprintf("A%s/CSI%s", fix, appr) },
		WithName("at_fix_cleared_straight_in_approach"),
		WithPriority(16),
		WithSayAgainOnFail(),
		WithSayAgainMinTokens(3),
	)

	registerSTTCommand(
		"at {fix} [cleared] [clear] [for] [approach] {approach}",
		func(fix, appr string) string { return fmt.Sprintf("A%s/C%s", fix, appr) },
		WithName("at_fix_cleared_approach"),
		WithPriority(15),
		WithSayAgainOnFail(),
		WithSayAgainMinTokens(3),
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
	// Non-ILS: "at FIX intercept the approach course"
	registerSTTCommand(
		"at {fix} intercept|join [the] [final] approach [course]",
		func(fix string) string { return fmt.Sprintf("A%s/I", fix) },
		WithName("at_fix_intercept_approach"),
		WithPriority(15),
	)

	// "[proceed] direct FIX intercept the localizer" — preserves the explicit
	// "direct" instruction and adds an at-fix intercept. Emits two commands:
	// D{fix} routes the aircraft (also handles approach-only fixes via
	// directFixWaypoints), A{fix}/I sets up the intercept-at-fix trigger.
	registerSTTCommand(
		"direct|proceed [direct] [to] [at] {fix} intercept [the] localizer",
		func(fix string) string { return fmt.Sprintf("D%s A%s/I", fix, fix) },
		WithName("direct_fix_intercept_localizer"),
		WithPriority(15),
	)
	registerSTTCommand(
		"direct|proceed [direct] [to] [at] {fix} intercept [the] [runway] {num:1-36} [left|right|center] localizer",
		func(fix string, _ int) string { return fmt.Sprintf("D%s A%s/I", fix, fix) },
		WithName("direct_fix_intercept_localizer_runway"),
		WithPriority(16),
	)
	registerSTTCommand(
		"direct|proceed [direct] [to] [at] {fix} intercept|join [the] [final] approach [course]",
		func(fix string) string { return fmt.Sprintf("D%s A%s/I", fix, fix) },
		WithName("direct_fix_intercept_approach"),
		WithPriority(15),
	)

	registerSTTCommand(
		"expect [vectors] [for] [to] [the] {approach_lahso}",
		func(appr string) string { return fmt.Sprintf("E%s", appr) },
		WithName("expect_approach"),
		WithPriority(15),     // Higher than heading commands to match approach context first
		WithSayAgainOnFail(), // "expect [approach]" should ask for clarification if approach unrecognized
	)

	// "standby for the approach" is informational — swallow it silently.
	registerSTTCommand(
		"standby [for] [the] approach",
		func() string { return "" },
		WithName("standby_approach"),
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

	// Visual runway commands are higher priority than generic approach matches
	// so "visual approach runway 22L" doesn't resolve to a charted visual.
	registerSTTCommand(
		"expect [the|a|vectors] [for] [to] {visual_approach_lahso}",
		func(appr string) string { return fmt.Sprintf("EVA%s", appr) },
		WithName("expect_visual"),
		WithPriority(17),
		WithSayAgainOnFail(),
	)
	registerSTTCommand(
		"vector|vectors [for] [to] [the] {visual_approach_lahso}",
		func(appr string) string { return fmt.Sprintf("EVA%s", appr) },
		WithName("vectors_visual"),
		WithPriority(17),
	)
	registerSTTCommand(
		"cleared [the] {visual_approach_lahso}",
		func(appr string) string { return fmt.Sprintf("CVA%s", appr) },
		WithName("cleared_visual"),
		WithPriority(17),
		WithSayAgainOnFail(),
	)

	registerSTTCommand(
		"cleared [approach] [for] {approach}",
		func(appr string) string { return fmt.Sprintf("C%s", appr) },
		WithName("cleared_approach"),
		WithPriority(13),
		WithSayAgainOnFail(),     // Garbled approach clearances should ask for clarification
		WithSayAgainMinTokens(2), // Require approach type keyword, not just "cleared"
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
	// Pattern: "intercept the final approach course" / "intercept the approach" -
	// ATC equivalent of "intercept the localizer". Used for both ILS and RNAV.
	registerSTTCommand(
		"intercept|join|set [the] [final] approach [course]",
		func() string { return "I" },
		WithName("intercept_approach_course"),
		WithPriority(11),
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
		WithKind(kindInformational),
		WithPriority(20),
	)

	// Contact tower patterns - need to handle "contact <facility> tower"
	registerSTTCommand(
		"contact tower [{frequency_value}]",
		func(freq *av.Frequency) string {
			if freq == nil {
				return "TO"
			}
			return fmt.Sprintf("TO/%d", int(*freq))
		},
		WithName("contact_tower"),
		WithPriority(15),
	)
	// "{facility} tower" (when "contact" is garbled but facility name remains)
	// E.g., "Konak tower", "San Francisco tower"
	// Uses {garbled_word} to require a non-command word before "tower",
	// preventing bare "tower" from matching as noise in transcripts.
	registerSTTCommand(
		"{garbled_word} tower [{frequency_value}]",
		func(_ string, freq *av.Frequency) string {
			if freq == nil {
				return "TO"
			}
			return fmt.Sprintf("TO/%d", int(*freq))
		},
		WithName("facility_tower"),
		WithPriority(5),
	)
	// Pattern: "contact Kennedy Tower" or "contact Lindbergh Tower"
	// The {text} parameter consumes one token (the facility name)
	registerSTTCommand(
		"contact {text} tower [{frequency_value}]",
		func(_ string, freq *av.Frequency) string {
			if freq == nil {
				return "TO"
			}
			return fmt.Sprintf("TO/%d", int(*freq))
		},
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
		"approved",
		func() string { return "APPROVED" },
		WithName("approved"),
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
		formatTrafficCommand,
		WithName("traffic_advisory"),
		WithPriority(10),
	)

	registerSTTCommand(
		"traffic landing [the] parallel [at] [your] {traffic}",
		formatTrafficCommand,
		WithName("traffic_advisory"),
		WithPriority(10),
	)

	// Descriptor-position visual-sep advisory: controller calls traffic by
	// relative direction ("off your left", "from the north") rather than
	// o'clock, and reports that the other aircraft has us in sight and will
	// maintain visual separation. The pilot has nothing to do — emit no
	// command so the framework treats it as informational chatter.
	registerSTTCommand(
		"traffic {traffic_visual_sep}",
		func(_ bool) string { return "" },
		WithName("traffic_descriptor_visual_sep"),
		WithPriority(11),
	)

	// "traffic <position> no factor" — informational chatter; the controller is
	// announcing traffic that is not a separation factor, no command intended.
	registerSTTCommand(
		"traffic [at|to] [your] {num:1-12} [o'clock] no factor",
		func(_ int) string { return "" },
		WithName("traffic_no_factor"),
		WithPriority(11),
	)
	registerSTTCommand(
		"traffic no factor",
		func() string { return "" },
		WithName("traffic_no_factor_bare"),
		WithPriority(11),
	)

	registerSTTCommand(
		"maintain visual separation [from] [the] [traffic]",
		func() string { return "VISSEP" },
		WithName("visual_separation"),
		WithPriority(15),
	)

	registerSTTCommand(
		"caution wake turbulence",
		func() string { return "CWT" },
		WithName("caution_wake_turbulence"),
		WithPriority(15),
	)

	// The traffic description that follows a wake-turbulence advisory
	// ("you will follow a heavy Boeing triple seven"). Consuming it as
	// informational keeps the type digits from being misread as a heading
	// or altitude command. "heavy"/"heavier" between "follow" and the type
	// is absorbed by filler skipping and slot slack; listing them as
	// optionals would let "heading" fuzzy-match "heavier" and hijack "fly
	// heading 330" (A330 is a type number).
	registerSTTCommand(
		"follow [a|the] {aircraft_type}",
		func(_ string) string { return "" },
		WithName("follow_traffic_type"),
		WithKind(kindInformational),
		WithPriority(10),
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

	registerSTTCommand(
		"{atis_letter} [is] [now] current",
		func(letter string) string { return "ATIS/" + letter },
		WithName("atis_letter_current"),
		WithPriority(15),
	)

	// === AIRPORT ADVISORY ===
	registerSTTCommand(
		"[the] airport|field [is|its] [gonna] [will] [be] [at] [your] {num:1-12} o'clock {num:1-50} [miles|mile] [report] [the] [field|airport] [in] [sight]",
		func(oclock int, miles int) string {
			return fmt.Sprintf("AP/%d/%d", oclock, miles)
		},
		WithName("airport_advisory"),
		WithPriority(10),
	)
	registerSTTCommand(
		"report [the] [field|airport] in sight [at] [your] {num:1-12} o'clock {num:1-50} [miles|mile]",
		func(oclock int, miles int) string {
			return fmt.Sprintf("AP/%d/%d", oclock, miles)
		},
		WithName("airport_advisory_report"),
		WithPriority(10),
	)
	// Variant that accepts any leading word(s) before o'clock (e.g., "kennedy is at
	// your 11 o'clock 8 miles"). The {fix} type absorbs the airport name token.
	registerSTTCommand(
		"{fix} [is] [at] [your] {num:1-12} o'clock {num:1-50} [miles|mile] [report] [the] [field|airport] [in] [sight]",
		func(_ string, oclock int, miles int) string {
			return fmt.Sprintf("AP/%d/%d", oclock, miles)
		},
		WithName("airport_advisory_named"),
		WithPriority(9), // Lower priority so explicit "airport"/"field" patterns win
	)
	// "do you have the field/airport [in sight]" — bare inquiry with no o'clock
	// or distance. The "in sight" suffix is optional so this also covers
	// "do you have the field". The probability of sighting depends on distance
	// vs. the weather-influenced effective visual range.
	registerSTTCommand(
		"[do] have [the] field|airport [in sight]",
		func() string { return "AP" },
		WithName("airport_in_sight_inquiry"),
		WithPriority(10),
	)
	registerSTTCommand(
		"report [the] field|airport in sight",
		func() string { return "AP" },
		WithName("airport_in_sight_report"),
		WithPriority(10),
	)

	// === TRAFFIC IN-SIGHT INQUIRY ===
	// Bare traffic inquiries with no o'clock/distance/altitude — the controller
	// is asking whether the pilot has previously-called or obvious nearby
	// traffic in sight. The TRAFFIC command re-evaluates any queued
	// FutureTrafficCheck and, failing that, looks for a single nearby aircraft
	// in front of the asker.
	registerSTTCommand(
		"[do] have [the] traffic [in sight]",
		func() string { return "TRAFFIC" },
		WithName("traffic_in_sight_inquiry"),
		WithPriority(10),
	)
	registerSTTCommand(
		"report [the] traffic in sight",
		func() string { return "TRAFFIC" },
		WithName("traffic_in_sight_report"),
		WithPriority(10),
	)

	// Compound: "report [the] traffic and [the] field|airport in sight" —
	// emits both commands. Higher priority than the individual patterns so it
	// wins when both clauses are present (otherwise the field/airport pattern
	// would slack-skip past "traffic" and emit only AP).
	registerSTTCommand(
		"report [the] traffic [and] [the] field|airport in sight",
		func() string { return "TRAFFIC AP" },
		WithName("traffic_and_airport_in_sight"),
		WithPriority(15),
	)

	// === INFORMATIONAL / DISCOURSE SEGMENTS ===
	// These consume tokens without issuing commands. Their kind lets the
	// output assembly recognize acknowledgment-only, position-ID-only, and
	// handoff transmissions from what was matched, rather than from
	// positional token stripping.

	registerSTTCommand(
		"roger|wilco|copy|affirm|affirmative",
		func() string { return "" },
		WithName("acknowledgment"),
		WithKind(kindAcknowledgment),
	)

	registerSTTCommand(
		"hello|hey|hi|howdy",
		func() string { return "" },
		WithName("greeting"),
		WithKind(kindAcknowledgment),
	)

	// Controller position identification ("New York departure", "Boston
	// approach"): up to two facility-name words before the position word.
	// Absorbing the facility words matters: it makes this a real match at
	// the head of the phrase, so a command template cannot poach garbled
	// facility words ("bah SET approach") out of the middle of it. The
	// beam only accepts this near the start of the transmission.
	registerSTTCommand(
		"[{facility_word}] [{facility_word}] departure|approach|center",
		func(_, _ *string) string { return "" },
		WithName("position_id"),
		WithKind(kindPositionID),
		// Weak evidence: any real command template at the same anchor wins.
		WithPriority(1),
	)

	// Sign-off pleasantries; "one" in "have a good one" tokenizes as the
	// digit 1.
	registerSTTCommand(
		"have [a] good|nice|great day|night|evening|one|1",
		func() string { return "" },
		WithName("sign_off_have"),
		WithKind(kindSignOff),
	)

	registerSTTCommand(
		"good day|night|evening|morning",
		func() string { return "" },
		WithName("sign_off_good"),
		WithKind(kindSignOff),
	)

}

func formatTrafficCommand(tr trafficResult) string {
	alt := fmt.Sprintf("%d", tr.altitude)
	if tr.altitudeUnknown {
		alt = "UNK"
	}
	if tr.otherTrafficMaintainsVisual {
		return fmt.Sprintf("TRAFFIC/%d/%d/%s/VISSEP", tr.oclock, tr.miles, alt)
	}
	return fmt.Sprintf("TRAFFIC/%d/%d/%s", tr.oclock, tr.miles, alt)
}
