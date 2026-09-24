// aviation/db/snapshot.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	gomath "math"
	"reflect"
	"slices"
	"strings"

	"github.com/mmp/vice/util"

	"github.com/vmihailenco/msgpack/v5"
)

// A session log carries the database its sim ran on, so that a replay flies
// with the data the session had rather than with whatever the resources hold
// by then. Encode and Decode give its serialized form, and Hash identifies its
// contents so that sessions run on the same data can share one copy.

// Encode writes the database in the form Decode reads: msgpack, compressed
// with flate.
func (d *StaticDatabase) Encode(w io.Writer) error {
	fw, err := flate.NewWriter(w, flate.BestSpeed)
	if err != nil {
		return err
	}
	if err := msgpack.NewEncoder(fw).Encode(d); err != nil {
		return err
	}
	return fw.Close()
}

// Decode reads a database written by Encode.
func Decode(r io.Reader) (*StaticDatabase, error) {
	fr := flate.NewReader(r)
	defer fr.Close()

	var d StaticDatabase
	if err := msgpack.NewDecoder(fr).Decode(&d); err != nil {
		return nil, err
	}
	if conflicts := d.buildFAAIndex(); len(conflicts) > 0 {
		return nil, errors.New(strings.Join(conflicts, "\n"))
	}
	return &d, nil
}

// Hash returns a digest of the database's contents. It is the same for
// databases holding the same data, however their maps happen to be laid out.
func (d *StaticDatabase) Hash() string {
	h := sha256.New()
	hashValue(h, reflect.ValueOf(d).Elem())
	return hex.EncodeToString(h.Sum(nil))
}

// hashValue adds v's exported contents to h. Go randomizes map iteration
// order, so each map entry is hashed on its own and the entry digests are
// sorted before they are added.
func hashValue(h hash.Hash, v reflect.Value) {
	var buf [8]byte
	writeUint := func(u uint64) {
		binary.LittleEndian.PutUint64(buf[:], u)
		h.Write(buf[:])
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			writeUint(0)
		} else {
			writeUint(1)
			hashValue(h, v.Elem())
		}
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			if t.Field(i).IsExported() {
				hashValue(h, v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		writeUint(uint64(v.Len()))
		for i := range v.Len() {
			hashValue(h, v.Index(i))
		}
	case reflect.Map:
		entries := make([][sha256.Size]byte, 0, v.Len())
		for it := v.MapRange(); it.Next(); {
			eh := sha256.New()
			hashValue(eh, it.Key())
			hashValue(eh, it.Value())
			entries = append(entries, [sha256.Size]byte(eh.Sum(nil)))
		}
		slices.SortFunc(entries, func(a, b [sha256.Size]byte) int { return bytes.Compare(a[:], b[:]) })
		writeUint(uint64(len(entries)))
		for _, e := range entries {
			h.Write(e[:])
		}
	case reflect.String:
		writeUint(uint64(v.Len()))
		h.Write([]byte(v.String()))
	case reflect.Bool:
		writeUint(uint64(util.Select(v.Bool(), 1, 0)))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		writeUint(uint64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		writeUint(v.Uint())
	case reflect.Float32, reflect.Float64:
		writeUint(gomath.Float64bits(v.Float()))
	default:
		panic(fmt.Sprintf("%s: unhandled kind %s in database hash", v.Type(), v.Kind()))
	}
}
