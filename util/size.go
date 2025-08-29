package util

import (
	"fmt"
	"io"
	"reflect"
	"unsafe"
)

// SizeOf returns the total size in bytes of the given object.
// If printMembers is true and obj is a struct, it prints the size of each field.
// If threshold > 0, prints any element that is threshold bytes or larger.
func SizeOf(obj any, w io.Writer, printMembers bool, threshold int64) int64 {
	if obj == nil {
		return 0
	}

	v := reflect.ValueOf(obj)
	return sizeOfValue(v, w, printMembers, threshold, 0, make(map[uintptr]bool),
		func() string { return "" })
}

func sizeOfValue(v reflect.Value, w io.Writer, printMembers bool, threshold int64, depth int, visited map[uintptr]bool,
	path func() string) int64 {
	if !v.IsValid() {
		return 0
	}

	// Handle pointers to avoid infinite recursion
	vkind := v.Kind()
	if vkind == reflect.Ptr || vkind == reflect.Map || vkind == reflect.Slice {
		if !v.IsNil() {
			ptr := v.Pointer()
			if visited[ptr] {
				return int64(v.Type().Size())
			}
			visited[ptr] = true
		}
	}

	t := v.Type()
	baseSize := int64(t.Size())
	totalSize := baseSize

	switch vkind {
	case reflect.Ptr:
		if !v.IsNil() {
			totalSize += sizeOfValue(v.Elem(), w, false, threshold, depth+1, visited,
				func() string { return path() + ".*" })
		}

	case reflect.Struct:
		if printMembers && depth == 0 {
			fmt.Fprintf(w, "Struct %s total size: %d bytes\n", t.Name(), baseSize)
			fmt.Fprintln(w, "Field sizes:")
		}

		for i := range v.NumField() {
			field := v.Field(i)
			fieldType := t.Field(i)
			fieldName := fieldType.Name
			fieldSize := sizeOfValue(field, w, false, threshold, depth+1, visited,
				func() string { return path() + "." + fieldName })

			if printMembers && depth == 0 {
				fmt.Fprintf(w, "  %s (%s): %d bytes\n", fieldType.Name, fieldType.Type, fieldSize)
			}

			// For embedded fields, add their deep size
			if fk := field.Kind(); fk == reflect.Ptr || fk == reflect.Slice || fk == reflect.Map {
				totalSize += fieldSize - int64(field.Type().Size())
			}
		}

	case reflect.Slice:
		if !v.IsNil() {
			elemSize := int64(t.Elem().Size())
			sliceDataSize := int64(v.Len()) * elemSize
			totalSize += sliceDataSize

			// For slices of pointers or complex types, calculate deep size
			if tek := t.Elem().Kind(); tek == reflect.Ptr || tek == reflect.Struct || tek == reflect.Slice || tek == reflect.Map {
				for i := range v.Len() {
					totalSize += sizeOfValue(v.Index(i), w, false, threshold, depth+1, visited,
						func() string { return fmt.Sprintf("%s[%d]", path(), i) }) - elemSize
				}
			}
		}

	case reflect.Map:
		if !v.IsNil() {
			// Estimate map overhead (buckets, etc.)
			mapOverhead := int64(unsafe.Sizeof(struct {
				count      int
				flags      uint8
				B          uint8
				noverflow  uint16
				hash0      uint32
				buckets    unsafe.Pointer
				oldbuckets unsafe.Pointer
				nevacuate  uintptr
				extra      unsafe.Pointer
			}{}))
			totalSize += mapOverhead

			// Add size of all keys and values
			iter := v.MapRange()
			for iter.Next() {
				totalSize += sizeOfValue(iter.Key(), w, false, threshold, depth+1, visited,
					func() string { return path() + ".key" })
				totalSize += sizeOfValue(iter.Value(), w, false, threshold, depth+1, visited,
					func() string { return path() + ".value" })
			}
		}

	case reflect.String:
		totalSize += int64(v.Len())

	case reflect.Array:
		elemSize := int64(t.Elem().Size())
		if tek := t.Elem().Kind(); tek == reflect.Ptr || tek == reflect.Struct || tek == reflect.Slice || tek == reflect.Map {
			for i := range v.Len() {
				totalSize += sizeOfValue(v.Index(i), w, false, threshold, depth+1, visited,
					func() string { return fmt.Sprintf("%s[%d]", path(), i) }) - elemSize
			}
		}

	case reflect.Interface:
		if !v.IsNil() {
			totalSize += sizeOfValue(v.Elem(), w, false, threshold, depth+1, visited, path)
		}
	}

	// Check threshold and print if size meets or exceeds it
	if threshold > 0 && totalSize >= threshold {
		p := path()
		if p == "" {
			p = fmt.Sprintf("<%s>", t.String())
		}
		if depth > 0 {
			fmt.Fprintf(w, "%*c%s: %d bytes\n", depth*2, ' ', p, totalSize)
		} else {
			fmt.Fprintf(w, "%s: %d bytes\n", p, totalSize)
		}
	}

	return totalSize
}
