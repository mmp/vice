package wx

import (
	_ "embed"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"net/url"
	"sort"
	"sync"

	"github.com/mmp/vice/math"
)

// Precip is the object type that is stored in GCS after wx ingest for precipitation.
type Precip struct {
	DBZ        []byte
	Resolution int
	Latitude   float32
	Longitude  float32
}

func FetchRadarImage(center math.Point2LL, radius float32, resolution int) (image.Image, math.Extent2D, error) {
	// The weather radar image comes via a WMS GetMap request from the NOAA.
	//
	// Relevant background:
	// https://enterprise.arcgis.com/en/server/10.3/publish-services/windows/communicating-with-a-wms-service-in-a-web-browser.htm
	// http://schemas.opengis.net/wms/1.3.0/capabilities_1_3_0.xsd
	// NOAA weather: https://opengeo.ncep.noaa.gov/geoserver/www/index.html
	// https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?service=wms&version=1.3.0&request=GetCapabilities
	params := url.Values{}
	params.Add("SERVICE", "WMS")
	params.Add("REQUEST", "GetMap")
	params.Add("FORMAT", "image/png")
	params.Add("WIDTH", fmt.Sprintf("%d", resolution))
	params.Add("HEIGHT", fmt.Sprintf("%d", resolution))
	params.Add("LAYERS", "conus_bref_qcd")
	bbox := math.BoundLatLongCircle(center, radius)
	params.Add("BBOX", fmt.Sprintf("%f,%f,%f,%f", bbox.P0[0], bbox.P0[1], bbox.P1[0], bbox.P1[1]))

	url := "https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?" + params.Encode()

	resp, err := http.Get(url)
	if err != nil {
		return nil, math.Extent2D{}, err
	}
	defer resp.Body.Close()

	img, err := png.Decode(resp.Body)
	return img, bbox, err
}

// A single scanline of this color map, converted to RGB bytes:
// https://opengeo.ncep.noaa.gov/geoserver/styles/reflectivity.png
//
//go:embed radar_reflectivity.rgb
var radarReflectivity []byte

type kdNode struct {
	rgb [3]byte
	dbz float32
	c   [2]*kdNode
}

func makeRadarKdTree() *kdNode {
	type rgbRefl struct {
		rgb [3]byte
		dbz float32
	}

	var r []rgbRefl

	for i := 0; i < len(radarReflectivity); i += 3 {
		r = append(r, rgbRefl{
			rgb: [3]byte{radarReflectivity[i], radarReflectivity[i+1], radarReflectivity[i+2]},
			// Approximate range of the reflectivity color ramp
			dbz: math.Lerp(float32(i)/float32(len(radarReflectivity)), -25, 73),
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
	// All white -> ~nil
	if rgb[0] == 255 && rgb[1] == 255 && rgb[2] == 255 {
		return -100
	}

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

	if true {
		n, _ := searchTree(root, nil, 100000, 0)
		return n.dbz
	} else {
		// Debugging: verify the point found is indeed the closest by
		// exhaustively checking the distance to all of points in the color
		// map.
		n, nd := searchTree(root, nil, 100000, 0)

		closest, closestDist := -1, float32(100000)
		for i := 0; i < len(radarReflectivity); i += 3 {
			d := dist(radarReflectivity[i : i+3])
			if d < closestDist {
				closestDist = d
				closest = i
			}
		}

		// Note that multiple points in the color map may have the same
		// distance to the lookup point; thus we only check the distance
		// here and not the reflectivity (which should be very close but is
		// not necessarily the same.)
		if nd != closestDist {
			fmt.Printf("WAH %d,%d,%d -> %d,%d,%d: dist %f vs %d,%d,%d: dist %f\n",
				int(rgb[0]), int(rgb[1]), int(rgb[2]),
				int(n.rgb[0]), int(n.rgb[1]), int(n.rgb[2]), nd,
				int(radarReflectivity[closest]), int(radarReflectivity[closest+1]), int(radarReflectivity[closest+2]),
				closestDist)
		}

		return n.dbz
	}
}

// Allow concurrent calls to RadarImageToDBZ
var getRadarKdTree = sync.OnceValue(func() *kdNode { return makeRadarKdTree() })

func RadarImageToDBZ(img image.Image) []byte {
	// Convert the Image returned by png.Decode to a simple 8-bit RGBA image.
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, img.Bounds(), img, image.Point{}, draw.Over)

	root := getRadarKdTree()

	// Determine the dBZ for each pixel.
	ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
	dbzImage := make([]byte, nx*ny)
	for y := 0; y < ny; y++ {
		for x := 0; x < nx; x++ {
			px := rgba.RGBAAt(x, y)
			dbz := estimateDBZ(root, [3]byte{px.R, px.G, px.B})

			dbzImage[x+y*nx] = byte(max(0, min(255, dbz)))
		}
	}

	return dbzImage
}
