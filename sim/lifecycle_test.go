// sim/lifecycle_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A saved sim is restored from the JSON that GetSerializeSimJSON writes, so a
// field that JSON skips comes back zero. No exported field reachable from a
// Sim may be tagged json:"-". A type with its own MarshalJSON decides what it
// writes, so the walk stops there.
func TestSimJSONOmitsNoFields(t *testing.T) {
	marshaler := reflect.TypeFor[json.Marshaler]()
	seen := make(map[reflect.Type]bool)

	var walk func(ty reflect.Type, path string)
	walk = func(ty reflect.Type, path string) {
		if seen[ty] {
			return
		}
		seen[ty] = true
		if ty.Implements(marshaler) || reflect.PointerTo(ty).Implements(marshaler) {
			return
		}

		switch ty.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(ty.Elem(), path)
		case reflect.Struct:
			for f := range ty.Fields() {
				if !f.IsExported() && !f.Anonymous {
					continue
				}
				if f.Tag.Get("json") == "-" {
					t.Errorf("%s.%s is tagged json:\"-\" and would be lost when a saved sim is restored",
						path, f.Name)
				}
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	walk(reflect.TypeFor[Sim](), "Sim")
}
