package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"golang.org/x/sync/errgroup"
)

// NOAA high-resolution rapid refresh: https://rapidrefresh.noaa.gov/hrrr/
func ingestHRRR(sb StorageBackend) error {
	tmp := os.Getenv("WXINGEST_TMP")
	if tmp == "" {
		return errors.New("Must set WXINGEST_TMP environment variable for HRRR")
	}
	_ = os.RemoveAll(tmp)
	if err := os.Mkdir(tmp, 0755); err != nil {
		return err
	}
	if err := os.Chdir(tmp); err != nil {
		return err
	}

	hrrrsb, err := MakeGCSBackend("high-resolution-rapid-refresh")
	if err != nil {
		return err
	}

	existing := listIngestedAtmos(sb)

	tfr := util.MakeTempFileRegistry(nil)
	defer tfr.RemoveAll()

	tCh := make(chan time.Time)
	var eg errgroup.Group
	eg.Go(func() error {
		// Roughly when the scrape started; ingest will run for this time and
		// it will be incremented by an hour at a time until it is a few hours
		// before the current time.
		fetchTime := time.Date(2025, 7, 28, 0, 0, 0, 0, time.UTC)

		doTime := func(t time.Time) bool {
			// Days that for various reasons the scraper didn't get a full
			// days' information; don't spend the time processing HRRRs for
			// those days since it'll be wasted effort.
			skipDays := []time.Time{
				time.Date(2025, 7, 30, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 8, 11, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 8, 12, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 8, 13, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 8, 14, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 9, 3, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 9, 4, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 9, 5, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 9, 6, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 9, 7, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 9, 8, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 9, 9, 0, 0, 0, 0, time.UTC),
			}
			for _, skip := range skipDays {
				if t.Year() == skip.Year() && t.Month() == skip.Month() && t.Day() == skip.Day() {
					return false
				}
			}

			tracons := existing[fetchTime] // may be empty
			slices.Sort(tracons)
			return !slices.Equal(tracons, wx.AtmosTRACONs)
		}

		// Stop once we get close to the current time.
		for time.Since(fetchTime) > 3*time.Hour {
			if doTime(fetchTime) {
				tCh <- fetchTime
				if *hrrrQuick {
					break
				}
			}
			fetchTime = fetchTime.Add(time.Hour)
		}
		close(tCh)
		return nil
	})

	type downloadedHRRR struct {
		path string
		t    time.Time
	}
	const nTimeWorkers = 2
	hrrrCh := make(chan downloadedHRRR, nTimeWorkers)
	eg.Go(func() error {
		// Download HRRR files in a goroutine so that we can start
		// downloading the next one after the one currently being
		// processed.
		defer close(hrrrCh)
		for t := range tCh {
			path, err := downloadHRRRForTime(t, tfr, hrrrsb)
			if err != nil {
				return err
			}
			hrrrCh <- downloadedHRRR{path: path, t: t}
		}
		return nil
	})

	// Do two times at once to keep the CPU busy and avoid low
	// utilization at the end when just a few TRACONs are left.
	for range nTimeWorkers {
		eg.Go(func() error {
			for hrrr := range hrrrCh {
				LogInfo("Starting work on " + hrrr.t.Format(time.RFC3339))
				if err := ingestHRRRForTime(hrrr.path, hrrr.t, existing[hrrr.t], tfr, sb, hrrrsb); err != nil {
					return err
				}
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	return generateManifests(sb, "atmos", slices.Values(wx.AtmosTRACONs))

}

func listIngestedAtmos(sb StorageBackend) map[time.Time][]string {
	ingested := make(map[time.Time][]string) // which TRACONs have the data for the time

	if *hrrrQuick {
		// start at the beginning
		return nil
	}

	// List all objects under atmos/ in one call
	atmosPaths, err := sb.List("atmos/")
	if err != nil {
		LogError("Failed to list atmos/ directory: %v", err)
		return ingested
	}

	// Parse all paths in a single pass
	for path := range atmosPaths {
		// Parse paths like atmos/BOI/2025-07-27T18:00:00Z.msgpack.zst
		parts := strings.Split(strings.TrimPrefix(path, "atmos/"), "/")
		if len(parts) != 2 {
			LogError("%s: malformed path", path)
			continue
		}
		if parts[1] == "manifest.msgpack.zst" {
			continue
		}

		tracon := parts[0]

		tm, err := time.Parse(time.RFC3339, strings.TrimSuffix(parts[1], ".msgpack.zst"))
		if err != nil {
			LogError("%s", err)
			continue
		}

		ingested[tm] = append(ingested[tm], tracon)
	}

	LogInfo("Found %d ingested atmos TRACON objects for %d times", len(atmosPaths), len(ingested))

	return ingested
}

func downloadHRRRForTime(t time.Time, tfr *util.TempFileRegistry, hrrrsb StorageBackend) (string, error) {
	// Download the grib2 file from the NOAA archive
	hrrrpath := fmt.Sprintf("hrrr.%d%02d%02d/conus/hrrr.t%02dz.wrfprsf00.grib2", t.Year(), t.Month(), t.Day(), t.Hour())
	hrrrr, err := hrrrsb.OpenRead(hrrrpath)
	if err != nil {
		return "", err
	}
	defer hrrrr.Close()

	hf, err := os.Create(fmt.Sprintf("%s.grib2", t.Format(time.RFC3339)))
	if err != nil {
		return "", err
	}
	tfr.RegisterPath(hf.Name())

	LogInfo("%s: downloading", hrrrpath)

	n, err := io.Copy(hf, hrrrr)
	if err != nil {
		hf.Close()
		return "", err
	}

	if err := hf.Close(); err != nil {
		return "", err
	}

	LogInfo("%s: downloaded %s to %s", hrrrpath, util.ByteCount(n), hf.Name())
	return hf.Name(), nil
}

func ingestHRRRForTime(gribPath string, t time.Time, existingTRACONs []string, tfr *util.TempFileRegistry,
	sb, hrrrsb StorageBackend) error {
	defer tfr.RemoveAllPrefix(t.Format(time.RFC3339))

	var eg errgroup.Group
	var totalUploads, totalUploadBytes int64
	sem := make(chan struct{}, *nWorkers)
	for _, tracon := range wx.AtmosTRACONs {
		if !slices.Contains(existingTRACONs, tracon) {
			eg.Go(func() error {
				sem <- struct{}{}
				defer func() { <-sem }()

				n, err := ingestHRRRForTracon(gribPath, tracon, tfr, t, sb)
				if err == nil {
					LogInfo("Uploaded %s for %s-%s", util.ByteCount(n), tracon, t.Format(time.RFC3339))
					atomic.AddInt64(&totalUploads, 1)
					atomic.AddInt64(&totalUploadBytes, n)
				}

				return err
			})
		}
	}

	return eg.Wait()
}

func ingestHRRRForTracon(gribPath string, tracon string, tfr *util.TempFileRegistry, t time.Time, sb StorageBackend) (int64, error) {
	pathPrefix := tracon + "-" + t.Format(time.RFC3339)
	defer tfr.RemoveAllPrefix(pathPrefix)

	f, err := gribToCSV(gribPath, tracon, pathPrefix, tfr)
	if err != nil {
		return 0, err
	}

	sf, err := sampleFieldFromCSV(tracon, f)
	if err != nil {
		return 0, err
	}

	return uploadWeatherAtmos(sf, tracon, t, sb)
}

func gribToCSV(gribPath, tracon, pathPrefix string, tfr *util.TempFileRegistry) (*os.File, error) {
	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for TRACON", tracon)
	}
	center, radius := tspec.Center(), tspec.Radius

	bbox := math.BoundLatLongCircle(center, radius)

	smallGribPath := pathPrefix + ".grib2"
	cmd := exec.Command("wgrib2", gribPath, "-small_grib", fmt.Sprintf("%f:%f", bbox.P0[0], bbox.P1[0]), /* longitude */
		fmt.Sprintf("%f:%f", bbox.P0[1], bbox.P1[1]) /* latitude */, smallGribPath)
	tfr.RegisterPath(smallGribPath)

	//LogInfo("Running " + cmd.String())
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	cf, err := os.Create(pathPrefix + ".csv")
	if err != nil {
		return nil, err
	}
	tfr.RegisterPath(cf.Name())

	cmd = exec.Command("wgrib2", smallGribPath, "-match", ":(UGRD|VGRD|TMP|HGT):", "-csv", cf.Name())
	//LogInfo("Running " + cmd.String())
	if err := cmd.Run(); err != nil {
		cf.Close()
		return nil, err
	}

	if err := cf.Sync(); err != nil {
		cf.Close()
		return nil, err
	}

	if _, err := cf.Seek(0, 0); err != nil {
		return nil, err
	}

	return cf, nil
}

func sampleFieldFromCSV(tracon string, f *os.File) (*wx.Atmos, error) {
	eg, ctx := errgroup.WithContext(context.Background())

	// Read chunks of the file asynchronously and with double-buffering so
	// that reads continue as processing is being performed.
	freeBufCh := make(chan []byte, 1)
	eg.Go(func() error {
		freeBufCh <- make([]byte, 16*1024*1024)
		freeBufCh <- make([]byte, 16*1024*1024)
		return nil
	})

	readBufCh := make(chan []byte, 1)
	eg.Go(func() error { return readCSV(ctx, f, freeBufCh, readBufCh) })

	sf, err := parseWindCSV(ctx, tracon, f.Name(), readBufCh, freeBufCh)
	if err != nil {
		return nil, err
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return sf, nil
}

func readCSV(ctx context.Context, f *os.File, freeBufCh <-chan []byte, readBufCh chan<- []byte) error {
	for {
		buf := <-freeBufCh
		n, err := f.Read(buf)
		if n == 0 && err == io.EOF {
			close(readBufCh) // no more coming
			return nil
		} else if err != nil && err != io.EOF {
			return err
		} else {
			select {
			case readBufCh <- buf[:n]:
			case <-ctx.Done():
				close(readBufCh)
				return nil
			}
		}
	}
}

func commaSplit(s []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		for {
			if idx := bytes.IndexByte(s, ','); idx != -1 {
				if !yield(s[:idx]) { // don't include the comma
					return
				}
				s = s[idx+1:]
			} else {
				yield(s)
				return
			}
		}
	}
}

var div10 = []float64{
	1,
	10,
	100,
	1000,
	10000,
	100000,
	1000000,
	10000000,
	100000000,
	1000000000,
	10000000000,
	100000000000,
	1000000000000,
}

func atof(s []byte) float32 {
	var neg, decimal bool
	var ndec int
	var digits int64
	for _, ch := range s {
		switch {
		case ch == '-':
			neg = true
		case ch == '.':
			decimal = true
		case ch >= '0' && ch <= '9':
			digits *= 10
			digits += int64(ch - '0')
			if decimal {
				ndec++
			}
		case ch == 'e':
			// Doh--scientific notation. punt
			v, err := strconv.ParseFloat(string(s), 32)
			if err != nil {
				panic(err)
			}
			return float32(v)
		default:
			panic("bad string passed to atof: " + string(s))
		}
	}
	vf := float64(digits)
	if neg {
		vf = -vf
	}
	if ndec > 0 {
		vf /= div10[ndec]
	}

	return float32(vf)
}

const (
	LineItemUnsetType = iota
	LineItemUDirection
	LineItemVDirection
	LineItemTemperature
	LineItemHeight
)

type LineItem struct {
	Lat, Long, Value float32
	Type             int
	Level            int
}

func parseWindCSV(ctx context.Context, tracon, filename string, readBufCh <-chan []byte, freeBufCh chan<- []byte) (*wx.Atmos, error) {
	bp := 0 // buf pos
	var buf []byte

	var getline func() []byte
	getline = func() []byte {
		if idx := bytes.IndexByte(buf[bp:], '\n'); idx != -1 {
			line := buf[bp : bp+idx] // no \n
			bp += idx + 1            // skip \n
			return line
		}

		// reached the end without finding a newline
		var accum []byte
		if buf != nil {
			accum = append(accum, buf[bp:]...) // copy!
			freeBufCh <- buf
		}

		var ok bool
		select {
		case buf, ok = <-readBufCh:
			break
		case <-ctx.Done():
			return nil
		}

		bp = 0
		if !ok { // EOF
			return accum
		}
		return append(accum, getline()...)
	}

	var arena []wx.SampleStack
	allocStack := func() *wx.SampleStack {
		if len(arena) == 0 {
			arena = make([]wx.SampleStack, 1024)
		}
		s := &arena[0]
		arena = arena[1:]
		return s
	}

	at := wx.MakeAtmos()

	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for TRACON", tracon)
	}
	center, radius := tspec.Center(), tspec.Radius
	nmPerLongitude := math.NMPerLongitudeAt(center)

	start := time.Now()
	n, nbytes := 0, 0
	for {
		line := getline()
		if len(line) == 0 {
			elapsed := time.Since(start).Seconds()
			LogInfo("%s: processed %d lines of HRRR CSV (%.2f M / sec, %.2f MB/s)", filename, n,
				float64(n)/elapsed/(1024*1024), float64(nbytes)/elapsed/(1024*1024))

			return &at, nil
		}
		if n%1000000 == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				break
			}
		}

		n++
		nbytes += len(line)
		if item, err := parseHRRRLine(line); err != nil {
			return nil, err
		} else if item.Type != LineItemUnsetType {
			pt := math.Point2LL{item.Long, item.Lat}
			if d := math.NMDistance2LLFast(center, pt, nmPerLongitude); d > radius {
				// Skip this point
				continue
			}

			stack, ok := at.SampleStacks[pt]
			if !ok {
				stack = allocStack()
				at.SampleStacks[pt] = stack
			}

			switch item.Type {
			case LineItemUDirection:
				stack.Levels[item.Level].UComponent = item.Value
			case LineItemVDirection:
				stack.Levels[item.Level].VComponent = item.Value
			case LineItemTemperature:
				stack.Levels[item.Level].Temperature = item.Value
			case LineItemHeight:
				stack.Levels[item.Level].Height = item.Value
			}
		}
	}
}

func parseHRRRLine(line []byte) (LineItem, error) {
	var li LineItem
	// "2025-08-06 03:00:00","2025-08-06 03:00:00","HGT","50 mb",-122.72,21.1381,20804.8
	if line[43] != ',' {
		return LineItem{}, fmt.Errorf("Found %q at 43 in %q", string(line[43]), line)
	}
	line = line[44:]

	var record [5][]byte
	i := 0
	for r := range commaSplit(line) { // strings.SplitSeq(line, ",") {
		if r[0] == '"' {
			r = r[1 : len(r)-1]
		}
		record[i] = r
		i++
	}
	if i != len(record) {
		return LineItem{}, fmt.Errorf("Didn't find 5 records in line %q. OUT OF SPACE?", string(line))
	}

	level := wx.LevelIndexFromId(record[1])
	if level == -1 {
		// not a level we care about (cloud tops, etc.)
		return LineItem{}, nil
	}
	li.Level = level

	if bytes.Equal(record[0], []byte("UGRD")) {
		li.Type = LineItemUDirection
	} else if bytes.Equal(record[0], []byte("VGRD")) {
		li.Type = LineItemVDirection
	} else if bytes.Equal(record[0], []byte("TMP")) {
		li.Type = LineItemTemperature
	} else if bytes.Equal(record[0], []byte("HGT")) {
		li.Type = LineItemHeight
	}

	li.Lat = atof(record[3])
	li.Long = atof(record[2])
	li.Value = atof(record[4])

	return li, nil
}

func uploadWeatherAtmos(at *wx.Atmos, tracon string, t time.Time, st StorageBackend) (int64, error) {
	soa, err := at.ToSOA()
	if err != nil {
		return 0, err
	}
	if err := wx.CheckAtmosConversion(*at, soa); err != nil {
		return 0, err
	}

	path := fmt.Sprintf("atmos/%s/%s.msgpack.zst", tracon, t.Format(time.RFC3339))

	if *hrrrQuick {
		// skip upload
		var drb DryRunBackend
		return drb.StoreObject(path, soa)
	}

	return st.StoreObject(path, soa)
}
