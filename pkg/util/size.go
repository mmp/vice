package util

import (
	"fmt"
	"reflect"
	"unsafe"
)

// SizeOf returns the total size in bytes of the given object.
// If printMembers is true and obj is a struct, it prints the size of each field.
// If threshold > 0, prints any element that is threshold bytes or larger.
func SizeOf(obj any, printMembers bool, threshold int64) int64 {
	if obj == nil {
		return 0
	}

	v := reflect.ValueOf(obj)
	return sizeOfValue(v, printMembers, threshold, "", make(map[uintptr]bool), "")
}

func sizeOfValue(v reflect.Value, printMembers bool, threshold int64, indent string, visited map[uintptr]bool, path string) int64 {
	if !v.IsValid() {
		return 0
	}

	// Handle pointers to avoid infinite recursion
	if v.Kind() == reflect.Ptr || v.Kind() == reflect.Map || v.Kind() == reflect.Slice {
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

	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			totalSize += sizeOfValue(v.Elem(), false, threshold, indent+"  ", visited, path+".*")
		}

	case reflect.Struct:
		if printMembers && indent == "" {
			fmt.Printf("Struct %s total size: %d bytes\n", t.Name(), baseSize)
			fmt.Println("Field sizes:")
		}

		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			fieldType := t.Field(i)
			fieldSize := sizeOfValue(field, false, threshold, indent+"  ", visited, path+"."+fieldType.Name)

			if printMembers && indent == "" {
				fmt.Printf("  %s (%s): %d bytes\n", fieldType.Name, fieldType.Type, fieldSize)
			}

			// For embedded fields, add their deep size
			if field.Kind() == reflect.Ptr || field.Kind() == reflect.Slice || field.Kind() == reflect.Map {
				totalSize += fieldSize - int64(field.Type().Size())
			}
		}

	case reflect.Slice:
		if !v.IsNil() {
			elemSize := int64(t.Elem().Size())
			sliceDataSize := int64(v.Len()) * elemSize
			totalSize += sliceDataSize

			// For slices of pointers or complex types, calculate deep size
			if t.Elem().Kind() == reflect.Ptr || t.Elem().Kind() == reflect.Struct || 
			   t.Elem().Kind() == reflect.Slice || t.Elem().Kind() == reflect.Map {
				for i := 0; i < v.Len(); i++ {
					totalSize += sizeOfValue(v.Index(i), false, threshold, indent+"  ", visited, fmt.Sprintf("%s[%d]", path, i)) - elemSize
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
				totalSize += sizeOfValue(iter.Key(), false, threshold, indent+"  ", visited, path+".key")
				totalSize += sizeOfValue(iter.Value(), false, threshold, indent+"  ", visited, path+".value")
			}
		}

	case reflect.String:
		totalSize += int64(v.Len())

	case reflect.Array:
		elemSize := int64(t.Elem().Size())
		if t.Elem().Kind() == reflect.Ptr || t.Elem().Kind() == reflect.Struct ||
		   t.Elem().Kind() == reflect.Slice || t.Elem().Kind() == reflect.Map {
			for i := 0; i < v.Len(); i++ {
				totalSize += sizeOfValue(v.Index(i), false, threshold, indent+"  ", visited, fmt.Sprintf("%s[%d]", path, i)) - elemSize
			}
		}

	case reflect.Interface:
		if !v.IsNil() {
			totalSize += sizeOfValue(v.Elem(), false, threshold, indent+"  ", visited, path)
		}
	}

	// Check threshold and print if size meets or exceeds it
	if threshold > 0 && totalSize >= threshold {
		if path == "" {
			path = fmt.Sprintf("<%s>", t.String())
		}
		fmt.Printf("%s: %d bytes\n", path, totalSize)
	}

	return totalSize
}