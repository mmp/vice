// aviation/flights.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// Flight is one real-world flight at one of a facility's airports. Times are
// UTC, as they are everywhere else in Vice; the local time at an airport is a
// matter for the UI, which is the only place a controller wants to see it.
type Flight struct {
	Airport      string // the facility airport it departs from or arrives at
	Callsign     string
	Other        string // the airport at the other end
	AircraftType string
	Day          uint16 // UTC date, in days from 1970-01-01
	Minute       int    // minutes after UTC midnight
	Departure    bool
}

// FlightDataSubstitutes gives the real airport whose traffic each made-up
// airport borrows. The FAA Academy scenarios fly fictional airports that the
// source data knows nothing about, so each one stands on the flights of a real
// airport with the traffic rate and character it wants. Donors must be
// four-character ICAO codes: that is all the source data's airport lists hold.
var FlightDataSubstitutes = map[string]string{
	"KAAC": "KEWR", // Academy: Newark
	"KBRT": "KBED", // Bartles: Hanscom Field, Boston's business jet reliever
	"KJKE": "KWRI", // Jeske: McGuire AFB, for the military traffic
	"4Y3":  "KTEB", // James: Teterboro
	"4V4":  "KOXC", // Viney: Waterbury-Oxford, turboprops and light singles
}

// Date returns the UTC calendar date the flight operates on.
func (f Flight) Date() time.Time { return FlightDataDate(f.Day) }

// Time returns the instant the flight takes off or touches down.
func (f Flight) Time() time.Time {
	return f.Date().Add(time.Duration(f.Minute) * time.Minute)
}

// FlightDataDayNumber returns the number of days from 1970-01-01 to t's UTC
// calendar date; the time of day is dropped here.
func FlightDataDayNumber(t time.Time) uint16 {
	utc := t.UTC()
	date := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	return uint16(date.Unix() / secondsPerDay)
}

// FlightDataDate is the inverse of FlightDataDayNumber.
func FlightDataDate(day uint16) time.Time {
	return time.Unix(int64(day)*secondsPerDay, 0).UTC()
}

// SortFlights orders flights the way the encoding wants them: by airport, then
// by callsign and direction, then by time. Grouping each airport's flights
// together and then each callsign's is what makes the encoding compact.
func SortFlights(flights []Flight) {
	sort.Slice(flights, func(i, j int) bool {
		a, b := flights[i], flights[j]
		if a.Airport != b.Airport {
			return a.Airport < b.Airport
		}
		if a.Callsign != b.Callsign {
			return a.Callsign < b.Callsign
		}
		if a.Departure != b.Departure {
			return a.Departure
		}
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.Minute != b.Minute {
			return a.Minute < b.Minute
		}
		if a.Other != b.Other {
			return a.Other < b.Other
		}
		return a.AircraftType < b.AircraftType
	})
}

// Historical flight data is stored one file per cell of a grid of latitude and
// longitude under resources/traffic/flights, named for the cell's southwest
// corner: N40W074.flt, N32W112.flt, and so on. A file holds the departures and
// arrivals at every airport in its cell over a period of past months or years.
// An airport's flights are stored in exactly one file, so a sim reads the files
// covering the airports its scenario flies--usually one--and lets SelectFlights
// drop the rest of the cell.
//
// A file is stored a column at a time rather than a flight at a time: each
// field becomes its own stream and each stream is flate compressed on its own.
//
// The airport the flight is at and the airport at the other end get separate
// streams and separate dictionaries: a cell holds a handful of busy airports
// while their traffic reaches hundreds, so sharing one dictionary would cost an
// extra byte on every index for no gain.
//
// Flights are sorted by airport, then by callsign, then by direction, then by
// time, which leaves long runs of identical airports and aircraft types and
// turns the times of a flight that operates daily into a nearly constant
// day-to-day step.
//
// Times are UTC, split into a day and a minute of the day, both delta encoded
// within each run of a callsign, so a daily flight costs one day step of one
// and whatever its departure time wandered by. Days are counted from the first
// day in the file, which the header records.
//
// The stretches of time the data covers are a property of the import as a
// whole rather than of any one cell--the source has gone down for days at a
// time--so they live in metadata.json alongside the cells rather than being
// repeated in every file.
//
// The streams appear in the file in this order.
const (
	streamAirports = iota // the facility's own airports
	streamAirportIndices
	streamCallsignBases // distinct callsign prefixes: "DAL", "N", ...
	streamCallsignBaseIndices
	streamFlightNumbers     // the rest of the callsign when it is a number
	streamFlightNumberTexts // and when it isn't: "484EM", ...
	streamOtherAirports     // airports at the other end of the flight
	streamOtherAirportIndices
	streamAircraftTypes // distinct aircraft types
	streamAircraftTypeIndices
	streamDirections // departure or arrival
	streamDays       // days from the first one in the header
	streamMinutes    // minute of the UTC day
	numStreams
)

const (
	flightDataMagic = "VFLT"
	// Bumped to 2 when times went from local to the airport to UTC, so that a
	// file written before then is rejected rather than read hours off, to 3 when
	// the header stopped recording the last day and started recording the
	// stretches of time the file has flights for, and to 4 when files went from
	// one per facility to one per grid cell and those stretches moved to
	// metadata.json.
	flightDataVersion = 4

	// flightDataGap is how long the data has to go without a single flight
	// before it is called a gap rather than a lull. Anything much shorter would
	// call every night a gap; the outages this is here to catch run for days.
	flightDataGap = 24 * time.Hour

	// FlightDataDirectory is where historical flight data lives in the resources.
	FlightDataDirectory = "traffic/flights"

	// FlightDataExtension is the suffix of a flight data cell file.
	FlightDataExtension = ".flt"

	// FlightDataMetadataName is the file in FlightDataDirectory holding what the
	// cells have in common.
	FlightDataMetadataName = "metadata.json"

	// flightDataCellDegrees is how much latitude and longitude one cell spans:
	// about 120 nm north to south, and 90 to 110 across the continental US.
	// Making cells smaller costs no space--a flight is stored once however the
	// grid is drawn--but it splits the data over more files for a sim to open.
	flightDataCellDegrees = 2

	// notANumber marks a callsign whose flight number isn't numeric, which is
	// how registration callsigns like N484EM are recorded.
	notANumber = -1

	secondsPerDay = 24 * 60 * 60
)

// dictionary collects the distinct values of a field in the order they first
// appear and gives the index of each one.
type dictionary struct {
	indices map[string]uint64
	values  []string
}

func makeDictionary() *dictionary {
	return &dictionary{indices: make(map[string]uint64)}
}

func (d *dictionary) index(value string) uint64 {
	if index, ok := d.indices[value]; ok {
		return index
	}
	index := uint64(len(d.values))
	d.values = append(d.values, value)
	d.indices[value] = index
	return index
}

func (d *dictionary) encode() []byte { return encodeStrings(d.values) }

// encodeStrings writes the number of values followed by the values themselves,
// newline separated. These streams hold callsign parts, airports, and aircraft
// types, all of which are letters and digits, so a newline can't appear inside
// one. The count is what tells an empty stream apart from one holding a single
// empty value.
func encodeStrings(values []string) []byte {
	var scratch [binary.MaxVarintLen64]byte
	out := scratch[:binary.PutUvarint(scratch[:], uint64(len(values)))]
	return append(out, strings.Join(values, "\n")...)
}

func decodeStrings(data []byte) ([]string, error) {
	r := bytes.NewReader(data)
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, fmt.Errorf("read string count: %w", err)
	}
	if n == 0 {
		return nil, nil
	}

	values := strings.Split(string(data[len(data)-r.Len():]), "\n")
	if uint64(len(values)) != n {
		return nil, fmt.Errorf("stream holds %d strings, expected %d", len(values), n)
	}
	return values, nil
}

func appendUvarints(values []uint64) []byte {
	buf := make([]byte, 0, len(values))
	var scratch [binary.MaxVarintLen64]byte
	for _, v := range values {
		buf = append(buf, scratch[:binary.PutUvarint(scratch[:], v)]...)
	}
	return buf
}

func appendVarints(values []int64) []byte {
	buf := make([]byte, 0, len(values))
	var scratch [binary.MaxVarintLen64]byte
	for _, v := range values {
		buf = append(buf, scratch[:binary.PutVarint(scratch[:], v)]...)
	}
	return buf
}

func readUvarints(data []byte, n int) ([]uint64, error) {
	values := make([]uint64, n)
	r := bytes.NewReader(data)
	for i := range values {
		v, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	return values, nil
}

func readVarints(data []byte, n int) ([]int64, error) {
	values := make([]int64, n)
	r := bytes.NewReader(data)
	for i := range values {
		v, err := binary.ReadVarint(r)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	return values, nil
}

// runBoundaries marks where each new run of flights sharing an airport,
// callsign, and direction starts. Times are delta encoded within a run.
func runBoundaries(starts []bool) []int {
	var runs []int
	for _, start := range starts {
		if start || len(runs) == 0 {
			runs = append(runs, 1)
		} else {
			runs[len(runs)-1]++
		}
	}
	return runs
}

// deltaEncodeRuns delta encodes each run separately, so that the first flight
// of each callsign is stored as it is and the ones that follow are stored as
// the change from the flight before.
func deltaEncodeRuns(values []int64, runs []int) []int64 {
	encoded := make([]int64, 0, len(values))
	start := 0
	for _, n := range runs {
		encoded = append(encoded, util.DeltaEncode(values[start:start+n])...)
		start += n
	}
	return encoded
}

func deltaDecodeRuns(encoded []int64, runs []int) []int64 {
	values := make([]int64, 0, len(encoded))
	start := 0
	for _, n := range runs {
		values = append(values, util.DeltaDecode(encoded[start:start+n])...)
		start += n
	}
	return values
}

// EncodeFlights encodes a facility's flight data. Flights must be in the order
// SortFlights puts them in.
func EncodeFlights(flights []Flight) ([]byte, error) {
	ownAirports, bases := makeDictionary(), makeDictionary()
	airports, aircraftTypes := makeDictionary(), makeDictionary()
	ownAirportIndices := make([]uint64, len(flights))
	baseIndices := make([]uint64, len(flights))
	airportIndices := make([]uint64, len(flights))
	aircraftTypeIndices := make([]uint64, len(flights))
	numbers := make([]int64, len(flights))
	days := make([]int64, len(flights))
	minutes := make([]int64, len(flights))
	directions := make([]byte, len(flights))
	runStarts := make([]bool, len(flights))
	var numberTexts []string

	// Days are stored relative to the first one in the file so that they stay
	// small however far from the epoch the data is.
	var firstDay uint16
	for i, f := range flights {
		if i == 0 || f.Day < firstDay {
			firstDay = f.Day
		}
	}

	for i, f := range flights {
		base, number := SplitCallsign(f.Callsign)
		baseIndices[i] = bases.index(base)
		// Only store the flight number as a number when that reproduces it
		// exactly; "0123" would otherwise come back as "123".
		if value, err := strconv.ParseInt(number, 10, 32); err == nil &&
			strconv.FormatInt(value, 10) == number {
			numbers[i] = value
		} else {
			numbers[i] = notANumber
			numberTexts = append(numberTexts, number)
		}

		ownAirportIndices[i] = ownAirports.index(f.Airport)
		airportIndices[i] = airports.index(f.Other)
		aircraftTypeIndices[i] = aircraftTypes.index(f.AircraftType)
		days[i] = int64(f.Day - firstDay)
		minutes[i] = int64(f.Minute)
		if f.Departure {
			directions[i] = 1
		}
		runStarts[i] = i == 0 || f.Airport != flights[i-1].Airport ||
			f.Callsign != flights[i-1].Callsign || f.Departure != flights[i-1].Departure
	}
	runs := runBoundaries(runStarts)

	streams := make([][]byte, numStreams)
	streams[streamAirports] = ownAirports.encode()
	streams[streamAirportIndices] = appendUvarints(ownAirportIndices)
	streams[streamCallsignBases] = bases.encode()
	streams[streamCallsignBaseIndices] = appendUvarints(baseIndices)
	streams[streamFlightNumbers] = appendVarints(numbers)
	streams[streamFlightNumberTexts] = encodeStrings(numberTexts)
	streams[streamOtherAirports] = airports.encode()
	streams[streamOtherAirportIndices] = appendUvarints(airportIndices)
	streams[streamAircraftTypes] = aircraftTypes.encode()
	streams[streamAircraftTypeIndices] = appendUvarints(aircraftTypeIndices)
	streams[streamDirections] = directions
	streams[streamDays] = appendVarints(deltaEncodeRuns(days, runs))
	streams[streamMinutes] = appendVarints(deltaEncodeRuns(minutes, runs))

	return writeStreams(firstDay, len(flights), streams)
}

// writeStreams compresses each stream and assembles the file: the magic and
// version, the first day, the number of flights, the compressed length of each
// stream, and then the streams themselves.
func writeStreams(firstDay uint16, count int, streams [][]byte) ([]byte, error) {
	compressed := make([][]byte, len(streams))
	for i, stream := range streams {
		var buf bytes.Buffer
		w, err := flate.NewWriter(&buf, flate.BestCompression)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(stream); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		compressed[i] = buf.Bytes()
	}

	var out bytes.Buffer
	out.WriteString(flightDataMagic)
	out.WriteByte(flightDataVersion)
	var scratch [binary.MaxVarintLen64]byte
	putUvarint := func(v uint64) { out.Write(scratch[:binary.PutUvarint(scratch[:], v)]) }
	putUvarint(uint64(firstDay))
	putUvarint(uint64(count))

	for _, stream := range compressed {
		putUvarint(uint64(len(stream)))
	}
	for _, stream := range compressed {
		out.Write(stream)
	}
	return out.Bytes(), nil
}

// flightDataHeader is everything before the compressed streams.
type flightDataHeader struct {
	firstDay  uint16
	count     int
	lengths   []uint64
	streamsAt int // offset of the first compressed stream
}

func readFlightDataHeader(data []byte) (flightDataHeader, error) {
	var h flightDataHeader
	if len(data) < len(flightDataMagic)+1 || string(data[:len(flightDataMagic)]) != flightDataMagic {
		return h, fmt.Errorf("not a flight data file")
	}
	if version := data[len(flightDataMagic)]; version != flightDataVersion {
		return h, fmt.Errorf("flight data version %d, expected %d", version, flightDataVersion)
	}

	r := bytes.NewReader(data[len(flightDataMagic)+1:])
	first, err := binary.ReadUvarint(r)
	if err != nil {
		return h, err
	}
	count, err := binary.ReadUvarint(r)
	if err != nil {
		return h, err
	}

	h.firstDay, h.count = uint16(first), int(count)

	h.lengths = make([]uint64, numStreams)
	for i := range h.lengths {
		if h.lengths[i], err = binary.ReadUvarint(r); err != nil {
			return h, err
		}
	}
	h.streamsAt = len(data) - r.Len()
	return h, nil
}

func readStreams(data []byte) (flightDataHeader, [][]byte, error) {
	h, err := readFlightDataHeader(data)
	if err != nil {
		return h, nil, err
	}

	r := bytes.NewReader(data[h.streamsAt:])
	streams := make([][]byte, numStreams)
	for i, length := range h.lengths {
		compressed := make([]byte, length)
		if _, err := io.ReadFull(r, compressed); err != nil {
			return h, nil, err
		}
		if streams[i], err = io.ReadAll(flate.NewReader(bytes.NewReader(compressed))); err != nil {
			return h, nil, err
		}
	}
	return h, streams, nil
}

// DecodeFlights reads back what EncodeFlights wrote.
func DecodeFlights(data []byte) ([]Flight, error) {
	h, streams, err := readStreams(data)
	if err != nil {
		return nil, err
	}
	if h.count == 0 {
		return nil, nil
	}
	if len(streams[streamDirections]) != h.count {
		return nil, fmt.Errorf("flight data has %d flights but %d directions", h.count,
			len(streams[streamDirections]))
	}

	ownAirports, err := decodeStrings(streams[streamAirports])
	if err != nil {
		return nil, err
	}
	bases, err := decodeStrings(streams[streamCallsignBases])
	if err != nil {
		return nil, err
	}
	airports, err := decodeStrings(streams[streamOtherAirports])
	if err != nil {
		return nil, err
	}
	aircraftTypes, err := decodeStrings(streams[streamAircraftTypes])
	if err != nil {
		return nil, err
	}

	ownAirportIndices, err := readUvarints(streams[streamAirportIndices], h.count)
	if err != nil {
		return nil, err
	}
	baseIndices, err := readUvarints(streams[streamCallsignBaseIndices], h.count)
	if err != nil {
		return nil, err
	}
	airportIndices, err := readUvarints(streams[streamOtherAirportIndices], h.count)
	if err != nil {
		return nil, err
	}
	aircraftTypeIndices, err := readUvarints(streams[streamAircraftTypeIndices], h.count)
	if err != nil {
		return nil, err
	}
	numbers, err := readVarints(streams[streamFlightNumbers], h.count)
	if err != nil {
		return nil, err
	}
	numberTexts, err := decodeStrings(streams[streamFlightNumberTexts])
	if err != nil {
		return nil, err
	}

	// Rebuild the callsigns first: the run of flights each one covers is what
	// the times were delta encoded against.
	callsigns := make([]string, h.count)
	text := 0
	for i := range callsigns {
		if int(baseIndices[i]) >= len(bases) {
			return nil, fmt.Errorf("callsign base index %d out of range", baseIndices[i])
		}
		number := ""
		if numbers[i] == notANumber {
			if text >= len(numberTexts) {
				return nil, fmt.Errorf("ran out of flight number text")
			}
			number = numberTexts[text]
			text++
		} else {
			number = strconv.FormatInt(numbers[i], 10)
		}
		callsigns[i] = bases[baseIndices[i]] + number
	}

	directions := streams[streamDirections]
	runStarts := make([]bool, h.count)
	for i := range runStarts {
		runStarts[i] = i == 0 || ownAirportIndices[i] != ownAirportIndices[i-1] ||
			callsigns[i] != callsigns[i-1] || directions[i] != directions[i-1]
	}
	runs := runBoundaries(runStarts)

	encodedDays, err := readVarints(streams[streamDays], h.count)
	if err != nil {
		return nil, err
	}
	encodedMinutes, err := readVarints(streams[streamMinutes], h.count)
	if err != nil {
		return nil, err
	}
	days := deltaDecodeRuns(encodedDays, runs)
	minutes := deltaDecodeRuns(encodedMinutes, runs)

	flights := make([]Flight, h.count)
	for i := range flights {
		if int(airportIndices[i]) >= len(airports) ||
			int(aircraftTypeIndices[i]) >= len(aircraftTypes) ||
			int(ownAirportIndices[i]) >= len(ownAirports) {
			return nil, fmt.Errorf("dictionary index out of range")
		}
		flights[i] = Flight{
			Airport:      ownAirports[ownAirportIndices[i]],
			Callsign:     callsigns[i],
			Other:        airports[airportIndices[i]],
			AircraftType: aircraftTypes[aircraftTypeIndices[i]],
			Day:          h.firstDay + uint16(days[i]),
			Minute:       int(minutes[i]),
			Departure:    directions[i] == 1,
		}
	}
	return flights, nil
}

// FlightDataCell names the flight data file an airport's flights are stored in:
// the cell of latitude and longitude holding it, named for the cell's southwest
// corner. Import and runtime both go through here, so that they always agree on
// where an airport's flights are.
func FlightDataCell(p math.Point2LL) string {
	lat := floorDegrees(p.Latitude())
	long := floorDegrees(p.Longitude())

	hemisphere := func(v int, positive, negative byte) (byte, int) {
		if v < 0 {
			return negative, -v
		}
		return positive, v
	}
	ns, latitude := hemisphere(lat, 'N', 'S')
	ew, longitude := hemisphere(long, 'E', 'W')
	return fmt.Sprintf("%c%02d%c%03d", ns, latitude, ew, longitude)
}

// floorDegrees rounds a coordinate down to the start of the cell holding it.
func floorDegrees(v float32) int {
	d := int(v) / flightDataCellDegrees * flightDataCellDegrees
	if v < 0 && float32(d) != v {
		d -= flightDataCellDegrees
	}
	return d
}

// FlightDataCells returns the cells holding the given airports' flights, with
// no repeats. Airports the database doesn't know are left out: there can be no
// flight data for an airport with no position.
func FlightDataCells(airports ...map[string]bool) []string {
	var cells []string
	for _, set := range airports {
		for icao := range set {
			ap, ok := DB.Airports[icao]
			if !ok {
				continue
			}
			if cell := FlightDataCell(ap.Location); !slices.Contains(cells, cell) {
				cells = append(cells, cell)
			}
		}
	}
	slices.Sort(cells)
	return cells
}

// FlightDataPath is where a cell's flight data lives in the resources.
func FlightDataPath(cell string) string {
	return path.Join(FlightDataDirectory, cell+FlightDataExtension)
}

// ReadFlightData returns a cell's flight data file, or nil if there is none for
// it. Most cells have no flights at all, but a file that is there and can't be
// read is an error like any other.
func ReadFlightData(resources fs.FS, cell string) ([]byte, error) {
	data, err := fs.ReadFile(resources, FlightDataPath(cell))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// ReadFlightDataCells decodes the flight data for each of the given cells and
// returns all of it together. Cells with no data contribute nothing.
func ReadFlightDataCells(resources fs.FS, cells []string) ([]Flight, error) {
	var flights []Flight
	for _, cell := range cells {
		data, err := ReadFlightData(resources, cell)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cell, err)
		}
		if data == nil {
			continue
		}
		cellFlights, err := DecodeFlights(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cell, err)
		}
		flights = append(flights, cellFlights...)
	}
	return flights, nil
}

// FlightDataIntervals returns the stretches of time the flight data covers; it
// has a gap wherever the list does. They come from metadata.json rather than
// from the cells, since the source data goes down for everywhere at once. The
// result is empty when there is no flight data at all.
func FlightDataIntervals(resources fs.FS) ([]util.TimeInterval, error) {
	data, err := fs.ReadFile(resources, path.Join(FlightDataDirectory, FlightDataMetadataName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var metadata flightDataMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("%s: %w", FlightDataMetadataName, err)
	}
	return metadata.Intervals, nil
}

// flightDataMetadata is what metadata.json holds: everything about an import
// that isn't particular to one cell.
type flightDataMetadata struct {
	Intervals []util.TimeInterval `json:"intervals"`
}

// EncodeFlightDataMetadata renders the contents of metadata.json.
func EncodeFlightDataMetadata(intervals []util.TimeInterval) ([]byte, error) {
	return json.MarshalIndent(flightDataMetadata{Intervals: intervals}, "", "  ")
}

// FlightIntervals returns the stretches of time a set of flights covers,
// splitting wherever they go a full day without one.
func FlightIntervals(flights []Flight) []util.TimeInterval {
	times := util.MapSlice(flights, Flight.Time)
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	return util.FindTimeIntervals(times, flightDataGap)
}

// MergeFlightIntervals combines the intervals of several sets of flights into
// the intervals of all of them together, joining any two that are less than a
// full day apart. Computing the intervals a cell at a time and merging here
// gives the same answer as gathering every flight's time at once, without
// holding tens of millions of them in memory to sort.
func MergeFlightIntervals(intervals []util.TimeInterval) []util.TimeInterval {
	if len(intervals) == 0 {
		return nil
	}

	sorted := slices.Clone(intervals)
	slices.SortFunc(sorted, func(a, b util.TimeInterval) int { return a.Start().Compare(b.Start()) })

	merged := sorted[:1]
	for _, iv := range sorted[1:] {
		last := &merged[len(merged)-1]
		if iv.Start().Sub(last.End()) > flightDataGap {
			merged = append(merged, iv)
		} else if iv.End().After(last.End()) {
			last[1] = iv.End()
		}
	}
	return merged
}

// SelectFlights returns the flights at the given airports that take off or
// touch down between start and end, in time order. This is what a sim is handed
// when it launches: the flights it needs and nothing else, out of everything
// its cells hold. Flights whose callsign prefix isn't in airlines (pass
// DB.Airlines) are left out; the sim can't voice a callsign it has no telephony
// for.
//
// Decoding a cell costs enough that anything asking about several windows of
// it--the New Sim dialog previews a fresh one each time the start time
// moves--wants to do it once and select from the result.
func SelectFlights(flights []Flight, departureAirports, arrivalAirports map[string]bool,
	airlines map[string]Airline, start, end time.Time) []Flight {
	// The window can only hold flights on the days it touches.
	firstDay := FlightDataDayNumber(start)
	lastDay := FlightDataDayNumber(end)

	var window []Flight
	for _, f := range flights {
		// A scenario often departs airports it doesn't land, so filter by
		// what the flight does there, not just where.
		if f.Departure {
			if !departureAirports[f.Airport] {
				continue
			}
		} else if !arrivalAirports[f.Airport] {
			continue
		}
		base, _ := SplitCallsign(f.Callsign)
		if _, ok := airlines[base]; !ok {
			continue
		}
		if f.Day < firstDay || f.Day > lastDay {
			continue
		}
		if t := f.Time(); !t.Before(start) && t.Before(end) {
			window = append(window, f)
		}
	}

	slices.SortFunc(window, func(a, b Flight) int {
		if c := a.Time().Compare(b.Time()); c != 0 {
			return c
		}
		return strings.Compare(a.Callsign, b.Callsign)
	})
	return window
}
