// util/json.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

///////////////////////////////////////////////////////////////////////////
// JSON

// DuplicateJSONKey represents a duplicate key found in JSON.
type DuplicateJSONKey struct {
	Path string // JSON path to the duplicate (e.g., "scenarios.P50.inbound_rates")
	Key  string // The duplicate key name
}

// FindDuplicateJSONKeys scans JSON content and returns all duplicate keys found.
// It uses the json.Decoder token-based API to walk the JSON structure while
// tracking seen keys at each object nesting level.
func FindDuplicateJSONKeys(data []byte) []DuplicateJSONKey {
	dec := json.NewDecoder(bytes.NewReader(data))
	var duplicates []DuplicateJSONKey

	// Stack entry tracks state for each nesting level
	type stackEntry struct {
		isObject  bool            // true for object, false for array
		seenKeys  map[string]bool // keys seen at this level (only for objects)
		expectKey bool            // true if next string token is an object key
		popPath   bool            // true if we should pop path when closing this container
	}
	var stack []stackEntry
	var path []string // Current path components

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}

		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				// Check if this object is a value (parent is object expecting value)
				popPath := len(stack) > 0 && stack[len(stack)-1].isObject && !stack[len(stack)-1].expectKey
				stack = append(stack, stackEntry{
					isObject:  true,
					seenKeys:  make(map[string]bool),
					expectKey: true,
					popPath:   popPath,
				})
			case '}':
				// Pop path only if this object was a value of a key
				if len(stack) > 0 {
					if stack[len(stack)-1].popPath && len(path) > 0 {
						path = path[:len(path)-1]
					}
					stack = stack[:len(stack)-1]
				}
				// After closing, parent expects next key if it's an object
				if len(stack) > 0 && stack[len(stack)-1].isObject {
					stack[len(stack)-1].expectKey = true
				}
			case '[':
				// Check if this array is a value (parent is object expecting value)
				popPath := len(stack) > 0 && stack[len(stack)-1].isObject && !stack[len(stack)-1].expectKey
				stack = append(stack, stackEntry{
					isObject: false,
					popPath:  popPath,
				})
			case ']':
				// Pop path only if this array was a value of a key
				if len(stack) > 0 {
					if stack[len(stack)-1].popPath && len(path) > 0 {
						path = path[:len(path)-1]
					}
					stack = stack[:len(stack)-1]
				}
				// After closing, parent expects next key if it's an object
				if len(stack) > 0 && stack[len(stack)-1].isObject {
					stack[len(stack)-1].expectKey = true
				}
			}
		case string:
			if len(stack) > 0 {
				top := &stack[len(stack)-1]
				if top.isObject && top.expectKey {
					// This is an object key
					if top.seenKeys[v] {
						fullPath := strings.Join(path, ".")
						duplicates = append(duplicates, DuplicateJSONKey{
							Path: fullPath,
							Key:  v,
						})
					}
					top.seenKeys[v] = true
					top.expectKey = false
					path = append(path, v)
				} else {
					// This is a string value - pop the key from path
					if top.isObject {
						top.expectKey = true
						if len(path) > 0 {
							path = path[:len(path)-1]
						}
					}
				}
			}
		default:
			// Other primitive values (numbers, bools, null) - pop the key from path
			if len(stack) > 0 {
				top := &stack[len(stack)-1]
				if top.isObject {
					top.expectKey = true
					if len(path) > 0 {
						path = path[:len(path)-1]
					}
				}
			}
		}
	}

	return duplicates
}

func UnmarshalJSON[T any](r io.Reader, out *T) error {
	// Unfortunately we need the contents as an array of bytes so that we
	// can issue reasonable errors.
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return UnmarshalJSONBytes(b, out)
}

// Unmarshal the bytes into the given type but go through some efforts to
// return useful error messages when the JSON is invalid...
func UnmarshalJSONBytes[T any](b []byte, out *T) error {
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

// CheckJSON checks whether the provided JSON is syntactically valid and
// then typechecks it with respect to the provided type T.
func CheckJSON[T any](contents []byte, e *ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	var items interface{}
	if err := UnmarshalJSONBytes(contents, &items); err != nil {
		e.Error(err)
		return
	}

	var t T
	ty := reflect.TypeOf(t)
	structTypeCache := make(map[reflect.Type]map[string]reflect.Type)
	typeCheckJSON(items, ty, structTypeCache, e)
}

// TypeCheckJSON returns a Boolean indicating whether the provided raw
// unmarshaled JSON values are type-compatible with the given type T.
func TypeCheckJSON[T any](json interface{}) bool {
	var e ErrorLogger
	ty := reflect.TypeOf((*T)(nil)).Elem()
	structTypeCache := make(map[reflect.Type]map[string]reflect.Type)
	typeCheckJSON(json, ty, structTypeCache, &e)
	return !e.HaveErrors()
}

// JSONChecker is an interface that allows types that implement custom JSON
// unmarshalers to check whether raw unmarshled JSON types are compatible
// with their underlying type.
type JSONChecker interface {
	CheckJSON(json interface{}) bool
}

func typeCheckJSON(json interface{}, ty reflect.Type, structTypeCache map[reflect.Type]map[string]reflect.Type, e *ErrorLogger) {
	for ty.Kind() == reflect.Ptr {
		ty = ty.Elem()
	}

	// Use the type's JSONChecker, if there is one.
	chty := reflect.TypeOf((*JSONChecker)(nil)).Elem()
	if ty.Implements(chty) || reflect.PointerTo(ty).Implements(chty) {
		checker := reflect.New(ty).Interface().(JSONChecker)
		if !checker.CheckJSON(json) {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		}
		return
	}

	switch ty.Kind() {
	case reflect.Array, reflect.Slice:
		if array, ok := json.([]interface{}); ok {
			for _, item := range array {
				typeCheckJSON(item, ty.Elem(), structTypeCache, e)
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
				typeCheckJSON(v, ty.Elem(), structTypeCache, e)
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
			// For each struct type encountered, structTypeCache holds a
			// map from the JSON name of each struct element to its
			// corresponding reflect.Type to avoid the cost and dynamic
			// memory allocation of repeated calls to
			// reflect.VisibleFields.
			types, ok := structTypeCache[ty]
			if !ok {
				types = make(map[string]reflect.Type)
				for _, field := range reflect.VisibleFields(ty) {
					if jtag, ok := field.Tag.Lookup("json"); ok {
						name, _, _ := strings.Cut(jtag, ",")
						types[name] = field.Type
					}
				}
				structTypeCache[ty] = types
			}

			for item, values := range items {
				if ty, ok := types[item]; ok {
					e.Push(item)
					typeCheckJSON(values, ty, structTypeCache, e)
					e.Pop()
				} else {
					e.ErrorString("The entry %q is not an expected JSON object. Is it misspelled?", item)
				}
			}
		}
	}
}
