package wx

import (
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"io"
	"sort"
	"sync"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

// Precip is the object type that is stored in GCS after wx ingest for precipitation.
type Precip struct {
	DBZ        []byte
	Resolution int
	Latitude   float32
	Longitude  float32
}

func DecodePrecip(r io.Reader) (*Precip, error) {
	zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var precip Precip
	if err := msgpack.NewDecoder(zr).Decode(&precip); err != nil {
		return nil, err
	}

	precip.DBZ = util.DeltaDecode(precip.DBZ)

	return &precip, nil
}

// lat-long bounds
func (p Precip) BoundsLL() math.Extent2D {
	centerLL := math.Point2LL{p.Longitude, p.Latitude}

	// Resolution is in pixels, and we have 0.5nm per pixel (2 samples per nm)
	widthNM := float32(p.Resolution) / 2
	return math.BoundLatLongCircle(centerLL, widthNM/2 /* radius */)
}

// MakeDebugPrecip returns a synthetic Precip blob shaped like an inscribed
// circle inside the scope's bounding square, divided into three horizontal
// bands so each ERAM/STARS render path lights up: severe (top, dBZ 55 → ERAM
// Extreme / STARS L5+), medium (middle, dBZ 45 → ERAM Heavy / STARS L3+), low
// (bottom, dBZ 35 → ERAM Moderate / STARS L2). Used to exercise the rendering
// pipeline without depending on the live precip server. sideNM is the full
// square side length in nautical miles; resolution is fixed at 2 px/NM.
func MakeDebugPrecip(center math.Point2LL, sideNM int) *Precip {
	const pixelsPerNM = 2
	resolution := sideNM * pixelsPerNM
	dbz := make([]byte, resolution*resolution)

	half := float32(resolution) / 2
	radius2 := half * half
	thirdY := resolution / 3
	for y := 0; y < resolution; y++ {
		var v byte
		switch {
		case y < thirdY:
			v = 55 // severe → Extreme
		case y < 2*thirdY:
			v = 45 // medium → Heavy
		default:
			v = 35 // low → Moderate
		}
		dy := float32(y) - half + 0.5
		for x := 0; x < resolution; x++ {
			dx := float32(x) - half + 0.5
			if dx*dx+dy*dy <= radius2 {
				dbz[x+y*resolution] = v
			}
		}
	}

	return &Precip{
		DBZ:        dbz,
		Resolution: resolution,
		Latitude:   center[1],
		Longitude:  center[0],
	}
}

// PrecipSource identifies the provider a scraped radar image came from; the
// provider determines the color ramp used to recover dBZ from the image and
// how no-data pixels are encoded.
type PrecipSource string

const (
	// PrecipSourceNWSWMS is the original opengeo.ncep.noaa.gov GeoServer
	// imagery (opaque, white background for no data). It is the zero value
	// since scrape blobs written before the Source field existed all came
	// from there.
	PrecipSourceNWSWMS PrecipSource = ""

	// PrecipSourceIEMN0Q is the Iowa Environmental Mesonet NEXRAD n0q
	// composite (transparent background for no data).
	PrecipSourceIEMN0Q PrecipSource = "iem-n0q"
)

// A single scanline of this color map, converted to RGB bytes:
// https://opengeo.ncep.noaa.gov/geoserver/styles/reflectivity.png
//
//go:embed radar_reflectivity.rgb
var radarReflectivity []byte

// RGB triplets for indices 1-255 of the palette of the IEM NEXRAD n0q
// composite; index i corresponds to (i-65)/2 dBZ, from -32 dBZ at index 1 to
// +95 dBZ at index 255. Palette index 0 marks no data and is served fully
// transparent, so it is omitted here and detected via alpha instead.
// Extracted from the palette of
// https://mesonet.agron.iastate.edu/data/gis/images/4326/USCOMP/n0q_0.png
//
//go:embed iem_n0q.rgb
var iemN0QPalette []byte

type kdNode struct {
	rgb [3]byte
	dbz float32
	c   [2]*kdNode
}

// makeRadarKdTree builds a kd-tree over the RGB triplets in colorMap;
// entryDBZ gives the dBZ value for the k'th triplet.
func makeRadarKdTree(colorMap []byte, entryDBZ func(k int) float32) *kdNode {
	type rgbRefl struct {
		rgb [3]byte
		dbz float32
	}

	var r []rgbRefl

	for i := 0; i < len(colorMap); i += 3 {
		r = append(r, rgbRefl{
			rgb: [3]byte{colorMap[i], colorMap[i+1], colorMap[i+2]},
			dbz: entryDBZ(i / 3),
		})
	}

	// Build a kd-tree over the RGB points in the color map.
	var buildTree func(r []rgbRefl, depth int) *kdNode
	buildTree = func(r []rgbRefl, depth int) *kdNode {
		if len(r) == 0 {
			return nil
		}
		if len(r) == 1 {
			return &kdNode{rgb: r[0].rgb, dbz: r[0].dbz}
		}

		// The split dimension cycles through RGB with tree depth.
		dim := depth % 3

		// Sort the points in the current dimension (we actually just need
		// to partition around the midpoint, but...)
		sort.Slice(r, func(i, j int) bool {
			return r[i].rgb[dim] < r[j].rgb[dim]
		})

		// Split in the middle and recurse
		mid := len(r) / 2
		return &kdNode{
			rgb: r[mid].rgb,
			dbz: r[mid].dbz,
			c:   [2]*kdNode{buildTree(r[:mid], depth+1), buildTree(r[mid+1:], depth+1)},
		}
	}

	return buildTree(r, 0)
}

// Returns estimated dBZ (https://en.wikipedia.org/wiki/DBZ_(meteorology)) for
// an RGB by going backwards from the color ramp.
func estimateDBZ(root *kdNode, rgb [3]byte) float32 {
	// Returns the distnace between the specified RGB and the RGB passed to
	// estimateDBZ.
	dist := func(o []byte) float32 {
		d2 := math.Sqr(int(o[0])-int(rgb[0])) + math.Sqr(int(o[1])-int(rgb[1])) + math.Sqr(int(o[2])-int(rgb[2]))
		return math.Sqrt(float32(d2))
	}

	var searchTree func(n *kdNode, closestNode *kdNode, closestDist float32, depth int) (*kdNode, float32)
	searchTree = func(n *kdNode, closestNode *kdNode, closestDist float32, depth int) (*kdNode, float32) {
		if n == nil {
			return closestNode, closestDist
		}

		// Check the current node
		d := dist(n.rgb[:])
		if d < closestDist {
			closestDist = d
			closestNode = n
		}

		// Split dimension as in buildTree above
		dim := depth % 3

		// Initially traverse the tree based on which side of the split
		// plane the lookup point is on.
		var first, second *kdNode
		if rgb[dim] < n.rgb[dim] {
			first, second = n.c[0], n.c[1]
		} else {
			first, second = n.c[1], n.c[0]
		}

		closestNode, closestDist = searchTree(first, closestNode, closestDist, depth+1)

		// If the distance to the split plane is less than the distance to
		// the closest point found so far, we need to check the other side
		// of the split.
		if float32(math.Abs(int(rgb[dim])-int(n.rgb[dim]))) < closestDist {
			closestNode, closestDist = searchTree(second, closestNode, closestDist, depth+1)
		}

		return closestNode, closestDist
	}

	n, _ := searchTree(root, nil, 100000, 0)
	return n.dbz
}

// Allow concurrent calls to RadarImageToDBZ
var getRadarKdTree = sync.OnceValue(func() *kdNode {
	n := len(radarReflectivity) / 3
	return makeRadarKdTree(radarReflectivity, func(k int) float32 {
		// Approximate range of the reflectivity color ramp
		return math.Lerp(float32(k)/float32(n), -25, 73)
	})
})

var getIEMN0QKdTree = sync.OnceValue(func() *kdNode {
	return makeRadarKdTree(iemN0QPalette, func(k int) float32 {
		// Entry k is palette index k+1, which maps to (k+1-65)/2 dBZ.
		return float32(k-64) / 2
	})
})

func RadarImageToDBZ(img image.Image, src PrecipSource) []byte {
	// Convert the Image returned by png.Decode to a simple 8-bit RGBA image.
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, img.Bounds(), img, image.Point{}, draw.Over)

	var root *kdNode
	var noData func(px color.RGBA) bool
	switch src {
	case PrecipSourceIEMN0Q:
		root = getIEMN0QKdTree()
		noData = func(px color.RGBA) bool { return px.A == 0 }
	default:
		root = getRadarKdTree()
		// All white -> ~nil
		noData = func(px color.RGBA) bool { return px.R == 255 && px.G == 255 && px.B == 255 }
	}

	// Determine the dBZ for each pixel.
	ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
	dbzImage := make([]byte, nx*ny)
	for y := range ny {
		for x := range nx {
			px := rgba.RGBAAt(x, y)
			dbz := float32(-100)
			if !noData(px) {
				dbz = estimateDBZ(root, [3]byte{px.R, px.G, px.B})
			}

			dbzImage[x+y*nx] = byte(max(0, min(255, dbz)))
		}
	}

	return dbzImage
}
