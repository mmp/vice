// pkg/util/json.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/iancoleman/orderedmap"
)

///////////////////////////////////////////////////////////////////////////
// JSON

// Unmarshal the bytes into the given type but go through some efforts to
// return useful error messages when the JSON is invalid...
func UnmarshalJSON[T any](b []byte, out *T) error {
	err := json.Unmarshal(b, out)
	if err == nil {
		return nil
	}

	decodeOffset := func(offset int64) (line, char int) {
		line, char = 1, 1
		for i := 0; i < int(offset) && i < len(b); i++ {
			if b[i] == '\n' {
				line++
				char = 1
			} else {
				char++
			}
		}
		return
	}

	switch jerr := err.(type) {
	case *json.SyntaxError:
		line, char := decodeOffset(jerr.Offset)
		return fmt.Errorf("Error at line %d, character %d: %v", line, char, jerr)

	case *json.UnmarshalTypeError:
		line, char := decodeOffset(jerr.Offset)
		return fmt.Errorf("Error at line %d, character %d: %s value for %s.%s invalid for type %s",
			line, char, jerr.Value, jerr.Struct, jerr.Field, jerr.Type.String())

	default:
		return err
	}
}

///////////////////////////////////////////////////////////////////////////

func CheckJSONVsSchema[T any](contents []byte, e *ErrorLogger) {
	var items interface{}
	if err := UnmarshalJSON(contents, &items); err != nil {
		e.Error(err)
		return
	}

	var t T
	ty := reflect.TypeOf(t)
	checkJSONVsSchemaRecursive(items, ty, e)
}

type jsonChecker func(json interface{}) bool

var jsonTypeCheckers map[reflect.Type]jsonChecker = make(map[reflect.Type]jsonChecker)

func init() {
	// OrderedMap has its own unmarshal support; since it is a
	// map[string]interface{}, there's nothing more to check here...
	RegisterJSONTypeChecker[orderedmap.OrderedMap](func(json interface{}) bool { return true })
}

// RegisterJSONTypeChecker allows types that do custom JSON unmarshaling to
// register a function using that is called when the type is encountered
// during checkJSONVsSchemaRecursive's traversal.  The associated checker
// function just returns a Boolean value indicating whether the provided
// json is valid for the type.
func RegisterJSONTypeChecker[T any](f func(json interface{}) bool) {
	var t T
	ty := reflect.TypeOf(t)
	if _, ok := jsonTypeCheckers[ty]; ok {
		panic(fmt.Sprintf("type %T JSON checker redefined", t))
	}
	jsonTypeCheckers[ty] = f
}

func checkJSONVsSchemaRecursive(json interface{}, ty reflect.Type, e *ErrorLogger) {
	for ty.Kind() == reflect.Ptr {
		ty = ty.Elem()
	}

	if f, ok := jsonTypeCheckers[ty]; ok {
		if !f(json) {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		}
		return
	}

	switch ty.Kind() {
	case reflect.Array, reflect.Slice:
		if array, ok := json.([]interface{}); ok {
			for _, item := range array {
				checkJSONVsSchemaRecursive(item, ty.Elem(), e)
			}
		} else if _, ok := json.(string); ok {
			// Some things (e.g., WaypointArray, Point2LL) are array/slice
			// types but are JSON encoded as strings. We'll treat a string
			// value for an array/slice as ok as far as validation here.
		} else {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		}

	case reflect.Map:
		if m, ok := json.(map[string]interface{}); ok {
			for k, v := range m {
				e.Push(k)
				checkJSONVsSchemaRecursive(v, ty.Elem(), e)
				e.Pop()
			}
		} else {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		}

	case reflect.Struct:
		if items, ok := json.(map[string]interface{}); !ok {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		} else {
			for item, values := range items {
				found := false
				for _, field := range reflect.VisibleFields(ty) {
					if j, ok := field.Tag.Lookup("json"); ok {
						for _, jf := range strings.Split(j, ",") {
							if item == jf {
								found = true
								e.Push(jf)
								checkJSONVsSchemaRecursive(values, field.Type, e)
								e.Pop()
								break
							}
						}
					}
				}
				if !found {
					e.ErrorString("The entry \"" + item + "\" is not an expected JSON object. Is it misspelled?")
				}
			}
		}
	}
}
