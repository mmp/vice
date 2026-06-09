// math/latlong_test.go

package math

import (
	"math/rand/v2"
	"testing"
)

func TestPathBytesRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		pts  []Point2LL
	}{
		{
			name: "empty",
			pts:  nil,
		},
		{
			name: "single",
			pts:  []Point2LL{{-73.78, 40.64}},
		},
		{
			name: "ZNY-area polyline",
			pts: []Point2LL{
				{-73.78, 40.64}, {-73.79, 40.65}, {-73.80, 40.66},
				{-73.81, 40.67}, {-73.82, 40.66}, {-73.81, 40.65},
			},
		},
		{
			name: "zeros and negatives",
			pts: []Point2LL{
				{0, 0}, {-180, 90}, {180, -90}, {0, 0},
			},
		},
		{
			name: "small deltas",
			pts: func() []Point2LL {
				pts := make([]Point2LL, 100)
				for i := range pts {
					pts[i] = Point2LL{
						-73.78 + float32(i)*0.0001,
						40.64 + float32(i)*0.00007,
					}
				}
				return pts
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := EncodePathBytes(nil, tc.pts)
			if len(enc) != 8*len(tc.pts) {
				t.Fatalf("encoded length %d, want %d", len(enc), 8*len(tc.pts))
			}
			got := make([]Point2LL, len(tc.pts))
			n, err := DecodePathBytes(enc, got)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if n != len(enc) {
				t.Errorf("DecodePathBytes consumed %d, want %d", n, len(enc))
			}
			for i := range tc.pts {
				if got[i] != tc.pts[i] {
					t.Errorf("pt %d: got %v, want %v", i, got[i], tc.pts[i])
				}
			}
		})
	}
}

// TestPathBytesAppendsToExisting verifies the append-semantics: encoding
// onto a non-empty buffer preserves the existing bytes and DecodePathBytes
// only reads the trailing path region.
func TestPathBytesAppendsToExisting(t *testing.T) {
	pts := []Point2LL{{-73.78, 40.64}, {-73.79, 40.65}, {-73.80, 40.66}}
	prefix := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	out := EncodePathBytes(prefix, pts)
	if len(out) != len(prefix)+8*len(pts) {
		t.Fatalf("length after append: %d, want %d", len(out), len(prefix)+8*len(pts))
	}
	for i, b := range prefix {
		if out[i] != b {
			t.Errorf("prefix byte %d: got %x, want %x", i, out[i], b)
		}
	}

	got := make([]Point2LL, len(pts))
	n, err := DecodePathBytes(out[len(prefix):], got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n != 8*len(pts) {
		t.Errorf("consumed %d, want %d", n, 8*len(pts))
	}
	for i := range pts {
		if got[i] != pts[i] {
			t.Errorf("pt %d: got %v, want %v", i, got[i], pts[i])
		}
	}
}

// TestPathBytesShortStream confirms DecodePathBytes reports an error
// rather than reading past the end of the input.
func TestPathBytesShortStream(t *testing.T) {
	pts := []Point2LL{{1, 2}, {3, 4}, {5, 6}}
	enc := EncodePathBytes(nil, pts)
	out := make([]Point2LL, len(pts))
	if _, err := DecodePathBytes(enc[:len(enc)-1], out); err == nil {
		t.Error("expected error for truncated stream, got nil")
	}
}

// TestPathBytesFuzzy throws a long randomized path through the codec to
// catch any bit-cast / byte-split / delta arithmetic mistakes.
func TestPathBytesFuzzy(t *testing.T) {
	r := rand.New(rand.NewPCG(42, 0xa5a5a5a5))
	for trial := 0; trial < 32; trial++ {
		n := 1 + r.IntN(500)
		pts := make([]Point2LL, n)
		// Walk randomly so adjacent values are similar (the realistic case).
		cur := Point2LL{-100 + 200*r.Float32(), -80 + 160*r.Float32()}
		for i := range pts {
			cur[0] += (r.Float32() - 0.5) * 0.01
			cur[1] += (r.Float32() - 0.5) * 0.01
			pts[i] = cur
		}
		enc := EncodePathBytes(nil, pts)
		got := make([]Point2LL, n)
		if _, err := DecodePathBytes(enc, got); err != nil {
			t.Fatalf("trial %d: decode: %v", trial, err)
		}
		for i := range pts {
			if got[i] != pts[i] {
				t.Errorf("trial %d pt %d: got %v, want %v", trial, i, got[i], pts[i])
				break
			}
		}
	}
}
