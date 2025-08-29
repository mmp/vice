// pkg/wx/model.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

type WindLayer struct {
	Altitude        float32
	Direction       float32    // 0 -> variable
	DirectionVector [2]float32 // normalized vector
	Speed           float32
	Gust            float32
}

// ParseWindLayers parses a string of the form
// "alt/dir/spd,alt/dir/spd,..." and returns the corresponding WindLayer
// objects. Errors are logged to the provided ErrorLogger.
func ParseWindLayers(str string, e *util.ErrorLogger) []WindLayer {
	var layers []WindLayer
	for l := range strings.SplitSeq(str, ",") {
		f := strings.Split(l, "/")
		var layer WindLayer
		if len(f) != 3 {
			e.ErrorString("expected three numbers separated by '/'s in wind layer %q entry", l)
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}

		if alt, err := strconv.Atoi(f[0]); err != nil {
			e.ErrorString("invalid altitude %q in wind layer %q", f[0], str)
			continue
		} else {
			layer.Altitude = float32(alt)
		}

		if dir, err := strconv.Atoi(f[1]); err != nil {
			e.ErrorString("invalid direction %q in wind layer %q", f[1], str)
			continue
		} else if dir < 0 || dir > 360 {
			e.ErrorString("wind layer direction %d must be between 0-360", dir)
			continue
		} else {
			layer.Direction = float32(dir)
			layer.DirectionVector = math.SinCos(math.Radians(layer.Direction))
		}

		s, g, ok := strings.Cut(f[2], "g")
		if ok {
			if gst, err := strconv.Atoi(g); err != nil {
				e.ErrorString("invalid gust %q in wind layer %q", g, str)
				continue
			} else if gst < 0 {
				e.ErrorString("gusts %d must be >= 0 in wind layer %q", gst, str)
				continue
			} else {
				layer.Gust = float32(gst)
			}
		}
		if spd, err := strconv.Atoi(s); err != nil {
			e.ErrorString("invalid speed %q in wind layer %q", s, str)
			continue
		} else if spd < 0 {
			e.ErrorString("speed %d must be >= 0 in wind layer %q", spd, str)
			continue
		} else {
			layer.Speed = float32(spd)
		}

		if layer.Gust != 0 && layer.Gust < layer.Speed {
			e.ErrorString("gusts %f must be >= speed %f in wind layer %q", layer.Gust, layer.Speed, str)
			continue
		}

		layers = append(layers, layer)
	}
	return layers
}

func BlendWindLayers(wts []float32, layers []WindLayer) WindLayer {
	var alt, sumwt float32
	var vspd, vgst [2]float32
	for i, wt := range wts {
		if wt != 0 {
			layer := layers[i]
			alt += wt * layer.Altitude
			vspd = math.Add2f(vspd, math.Scale2f(layer.DirectionVector, layer.Speed*wt))
			vgst = math.Add2f(vgst, math.Scale2f(layer.DirectionVector, layer.Gust*wt))
			sumwt += wt
		}
	}

	invwt := 1 / sumwt
	return WindLayer{
		Altitude:        alt * invwt,
		Direction:       math.VectorHeading(vspd),
		DirectionVector: math.Normalize2f(vspd),
		Speed:           math.Length2f(vspd) * invwt,
		Gust:            math.Length2f(vgst) * invwt,
	}
}

func interpolateWind(alt float32, layers []WindLayer) WindLayer {
	if alt <= layers[0].Altitude {
		return layers[0]
	} else if alt >= layers[len(layers)-1].Altitude {
		return layers[len(layers)-1]
	} else {
		i := 0 // precondition: alt > layers[i].Altitude
		for i = range layers {
			if alt < layers[i].Altitude {
				break
			}
		}

		l0, l1 := layers[i-1], layers[i]
		t := (alt - l0.Altitude) / (l1.Altitude - l0.Altitude)
		wts := [2]float32{1 - t, t}
		return BlendWindLayers(wts[:], layers[i-1:i+1])
	}
}

type WindTreeNode struct {
	Children  [2]*WindTreeNode
	Location  math.Point2LL
	SplitAxis int
	Layers    []WindLayer
}

type WeatherModel struct {
	METAR              map[string]*METAR
	NmPerLongitude     float32
	MagneticVariation  float32
	WindRoot           *WindTreeNode
	StartTime          time.Time
	CurrentTime        time.Time
	GustStartTime      time.Time
	GustActiveDuration time.Duration
	GustCalmDuration   time.Duration
}

func MakeWeatherModel(airports []string, now time.Time, nmPerLongitude float32, magneticVariation float32,
	wind map[math.Point2LL][]WindLayer, lg *log.Logger) *WeatherModel {
	wm := &WeatherModel{
		MagneticVariation:  magneticVariation,
		NmPerLongitude:     nmPerLongitude,
		StartTime:          now,
		GustStartTime:      now,
		GustActiveDuration: 4 * time.Second, // Don't bother randomizing the first one
		GustCalmDuration:   26 * time.Second,
	}

	wind = wm.updateMETARs(airports, wind, lg)

	var nodes []*WindTreeNode
	for loc, layers := range wind {
		layers = slices.Clone(layers)

		// Sort layers by altitude
		slices.SortFunc(layers, func(a, b WindLayer) int { return int(a.Altitude - b.Altitude) })

		nodes = append(nodes, &WindTreeNode{Location: loc, Layers: layers})
	}

	var buildTree func(nodes []*WindTreeNode, axis int) *WindTreeNode
	buildTree = func(nodes []*WindTreeNode, axis int) *WindTreeNode {
		if len(nodes) == 0 {
			return nil
		}

		cur := nodes[0]

		// Partition
		var n0, n1 []*WindTreeNode
		for _, node := range nodes[1:] {
			if node.Location[axis] < cur.Location[axis] {
				n0 = append(n0, node)
			} else {
				n1 = append(n1, node)
			}
		}

		cur.SplitAxis = axis
		cur.Children[0] = buildTree(n0, (axis+1)%2)
		cur.Children[1] = buildTree(n1, (axis+1)%2)

		return cur
	}
	wm.WindRoot = buildTree(nodes, 0)

	return wm
}

func (w *WeatherModel) updateMETARs(airports []string, wind map[math.Point2LL][]WindLayer, lg *log.Logger) map[math.Point2LL][]WindLayer {
	if m, err := fetchMETARs(airports); err == nil {
		w.METAR = m
	} else {
		lg.Warnf("Error fetching METAR-randomizing: %v", err)
		r := rand.Make()

		if w.METAR == nil {
			w.METAR = make(map[string]*METAR)

			// Make up plausible METAR entries for the airports
			mbase := METAR{
				ReportTime:  time.Now().Format("2006-01-02 15:04:05"), // time.DateTime
				Temperature: float32(10 + r.Intn(10)),
				Altimeter:   float32(990 + r.Intn(1030-990)),
				RawText:     "TODO",
			}
			mbase.Wind.Direction = float32(10 * (1 + r.Intn(36)))
			mbase.Wind.Speed = float32(5 + r.Intn(20))

			if r.Bool() {
				mbase.Wind.Gust = mbase.Wind.Speed + 3 + float32(r.Intn(7))
			}
			mbase.Dewpoint = mbase.Temperature - float32(r.Intn(5))

			for _, ap := range airports {
				m := mbase
				m.ICAO = ap
				w.METAR[ap] = &m
			}
		}

		// Perturb them so they're not all the same
		for icao, m := range w.METAR {
			da := -3 + 6*r.Float32()                // altimeter (in hPa)
			dt := -2 + 4*r.Float32()                // temperature/dewpoint (C)
			ds := -3 + 6*r.Float32()                // windspeed
			dd := [3]float32{-10, 0, 10}[r.Intn(3)] // wind direction

			loc := av.DB.Airports[icao].Location
			m.Longitude = loc[0]
			m.Latitude = loc[1]
			m.Altimeter += da
			m.Temperature += dt
			m.Dewpoint += dt
			m.Wind.Speed = max(0, m.Wind.Speed+ds)
			if !m.Wind.Variable {
				m.Wind.Direction = math.NormalizeHeading(m.Wind.Direction + dd)
			}
		}
	}

	if len(wind) > 0 {
		// If the user provided wind, we'll override METAR winds with that; use the closest wind sample.
		for _, metar := range w.METAR {
			var mind float32
			var wl *WindLayer
			for p, layers := range wind {
				d := math.NMDistance2LL(p, metar.Location())
				if wl == nil || d < mind {
					mind = d
					wl = &layers[0]
					// The layers aren't necessarily sorted by altitude
					// yet, so check all of them to find the lowest one,
					// which we'll assume is at ground level.
					for _, l := range layers[1:] {
						if l.Altitude < wl.Altitude {
							wl = &l
						}
					}
				}
			}

			metar.Wind.Variable = wl.Direction == 0
			metar.Wind.Direction = wl.Direction
			metar.Wind.Speed = wl.Speed
			metar.Wind.Gust = wl.Gust
		}

		// TODO(maybe): update metar.RawText?
	} else {
		// Create ground-level wind samples from METAR wind.
		wind = make(map[math.Point2LL][]WindLayer)

		for _, metar := range w.METAR {
			ap := av.DB.Airports[metar.ICAO]
			wind[ap.Location] = []WindLayer{WindLayer{
				Altitude:        float32(ap.Elevation),
				Direction:       util.Select(metar.Wind.Variable, 0, metar.Wind.Direction),
				DirectionVector: math.SinCos(math.Radians(metar.Wind.Direction)),
				Speed:           metar.Wind.Speed,
				Gust:            metar.Wind.Gust,
			}}
		}
	}

	return wind
}

type WeatherSample struct {
	Altimeter   float32
	Temperature float32
	Dewpoint    float32
	Wind        WindSample
}

type WindSample struct {
	// Vector is the wind vector at the sample time, incorporating
	// gusting, if relevant. Note that it corresponds to the wind's
	// effect on an aircraft (e.g., if the wind heading is 090, the
	// vector will be pointed in the -x direction.)
	Vector    [2]float32
	Direction float32
	Speed     float32 // current speed in knots, including gusting.
	Gust      float32
}

func (w *WeatherModel) UpdateTime(t time.Time) {
	// TODO...
	w.CurrentTime = t
}

func (w *WeatherModel) GetMETAR(icao string) *METAR {
	return w.METAR[icao]
}

func (w *WeatherModel) LookupWX(p math.Point2LL, alt float32) WeatherSample {
	var ws WeatherSample

	// Altimeter/temp/dewpoint via METAR: currently we use all of them,
	// weighted by distance; this is probably not ideal.
	var sumwt float32
	for _, m := range w.METAR {
		d := math.NMDistance2LLFast(p, m.Location(), w.NmPerLongitude)
		wt := 1 / max(0.01, d)

		ws.Altimeter += wt * m.Altimeter
		ws.Temperature += wt * m.Temperature
		ws.Dewpoint += wt * m.Dewpoint
		sumwt += wt
	}
	ws.Altimeter /= sumwt
	ws.Temperature /= sumwt
	ws.Dewpoint /= sumwt

	ws.Wind = w.LookupWind(p, alt)

	return ws
}

func (w *WeatherModel) LookupWind(p math.Point2LL, alt float32) WindSample {
	// Wind: first find up to the three nearest sample points
	const nw = 3
	var wind [nw]*WindTreeNode
	var dist [nw]float32
	var search func(node *WindTreeNode)
	search = func(node *WindTreeNode) {
		if node == nil {
			return
		}

		d := math.NMDistance2LLFast(p, node.Location, w.NmPerLongitude)
		for i := range nw {
			if wind[i] == nil || d < dist[i] {
				// Sort by distance, low to high
				for j := nw - 1; j > i; j-- {
					wind[j], dist[j] = wind[j-1], dist[j-1]
				}
				wind[i] = node
				dist[i] = d
				break
			}
		}

		// Always recurse on the side of the lookup point; do this first to
		// try to bring down the maximum distance.
		below := p[node.SplitAxis] < node.Location[node.SplitAxis]
		if below {
			search(node.Children[0])
		} else {
			search(node.Children[1])
		}

		// Recurse on the other side if wind[]/dist[] aren't yet filled and
		// otherwise depending on the maximum distance to what we have
		// found compared to the distance to the split plane.
		recurse := wind[nw-1] == nil || math.Abs(p[node.SplitAxis]-node.Location[node.SplitAxis]) < dist[nw-1]
		if recurse && below {
			search(node.Children[1])
		} else if recurse {
			search(node.Children[0])
		}
	}
	search(w.WindRoot)

	var bwt [nw]float32
	var blayers [nw]WindLayer
	for i, wnd := range wind {
		if wnd != nil {
			blayers[i] = interpolateWind(alt, wnd.Layers)
			d := math.NMDistance2LLFast(p, wnd.Location, w.NmPerLongitude)
			bwt[i] = 1 / max(0.01, d)
		}
	}
	wl := BlendWindLayers(bwt[:], blayers[:])

	ws := WindSample{
		Direction: wl.Direction,
		Speed:     wl.Speed,
		Gust:      wl.Gust,
	}

	if ws.Gust > ws.Speed {
		// How far are we into the gust cycle?
		elapsed := w.CurrentTime.Sub(w.GustStartTime)

		// Don't bother randomizing the length of the ramp up and ramp down cycles.
		const rampUp = 2 * time.Second
		const rampDown = 4 * time.Second
		if elapsed < rampUp {
			// Lerp from the base speed to the gust speed over the ramp up period
			ws.Speed = math.Lerp(float32(elapsed.Seconds()/rampUp.Seconds()), ws.Speed, ws.Gust)
		} else if elapsed -= rampUp; elapsed < w.GustActiveDuration {
			// Gust is active: return fixed gust speed
			ws.Speed = wl.Gust
		} else if elapsed -= w.GustActiveDuration; elapsed < rampDown {
			// Ramping down: lerp back down to the base speed
			ws.Speed = math.Lerp(float32(elapsed.Seconds()/rampDown.Seconds()), ws.Gust, ws.Speed)
		} else if elapsed -= rampDown; elapsed < w.GustCalmDuration {
			// Calm period; leave the speed unchanged
		} else {
			// We've reached the end of the calm period; prepare for a new cycle.
			r := rand.Make()
			w.GustStartTime = w.CurrentTime
			w.GustActiveDuration = 3*time.Second + time.Duration(2*r.Float32()*float32(time.Second))
			w.GustCalmDuration = 5*time.Second + time.Duration(10*r.Float32()*float32(time.Second))
		}
	}

	// point the vector so it's how the aircraft is affected
	v := math.SinCos(math.Radians(math.OppositeHeading(ws.Direction)))
	ws.Vector = math.Scale2f(v, ws.Speed/3600) // knots -> per second

	return ws
}

///////////////////////////////////////////////////////////////////////////
// METAR

type METAR struct {
	ICAO        string  `json:"icaoId"`
	ReportTime  string  `json:"reportTime"`
	Temperature float32 `json:"temp"` // in Celcius
	Dewpoint    float32 `json:"dewp"` // in Celcius
	Wind        struct {
		Variable  bool
		Direction float32 // Only set/valid if Variable is false.
		Speed     float32 `json:"wspd"` // Wind speed in knots
		Gust      float32 `json:"wgst"` // Wind gusts in knots
	}
	Altimeter float32 `json:"altim"` // in hPa
	RawText   string  `json:"rawOb"` // Raw text of observation

	Latitude  float32 `json:"lat"`
	Longitude float32 `json:"lon"`

	// The JSON comes back sort of wonky; we decode into this but then
	// clean it up into the Wind member above.
	WindDirRaw any `json:"wdir"` // Wind direction in degrees or "VRB" for variable winds

	// Present in the data but not currently used
	//MetarId     int         `json:"metar_id"`
	//ReceiptTime string      `json:"receiptTime"`
	//ObsTime     int         `json:"obsTime"`
	//Visib string  `json:"visib"`
	//Slp        float64      `json:"slp"`
	//QcField    int          `json:"qcField"`
	//WxString   *string  `json:"wxString"` // Encoded present weather string
	//PresTend   float64      `json:"presTend"`
	//MaxT       *float64  `json:"maxT"` // Maximum temperature over last 6 hours in Celcius
	//MinT       *float64  `json:"minT"` // Minimum temperature over last 6 hours in Celcius
	//MaxT24     *float64  `json:"maxT24"` // Maximum temperature over last 24 hours in Celcius
	//MinT24     *float64  `json:"minT24"` // Minimum temperature over last 24 hours in Celcius
	//Precip     *float64  `json:"precip"` // Precipitation over last hour in inches
	//Pcp3Hr     *float64  `json:"pcp3hr"` // Precipitation over last 3 hours in inches
	//Pcp6Hr     *float64  `json:"pcp6hr"` // Precipitation over last 6 hours in inches
	//Pcp24Hr    *float64  `json:"pcp24hr"` // Precipitation over last 24 hours in inches
	//Snow       *float64  `json:"snow"` // Snow depth in inches
	//VertVis    *int  `json:"vertVis"` // Vertical visibility in feet
	//Elevation int     `json:"elev"`
	//MetarType  string       `json:"metarType"`
	//MostRecent int          `json:"mostRecent"`
	//Prior      int          `json:"prior"`
	//Name       string       `json:"name"`
	//Clouds     []cloudLayer `json:"clouds"`
}

func (m METAR) Location() math.Point2LL {
	return math.Point2LL{m.Longitude, m.Latitude}
}

func (m METAR) Altimeter_inHg() float32 {
	// Conversion formula (hectoPascal to Inch of Mercury): 29.92 * (hpa / 1013.2)
	return 0.02953 * m.Altimeter
}

func (m METAR) ShortString() string {
	// Try to peel off the ICAO code at the start and then all of the remarks.
	s := strings.IndexByte(m.RawText, ' ')
	e := strings.Index(m.RawText, "RMK")
	if s != -1 && e != -1 {
		return m.RawText[s+1 : e-1]
	} else {
		return m.RawText
	}
}

func fetchMETARs(icao []string) (map[string]*METAR, error) {
	m := make(map[string]*METAR)

	const aviationWeatherCenterDataApi = `https://aviationweather.gov/api/data/metar?ids=%s&format=json`
	query := url.QueryEscape(strings.Join(icao, ","))
	requestUrl := fmt.Sprintf(aviationWeatherCenterDataApi, query)

	res, err := http.Get(requestUrl)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	metars := make([]METAR, 0, len(icao))
	if err = json.NewDecoder(res.Body).Decode(&metars); err != nil {
		return nil, err
	}

	for _, met := range metars {
		if d, ok := met.WindDirRaw.(float64); ok {
			met.Wind.Direction = float32(d)
		} else {
			met.Wind.Variable = true
		}

		m[met.ICAO] = &met
	}

	return m, nil
}
