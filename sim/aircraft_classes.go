// sim/aircraft_classes.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// Adapted aircraft type classes (DMS Aircraft Type Classes Window, Sec.
// 4.21.1) group aircraft types into named classes for rule matching: the
// facility adaptation's "automatic_handoff_classes" are referenced by handoff
// filters' "actype_class" and its "tcp_assignment_classes" by fix-pair
// reassignment rules' "ac_type".

// validateAircraftClasses checks an adapted aircraft class map (DMS Aircraft
// Type Classes Window, Sec. 4.21.1): class names of 1-8 alphanumeric
// characters, at most 26 classes, and known member aircraft types.
func validateAircraftClasses(member string, classes map[string][]string, e *util.ErrorLogger) {
	e.Push(member)
	defer e.Pop()
	if len(classes) > 26 {
		e.ErrorString("at most 26 classes may be defined")
	}
	for name, types := range classes {
		if len(name) < 1 || len(name) > 8 {
			e.ErrorString("class name %q must be 1 to 8 characters", name)
		}
		for _, ch := range name {
			if !((ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
				e.ErrorString("class name %q must be uppercase alphanumeric", name)
				break
			}
		}
		for _, t := range types {
			if _, ok := av.DB.AircraftPerformance[t]; !ok {
				e.ErrorString("class %q: unknown aircraft type %q", name, t)
			}
		}
	}
}

// matchACTypeClass matches an adapted aircraft-type-class criterion: "" or "*"
// matches any aircraft; otherwise the flight's aircraft type must be a member
// of the named class in the given class map (DMS 4.21.1).
func matchACTypeClass(classes map[string][]string, field, acType string) bool {
	field = strings.ToUpper(strings.TrimSpace(field))
	if field == "" || field == "*" {
		return true
	}
	return slices.ContainsFunc(classes[field], func(t string) bool { return strings.EqualFold(t, acType) })
}
