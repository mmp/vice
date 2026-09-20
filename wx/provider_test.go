package wx

import (
	"image"
	"io"
	"testing"
	"time"

	"github.com/mmp/vice/log"

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
