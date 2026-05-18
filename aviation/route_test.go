// pkg/aviation/route_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"strings"
	"testing"

	"github.com/mmp/vice/math"
)

type testLocator map[string]math.Point2LL

func (tl testLocator) Locate(fix string) (math.Point2LL, bool) {
	p, ok := tl[fix]
	return p, ok
}

func (tl testLocator) Similar(fix string) []string {
	return nil
}

func (tl testLocator) LocateDME(fix string) (math.Point2LL, int, bool) {
	p, ok := tl[fix]
	return p, 33, ok
}

func TestHoldEntry(t *testing.T) {
	for _, tc := range []struct {
		name         string
		turn         TurnDirection
		headingToFix math.MagneticHeading
		want         HoldEntry
	}{
		{
			name:         "right direct",
			turn:         TurnRight,
			headingToFix: 100,
			want:         HoldEntryDirect,
		},
		{
			name:         "right parallel",
			turn:         TurnRight,
			headingToFix: 330,
			want:         HoldEntryParallel,
		},
		{
			name:         "right teardrop",
			turn:         TurnRight,
			headingToFix: 250,
			want:         HoldEntryTeardrop,
		},
		{
			name:         "left direct",
			turn:         TurnLeft,
			headingToFix: 20,
			want:         HoldEntryDirect,
		},
		{
			name:         "left parallel",
			turn:         TurnLeft,
			headingToFix: 220,
			want:         HoldEntryParallel,
		},
		{
			name:         "left teardrop",
			turn:         TurnLeft,
			headingToFix: 310,
			want:         HoldEntryTeardrop,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hold := Hold{InboundCourse: 90, TurnDirection: tc.turn}
			if got := hold.Entry(tc.headingToFix); got != tc.want {
				t.Fatalf("Entry(%v) = %v, want %v", tc.headingToFix, got, tc.want)
			}
		})
	}
}

func TestParseWaypointActionGroups(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := ParseRoute("_EWR4_4La/h039@a500/r055@d4.0IEZA/l290/ho5W")
	if err != nil {
		t.Fatal(err)
	}
	if len(wps) != 1 {
		t.Fatalf("expected 1 waypoint, got %d", len(wps))
	}

	groups := wps[0].ActionGroups()
	if len(groups) != 3 {
		t.Fatalf("expected 3 action groups, got %d", len(groups))
	}
	if groups[0].Actions.Heading.Heading != 39 {
		t.Errorf("expected first heading 39, got %d", groups[0].Actions.Heading.Heading)
	}
	if groups[0].Until.Type != WaypointActionAltitude || groups[0].Until.Altitude != 500 {
		t.Fatalf("unexpected altitude action group: %+v", groups[0])
	}

	if groups[1].Actions.Heading.Turn != TurnRight || groups[1].Actions.Heading.Heading != 55 {
		t.Errorf("expected right turn to heading 55, got turn %v heading %d",
			groups[1].Actions.Heading.Turn, groups[1].Actions.Heading.Heading)
	}
	if groups[1].Until.Type != WaypointActionDME || groups[1].Until.DMEFix != "IEZA" || groups[1].Until.DMEDistance != 4 {
		t.Fatalf("unexpected DME action group: %+v", groups[1])
	}

	if groups[2].Actions.Heading.Turn != TurnLeft || groups[2].Actions.Heading.Heading != 290 {
		t.Errorf("expected left turn to heading 290, got turn %v heading %d",
			groups[2].Actions.Heading.Turn, groups[2].Actions.Heading.Heading)
	}
	if groups[2].Actions.HandoffController != "5W" {
		t.Fatalf("expected final action group handoff to 5W, got %+v", groups[2].Actions)
	}
	if groups[2].Until.Type != WaypointActionNoTermination {
		t.Fatalf("unexpected final action group: %+v", groups[2])
	}
}

func TestInitializeActionGroupDMEFix(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := ParseRoute("_EWR4_4La/r055@d4.0IEZA/l290")
	if err != nil {
		t.Fatal(err)
	}

	loc := testLocator{
		"_EWR4_4La": {-74.161563, 40.695431},
		"IEZA":      {-74.161563, 40.695431},
	}
	wps = wps.InitializeLocations(loc, 45, 0, false, nil)

	groups := wps[0].ActionGroups()
	if len(groups) == 0 {
		t.Fatal("expected action groups")
	}
	if groups[0].Until.DMEFixLocation.IsZero() {
		t.Fatal("expected initialized DME fix location")
	}
	if groups[0].Until.DMEFixElevation != 33 {
		t.Fatalf("expected initialized DME fix elevation 33, got %d", groups[0].Until.DMEFixElevation)
	}
}

func TestParseLegacyModifierAfterActionGroup(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := ParseRoute("_EWR4_4La/h039@a500/r055/radius2.0/land")
	if err != nil {
		t.Fatal(err)
	}
	if len(wps[0].ActionGroups()) != 2 {
		t.Fatalf("expected 2 action groups, got %d", len(wps[0].ActionGroups()))
	}
	if wps[0].Radius() != 2 {
		t.Fatalf("expected legacy radius modifier to apply after action group, got %.1f", wps[0].Radius())
	}
	if !wps[0].Land() {
		t.Fatal("expected legacy land modifier to apply after action group")
	}
}

func TestParseActionGroupClearApproachAndDuplicateAltitudes(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := ParseRoute("_EWR4_4La/h039@a500/r055/clearapp")
	if err != nil {
		t.Fatal(err)
	}
	groups := wps[0].ActionGroups()
	if len(groups) != 2 {
		t.Fatalf("expected 2 action groups, got %d", len(groups))
	}
	if !groups[1].Actions.ClearApproach {
		t.Fatal("expected clearapp in final action group")
	}

	if _, err := ParseRoute("_EWR4_4La/h039@a500/c50/c100"); err == nil {
		t.Fatal("expected duplicate climb altitude action to fail")
	}
	if _, err := ParseRoute("_EWR4_4La/h039@a500/d50/d100"); err == nil {
		t.Fatal("expected duplicate descend altitude action to fail")
	}
}

func TestParseActionGroupErrorIncludesWaypointContext(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	_, err := ParseRoute("KJFK-13R/h314@4 SKORR")
	if err == nil {
		t.Fatal("expected invalid action termination")
	}
	for _, want := range []string{"KJFK-13R/h314@4", "/h314@4", "4: invalid waypoint action termination"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error %q to include %q", err, want)
		}
	}
}
