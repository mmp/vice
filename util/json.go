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

// FindDuplicateJSONKeys scans JSON content and returns all duplicate keys
// found. It walks the JSON structure recursively over the json.Decoder
// token stream, tracking the seen keys at each object nesting level. A
// per-walker pool of map[string]struct{} is reused across object scopes
// so that deeply nested input does not allocate a fresh map per `{`.
func FindDuplicateJSONKeys(data []byte) []DuplicateJSONKey {
	w := dupKeyWalker{dec: json.NewDecoder(bytes.NewReader(data))}
	_ = w.walkValue()
	return w.dups
}

type dupKeyWalker struct {
	dec     *json.Decoder
	path    []string
	dups    []DuplicateJSONKey
	keysets []map[string]struct{}
}

func (w *dupKeyWalker) borrowKeyset() map[string]struct{} {
	if n := len(w.keysets); n > 0 {
		m := w.keysets[n-1]
		w.keysets = w.keysets[:n-1]
		clear(m)
		return m
	}
	return make(map[string]struct{})
}

func (w *dupKeyWalker) returnKeyset(m map[string]struct{}) {
	w.keysets = append(w.keysets, m)
}

// walkValue reads the next token from the decoder and dispatches on it.
// Primitive values are consumed and ignored; objects and arrays recurse
// into walkObject/walkArray which consume their own closing delimiter.
func (w *dupKeyWalker) walkValue() error {
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			return w.walkObject()
		case '[':
			return w.walkArray()
		}
	}
	return nil
}

func (w *dupKeyWalker) walkObject() error {
	seen := w.borrowKeyset()
	defer w.returnKeyset(seen)

	for w.dec.More() {
		tok, err := w.dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			// Shouldn't happen for valid JSON object keys, but if the
			// decoder gives us something unexpected, stop walking this
			// object.
			return nil
		}
		if _, dup := seen[key]; dup {
			w.dups = append(w.dups, DuplicateJSONKey{
				Path: strings.Join(w.path, "."),
				Key:  key,
			})
		}
		seen[key] = struct{}{}

		w.path = append(w.path, key)
		if err := w.walkValue(); err != nil {
			return err
		}
		w.path = w.path[:len(w.path)-1]
	}

	// Consume the closing '}'.
	_, err := w.dec.Token()
	return err
}

func (w *dupKeyWalker) walkArray() error {
	for w.dec.More() {
		if err := w.walkValue(); err != nil {
			return err
		}
	}
	// Consume the closing ']'.
	_, err := w.dec.Token()
	return err
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

	var items any
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
func TypeCheckJSON[T any](json any) bool {
	var e ErrorLogger
	ty := reflect.TypeFor[T]()
	structTypeCache := make(map[reflect.Type]map[string]reflect.Type)
	typeCheckJSON(json, ty, structTypeCache, &e)
	return !e.HaveErrors()
}

// JSONChecker is an interface that allows types that implement custom JSON
// unmarshalers to check whether raw unmarshled JSON types are compatible
// with their underlying type.
type JSONChecker interface {
	CheckJSON(json any) bool
}

func typeCheckJSON(json any, ty reflect.Type, structTypeCache map[reflect.Type]map[string]reflect.Type, e *ErrorLogger) {
	for ty.Kind() == reflect.Pointer {
		ty = ty.Elem()
	}

	// Use the type's JSONChecker, if there is one.
	chty := reflect.TypeFor[JSONChecker]()
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
		if array, ok := json.([]any); ok {
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
		if m, ok := json.(map[string]any); ok {
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
		if items, ok := json.(map[string]any); !ok {
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
