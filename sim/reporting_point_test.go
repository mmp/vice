// sim/reporting_point_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// reportingPointTestPosition is where newReportingPointScenario puts the
// aircraft: 8nm south of the airport, heading north.
var reportingPointTestPosition = math.Point2LL{0, -8.0 / 60}

// fromTestPosition returns the point nm miles from reportingPointTestPosition on the
// given bearing.
func fromTestPosition(bearing, nm float32) math.Point2LL {
	return math.Offset2LL(reportingPointTestPosition, math.TrueHeading(bearing), nm, 52)
}

func testReportingPoint(id string, loc math.Point2LL, names ...string) *av.ReportingPoint {
	return &av.ReportingPoint{Id: id, Names: names, Location: av.ScenarioPoint2LL{Point2LL: loc}}
}

var testBridge = testReportingPoint("BRIDGE", fromTestPosition(0, 4), "Dumbarton bridge", "bridge", "Dumbarton")

// reportingPoints returns the points keyed by their identifiers, as an
// approach holds them.
func reportingPoints(points ...*av.ReportingPoint) map[string]*av.ReportingPoint {
	m := make(map[string]*av.ReportingPoint)
	for _, rp := range points {
		m[rp.Id] = rp
	}
	return m
}

// newReportingPointScenario returns a scenario where the aircraft has been told
// to expect a charted visual approach with the given reporting points.
func newReportingPointScenario(t *testing.T, points ...*av.ReportingPoint) *VisualScenario {
	t.Helper()
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "36", Heading: 360, Threshold: airportLoc, Elevation: 13})
	vs := NewVisualScenario(t, airportLoc, "36", reportingPointTestPosition, 360)

	appr := vs.Sim.State.Airports["KJFK"].Approaches["V36"]
	appr.ReportingPoints = reportingPoints(points...)
	vs.AC.Nav.Approach = nav.Approach{AssignedId: "V36", Assigned: appr}
	return vs
}

func (vs *VisualScenario) ReportingPointAdvisory(oclock, miles int) speech.ReportingPointIntent {
	vs.t.Helper()
	intent, err := vs.Sim.ReportingPointAdvisory(vs.tcw, vs.callsign, "", oclock, miles)
	if err != nil {
		vs.t.Fatalf("ReportingPointAdvisory(%d,%d) error: %v", oclock, miles, err)
	}
	rpi, ok := intent.(speech.ReportingPointIntent)
	if !ok {
		vs.t.Fatalf("ReportingPointAdvisory(%d,%d): expected ReportingPointIntent, got %T", oclock, miles, intent)
	}
	return rpi
}

// lookingFor returns the reporting point the pilot is still looking for, if any.
func (vs *VisualScenario) lookingFor() *av.ReportingPoint {
	if f, ok := vs.Sim.FutureFieldChecks[vs.callsign]; ok {
		return f.ReportingPoint
	}
	return nil
}

// pendingTransmissionText returns the written text of the aircraft's pending
// transmission of the given type.
func (vs *VisualScenario) pendingTransmissionText(txType PendingTransmissionType) string {
	vs.t.Helper()
	for _, pcs := range vs.Sim.PendingContacts {
		for i, pc := range pcs {
			if pc.ADSBCallsign == vs.callsign && pc.Type == txType {
				pt, err := vs.Sim.renderContact(pcs[i])
				if err != nil {
					vs.t.Fatalf("%v", err)
				}
				return pt.Written
			}
		}
	}
	vs.t.Fatalf("no pending transmission of type %v", txType)
	return ""
}

func TestReportingPointAdvisoryRequiresChartedVisual(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "36", Heading: 360, Threshold: airportLoc, Elevation: 13})
	vs := NewVisualScenario(t, airportLoc, "36", reportingPointTestPosition, 360) // expecting the ILS

	for _, id := range []string{"", "BRIDGE"} {
		intent, err := vs.Sim.ReportingPointAdvisory(vs.tcw, vs.callsign, id, 12, 4)
		if err != nil {
			t.Fatalf("ReportingPointAdvisory error: %v", err)
		}
		if u, ok := intent.(speech.UnableIntent); !ok || u.Message != "unable, we're not expecting a charted visual" {
			t.Errorf("id %q: got %#v, want unable, not expecting a charted visual", id, intent)
		}
	}
}

func TestReportingPointAdvisory(t *testing.T) {
	vs := newReportingPointScenario(t, testBridge)

	switch intent := vs.ReportingPointAdvisory(12, 4); intent.Response {
	case speech.ReportingPointInSight:
		if vs.AC.SightedReportingPoint == nil || vs.AC.SightedReportingPoint.Name() != testBridge.Name() {
			t.Errorf("sighted %+v, want the bridge", vs.AC.SightedReportingPoint)
		}
		if !slices.Equal(intent.Names, testBridge.Names) {
			t.Errorf("reported with names %v, want %v", intent.Names, testBridge.Names)
		}
	case speech.ReportingPointLooking:
		// The pilot may never speak up (pilotNoReportProb), in which case
		// they aren't looking for anything.
		if rp := vs.lookingFor(); rp != nil && rp.Name() != testBridge.Name() {
			t.Errorf("looking for %+v, want the bridge", rp)
		}
	default:
		t.Errorf("unexpected response %v", intent.Response)
	}
}

func TestReportingPointAdvisoryWrongDirection(t *testing.T) {
	vs := newReportingPointScenario(t, testBridge)

	// The bridge is at 12 o'clock, not 3. The pilot keeps looking for it,
	// though pilotNoReportProb of the time they never speak up, so call it
	// until they do.
	for range 50 {
		if intent := vs.ReportingPointAdvisory(3, 4); intent.Response != speech.ReportingPointLooking {
			t.Fatalf("got response %v, want looking", intent.Response)
		}
		if vs.AC.SightedReportingPoint != nil {
			t.Fatalf("pilot reported %q in sight", vs.AC.SightedReportingPoint.Name())
		}
		if rp := vs.lookingFor(); rp != nil {
			if rp.Name() != testBridge.Name() {
				t.Errorf("looking for %q, want the bridge", rp.Name())
			}
			return
		}
	}
	t.Error("the pilot never kept looking for the bridge")
}

func TestReportingPointAdvisoryIMC(t *testing.T) {
	vs := newReportingPointScenario(t, testBridge)
	vs.SetMETAR("KJFK 1/2SM OVC002")

	if intent := vs.ReportingPointAdvisory(12, 4); intent.Response != speech.ReportingPointLookingIMC {
		t.Errorf("got response %v, want IMC", intent.Response)
	}
	if _, ok := vs.Sim.FutureFieldChecks[vs.callsign]; ok {
		t.Error("pilot in IMC keeps looking")
	}
}

func TestReportingPointAdvisoryAlreadyInSight(t *testing.T) {
	vs := newReportingPointScenario(t, testBridge)
	vs.AC.SightedReportingPoint = testBridge
	vs.SetMETAR("KJFK 1/2SM OVC002") // no second look

	for _, oclock := range []int{12, 0} {
		intent := vs.ReportingPointAdvisory(oclock, 4)
		if intent.Response != speech.ReportingPointInSight || !slices.Equal(intent.Names, testBridge.Names) {
			t.Errorf("oclock %d: got %+v, want the bridge in sight", oclock, intent)
		}
	}
}

func TestCalledReportingPoint(t *testing.T) {
	tower := testReportingPoint("TOWER", fromTestPosition(90, 5), "tower")
	behind := testReportingPoint("STADIUM", fromTestPosition(180, 2), "stadium")
	vs := newReportingPointScenario(t, testBridge, tower, behind)

	for _, c := range []struct {
		oclock, miles int
		want          string
	}{
		{12, 4, testBridge.Name()},
		{3, 5, tower.Name()},
		{6, 2, behind.Name()},
		// With no position, the nearest one that isn't behind the aircraft.
		{0, 0, testBridge.Name()},
	} {
		if rp := vs.AC.calledReportingPoint("", c.oclock, c.miles); rp == nil || rp.Name() != c.want {
			t.Errorf("%d o'clock %d miles: got %+v, want %q", c.oclock, c.miles, rp, c.want)
		}
	}

	names := util.MapSlice(vs.AC.ReportingPointsAhead(), func(rp av.ReportingPoint) string { return rp.Name() })
	if !slices.Equal(names, []string{testBridge.Name(), tower.Name()}) {
		t.Errorf("reporting points ahead: got %v, want the bridge and the tower", names)
	}
}

func TestNamedReportingPointCommands(t *testing.T) {
	stadium := testReportingPoint("STADIUM", fromTestPosition(0, 6), "stadium / arena", "stadium")
	for _, c := range []struct {
		command string
		seen    *av.ReportingPoint
		want    speech.ReportingPointResponse
	}{
		{command: "RP/STADIUM", seen: stadium, want: speech.ReportingPointInSight},
		{command: "RP/12/4/STADIUM", seen: stadium, want: speech.ReportingPointInSight},
		{command: "RP/STADIUM", seen: testBridge, want: speech.ReportingPointLookingIMC},
		{command: "RP/bridge", seen: testBridge, want: speech.ReportingPointInSight}, // identifiers ignore case
		{command: "RP", seen: testBridge, want: speech.ReportingPointInSight},
		{command: "RP", seen: stadium, want: speech.ReportingPointLookingIMC},
		{command: "RP/12/4", seen: testBridge, want: speech.ReportingPointInSight},
	} {
		t.Run(c.command+" with "+c.seen.Id+" in sight", func(t *testing.T) {
			vs := newReportingPointScenario(t, testBridge, stadium)
			vs.AC.SightedReportingPoint = c.seen
			vs.SetMETAR("KJFK 1/2SM OVC002")
			intent, err := vs.Sim.runOneControlCommand(vs.tcw, vs.callsign, c.command, 0)
			if err != nil {
				t.Fatal(err)
			}
			rpi, ok := intent.(speech.ReportingPointIntent)
			if !ok || rpi.Response != c.want {
				t.Fatalf("got %+v, want response %v", intent, c.want)
			}
			if c.want == speech.ReportingPointInSight && !slices.Equal(rpi.Names, c.seen.Names) {
				t.Errorf("reported %v, want %v", rpi.Names, c.seen.Names)
			}
		})
	}

	// A landmark's names aren't identifiers.
	vs := newReportingPointScenario(t, testBridge)
	intent, err := vs.Sim.runOneControlCommand(vs.tcw, vs.callsign, "RP/DUMBARTON", 0)
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := intent.(speech.UnableIntent); !ok || !strings.Contains(u.Message, "isn't on our expected approach") {
		t.Errorf("unknown landmark: got %+v, want unable", intent)
	}
	for _, command := range []string{"RP/", "RP/0/4", "RP/12/0", "RP/12/4/", "RP/13/4/BRIDGE", "RP/12/4/BRIDGE/X"} {
		if _, err := vs.Sim.runOneControlCommand(vs.tcw, vs.callsign, command, 0); err != ErrInvalidCommandSyntax {
			t.Errorf("%s: got %v, want invalid syntax", command, err)
		}
	}
}

func TestExpectApproachResetsReportingPoint(t *testing.T) {
	stadium := testReportingPoint("STADIUM", fromTestPosition(0, 5), "stadium")
	for _, c := range []struct {
		approach string
		cleared  bool
	}{
		{approach: "V36"},
		{approach: "UNKNOWN"},
		{approach: "STADIUM", cleared: true},
	} {
		t.Run(c.approach, func(t *testing.T) {
			vs := newReportingPointScenario(t, testBridge)
			ap := vs.Sim.State.Airports["KJFK"]
			appr := *ap.Approaches["V36"]
			appr.ReportingPoints = reportingPoints(stadium)
			ap.Approaches["STADIUM"] = &appr
			vs.AC.SightedReportingPoint = testBridge
			if _, err := vs.Sim.ExpectApproach(vs.tcw, vs.callsign, c.approach); err != nil {
				t.Fatal(err)
			}
			if (vs.AC.SightedReportingPoint == nil) != c.cleared {
				t.Fatalf("sighted %+v, want cleared = %v", vs.AC.SightedReportingPoint, c.cleared)
			}
			if !c.cleared {
				return
			}
			vs.SetMETAR("KJFK 1/2SM OVC002")
			if intent := vs.ReportingPointAdvisory(0, 0); intent.Response != speech.ReportingPointLookingIMC {
				t.Errorf("inquiry after reassignment: got %+v, want looking IMC", intent)
			}
			vs.SetMETAR("KJFK 10SM CLR")
			vs.AC.WantsVisualApproach = true
			vs.Sim.checkSpontaneousReportingPoint(vs.AC)
			if !vs.HasPendingTransmission(PendingTransmissionSpontaneousReportingPointInSight) {
				t.Error("new approach's landmark was not spontaneously reported")
			}
		})
	}
}

// A look for a landmark ends with the approach it belongs to, even if the new
// approach has a landmark of the same identifier; finding the old one mustn't
// permit the new approach's clearance.
func TestLandmarkLookEndsWithItsApproach(t *testing.T) {
	vs := newReportingPointScenario(t, testBridge)
	ap := vs.Sim.State.Airports["KJFK"]
	other := *ap.Approaches["V36"]
	other.ReportingPoints = reportingPoints(testReportingPoint("BRIDGE", fromTestPosition(180, 20), "San Mateo bridge"))
	ap.Approaches["OTHER"] = &other

	// Called at the wrong o'clock, the pilot keeps looking for the bridge,
	// though pilotNoReportProb of the time they never speak up.
	for range 50 {
		vs.ReportingPointAdvisory(3, 4)
		if vs.lookingFor() != nil {
			break
		}
	}
	if f := vs.Sim.FutureFieldChecks[vs.callsign]; f == nil || f.ApproachId != "V36" {
		t.Fatalf("got look %+v, want one for the V36 bridge", f)
	}

	if _, err := vs.Sim.ExpectApproach(vs.tcw, vs.callsign, "OTHER"); err != nil {
		t.Fatal(err)
	}
	vs.AdvanceTime(time.Minute)
	vs.CheckDelayedFieldInSight()

	if vs.HasPendingTransmission(PendingTransmissionReportingPointInSight) || vs.AC.SightedReportingPoint != nil {
		t.Errorf("pilot reported the previous approach's bridge in sight")
	}
	if vs.lookingFor() != nil {
		t.Error("look for the previous approach's bridge was kept")
	}
	intent, err := vs.Sim.ClearedApproach(vs.tcw, vs.callsign, "OTHER", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := intent.(speech.UnableIntent); !ok {
		t.Errorf("clearance accepted without the new approach's bridge in sight: %#v", intent)
	}
}

func TestDelayedReportingPointWithPreviousSighting(t *testing.T) {
	stadium := testReportingPoint("STADIUM", fromTestPosition(0, 5), "stadium")
	for _, c := range []struct {
		name     string
		seen     *av.ReportingPoint
		points   []*av.ReportingPoint
		approach string // that the pilot was asked to look for the stadium on
		report   bool
	}{
		{name: "different landmark", seen: testBridge, points: []*av.ReportingPoint{testBridge, stadium},
			approach: "V36", report: true},
		{name: "same landmark", seen: stadium, points: []*av.ReportingPoint{testBridge, stadium}, approach: "V36"},
		{name: "landmark from previous approach", seen: testBridge, points: []*av.ReportingPoint{testBridge},
			approach: "STADIUM"},
	} {
		t.Run(c.name, func(t *testing.T) {
			vs := newReportingPointScenario(t, c.points...)
			vs.AC.SightedReportingPoint = c.seen
			vs.Sim.FutureFieldChecks[vs.callsign] = &FutureFieldCheck{Time: vs.Sim.State.SimTime,
				ReportingPoint: stadium, ApproachId: c.approach}
			vs.AdvanceTime(time.Second)
			vs.CheckDelayedFieldInSight()
			if reported := vs.HasPendingTransmission(PendingTransmissionReportingPointInSight); reported != c.report {
				t.Fatalf("reported = %v, want %v", reported, c.report)
			}
			if c.report && vs.AC.SightedReportingPoint.Name() != stadium.Name() {
				t.Errorf("sighted %q, want stadium", vs.AC.SightedReportingPoint.Name())
			}
			if vs.lookingFor() != nil {
				t.Error("delayed check was not removed")
			}
		})
	}
}

func TestDelayedReportingPointInSight(t *testing.T) {
	vs := newReportingPointScenario(t, testBridge)
	vs.Sim.FutureFieldChecks[vs.callsign] = &FutureFieldCheck{Time: vs.Sim.State.SimTime,
		ReportingPoint: testBridge, ApproachId: "V36"}

	vs.AdvanceTime(time.Second)
	vs.CheckDelayedFieldInSight()

	if vs.AC.SightedReportingPoint == nil || vs.AC.SightedReportingPoint.Name() != testBridge.Name() {
		t.Fatalf("sighted %+v, want the bridge", vs.AC.SightedReportingPoint)
	}
	if vs.AC.FieldInSight {
		t.Error("finding the bridge reported the field in sight")
	}
	// The controller has named it, so any of its names will do.
	w := vs.pendingTransmissionText(PendingTransmissionReportingPointInSight)
	if !slices.ContainsFunc(testBridge.Names, func(name string) bool { return strings.HasPrefix(w, name+" in sight") }) {
		t.Errorf("got %q, want one of %v in sight", w, testBridge.Names)
	}
}

func TestSpontaneousReportingPoint(t *testing.T) {
	withLocation := func(loc math.Point2LL) *av.ReportingPoint {
		rp := *testBridge
		rp.Location = av.ScenarioPoint2LL{Point2LL: loc}
		return &rp
	}

	for _, c := range []struct {
		name   string
		rp     *av.ReportingPoint
		setup  func(vs *VisualScenario)
		report bool
	}{
		{name: "ahead", rp: testBridge, report: true},
		{name: "off to the side", rp: withLocation(fromTestPosition(40, 5)), report: true},
		{name: "doesn't report things", rp: testBridge,
			setup: func(vs *VisualScenario) { vs.AC.WantsVisualApproach = false }},
		{name: "too close", rp: withLocation(fromTestPosition(0, 2))},
		{name: "too far", rp: withLocation(fromTestPosition(0, 12))},
		{name: "too far off the nose", rp: withLocation(fromTestPosition(60, 5))},
		{name: "IMC", rp: testBridge, setup: func(vs *VisualScenario) { vs.SetMETAR("KJFK 1/2SM OVC002") }},
		{name: "cleared for the approach", rp: testBridge,
			setup: func(vs *VisualScenario) { vs.AC.Nav.Approach.Cleared = true }},
		{name: "already reported", rp: testBridge,
			setup: func(vs *VisualScenario) { vs.AC.SightedReportingPoint = testBridge }},
	} {
		t.Run(c.name, func(t *testing.T) {
			vs := newReportingPointScenario(t, c.rp)
			vs.AC.WantsVisualApproach = true
			if c.setup != nil {
				c.setup(vs)
			}

			vs.Sim.checkSpontaneousReportingPoint(vs.AC)

			reported := vs.HasPendingTransmission(PendingTransmissionSpontaneousReportingPointInSight)
			if reported != c.report {
				t.Fatalf("reported = %v, want %v", reported, c.report)
			}
			if !reported {
				return
			}
			// Unprompted, the pilot names it in full.
			if w := vs.pendingTransmissionText(PendingTransmissionSpontaneousReportingPointInSight); w != "Dumbarton bridge in sight" {
				t.Errorf("got %q, want the full name in sight", w)
			}

			// Having reported it, they don't report it again.
			vs.ClearPendingTransmissions()
			vs.Sim.checkSpontaneousReportingPoint(vs.AC)
			if vs.HasPendingTransmission(PendingTransmissionSpontaneousReportingPointInSight) {
				t.Error("reported it a second time")
			}
		})
	}
}

// A charted visual approach clearance requires one of its landmarks, the field,
// or preceding traffic landing the same runway in sight.
func TestChartedVisualClearanceRequiresSighting(t *testing.T) {
	precedingTraffic := func(vs *VisualScenario) {
		traffic := makeVisualTestAircraft(fromTestPosition(0, 3), 360)
		traffic.ADSBCallsign = "AAL5207"
		traffic.Nav.Approach = nav.Approach{Cleared: true, Assigned: &av.Approach{Type: av.ILSApproach, Runway: "36"}}
		vs.Sim.Aircraft[traffic.ADSBCallsign] = traffic
		vs.AC.RecordSighting(traffic.ADSBCallsign, vs.Sim.State.SimTime)
	}
	otherBridge := testReportingPoint("SANMATEO", fromTestPosition(30, 6), "San Mateo bridge", "bridge")
	bridge := []*av.ReportingPoint{testBridge}

	for _, c := range []struct {
		name    string
		points  []*av.ReportingPoint
		setup   func(vs *VisualScenario)
		refusal string // "" if the pilot accepts the clearance
	}{
		{name: "nothing in sight", points: bridge, refusal: "unable, Dumbarton bridge not in sight"},
		{name: "nothing in sight, no landmarks", refusal: "unable, we don't have the field in sight"},
		{name: "landmark in sight", points: bridge,
			setup: func(vs *VisualScenario) { vs.AC.SightedReportingPoint = testBridge }},
		{name: "another approach's landmark in sight", points: bridge,
			setup:   func(vs *VisualScenario) { vs.AC.SightedReportingPoint = otherBridge },
			refusal: "unable, Dumbarton bridge not in sight"},
		{name: "field in sight", points: bridge, setup: func(vs *VisualScenario) { vs.AC.FieldInSight = true }},
		{name: "preceding traffic in sight", points: bridge, setup: precedingTraffic},
		{name: "restated clearance", points: bridge, setup: func(vs *VisualScenario) { vs.AC.Nav.Approach.Cleared = true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			vs := newReportingPointScenario(t, c.points...)
			if c.setup != nil {
				c.setup(vs)
			}

			intent, err := vs.Sim.ClearedApproach(vs.tcw, vs.callsign, "V36", false)
			if err != nil {
				t.Fatalf("ClearedApproach error: %v", err)
			}
			if c.refusal == "" {
				if _, ok := intent.(speech.ClearedApproachIntent); !ok {
					t.Errorf("expected the clearance to be accepted, got %#v", intent)
				}
				return
			}

			r := vrand.Make()
			if rd, err := speech.RenderIntents([]speech.CommandIntent{intent}, r).Render(r); err != nil || rd.Written != c.refusal {
				t.Errorf("got %q (err %v), want %q", rd.Written, err, c.refusal)
			}
			if vs.AC.Nav.Approach.Cleared {
				t.Error("refused clearance left the aircraft cleared")
			}
		})
	}
}

func TestChartedVisualAtFixClearanceRequiresSighting(t *testing.T) {
	vs := newReportingPointScenario(t, testBridge)

	intent, err := vs.Sim.AtFixCleared(vs.tcw, vs.callsign, "FAF36", "V36", false, 0)
	if err != nil {
		t.Fatalf("AtFixCleared error: %v", err)
	}
	if u, ok := intent.(speech.UnableIntent); !ok || !strings.Contains(u.Message, "in sight") {
		t.Errorf("expected the clearance to be refused for want of a sighting, got %#v", intent)
	}
	if vs.AC.Nav.Approach.EffectivelyCleared() {
		t.Error("refused clearance left the aircraft cleared")
	}
}
