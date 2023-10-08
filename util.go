// util.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	_ "embed"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/iancoleman/orderedmap"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/exp/constraints"
	"golang.org/x/exp/slog"
)

///////////////////////////////////////////////////////////////////////////
// decompression

var decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))

// decompressZstd decompresses data that was compressed using zstd.
// There's no error handling to speak of, since this is currently only used
// for data that's baked into the vice binary, so any issues with that
// should be evident upon a first run.
func decompressZstd(s string) string {
	b, err := decoder.DecodeAll([]byte(s), nil)
	if err != nil {
		lg.Errorf("Error decompressing buffer")
	}
	return string(b)
}

///////////////////////////////////////////////////////////////////////////
// text

// wrapText wraps the provided text string to the given column limit, returning the
// wrapped string and the number of lines it became.  indent gives the amount to
// indent wrapped lines.  By default, lines that start with a space are assumed to be
// preformatted and are not wrapped; providing a true value for wrapAll overrides
// that behavior and causes them to be wrapped as well.
func wrapText(s string, columnLimit int, indent int, wrapAll bool) (string, int) {
	var accum, result strings.Builder

	var wrapLine bool
	column := 0
	lines := 1

	flush := func() {
		if wrapLine && column > columnLimit {
			result.WriteRune('\n')
			lines++
			for i := 0; i < indent; i++ {
				result.WriteRune(' ')
			}
			column = indent + accum.Len()
		}
		result.WriteString(accum.String())
		accum.Reset()
	}

	for _, ch := range s {
		// If wrapAll isn't enabled, then if the line starts with a space,
		// assume it is preformatted and pass it through unchanged.
		if column == 0 {
			wrapLine = wrapAll || ch != ' '
		}

		accum.WriteRune(ch)
		column++

		if ch == '\n' {
			flush()
			column = 0
			lines++
		} else if ch == ' ' {
			flush()
		}
	}

	flush()
	return result.String(), lines
}

// stopShouting turns text of the form "UNITED AIRLINES" to "United Airlines"
func stopShouting(orig string) string {
	var s strings.Builder
	wsLast := true
	for _, ch := range orig {
		if unicode.IsSpace(ch) {
			wsLast = true
		} else if unicode.IsLetter(ch) {
			if wsLast {
				// leave it alone
				wsLast = false
			} else {
				ch = unicode.ToLower(ch)
			}
		}

		// otherwise leave it alone

		s.WriteRune(ch)
	}
	return s.String()
}

// atof is a utility for parsing floating point values that sends errors to
// the logging system.
func atof(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
		lg.Errorf("%s: error converting to float: %s", s, err)
		return 0
	} else {
		return v
	}
}

func isAllNumbers(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

var (
	//go:embed resources/nouns.txt
	nounsFile string
	nounList  []string

	//go:embed resources/adjectives.txt
	adjectivesFile string
	adjectiveList  []string
)

func getRandomAdjectiveNoun() string {
	if nounList == nil {
		nounList = strings.Split(nounsFile, "\n")
	}
	if adjectiveList == nil {
		adjectiveList = strings.Split(adjectivesFile, "\n")
	}

	return strings.TrimSpace(adjectiveList[rand.Intn(len(adjectiveList))]) + "-" +
		strings.TrimSpace(nounList[rand.Intn(len(nounList))])
}

///////////////////////////////////////////////////////////////////////////
// headings and directions

type CardinalOrdinalDirection int

const (
	North = iota
	NorthEast
	East
	SouthEast
	South
	SouthWest
	West
	NorthWest
)

func (co CardinalOrdinalDirection) Heading() float32 {
	return float32(co) * 45
}

func (co CardinalOrdinalDirection) ShortString() string {
	switch co {
	case North:
		return "N"
	case NorthEast:
		return "NE"
	case East:
		return "E"
	case SouthEast:
		return "SE"
	case South:
		return "S"
	case SouthWest:
		return "SW"
	case West:
		return "W"
	case NorthWest:
		return "NW"
	default:
		return "ERROR"
	}
}

func ParseCardinalOrdinalDirection(s string) (CardinalOrdinalDirection, error) {
	switch s {
	case "N":
		return North, nil
	case "NE":
		return NorthEast, nil
	case "E":
		return East, nil
	case "SE":
		return SouthEast, nil
	case "S":
		return South, nil
	case "SW":
		return SouthWest, nil
	case "W":
		return West, nil
	case "NW":
		return NorthWest, nil
	}

	return CardinalOrdinalDirection(0), fmt.Errorf("invalid direction")
}

func nmPerLongitude(p Point2LL) float32 {
	return 45
	// WANT: return 60 * sin(radians(p[1]))
}

// headingp2ll returns the heading from the point |from| to the point |to|
// in degrees.  The provided points should be in latitude-longitude
// coordinates and the provided magnetic correction is applied to the
// result.
func headingp2ll(from Point2LL, to Point2LL, nmPerLongitude float32, magCorrection float32) float32 {
	v := Point2LL{to[0] - from[0], to[1] - from[1]}

	// Note that atan2() normally measures w.r.t. the +x axis and angles
	// are positive for counter-clockwise. We want to measure w.r.t. +y and
	// to have positive angles be clockwise. Happily, swapping the order of
	// values passed to atan2()--passing (x,y), gives what we want.
	angle := degrees(atan2(v[0]*nmPerLongitude, v[1]*nmPerLatitude))
	return NormalizeHeading(angle + magCorrection)
}

// headingDifference returns the minimum difference between two
// headings. (i.e., the result is always in the range [0,180].)
func headingDifference(a float32, b float32) float32 {
	var d float32
	if a > b {
		d = a - b
	} else {
		d = b - a
	}
	if d > 180 {
		d = 360 - d
	}
	return d
}

// compass converts a heading expressed into degrees into a string
// corresponding to the closest compass direction.
func compass(heading float32) string {
	h := NormalizeHeading(heading + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"North", "Northeast", "East", "Southeast",
		"South", "Southwest", "West", "Northwest"}[idx]
}

// shortCompass converts a heading expressed in degrees into an abbreviated
// string corresponding to the closest compass direction.
func shortCompass(heading float32) string {
	h := NormalizeHeading(heading + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}[idx]
}

// headingAsHour converts a heading expressed in degrees into the closest
// "o'clock" value, with an integer result in the range [1,12].
func headingAsHour(heading float32) int {
	heading = NormalizeHeading(heading - 15)
	// now [0,30] is 1 o'clock, etc
	return 1 + int(heading/30)
}

// Reduces it to [0,360).
func NormalizeHeading(h float32) float32 {
	if h < 0 {
		return 360 - NormalizeHeading(-h)
	}
	return mod(h, 360)
}

func OppositeHeading(h float32) float32 {
	return NormalizeHeading(h + 180)
}

///////////////////////////////////////////////////////////////////////////
// RGB

type RGB struct {
	R, G, B float32
}

type RGBA struct {
	R, G, B, A float32
}

func lerpRGB(x float32, a, b RGB) RGB {
	return RGB{R: lerp(x, a.R, b.R), G: lerp(x, a.G, b.G), B: lerp(x, a.B, b.B)}
}

func (r RGB) Equals(other RGB) bool {
	return r.R == other.R && r.G == other.G && r.B == other.B
}

func (r RGB) Scale(v float32) RGB {
	return RGB{R: r.R * v, G: r.G * v, B: r.B * v}
}

// RGBFromHex converts a packed integer color value to an RGB where the low
// 8 bits give blue, the next 8 give green, and then the next 8 give red.
func RGBFromHex(c int) RGB {
	r, g, b := (c>>16)&255, (c>>8)&255, c&255
	return RGB{R: float32(r) / 255, G: float32(g) / 255, B: float32(b) / 255}
}

///////////////////////////////////////////////////////////////////////////
// generics

func Select[T any](sel bool, a, b T) T {
	if sel {
		return a
	} else {
		return b
	}
}

// FlattenMap takes a map and returns separate slices corresponding to the
// keys and values stored in the map.  (The slices are ordered so that the
// i'th key corresponds to the i'th value, needless to say.)
func FlattenMap[K comparable, V any](m map[K]V) ([]K, []V) {
	keys := make([]K, 0, len(m))
	values := make([]V, 0, len(m))
	for k, v := range m {
		keys = append(keys, k)
		values = append(values, v)
	}
	return keys, values
}

// SortedMapKeys returns the keys of the given map, sorted from low to high.
func SortedMapKeys[K constraints.Ordered, V any](m map[K]V) []K {
	keys, _ := FlattenMap(m)
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// SortedMapKeysPred returns the keys of the given map sorted using the
// provided predicate function which should perform a "less than"
// comparison of key values.
func SortedMapKeysPred[K comparable, V any](m map[K]V, pred func(a *K, b *K) bool) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return pred(&keys[i], &keys[j]) })
	return keys
}

// DuplicateMap returns a newly-allocated map that stores copies of all of
// the values in the given map.
func DuplicateMap[K comparable, V any](m map[K]V) map[K]V {
	mnew := make(map[K]V)
	for k, v := range m {
		mnew[k] = v
	}
	return mnew
}

// FilterMap returns a newly-allocated result that is the result of
// applying the given predicate function to all of the elements in the
// given map and only including those for which the predicate returned
// true.
func FilterMap[K comparable, V any](m map[K]V, pred func(K, V) bool) map[K]V {
	mnew := make(map[K]V)
	for k, v := range m {
		if pred(k, v) {
			mnew[k] = v
		}
	}
	return mnew
}

// ReduceSlice applies the provided reduction function to the given slice,
// starting with the provided initial value.  The update rule applied is
// result=reduce( value, result), where the initial value of result is
// given by the initial parameter.
func ReduceSlice[V any, R any](s []V, reduce func(V, R) R, initial R) R {
	result := initial
	for _, v := range s {
		result = reduce(v, result)
	}
	return result
}

// ReduceMap applies the provided reduction function to the given map,
// starting with the provided initial value.  The update rule applied is
// result=reduce(key, value, result), where the initial value of result is
// given by the initial parameter.
func ReduceMap[K comparable, V any, R any](m map[K]V, reduce func(K, V, R) R, initial R) R {
	result := initial
	for k, v := range m {
		result = reduce(k, v, result)
	}
	return result
}

// DuplicateSlice returns a newly-allocated slice that is a copy of the
// provided one.
func DuplicateSlice[V any](s []V) []V {
	dupe := make([]V, len(s))
	copy(dupe, s)
	return dupe
}

// DeleteSliceElement deletes the i'th element of the given slice,
// returning the resulting slice.  Note that the provided slice s is
// modified!
func DeleteSliceElement[V any](s []V, i int) []V {
	// First move any subsequent elements down one position.
	if i+1 < len(s) {
		copy(s[i:], s[i+1:])
	}
	// And drop the now-unnecessary final element.
	return s[:len(s)-1]
}

// InsertSliceElement inserts the given value v at the index i in the slice
// s, moving all elements after i one place forward.
func InsertSliceElement[V any](s []V, i int, v V) []V {
	s = append(s, v) // just to grow the slice (unless i == len(s))
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// SliceEqual checks whether two slices are equal.
func SliceEqual[V comparable](a []V, b []V) bool {
	if len(a) != len(b) {
		return false
	}
	for i, f := range a {
		if f != b[i] {
			return false
		}
	}
	return true
}

// MapSlice returns the slice that is the result of applying the provided
// xform function to all of the elements of the given slice.
func MapSlice[F, T any](from []F, xform func(F) T) []T {
	var to []T
	for _, item := range from {
		to = append(to, xform(item))
	}
	return to
}

// FilterSlice applies the given filter function pred to the given slice,
// returning a new slice that only contains elements where pred returned
// true.
func FilterSlice[V any](s []V, pred func(V) bool) []V {
	var filtered []V
	for _, item := range s {
		if pred(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// Find returns the index of the first instance of the given value in the
// slice or -1 if it is not present.
func Find[V comparable](s []V, value V) int {
	for i, v := range s {
		if v == value {
			return i
		}
	}
	return -1
}

// Find returns the index of the first instance of the given value in the
// slice or -1 if it is not present.
func FindIf[V any](s []V, pred func(V) bool) int {
	for i, v := range s {
		if pred(v) {
			return i
		}
	}
	return -1
}

// AnySlice applies the given predicate to each element of the provided
// slice in sequence and returns true after the first element where the
// predicate returns true.  False is returned if the predicate always
// evaluates to false.
func AnySlice[V any](s []V, pred func(V) bool) bool {
	for _, v := range s {
		if pred(v) {
			return true
		}
	}
	return false
}

// Sample uniformly randomly samples an element of a non-empty slice.
func Sample[T any](slice []T) T {
	return slice[rand.Intn(len(slice))]
}

// SampleFiltered uniformly randomly samples a slice, returning the index
// of the sampled item, using provided predicate function to filter the
// items that may be sampled.  An index of -1 is returned if the slice is
// empty or the predicate returns false for all items.
func SampleFiltered[T any](slice []T, pred func(T) bool) int {
	idx := -1
	candidates := 0
	for i, v := range slice {
		if pred(v) {
			candidates++
			p := float32(1) / float32(candidates)
			if rand.Float32() < p {
				idx = i
			}
		}
	}
	return idx
}

// SampleWeighted randomly samples an element from the given slice with the
// probability of choosing each element proportional to the value returned
// by the provided callback.
func SampleWeighted[T any](slice []T, weight func(T) int) int {
	// Weighted reservoir sampling...
	idx := -1
	sumWt := 0
	for i, v := range slice {
		w := weight(v)
		if w == 0 {
			continue
		}

		sumWt += w
		p := float32(w) / float32(sumWt)
		if rand.Float32() < p {
			idx = i
		}
	}
	return idx
}

///////////////////////////////////////////////////////////////////////////
// TransientMap

// TransientMap represents a set of objects with a built-in expiry time in
// the future; after an item's time passes, it is automatically removed
// from the set.
type TransientMap[K comparable, V any] struct {
	m map[K]valueTime[V]
}

type valueTime[V any] struct {
	v V
	t time.Time
}

func NewTransientMap[K comparable, V any]() *TransientMap[K, V] {
	return &TransientMap[K, V]{m: make(map[K]valueTime[V])}
}

func (t *TransientMap[K, V]) flush() {
	now := time.Now()
	for k, vt := range t.m {
		if now.After(vt.t) {
			delete(t.m, k)
		}
	}
}

// Add adds a given value to the set; it will no longer be there after the
// specified duration has passed.
func (t *TransientMap[K, V]) Add(key K, value V, d time.Duration) {
	t.m[key] = valueTime[V]{v: value, t: time.Now().Add(d)}
}

// Get looks up the given key in the map and returns its value and a
// Boolean that indicates whether it was found.
func (t *TransientMap[K, V]) Get(key K) (V, bool) {
	t.flush()
	vt, ok := t.m[key]
	return vt.v, ok
}

// Delete deletes the item in the map with the given key, if present.
func (t *TransientMap[K, V]) Delete(key K) {
	delete(t.m, key)
}

///////////////////////////////////////////////////////////////////////////
// RingBuffer

// RingBuffer represents an array of no more than a given maximum number of
// items.  Once it has filled, old items are discarded to make way for new
// ones.
type RingBuffer[V any] struct {
	entries []V
	max     int
	index   int
}

func NewRingBuffer[V any](capacity int) *RingBuffer[V] {
	return &RingBuffer[V]{max: capacity}
}

// Add adds all of the provided values to the ring buffer.
func (r *RingBuffer[V]) Add(values ...V) {
	for _, v := range values {
		if len(r.entries) < r.max {
			// Append to the entries slice if it hasn't yet hit the limit.
			r.entries = append(r.entries, v)
		} else {
			// Otherwise treat r.entries as a ring buffer where
			// (r.index+1)%r.max is the oldest entry and successive newer
			// entries follow.
			r.entries[r.index%r.max] = v
		}
		r.index++
	}
}

// Size returns the total number of items stored in the ring buffer.
func (r *RingBuffer[V]) Size() int {
	return min(len(r.entries), r.max)
}

// Get returns the specified element of the ring buffer where the index i
// is between 0 and Size()-1 and 0 is the oldest element in the buffer.
func (r *RingBuffer[V]) Get(i int) V {
	return r.entries[(r.index+i)%len(r.entries)]
}

///////////////////////////////////////////////////////////////////////////
// Networking miscellany

func FetchURL(url string) ([]byte, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var text []byte
	if text, err = io.ReadAll(response.Body); err != nil {
		return nil, err
	}

	return text, nil
}

///////////////////////////////////////////////////////////////////////////
// Image processing

func GenerateImagePyramid(img image.Image) []image.Image {
	var pyramid []image.Image

	// We always work with image.RGBA in the following..
	nx, ny := img.Bounds().Dx(), img.Bounds().Dy()
	prevLevel, ok := img.(*image.RGBA)
	if !ok {
		prevLevel = image.NewRGBA(image.Rect(0, 0, nx, ny))
		draw.Draw(prevLevel, prevLevel.Bounds(), img, img.Bounds().Min, draw.Src)
	}
	pyramid = append(pyramid, prevLevel)

	for nx != 1 || ny != 1 {
		ox, oy := nx, ny
		nx, ny = max(nx/2, 1), max(ny/2, 1)

		next := make([]uint8, nx*ny*4)
		lookup := func(x, y int) color.RGBA {
			if x > ox-1 {
				x = ox - 1
			}
			if y > oy-1 {
				y = oy - 1
			}
			offset := 4*x + prevLevel.Stride*y
			return color.RGBA{
				R: prevLevel.Pix[offset],
				G: prevLevel.Pix[offset+1],
				B: prevLevel.Pix[offset+2],
				A: prevLevel.Pix[offset+3]}
		}
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				v := [4]color.RGBA{lookup(2*x, 2*y), lookup(2*x+1, 2*y), lookup(2*x, 2*y+1), lookup(2*x+1, 2*y+1)}

				// living large with a box filter
				next[4*(x+y*nx)+0] = uint8((int(v[0].R) + int(v[1].R) + int(v[2].R) + int(v[3].R) + 2) / 4)
				next[4*(x+y*nx)+1] = uint8((int(v[0].G) + int(v[1].G) + int(v[2].G) + int(v[3].G) + 2) / 4)
				next[4*(x+y*nx)+2] = uint8((int(v[0].B) + int(v[1].B) + int(v[2].B) + int(v[3].B) + 2) / 4)
				next[4*(x+y*nx)+3] = uint8((int(v[0].A) + int(v[1].A) + int(v[2].A) + int(v[3].A) + 2) / 4)
			}
		}

		nextLevel := &image.RGBA{
			Pix:    next,
			Stride: 4 * nx,
			Rect:   image.Rectangle{Max: image.Point{X: nx, Y: ny}}}
		pyramid = append(pyramid, nextLevel)
		prevLevel = nextLevel
	}

	return pyramid
}

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

func checkJSONVsSchemaRecursive(json interface{}, ty reflect.Type, e *ErrorLogger) {
	for ty.Kind() == reflect.Ptr {
		ty = ty.Elem()
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
		} else if ty == reflect.TypeOf(orderedmap.OrderedMap{}) {
			// Special case this since it has its own unmarshal support;
			// since it is a map[string]interface{}, there's nothing more
			// to check here...
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

///////////////////////////////////////////////////////////////////////////
// RPC/Networking stuff

// Straight out of net/rpc/server.go
type gobServerCodec struct {
	rwc    io.ReadWriteCloser
	dec    *gob.Decoder
	enc    *gob.Encoder
	encBuf *bufio.Writer
	closed bool
}

func (c *gobServerCodec) ReadRequestHeader(r *rpc.Request) error {
	return c.dec.Decode(r)
}

func (c *gobServerCodec) ReadRequestBody(body any) error {
	return c.dec.Decode(body)
}

func (c *gobServerCodec) WriteResponse(r *rpc.Response, body any) (err error) {
	if err = c.enc.Encode(r); err != nil {
		if c.encBuf.Flush() == nil {
			// Gob couldn't encode the header. Should not happen, so if it does,
			// shut down the connection to signal that the connection is broken.
			lg.Errorf("rpc: gob error encoding response: %v", err)
			c.Close()
		}
		return
	}
	if err = c.enc.Encode(body); err != nil {
		if c.encBuf.Flush() == nil {
			// Was a gob problem encoding the body but the header has been written.
			// Shut down the connection to signal that the connection is broken.
			lg.Errorf("rpc: gob error encoding body: %v", err)
			c.Close()
		}
		return
	}
	return c.encBuf.Flush()
}

func (c *gobServerCodec) Close() error {
	if c.closed {
		// Only call c.rwc.Close once; otherwise the semantics are undefined.
		return nil
	}
	c.closed = true
	return c.rwc.Close()
}

func MakeGOBServerCodec(conn io.ReadWriteCloser) rpc.ServerCodec {
	buf := bufio.NewWriter(conn)
	return &gobServerCodec{
		rwc:    conn,
		dec:    gob.NewDecoder(conn),
		enc:    gob.NewEncoder(buf),
		encBuf: buf,
	}
}

type LoggingServerCodec struct {
	rpc.ServerCodec
	label string
}

func MakeLoggingServerCodec(label string, c rpc.ServerCodec) *LoggingServerCodec {
	return &LoggingServerCodec{ServerCodec: c, label: label}
}

func (c *LoggingServerCodec) ReadRequestHeader(r *rpc.Request) error {
	err := c.ServerCodec.ReadRequestHeader(r)
	lg.Debug("server: rpc request", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.Any("error", err))
	return err
}

func (c *LoggingServerCodec) WriteResponse(r *rpc.Response, body any) error {
	err := c.ServerCodec.WriteResponse(r, body)
	lg.Debug("server: rpc response", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.Any("error", err))
	return err
}

// This from net/rpc/client.go...
type gobClientCodec struct {
	rwc    io.ReadWriteCloser
	dec    *gob.Decoder
	enc    *gob.Encoder
	encBuf *bufio.Writer
}

func (c *gobClientCodec) WriteRequest(r *rpc.Request, body any) (err error) {
	if err = c.enc.Encode(r); err != nil {
		return
	}
	if err = c.enc.Encode(body); err != nil {
		return
	}
	return c.encBuf.Flush()
}

func (c *gobClientCodec) ReadResponseHeader(r *rpc.Response) error {
	return c.dec.Decode(r)
}

func (c *gobClientCodec) ReadResponseBody(body any) error {
	return c.dec.Decode(body)
}

func (c *gobClientCodec) Close() error {
	return c.rwc.Close()
}

func MakeGOBClientCodec(conn io.ReadWriteCloser) rpc.ClientCodec {
	encBuf := bufio.NewWriter(conn)
	return &gobClientCodec{conn, gob.NewDecoder(conn), gob.NewEncoder(encBuf), encBuf}
}

type LoggingClientCodec struct {
	rpc.ClientCodec
	label string
}

func MakeLoggingClientCodec(label string, c rpc.ClientCodec) *LoggingClientCodec {
	return &LoggingClientCodec{ClientCodec: c, label: label}
}

func (c *LoggingClientCodec) WriteRequest(r *rpc.Request, v any) error {
	err := c.ClientCodec.WriteRequest(r, v)
	lg.Debug("client: rpc request", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.Any("error", err))
	return err
}

func (c *LoggingClientCodec) ReadResponseHeader(r *rpc.Response) error {
	err := c.ClientCodec.ReadResponseHeader(r)
	lg.Debug("client: rpc response", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.Any("error", err))
	return err
}

type CompressedConn struct {
	net.Conn
	r *zstd.Decoder
	w *zstd.Encoder
}

func MakeCompressedConn(c net.Conn) (*CompressedConn, error) {
	cc := &CompressedConn{Conn: c}
	var err error
	if cc.r, err = zstd.NewReader(c); err != nil {
		return nil, err
	}
	if cc.w, err = zstd.NewWriter(c); err != nil {
		return nil, err
	}
	return cc, nil
}

func (c *CompressedConn) Read(b []byte) (n int, err error) {
	n, err = c.r.Read(b)
	return
}

func (c *CompressedConn) Write(b []byte) (n int, err error) {
	n, err = c.w.Write(b)
	c.w.Flush()
	return
}

var RXTotal, TXTotal int64

type LoggingConn struct {
	net.Conn
	sent, received int64
	start          time.Time
	lastReport     time.Time
}

func MakeLoggingConn(c net.Conn) *LoggingConn {
	return &LoggingConn{
		Conn:       c,
		start:      time.Now(),
		lastReport: time.Now(),
	}
}

func GetLoggedRPCBandwidth() (int64, int64) {
	return atomic.LoadInt64(&RXTotal), atomic.LoadInt64(&TXTotal)
}

func (c *LoggingConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)

	atomic.AddInt64(&c.received, int64(n))
	atomic.AddInt64(&RXTotal, int64(n))
	c.maybeReport()

	return
}

func (c *LoggingConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)

	atomic.AddInt64(&c.sent, int64(n))
	atomic.AddInt64(&TXTotal, int64(n))
	c.maybeReport()

	return
}

func (c *LoggingConn) maybeReport() {
	if time.Since(c.lastReport) > 1*time.Minute {
		min := time.Since(c.start).Minutes()
		rec, sent := atomic.LoadInt64(&c.received), atomic.LoadInt64(&c.sent)
		lg.Info("bandwidth",
			slog.String("address", c.Conn.RemoteAddr().String()),
			slog.Int64("bytes_received", rec),
			slog.Int("bytes_received_per_minute", int(float64(rec)/min)),
			slog.Int64("bytes_transmitted", sent),
			slog.Int("bytes_transmitted_per_minute", int(float64(sent)/min)))
		c.lastReport = time.Now()
	}
}

func isRPCServerError(err error) bool {
	_, ok := err.(rpc.ServerError)
	return ok || errors.Is(err, rpc.ErrShutdown)
}

type RPCClient struct {
	*rpc.Client
}

func (c *RPCClient) CallWithTimeout(serviceMethod string, args any, reply any) error {
	pc := &PendingCall{
		Call:      c.Go(serviceMethod, args, reply, nil),
		IssueTime: time.Now(),
	}

	select {
	case <-pc.Call.Done:
		return pc.Call.Error

	case <-time.After(5 * time.Second):
		return ErrRPCTimeout
	}
}

type PendingCall struct {
	Call                *rpc.Call
	IssueTime           time.Time
	OnSuccess           func(any)
	OnErr               func(error)
	haveWarnedNoUpdates bool
}

func (p *PendingCall) CheckFinished(eventStream *EventStream) bool {
	select {
	case c := <-p.Call.Done:
		if c.Error != nil {
			if p.OnErr != nil {
				p.OnErr(c.Error)
			} else {
				lg.Errorf("%v", c.Error)
			}
		} else {
			if p.haveWarnedNoUpdates {
				p.haveWarnedNoUpdates = false
				if eventStream != nil {
					eventStream.Post(Event{
						Type:    StatusMessageEvent,
						Message: "Server connection reestablished!",
					})
				} else {
					lg.Errorf("Server connection reesablished")
				}
			}
			if p.OnSuccess != nil {
				p.OnSuccess(c.Reply)
			}
		}
		return true

	default:
		if s := time.Since(p.IssueTime); s > 5*time.Second {
			p.haveWarnedNoUpdates = true
			if eventStream != nil {
				eventStream.Post(Event{
					Type:    StatusMessageEvent,
					Message: "No updates from server in over 5 seconds. Network may have disconnected.",
				})
			} else {
				lg.Errorf("No updates from server in over 5 seconds. Network may have disconnected.")
			}
		}
		return false
	}
}

///////////////////////////////////////////////////////////////////////////

func getResourcesFS() fs.StatFS {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}

	dir := filepath.Dir(path)
	if runtime.GOOS == "darwin" {
		dir = filepath.Clean(filepath.Join(dir, "..", "Resources"))
	} else {
		dir = filepath.Join(dir, "resources")
	}

	fsys, ok := os.DirFS(dir).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}

	check := func(fs fs.StatFS) bool {
		_, errv := fsys.Stat("videomaps")
		_, errs := fsys.Stat("scenarios")
		return errv == nil && errs == nil
	}

	if check(fsys) {
		lg.Infof("%s: resources directory", dir)
		return fsys
	}

	// Try CWD (this is useful for development and debugging but shouldn't
	// be needed for release builds.
	lg.Infof("Trying CWD for resources FS")

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	dir = filepath.Join(wd, "resources")

	fsys, ok = os.DirFS(dir).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}

	if check(fsys) {
		return fsys
	}
	panic("unable to find videomaps in CWD")
}

// LoadResource loads the specified file from the resources directory, decompressing it if
// it is zstd compressed. It panics if the file is not found; missing resources are pretty
// much impossible to recover from.
func LoadResource(path string) []byte {
	b, err := fs.ReadFile(resourcesFS, path)
	if err != nil {
		panic(err)
	}

	if filepath.Ext(path) == ".zst" {
		return []byte(decompressZstd(string(b)))
	}

	return b
}

///////////////////////////////////////////////////////////////////////////

type StackFrame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
}

func Callstack() []StackFrame {
	var callers [16]uintptr
	n := runtime.Callers(3, callers[:]) // skip up to function that is doing logging
	frames := runtime.CallersFrames(callers[:n])

	var fr []StackFrame
	for i := 0; ; i++ {
		frame, more := frames.Next()
		fr = append(fr, StackFrame{
			File:     path.Base(frame.File),
			Line:     frame.Line,
			Function: strings.TrimPrefix(frame.Function, "main."),
		})

		// Don't keep going up into go runtime stack frames.
		if !more || frame.Function == "main.main" {
			break
		}
	}
	return fr
}

///////////////////////////////////////////////////////////////////////////
// LoggingMutex

var heldMutexesMutex sync.Mutex
var heldMutexes map[*LoggingMutex]interface{} = make(map[*LoggingMutex]interface{})

type LoggingMutex struct {
	sync.Mutex
	acq      time.Time
	ackStack []StackFrame
}

func (l *LoggingMutex) Lock(lg *Logger) {
	tryTime := time.Now()
	lg.Debug("attempting to acquire mutex", slog.Any("mutex", l))

	l.Mutex.Lock()

	heldMutexesMutex.Lock()
	heldMutexes[l] = nil
	heldMutexesMutex.Unlock()

	l.acq = time.Now()
	l.ackStack = Callstack()
	w := l.acq.Sub(tryTime)
	lg.Debug("acquired mutex", slog.Any("mutex", l), slog.Duration("wait", w))
	if w > time.Second {
		lg.Warn("long wait to acquire mutex", slog.Any("mutex", l), slog.Duration("wait", w))
	}
}

func (l *LoggingMutex) Unlock(lg *Logger) {
	heldMutexesMutex.Lock()
	if _, ok := heldMutexes[l]; !ok {
		lg.Error("mutex not held", slog.Any("held_mutexes", heldMutexes))
	}
	delete(heldMutexes, l)

	if d := time.Since(l.acq); d > time.Second {
		lg.Warn("mutex held for over 1 second", slog.Any("mutex", l), slog.Duration("held", d),
			slog.Any("held_mutexes", heldMutexes))
	}

	heldMutexesMutex.Unlock()

	l.Mutex.Unlock()

	lg.Debug("released mutex", slog.Any("mutex", l))
}
