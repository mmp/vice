package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type WindLevel struct {
	MB          float32
	UComponent  float32 //eastward
	VComponent  float32 // northward
	Temperature float32 // Kelvin, evidently
	Height      float32 // geopotential height
}

type WindSample struct {
	Levels []WindLevel
}

type WindCell map[[2]float32]WindSample

type WindGrid struct {
	Time     time.Time
	Cells    map[[2]int]WindCell
	HRRRPath string
}

type SOAWindCell struct {
	Lat    []float32
	Long   []float32
	Levels []SOAWindLevelCell
}

const HeightOffset = 500

type SOAWindLevelCell struct {
	MB float32
	// All of the following are delta encoded
	Heading     []uint8 // degrees/2
	Speed       []uint8 // knots
	Temperature []int8  // Temperature in Celsius
	Height      []uint8 // geopotential height + HeightOffset in 100s of feet
}

// Track temporary CSV files for cleanup
var (
	tempFilesMu sync.Mutex
	tempFiles   = make(map[string]struct{})
)

func registerTempFile(path string) {
	tempFilesMu.Lock()
	tempFiles[path] = struct{}{}
	tempFilesMu.Unlock()
}

func deleteTempFile(path string) {
	os.Remove(path)

	tempFilesMu.Lock()
	delete(tempFiles, path)
	tempFilesMu.Unlock()
}

func cleanupTempFiles() {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	for path := range tempFiles {
		if err := os.Remove(path); err != nil {
			// Best effort - file might already be deleted
			LogError("Failed to remove temp file %s: %v", path, err)
		} else {
			LogInfo("Cleaned up temp file: %s", path)
		}
	}
	tempFiles = make(map[string]struct{})
}

func UVToDirSpeed(u, v float32) (float32, float32) {
	dir := 270 - math.Degrees(math.Atan2(v, u))
	dir = math.NormalizeHeading(dir)

	spd := math.Sqrt(u*u+v*v) * 1.94384 // m/s-> knots

	return float32(dir), float32(spd)
}

func DirSpeedToUV(dir, speed float32) (float32, float32) {
	s := speed * 0.51444 // knots -> m/s
	d := math.Radians(dir)
	return -s * float32(math.Sin(d)), -s * float32(math.Cos(d))
}

func WindCellToSOA(c WindCell) (SOAWindCell, error) {
	soa := SOAWindCell{}

	keys := slices.Collect(maps.Keys(c))
	slices.SortFunc(keys, func(a, b [2]float32) int {
		if a[0] < b[0] {
			return -1
		} else if a[0] > b[0] {
			return 1
		} else if a[1] < b[1] {
			return -1
		} else if a[1] > b[1] {
			return 1
		}
		return 0
	})

	for _, k := range keys {
		// TODO: check ordering of latlong
		soa.Long = append(soa.Long, k[0])
		soa.Lat = append(soa.Lat, k[1])

		sample := c[k]
		if len(soa.Levels) == 0 {
			soa.Levels = make([]SOAWindLevelCell, len(sample.Levels))
			//levelTempEnc = make([]DeltaEncoder, len(sample.Levels))
		} else if len(sample.Levels) != len(soa.Levels) {
			return SOAWindCell{}, fmt.Errorf("non-uniform number of levels in different entries")
		}

		for i, level := range sample.Levels {
			if soa.Levels[i].MB == 0 {
				soa.Levels[i].MB = level.MB
			} else if soa.Levels[i].MB != level.MB {
				return SOAWindCell{}, fmt.Errorf("different MB layout in different cells")
			}

			hdg, spd := UVToDirSpeed(level.UComponent, level.VComponent)
			if hdg < 0 || hdg > 360 {
				return SOAWindCell{}, fmt.Errorf("bad heading: %f not in 0-360", hdg)
			}
			if spd < 0 || spd > 255 {
				return SOAWindCell{}, fmt.Errorf("bad speed: %f not in 0-255", spd)
			}
			soa.Levels[i].Heading = append(soa.Levels[i].Heading, uint8(math.Round(hdg+1)/2))
			soa.Levels[i].Speed = append(soa.Levels[i].Speed, uint8(math.Round(spd)))

			tc := level.Temperature - 273.15 // K -> C
			tq := int(math.Round(tc))
			if tq < -128 || tq > 127 {
				return SOAWindCell{}, fmt.Errorf("bad temperature: %d not in -128-127", tq)
			}
			soa.Levels[i].Temperature = append(soa.Levels[i].Temperature, int8(tq))

			h := level.Height + HeightOffset // deal with slightly below sea level
			h = (h + 50) / 100               // 100s of feet
			if h < 0 || h > 255 {
				return SOAWindCell{}, fmt.Errorf("bad remapped height: %f not in 0-255", h)
			}
			soa.Levels[i].Height = append(soa.Levels[i].Height, uint8(h))
		}
	}

	for i := range soa.Levels {
		if true {
			soa.Levels[i].Heading = util.DeltaEncode(soa.Levels[i].Heading)
			soa.Levels[i].Speed = util.DeltaEncode(soa.Levels[i].Speed)
			soa.Levels[i].Temperature = util.DeltaEncode(soa.Levels[i].Temperature)
			soa.Levels[i].Height = util.DeltaEncode(soa.Levels[i].Height)
		} else {
			hdg := util.DeltaEncode(soa.Levels[i].Heading)
			spd := util.DeltaEncode(soa.Levels[i].Speed)
			tmp := util.DeltaEncode(soa.Levels[i].Temperature)
			ht := util.DeltaEncode(soa.Levels[i].Height)

			hdg2 := util.DeltaDecode(hdg)
			spd2 := util.DeltaDecode(spd)
			tmp2 := util.DeltaDecode(tmp)
			ht2 := util.DeltaDecode(ht)

			for j := range hdg {
				if soa.Levels[i].Heading[j] != hdg2[j] {
					fmt.Printf("%d - %d\n", soa.Levels[i].Heading[j], hdg2[j])
				}
				if soa.Levels[i].Speed[j] != spd2[j] {
					fmt.Printf("%d - %d\n", soa.Levels[i].Speed[j], spd2[j])
				}
				if soa.Levels[i].Temperature[j] != tmp2[j] {
					fmt.Printf("%d - %d\n", soa.Levels[i].Temperature[j], tmp2[j])
				}
				if soa.Levels[i].Height[j] != ht2[j] {
					fmt.Printf("%d - %d\n", soa.Levels[i].Height[j], ht2[j])
				}
			}
		}
	}

	return soa, nil
}

func SOAWindCellToAOS(s SOAWindCell) WindCell {
	w := make(map[[2]float32]WindSample)

	levels := make([]SOAWindLevelCell, len(s.Levels))
	for i := range s.Levels {
		levels[i].MB = s.Levels[i].MB
		levels[i].Heading = util.DeltaDecode(s.Levels[i].Heading)
		levels[i].Speed = util.DeltaDecode(s.Levels[i].Speed)
		levels[i].Temperature = util.DeltaDecode(s.Levels[i].Temperature)
		levels[i].Height = util.DeltaDecode(s.Levels[i].Height)
	}

	for i := range s.Lat {
		samp := WindSample{}
		for _, level := range levels {
			wl := WindLevel{
				MB:          level.MB,
				Temperature: float32(level.Temperature[i]) + 273.15, // C -> K
				Height:      float32(level.Height[i])*100 - HeightOffset,
			}
			wl.UComponent, wl.VComponent = DirSpeedToUV(float32(level.Heading[i])*2, float32(level.Speed[i]))

			samp.Levels = append(samp.Levels, wl)
		}

		w[[2]float32{s.Long[i], s.Lat[i]}] = samp
	}

	return w
}

func CheckWindCellConversion(cell WindCell, soa SOAWindCell) error {
	ckcell := SOAWindCellToAOS(soa)
	if len(ckcell) != len(cell) {
		return fmt.Errorf("mismatch in number of entries %d - %d", len(cell), len(ckcell))
	}
	for p, s := range cell {
		cks, ok := ckcell[p]
		if !ok {
			return fmt.Errorf("missing point in map %v", p)
		}
		for i := range s.Levels {
			sl, ckl := s.Levels[i], cks.Levels[i]

			if sl.MB != ckl.MB {
				return fmt.Errorf("MB mismatch round trip %f - %f", sl.MB, ckl.MB)
			}

			d, s := UVToDirSpeed(sl.UComponent, sl.VComponent)
			cd, cs := UVToDirSpeed(ckl.UComponent, ckl.VComponent)
			if s >= 1 { // don't worry about direction for idle winds
				if math.HeadingDifference(d, cd) > 2 {
					fmt.Printf("SL: %+v\n", sl)
					fmt.Printf("Dir %f spd %f\n", d, s)

					fmt.Printf("CK: %+v\n", ckl)
					fmt.Printf("Dir %f spd %f\n", cd, cs)

					return fmt.Errorf("Direction mismatch round trip %f - %f", d, cd)
				}
			}

			if math.Abs(s-cs) > 1 {
				fmt.Printf("SL: %+v\n", sl)
				fmt.Printf("Dir %f spd %f\n", d, s)

				fmt.Printf("CK: %+v\n", ckl)
				fmt.Printf("Dir %f spd %f\n", cd, cs)

				return fmt.Errorf("Speed mismatch round trip %f - %f", s, cs)
			}

			if math.Abs(sl.Temperature-ckl.Temperature) > 0.51 {
				return fmt.Errorf("Temperature mismatch round trip %f - %f", sl.Temperature, ckl.Temperature)
			}
			if math.Abs(sl.Height-ckl.Height) > 51 {
				return fmt.Errorf("Height mismatch round trip %f - %f", sl.Height, ckl.Height)
			}
		}
	}
	return nil
}

// NOAA high-resolution rapid refresh: https://rapidrefresh.noaa.gov/hrrr/
func ingestHRRR(st StorageBackend) {
	// Set up signal handler for cleanup
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		LogInfo("Received interrupt signal, cleaning up temporary files...")
		cleanupTempFiles()
		os.Exit(0)
	}()

	type csvFile struct {
		f        *os.File
		hrrrPath string
	}
	fnCh := make(chan string)    // filenames of HRRR files to be ingested
	csvCh := make(chan csvFile)  // CSV files to be converted to WindGrids
	wgCh := make(chan *WindGrid) // WindGrids to be uploaded and archived

	// Grib -> CSV
	var gwg sync.WaitGroup
	gwg.Add(1)
	go func() {
		for path := range fnCh {
			f, err := gribToCSV(path)
			if err != nil {
				if f != nil {
					deleteTempFile(f.Name())
				}
				LogFatal("%s: %v", path, err)
			}

			csvCh <- csvFile{f: f, hrrrPath: path}
		}
		gwg.Done()
	}()

	// CSV -> WindGrid processing
	var cwg sync.WaitGroup
	cwg.Add(1)
	go func() {
		for cf := range csvCh {
			wg, err := windGridFromCSV(cf.f)

			if err != nil {
				LogFatal("%s: %v", cf.hrrrPath, err)
			} else {
				wg.HRRRPath = cf.hrrrPath
				wgCh <- wg
			}
		}
		cwg.Done()
	}()

	// Uploading WindGrids
	flags := ArchiverFlagsArchiveStorageClass
	if *dryRun {
		flags |= ArchiverFlagsDryRun
	}
	arch, err := MakeArchiver("HRRR", flags)
	if err != nil {
		LogFatal("Archiver: %v", err)
	}

	var uwg sync.WaitGroup
	uwg.Add(1)
	go func() {
		for wg := range wgCh {
			if err := uploadWindGrid(wg, st, arch); err != nil {
				LogFatal("%v", err)
			}
		}
		uwg.Done()
	}()

	EnqueueFiles("HRRR", fnCh)
	gwg.Wait()
	close(csvCh)
	cwg.Wait()
	close(wgCh)
	uwg.Wait()
}

func gribToCSV(path string) (*os.File, error) {
	start := time.Now()

	cf, err := os.CreateTemp("", "grib-*.csv")
	if err != nil {
		return nil, err
	}

	// Register the temp file for cleanup
	registerTempFile(cf.Name())

	cmd := exec.Command("wgrib2", path, "-match", ":(UGRD|VGRD|TMP|HGT):", "-csv", cf.Name())

	LogInfo("Running " + cmd.String())
	if err := cmd.Run(); err != nil {
		return cf, err
	}

	if err := cf.Sync(); err != nil {
		return cf, err
	}

	if _, err := cf.Seek(0, 0); err != nil {
		return cf, err
	}

	LogInfo("wgrib2 finished in %s", time.Since(start))

	return cf, nil
}

func windGridFromCSV(f *os.File) (*WindGrid, error) {
	defer deleteTempFile(f.Name())

	start := time.Now()

	cr := csv.NewReader(f)
	cr.ReuseRecord = true

	g := &WindGrid{Cells: make(map[[2]int]WindCell)}

	atoi := func(s string) (float32, error) {
		v, err := strconv.ParseFloat(s, 32)
		return float32(v), err
	}

	n := 0
	for {
		n++
		if n%10000000 == 0 {
			LogInfo("%s: processed %d lines", f.Name(), n)
		}

		if record, err := cr.Read(); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("CSV processing: %v (OUT OF SPACE?)", err)
		} else {
			if g.Time.IsZero() {
				g.Time, err = time.Parse(time.DateTime, record[1])
				if err != nil {
					return nil, fmt.Errorf("%s: %v", record[1], err)
				}

				g.Time = g.Time.UTC()
			}

			if !strings.HasSuffix(record[3], " mb") {
				// skip "surface" records
				continue
			}
			mb, err := atoi(strings.TrimSuffix(record[3], " mb"))
			if err != nil {
				return nil, err
			}

			long, err := atoi(record[4])
			if err != nil {
				return nil, err
			}
			lat, err := atoi(record[5])
			if err != nil {
				return nil, err
			}
			value, err := atoi(record[6])
			if err != nil {
				return nil, err
			}

			xy := [2]int{int(long), int(lat)}
			if _, ok := g.Cells[xy]; !ok {
				g.Cells[xy] = make(map[[2]float32]WindSample)
			}

			sample := g.Cells[xy][[2]float32{lat, long}]
			idx := slices.IndexFunc(sample.Levels, func(l WindLevel) bool { return l.MB == float32(mb) })
			if idx == -1 {
				idx = len(sample.Levels)
				sample.Levels = append(sample.Levels, WindLevel{MB: mb})
			}
			level := &sample.Levels[idx]

			switch record[2] {
			case "UGRD":
				level.UComponent = value
			case "VGRD":
				level.VComponent = value
			case "HGT":
				level.Height = value
			case "TMP":
				level.Temperature = value
			}

			g.Cells[xy][[2]float32{lat, long}] = sample
		}
	}

	LogInfo("WindGrid from CSV finished in %s", time.Since(start))
	return g, nil
}

func uploadWindGrid(g *WindGrid, st StorageBackend, arch *Archiver) error {
	start := time.Now()

	var wg sync.WaitGroup
	cellCh := make(chan [2]int)
	var totBytes int64
	for range *nWorkers {
		wg.Add(1)
		go func() {
			for xy := range cellCh {
				n, err := storeHRRRCell(st, xy, g.Cells[xy], g.Time)
				if err != nil {
					LogFatal("%v: %v", xy, err)
				}
				atomic.AddInt64(&totBytes, n)
			}
			wg.Done()
		}()
	}

	for xy := range g.Cells {
		cellCh <- xy
	}
	close(cellCh)

	wg.Wait()
	LogInfo("%s: %s stored in %d objects for HRRR file", g.HRRRPath, util.ByteCount(totBytes), len(g.Cells))
	LogInfo("upload finished in %s", time.Since(start))

	start = time.Now()
	err := arch.Archive(g.HRRRPath)
	LogInfo("archive finished in %s", time.Since(start))

	return err
}

func storeHRRRCell(st StorageBackend, xy [2]int, cell WindCell, t time.Time) (int64, error) {
	soa, err := WindCellToSOA(cell)
	if err != nil {
		return 0, err
	}
	if err := CheckWindCellConversion(cell, soa); err != nil {
		return 0, err
	}

	path := fmt.Sprintf("hrrr/%02d/%04d/%d/%02d/%02d/%02d.msgpack.zstd", xy[1], xy[0], t.Year(), t.Month(), t.Day(), t.Hour())
	return st.Store(path, soa)
}
