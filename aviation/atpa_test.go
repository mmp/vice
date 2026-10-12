// aviation/atpa_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"strings"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// atpaTestAirport sets up runway 35, running true north from thr35, with 17
// as its opposite end, and returns an airport with the given approaches.
func atpaTestAirport(t *testing.T, approaches map[string]*Approach) (*Airport, math.Point2LL, float32) {
	thr35 := math.Point2LL{-111.96, 40.77}
	nmPerLongitude := math.NMPerLongitudeAt(thr35)
	thr17 := math.Offset2LL(thr35, 0, 1.5, nmPerLongitude)

	oldDB := testDB
	testDB = testDatabase{Airports: map[ICAOAirportCode]testAirport{
		"KTST": {Id: "KTST", Runways: []Runway{{Id: "35", Heading: 345, Threshold: thr35}, {Id: "17", Heading: 165, Threshold: thr17}}},
	}}
	t.Cleanup(func() { testDB = oldDB })

	return &Airport{Approaches: approaches}, thr35, nmPerLongitude
}

// atpaTestFinal is testFinal's approach with the threshold waypoint that
// InitializeWaypoints adds, and a turn onto the runway if turn is set.
func atpaTestFinal(typ ApproachType, thr35 math.Point2LL, course math.TrueHeading, turn bool) *Approach {
	nmPerLongitude := math.NMPerLongitudeAt(thr35)
	thr17 := math.Offset2LL(thr35, 0, 1.5, nmPerLongitude)
	ap := testFinal(typ, thr35, thr17, course)
	if turn {
		ap.Waypoints[0] = append(ap.Waypoints[0], Waypoint{Fix: "_35_LOC_TURN", Location: math.Offset2LL(thr35, 0, -1, nmPerLongitude)})
	}
	ap.Waypoints[0] = append(ap.Waypoints[0], Waypoint{Fix: "_35_THRESHOLD", Location: thr35})
	return ap
}

func TestATPAVolumesShareStraightIn(t *testing.T) {
	ap, thr35, nmPerLongitude := atpaTestAirport(t, nil)
	ap.Approaches = map[string]*Approach{
		"I35": atpaTestFinal(ILSApproach, thr35, 0, false),
		"R35": atpaTestFinal(RNAVApproach, thr35, 0, false),
		"V35": atpaTestFinal(VisualApproach, thr35, 0, false),
	}
	var e util.ErrorLogger
	ap.finalizeATPAVolumes("KTST", testLocator{}, nmPerLongitude, -11, &e)
	if e.HaveErrors() {
		t.Fatal(e.String())
	}

	if len(ap.ATPAVolumes) != 1 {
		t.Fatalf("expected one volume, got %v", util.SortedMapKeys(ap.ATPAVolumes))
	}
	vol, ok := ap.ATPAVolumes["35"]
	if !ok {
		t.Fatalf("expected the volume to be named after the runway, got %v", util.SortedMapKeys(ap.ATPAVolumes))
	}
	for name, appr := range ap.Approaches {
		if appr.ATPAVolumeId != "35" {
			t.Errorf("%s: in volume %q, expected 35", name, appr.ATPAVolumeId)
		}
	}
	if vol.Runway != "35" || vol.Threshold != thr35 {
		t.Errorf("volume for runway %q at %v, expected 35 at %v", vol.Runway, vol.Threshold, thr35)
	}
	if r, ok := ap.ATPARunways["35"]; !ok || r.ReducedSeparation || r.Length != defaultATPALength {
		t.Errorf("expected a default ATPA entry for runway 35, got %+v", r)
	}
	if _, ok := ap.ATPARunways["17"]; ok {
		t.Errorf("runway 17 has no volumes but has an ATPA entry")
	}
	if math.Abs(vol.Path.Length-defaultATPALength) > 0.01 {
		t.Errorf("path length %.2f, expected %d", vol.Path.Length, defaultATPALength)
	}
	if len(vol.Outline) != 4 {
		t.Errorf("straight volume has %d outline points, expected 4", len(vol.Outline))
	}
	if far, _ := vol.Path.PointAtDistance(0); math.NMDistance2LL(math.NM2LL(far, nmPerLongitude), thr35) < 14.9 {
		t.Errorf("path starts %.1f nm from the threshold", math.NMDistance2LL(math.NM2LL(far, nmPerLongitude), thr35))
	}
}

// An offset LDA and a straight-in RNAV to the same runway get their own
// volumes, the LDA's following the localizer and turning onto the runway.
func TestATPAVolumesOffsetLocalizer(t *testing.T) {
	ap, thr35, nmPerLongitude := atpaTestAirport(t, nil)
	ap.Approaches = map[string]*Approach{
		"X35": atpaTestFinal(LDAApproach, thr35, 340, true),
		"R35": atpaTestFinal(RNAVApproach, thr35, 0, false),
	}
	var e util.ErrorLogger
	ap.finalizeATPAVolumes("KTST", testLocator{}, nmPerLongitude, -11, &e)
	if e.HaveErrors() {
		t.Fatal(e.String())
	}

	if ids := util.SortedMapKeys(ap.ATPAVolumes); strings.Join(ids, " ") != "35 X35" {
		t.Fatalf("expected volumes 35 and X35, got %v", ids)
	}
	if ap.Approaches["X35"].ATPAVolumeId != "X35" || ap.Approaches["R35"].ATPAVolumeId != "35" {
		t.Errorf("LDA in %q, RNAV in %q", ap.Approaches["X35"].ATPAVolumeId, ap.Approaches["R35"].ATPAVolumeId)
	}

	// The LDA volume follows the localizer from the turn point, not the
	// centerline, and its length counts the mile from the turn to the
	// threshold.
	lda, rnav := ap.ATPAVolumes["X35"], ap.ATPAVolumes["35"]
	turn := math.Offset2LL(thr35, 0, -1, nmPerLongitude)
	onLocalizer := math.Offset2LL(turn, 160, 5, nmPerLongitude)
	onCenterline := math.Offset2LL(thr35, 180, 6, nmPerLongitude)
	if !lda.Inside(onLocalizer, 2000, 340, nmPerLongitude) || rnav.Inside(onLocalizer, 2000, 340, nmPerLongitude) {
		t.Errorf("a point on the localizer should be in the LDA's volume only")
	}
	if lda.Inside(onCenterline, 2000, 0, nmPerLongitude) || !rnav.Inside(onCenterline, 2000, 0, nmPerLongitude) {
		t.Errorf("a point on the centerline should be in the RNAV's volume only")
	}
	if !lda.Inside(math.Offset2LL(thr35, 180, 0.5, nmPerLongitude), 500, 0, nmPerLongitude) {
		t.Errorf("short final after the turn should be in the LDA's volume")
	}
	if math.Abs(lda.Path.Length-defaultATPALength) > 0.01 {
		t.Errorf("LDA path length %.2f, expected %d", lda.Path.Length, defaultATPALength)
	}
	if far, _ := lda.Path.PointAtDistance(0); math.NMDistance2LL(math.NM2LL(far, nmPerLongitude), turn) < 13.9 {
		t.Errorf("LDA path starts %.1f nm from the turn, expected 14", math.NMDistance2LL(math.NM2LL(far, nmPerLongitude), turn))
	}
}

// A localizer only slightly off the runway shares the runway's name, and an
// RNAV on the same course shares its volume.
func TestATPAVolumesNearlyAlignedLocalizer(t *testing.T) {
	ap, thr35, nmPerLongitude := atpaTestAirport(t, nil)
	ap.Approaches = map[string]*Approach{
		"I35": atpaTestFinal(ILSApproach, thr35, 2, false),
		"R35": atpaTestFinal(RNAVApproach, thr35, 2, false),
	}
	var e util.ErrorLogger
	ap.finalizeATPAVolumes("KTST", testLocator{}, nmPerLongitude, -11, &e)
	if ids := util.SortedMapKeys(ap.ATPAVolumes); strings.Join(ids, " ") != "35" {
		t.Errorf("expected one volume named 35, got %v", ids)
	}
}

// An RNAV final that turns onto the runway gets a volume along its route,
// extended back along the course into the FAF.
func TestATPAVolumesCurvedFinal(t *testing.T) {
	ap, thr35, nmPerLongitude := atpaTestAirport(t, nil)
	turn := math.Offset2LL(thr35, 180, 1, nmPerLongitude) // 1nm final
	faf := Waypoint{Fix: "FAF", Location: math.Offset2LL(turn, 315, 4, nmPerLongitude)}
	faf.SetFAF(true)
	iaf := Waypoint{Fix: "IAF", Location: math.Offset2LL(faf.Location, 45, 5, nmPerLongitude)}
	iaf.SetIAF(true)
	ap.Approaches = map[string]*Approach{
		"R35": {Type: RNAVApproach, Runway: "35", Threshold: thr35, OppositeThreshold: math.Offset2LL(thr35, 0, 1.5, nmPerLongitude),
			Waypoints: []WaypointArray{{
				iaf,
				{Fix: "IF", Location: math.Offset2LL(faf.Location, 315, 5, nmPerLongitude)},
				faf,
				{Fix: "TURN", Location: turn},
				{Fix: "_35_THRESHOLD", Location: thr35},
			}}},
	}
	var e util.ErrorLogger
	ap.finalizeATPAVolumes("KTST", testLocator{}, nmPerLongitude, -11, &e)
	if e.HaveErrors() {
		t.Fatal(e.String())
	}
	vol, ok := ap.ATPAVolumes["R35"]
	if !ok {
		t.Fatalf("expected a volume named after the approach, got %v", util.SortedMapKeys(ap.ATPAVolumes))
	}
	if math.Abs(vol.Path.Length-defaultATPALength) > 0.01 {
		t.Errorf("path length %.2f, expected %d", vol.Path.Length, defaultATPALength)
	}

	// The path runs from the IF to the threshold, 10nm, and then 5nm more
	// along the course into the FAF. The IAF's leg is off that course.
	beyondIF := math.Offset2LL(faf.Location, 315, 8, nmPerLongitude)
	towardIAF := math.Offset2LL(faf.Location, 45, 3, nmPerLongitude)
	onFinal := math.Offset2LL(turn, 315, 2, nmPerLongitude)
	centerline := math.Offset2LL(thr35, 180, 3, nmPerLongitude)
	for _, tc := range []struct {
		name   string
		p      math.Point2LL
		hdg    math.TrueHeading
		inside bool
	}{
		{"on the final", onFinal, 135, true},
		{"on the short final", math.Offset2LL(thr35, 180, 0.5, nmPerLongitude), 0, true},
		{"beyond the IF on the course", beyondIF, 135, true},
		{"toward the IAF", towardIAF, 225, false},
		{"on the extended centerline", centerline, 0, false},
		{"on the final, flying the wrong way", onFinal, 315, false},
	} {
		if got := vol.Inside(tc.p, 2000, tc.hdg, nmPerLongitude); got != tc.inside {
			t.Errorf("%s: inside %v, expected %v", tc.name, got, tc.inside)
		}
	}
}

func TestATPARunwaySettings(t *testing.T) {
	ap, thr35, nmPerLongitude := atpaTestAirport(t, nil)
	ap.Approaches = map[string]*Approach{"I35": atpaTestFinal(ILSApproach, thr35, 0, false)}
	ap.ATPARunways = map[string]*ATPARunway{
		"35": {ReducedSeparation: true, ATPAVolumeGeometry: ATPAVolumeGeometry{Length: 20, LeftWidth: 6000, Ceiling: 3000}},
	}
	var e util.ErrorLogger
	ap.finalizeATPAVolumes("KTST", testLocator{}, nmPerLongitude, -11, &e)
	if e.HaveErrors() {
		t.Fatal(e.String())
	}
	vol := ap.ATPAVolumes["35"]
	if math.Abs(vol.Path.Length-20) > 0.01 || vol.LeftWidth != 6000 || vol.RightWidth != defaultATPAWidth ||
		vol.Floor != 100 || vol.Ceiling != 3000 || vol.MaxHeadingDeviation != defaultATPAHeadingDeviation {
		t.Errorf("settings not applied: %+v", vol.ATPAVolumeGeometry)
	}
	if !ap.ATPARunways["35"].ReducedSeparation {
		t.Errorf("runway 35 lost its reduced separation authorization")
	}
	left := math.Offset2LL(math.Offset2LL(thr35, 180, 5, nmPerLongitude), 270, 0.8, nmPerLongitude)
	right := math.Offset2LL(math.Offset2LL(thr35, 180, 5, nmPerLongitude), 90, 0.8, nmPerLongitude)
	if !vol.Inside(left, 2000, 0, nmPerLongitude) || vol.Inside(right, 2000, 0, nmPerLongitude) {
		t.Errorf("the volume should be wider to the left than the right")
	}
	if vol.Inside(left, 3500, 0, nmPerLongitude) {
		t.Errorf("above the ceiling should be outside")
	}

	for _, tc := range []struct {
		name string
		key  string
		err  string
	}{
		{"a suffix on the runway", "35 ATPA BOX", "is not a runway"},
		{"no approaches", "17", "no approaches land"},
		{"unknown runway", "9", "is not a runway"},
	} {
		ap.ATPARunways = map[string]*ATPARunway{tc.key: {}}
		var e util.ErrorLogger
		ap.finalizeATPAVolumes("KTST", testLocator{}, nmPerLongitude, -11, &e)
		if !strings.Contains(e.String(), tc.err) {
			t.Errorf("%s: expected an error containing %q, got %q", tc.name, tc.err, e.String())
		}
	}
}

func TestFitCourseLength(t *testing.T) {
	thr := math.Point2LL{-111.96, 40.77}
	nmPerLongitude := math.NMPerLongitudeAt(thr)
	far := math.Offset2LL(thr, 180, 20, nmPerLongitude)
	mid := math.Offset2LL(thr, 180, 5, nmPerLongitude)

	cut := fitCourseLength([]CRDARoutePoint{{Location: far}, {Location: mid}, {Location: thr}}, 15, nmPerLongitude)
	if math.Abs(cut.Length-15) > 0.01 || len(cut.Segments) != 2 {
		t.Errorf("cut path has length %.2f and %d segments, expected 15 and 2", cut.Length, len(cut.Segments))
	}
	extended := fitCourseLength([]CRDARoutePoint{{Location: mid}, {Location: thr}}, 15, nmPerLongitude)
	if math.Abs(extended.Length-15) > 0.01 {
		t.Errorf("extended path has length %.2f, expected 15", extended.Length)
	}
	for _, path := range []Path{cut, extended} {
		if start, _ := path.PointAtDistance(0); math.NMDistance2LL(math.NM2LL(start, nmPerLongitude), far) > 5.05 {
			t.Errorf("path starts %.2f nm from the 20nm point, expected 5", math.NMDistance2LL(math.NM2LL(start, nmPerLongitude), far))
		}
		if end, _ := path.PointAtDistance(path.Length); math.NMDistance2LL(math.NM2LL(end, nmPerLongitude), thr) > 0.01 {
			t.Errorf("path ends %.2f nm from the threshold", math.NMDistance2LL(math.NM2LL(end, nmPerLongitude), thr))
		}
	}
}
