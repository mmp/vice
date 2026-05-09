package wx

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mmp/vice/log"
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

func (b *testAtmosBackend) getAtmosGrid(facility string, t time.Time, primaryAirport string) (*AtmosByPointSOA, time.Time, time.Time, error) {
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
