// wx/provider_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"errors"
	"image"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"

	"image/color"
	"log/slog"
	"math/rand"
)

type testAtmosBackend struct {
	calls int
	t0    time.Time
	t1    time.Time
	soa   *AtmosByPointSOA
}

func (b *testAtmosBackend) getPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	return "", time.Time{}, nil
}

func (b *testAtmosBackend) getAtmosGrid(facility string, t time.Time, station string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	b.calls++
	return b.soa, b.t0, b.t1, nil
}

func TestProviderCachesAtmosGridByReturnedInterval(t *testing.T) {
	t0 := time.Date(2025, time.August, 6, 12, 0, 0, 0, time.UTC)
	backend := &testAtmosBackend{
		t0:  t0,
		t1:  t0.Add(time.Hour),
		soa: &AtmosByPointSOA{},
	}
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	provider := newProvider(lg, backend)

	if _, _, _, err := provider.GetAtmosGrid("P31", t0.Add(10*time.Minute), "KTPA"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := provider.GetAtmosGrid("P31", t0.Add(20*time.Minute), "KTPA"); err != nil {
		t.Fatal(err)
	}

	if backend.calls != 1 {
		t.Fatalf("backend calls = %d, want 1", backend.calls)
	}
}

// stalledBackend never answers, like a server on a network that has stopped
// delivering packets.
type stalledBackend struct{ unblock chan struct{} }

func (b stalledBackend) getPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	<-b.unblock
	return "", time.Time{}, nil
}

func (b stalledBackend) getAtmosGrid(facility string, t time.Time, station string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	<-b.unblock
	return nil, time.Time{}, time.Time{}, nil
}

func TestProviderGivesUpOnStalledPrecipRequest(t *testing.T) {
	backend := stalledBackend{unblock: make(chan struct{})}
	defer close(backend.unblock)
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	provider := newProvider(lg, backend)

	start := time.Now()
	if _, _, err := provider.GetPrecipURL("P31", start); err == nil {
		t.Errorf("expected an error from the resources fallback")
	}
	// The client gives up on the local server after 5s.
	if d := time.Since(start); d > backendFallbackTimeout+time.Second {
		t.Errorf("GetPrecipURL took %s", d)
	}
}

// timesEvery returns the times from start through end, step apart, leaving
// out those in [gapStart, gapEnd).
func timesEvery(start, end time.Time, step time.Duration, gapStart, gapEnd time.Time) []time.Time {
	var times []time.Time
	for t := start; !t.After(end); t = t.Add(step) {
		if t.Before(gapStart) || !t.Before(gapEnd) {
			times = append(times, t)
		}
	}
	return times
}

func TestFacilityTimeIntervalsRequireRadar(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, time.January, d, 0, 0, 0, 0, time.UTC) }

	// Atmos covers January 1-3 for each facility; A11's, from the Alaska
	// HRRR, comes every three hours, and ZHN's, from the NAM Hawaii nest,
	// every six. P31's radar has a two-hour gap on the 2nd; ZJX's radar
	// doesn't start until the 3rd.
	atmos, err := MakeManifestFromMap(map[string][]time.Time{
		"P31": timesEvery(day(1), day(4), time.Hour, time.Time{}, time.Time{}),
		"ZJX": timesEvery(day(1), day(4), time.Hour, time.Time{}, time.Time{}),
		"A11": timesEvery(day(1), day(4), 3*time.Hour, time.Time{}, time.Time{}),
		"ZHN": timesEvery(day(1), day(4), 6*time.Hour, time.Time{}, time.Time{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	precip, err := MakeManifestFromMap(map[string][]time.Time{
		"P31": timesEvery(day(1), day(4), 5*time.Minute, day(2).Add(10*time.Hour), day(2).Add(12*time.Hour)),
		"ZJX": timesEvery(day(3), day(4), 5*time.Minute, time.Time{}, time.Time{}),
		"HNL": timesEvery(day(1), day(3), 5*time.Minute, time.Time{}, time.Time{}),
		"A11": timesEvery(day(1), day(4), 5*time.Minute, time.Time{}, time.Time{}),
		"ZHN": timesEvery(day(1), day(4), 5*time.Minute, time.Time{}, time.Time{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		facility string
		want     []util.TimeInterval
	}{
		{"P31", []util.TimeInterval{{day(1), day(2)}, {day(3), day(4)}}},
		{"ZJX", []util.TimeInterval{{day(3), day(4)}}},
		{"HNL", []util.TimeInterval{{day(1), day(3)}}}, // radar but no atmos
		{"A11", []util.TimeInterval{{day(1), day(4)}}},
		{"ZHN", []util.TimeInterval{{day(1), day(4)}}},
		{"ZZZ", nil},
	} {
		if got := facilityTimeIntervals(atmos, precip, tc.facility); !slices.EqualFunc(got, tc.want,
			func(a, b util.TimeInterval) bool { return a[0].Equal(b[0]) && a[1].Equal(b[1]) }) {
			t.Errorf("%s: got %v, want %v", tc.facility, got, tc.want)
		}
	}
}

// testStore is an ObjectStore whose URLs are the object paths.
type testStore struct{}

func (testStore) GetReader(path string) (io.ReadCloser, error) {
	return nil, errors.New("unimplemented")
}
func (testStore) GetURL(path string, lifetime time.Duration) (string, error) { return path, nil }

func TestGCSPrecipURLHoldsImageThroughGaps(t *testing.T) {
	t0 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	gapStart, gapEnd := t0.Add(2*time.Hour), t0.Add(5*time.Hour)
	times := timesEvery(t0, t0.Add(8*time.Hour), 5*time.Minute, gapStart, gapEnd)
	precip, err := MakeManifestFromMap(map[string][]time.Time{"P31": times})
	if err != nil {
		t.Fatal(err)
	}
	g := &gcsBackend{gcsClient: testStore{}, precipManifest: precip}

	if _, _, err := g.getPrecipURL("P31", t0.Add(-time.Minute)); err == nil {
		t.Errorf("expected an error before the data")
	}

	for _, tc := range []struct {
		t         time.Time
		wantImage time.Time
		wantNext  time.Time
	}{
		{t0, t0, t0.Add(5 * time.Minute)}, // on an image
		{t0.Add(7 * time.Minute), t0.Add(5 * time.Minute), t0.Add(10 * time.Minute)},
		{gapStart.Add(2 * time.Hour), gapStart.Add(-5 * time.Minute), gapEnd},
		{t0.Add(8 * time.Hour), t0.Add(8 * time.Hour), noNextTime},  // the last image
		{t0.Add(10 * time.Hour), t0.Add(8 * time.Hour), noNextTime}, // after the data
	} {
		url, next, err := g.getPrecipURL("P31", tc.t)
		if err != nil {
			t.Fatalf("%s: %v", tc.t, err)
		}
		if wantURL := BuildObjectPath("precip", "P31", tc.wantImage); url != wantURL || !next.Equal(tc.wantNext) {
			t.Errorf("%s: got %q, next %s; want %q, next %s", tc.t, url, next, wantURL, tc.wantNext)
		}
	}
}

func TestRadarImageToDBZMemoization(t *testing.T) {
	// Build an image mixing exact palette colors, blended colors, and no-data
	// pixels; the memoized RadarImageToDBZ must match direct per-pixel
	// kd-tree lookups.
	for _, src := range []PrecipSource{PrecipSourceNWSWMS, PrecipSourceIEMN0Q} {
		rng := rand.New(rand.NewSource(6502))

		pal := radarReflectivity
		root := getRadarKdTree()
		haveData := func(px color.RGBA) bool { return px.R != 255 || px.G != 255 || px.B != 255 }
		noData := color.RGBA{R: 255, G: 255, B: 255, A: 255}
		if src == PrecipSourceIEMN0Q {
			pal = iemN0QPalette
			root = getIEMN0QKdTree()
			haveData = func(px color.RGBA) bool { return px.A != 0 }
			noData = color.RGBA{}
		}

		const nx, ny = 64, 64
		img := image.NewRGBA(image.Rect(0, 0, nx, ny))
		for y := range ny {
			for x := range nx {
				var px color.RGBA
				switch rng.Intn(3) {
				case 0: // exact palette color
					i := 3 * rng.Intn(len(pal)/3)
					px = color.RGBA{R: pal[i], G: pal[i+1], B: pal[i+2], A: 255}
				case 1: // arbitrary blended color
					px = color.RGBA{R: byte(rng.Intn(256)), G: byte(rng.Intn(256)), B: byte(rng.Intn(256)), A: 255}
				default:
					px = noData
				}
				img.SetRGBA(x, y, px)
			}
		}

		got := RadarImageToDBZ(img, src)

		for y := range ny {
			for x := range nx {
				px := img.RGBAAt(x, y)
				dbz := float32(-100)
				if haveData(px) {
					dbz = estimateDBZ(root, [3]byte{px.R, px.G, px.B})
				}
				want := byte(max(0, min(255, dbz)))
				if got[x+y*nx] != want {
					t.Errorf("source %q: pixel (%d,%d) rgba %v: got dBZ %d, want %d", src, x, y, px, got[x+y*nx], want)
				}
			}
		}
	}
}
