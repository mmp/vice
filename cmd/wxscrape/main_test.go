package main

import (
	"strings"
	"testing"
	"time"
)

// newTestCategory builds a category with sources whose last fetch was the
// given duration ago; a zero duration means the source has not been fetched.
func newTestCategory(overdueAfter time.Duration, ages map[string]time.Duration, order ...string) *category {
	c := &category{
		name:         "test",
		overdueAfter: overdueAfter,
		sources:      order,
		fetched:      make(map[string]time.Time),
	}
	for src, age := range ages {
		if age > 0 {
			c.fetched[src] = time.Now().Add(-age)
		}
	}
	return c
}

func TestCategoryFreshness(t *testing.T) {
	start := time.Now().Add(-3 * time.Hour)

	for _, tc := range []struct {
		name         string
		overdueAfter time.Duration
		ages         map[string]time.Duration
		order        []string
		wantOK       bool
		wantSource   string
	}{
		{
			name:         "nothing fetched yet, still inside the grace",
			overdueAfter: 26 * time.Hour,
			ages:         map[string]time.Duration{"KJFK": 0, "KLGA": 0},
			order:        []string{"KJFK", "KLGA"},
			wantOK:       true,
			wantSource:   "KJFK",
		},
		{
			name:         "nothing fetched and past the grace",
			overdueAfter: time.Hour,
			ages:         map[string]time.Duration{"KJFK": 0, "KLGA": 0},
			order:        []string{"KJFK", "KLGA"},
			wantOK:       false,
			wantSource:   "KJFK",
		},
		{
			name:         "one laggard among fresh sources",
			overdueAfter: 26 * time.Hour,
			ages: map[string]time.Duration{
				"KJFK": time.Hour,
				"KBOS": 31 * time.Hour,
				"KLGA": 2 * time.Hour,
			},
			order:      []string{"KJFK", "KBOS", "KLGA"},
			wantOK:     false,
			wantSource: "KBOS",
		},
		{
			name:         "all sources fetched recently",
			overdueAfter: 26 * time.Hour,
			ages:         map[string]time.Duration{"KJFK": time.Hour, "KBOS": 19 * time.Hour},
			order:        []string{"KJFK", "KBOS"},
			wantOK:       true,
			wantSource:   "KBOS",
		},
		{
			name:         "no cadence to hold it to",
			overdueAfter: 0,
			ages:         map[string]time.Duration{"KJFK": 100 * time.Hour},
			order:        []string{"KJFK"},
			wantOK:       true,
			wantSource:   "KJFK",
		},
		{
			name:         "no sources at all",
			overdueAfter: 0,
			wantOK:       true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCategory(tc.overdueAfter, tc.ages, tc.order...)

			if src, _ := c.oldestFetch(start); src != tc.wantSource {
				t.Errorf("oldest fetch is %q, expected %q", src, tc.wantSource)
			}

			var sb strings.Builder
			if ok := c.writeStatus(&sb, start); ok != tc.wantOK {
				t.Errorf("writeStatus returned %v, expected %v: %s", ok, tc.wantOK, sb.String())
			}

			status := sb.String()
			if want := "STALE"; (strings.Contains(status, want)) == tc.wantOK {
				t.Errorf("status %q does not match ok=%v", status, tc.wantOK)
			}
			if tc.wantSource != "" && !strings.Contains(status, tc.wantSource) {
				t.Errorf("status %q does not name %q", status, tc.wantSource)
			}
		})
	}
}

// The laggard is picked from a slice rather than by ranging over the map, so
// that the status text doesn't name a different source on each request.
func TestOldestFetchIsStable(t *testing.T) {
	start := time.Now()

	sources := []string{"KJFK", "KBOS", "KLGA", "KEWR", "KTEB", "KHPN", "KISP", "KSWF"}
	c := newTestCategory(26*time.Hour, nil, sources...)
	for _, src := range sources[1:] {
		c.markFetched(src)
	}

	for range 10 {
		if src, _ := c.oldestFetch(start); src != "KJFK" {
			t.Fatalf("oldest fetch is %q, expected KJFK", src)
		}
	}
}

func TestSinceFetch(t *testing.T) {
	c := newTestCategory(time.Hour, nil, "KJFK")

	if _, ok := c.sinceFetch("KJFK"); ok {
		t.Error("KJFK reports a previous fetch before it has been fetched")
	}

	c.markFetched("KJFK")
	if age, ok := c.sinceFetch("KJFK"); !ok {
		t.Error("KJFK reports no previous fetch after being fetched")
	} else if age > time.Minute {
		t.Errorf("KJFK was just fetched but reports an age of %s", age)
	}
}
