// nav/record_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// queryMethods are the exported methods of Nav that aren't commands: they
// read the Nav, or they are the update itself, which records separately.
var queryMethods = []string{
	"ApproachHeading", "AssignedHeading", "AssignedWaypoints", "Check", "ContactMessage",
	"DepartureHeading", "DepartureMessage", "DistanceAlongRoute", "DistanceToEndOfApproach", "ETA",
	"InterceptedButNotCleared", "IsAirborne", "LowestProcedureAltitude", "Mach", "OnApproach",
	"OnExtendedCenterline", "ResolveApproach", "SayAltitude", "SayHeading", "SayIndicatedSpeed",
	"SayMach", "SaySpeed", "SetLookups", "SetRecorder", "Summary", "TAS", "TakeSnapshot",
	"TargetAltitude", "TargetHeading", "TargetSpeed", "Update", "UpdateWithWeather",
}

// TestCommandsRecorded checks that every exported method of Nav other than
// the queries starts with "defer nav.record(...)()", naming itself and all of
// its parameters, so that a trace of a flight holds every command it was
// given.
func TestCommandsRecorded(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	var methods []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !ast.IsExported(fd.Name.Name) || !isNavMethod(fd) {
				continue
			}
			methods = append(methods, fd.Name.Name)
			if slices.Contains(queryMethods, fd.Name.Name) {
				continue
			}
			if problem := recordsCommand(fd); problem != "" {
				t.Errorf("%s: %s: %s", fset.Position(fd.Pos()), fd.Name.Name, problem)
			}
		}
	}

	for _, q := range queryMethods {
		if !slices.Contains(methods, q) {
			t.Errorf("%s is listed as a query but Nav has no such method", q)
		}
	}
}

func isNavMethod(fd *ast.FuncDecl) bool {
	if fd.Recv == nil {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Nav"
}

// recordsCommand returns what is wrong with how the method records itself,
// or "" if its first statement is "defer nav.record(...)()" with its name and
// parameters.
func recordsCommand(fd *ast.FuncDecl) string {
	if len(fd.Body.List) == 0 {
		return "has no statements"
	}
	ds, ok := fd.Body.List[0].(*ast.DeferStmt)
	if !ok || len(ds.Call.Args) != 0 {
		return "doesn't start with defer nav.record(...)()"
	}
	call, ok := ds.Call.Fun.(*ast.CallExpr)
	if !ok {
		return "doesn't start with defer nav.record(...)()"
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "record" {
		return "doesn't start with defer nav.record(...)()"
	}

	want := []string{`"` + fd.Name.Name + `"`}
	for _, p := range fd.Type.Params.List {
		for _, n := range p.Names {
			want = append(want, n.Name)
		}
	}
	var got []string
	for _, a := range call.Args {
		switch a := a.(type) {
		case *ast.BasicLit:
			got = append(got, a.Value)
		case *ast.Ident:
			got = append(got, a.Name)
		default:
			return "records an argument that isn't a parameter"
		}
	}
	if !slices.Equal(got, want) {
		return "records " + strings.Join(got, ", ") + ", not " + strings.Join(want, ", ")
	}
	return ""
}

type fakeRecorder struct {
	started  int
	commands []string
	lookups  []string
	updates  int
}

func (r *fakeRecorder) Start(nav *Nav)                   { r.started++ }
func (r *fakeRecorder) Command(name string, args ...any) { r.commands = append(r.commands, name) }
func (r *fakeRecorder) Lookup(kind, key string, result any) {
	r.lookups = append(r.lookups, kind+" "+key)
}
func (r *fakeRecorder) Update(u UpdateRecord) { r.updates++ }

// A command given inside another command, like the direct-to that a direct
// fix queues, or inside an update, like the altitude to depart a fix at, is
// the outer one's doing, so it isn't recorded on its own: replaying the trace
// gives it once.
func TestNestedCommandsRecordedOnce(t *testing.T) {
	f := newSkorrWaveyFlight(t, "SKORR/a11000 WAVEY/a7000/s250 ROBER")
	rec := &fakeRecorder{}
	f.nav.SetRecorder(rec)

	f.AfterFixAltitude("WAVEY", 5000)
	f.DirectFix("WAVEY")
	if want := []string{"AfterFixAltitude", "DirectFix"}; !slices.Equal(rec.commands, want) {
		t.Fatalf("recorded %v, want %v", rec.commands, want)
	}

	passed := false
	for range 900 {
		result := f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), nil, f.rules,
			f.simTime, false)
		f.simTime = f.simTime.Add(time.Second)
		if result.PassedWaypoint != nil && result.PassedWaypoint.Fix == "WAVEY" {
			passed = true
			break
		}
	}
	if !passed {
		t.Fatal("never passed WAVEY")
	}
	if len(rec.commands) != 2 {
		t.Errorf("the updates recorded commands: %v", rec.commands[2:])
	}
	if rec.updates == 0 {
		t.Error("no updates were recorded")
	}
	if rec.started != 1 {
		t.Errorf("the recorder was started %d times", rec.started)
	}
	if a := f.nav.Altitude.Assigned; a == nil || *a != 5000 {
		t.Errorf("the altitude to depart WAVEY at wasn't assigned: %v", a)
	}

	// A direct to a fix off the route asks the database where it is.
	f.DirectFix("JFK")
	if !slices.Contains(rec.lookups, "waypoint JFK") {
		t.Errorf("recorded lookups %v lack the fix", rec.lookups)
	}
}
