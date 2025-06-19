// pkg/aviation/weather.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////
// Wind

type Wind struct {
	Variable  bool
	Direction int `json:"direction"`
	Speed     int `json:"speed"`
	Gust      int `json:"gust"`
}

func (w Wind) String() string {
	if w.Speed <= 0 {
		return "00000KT"
	} else if w.Variable {
		return fmt.Sprintf("VRB%02dKT", w.Speed)
	} else {
		wind := fmt.Sprintf("%03d%02d", w.Direction, w.Speed)

		// According to Federal Meteorological Handbook No. 1 (FCM-H1-2019)
		//   Gusts are indicated by rapid fluctuations in wind speed
		//   with a variation of 10 knots or more between peaks and lulls.
		// The Aviation Weather Center reports gust values according to the above or revised definitions.
		if w.Gust > 0 {
			wind += fmt.Sprintf("G%02d", w.Gust)
		}

		return wind + "KT"
	}
}

func (w Wind) Randomize(r *rand.Rand) Wind {
	w.Speed += -3 + r.Intn(6)
	if w.Speed < 0 {
		w.Speed = 0
	} else if w.Speed < 4 {
		w.Variable = true
	} else {
		dir := 10 * ((w.Direction + 5) / 10)
		dir += [3]int{-10, 0, 10}[r.Intn(3)]
		w.Direction = dir
		gst := w.Gust - 3 + r.Intn(6)
		if gst-w.Speed > 5 {
			w.Gust = gst
		}
	}
	return w
}

type WindModel interface {
	GetWindVector(p math.Point2LL, alt float32) [2]float32
}

///////////////////////////////////////////////////////////////////////////
// METAR

type METAR struct {
	AirportICAO string
	Time        string
	Auto        bool
	Wind        Wind `json:"wind"` // WAR changing this from a strong to deserialization doesn't fail.
	Altimeter   string
	Weather     string
	Rmk         string
}

func (m METAR) String() string {
	auto := util.Select(m.Auto, "AUTO", "")
	return strings.Join([]string{m.AirportICAO, m.Time, auto, m.Wind.String(), m.Weather, m.Altimeter, m.Rmk}, " ")
}

type avWeatherMETAR struct {
	//MetarId     int         `json:"metar_id"`
	IcaoId string `json:"icaoId"` // ICAO identifier
	//ReceiptTime string      `json:"receiptTime"`
	//ObsTime     int         `json:"obsTime"`
	//ReportTime  string      `json:"reportTime"`
	//Temp        float64     `json:"temp"`
	//Dewp        float64     `json:"dewp"`
	WindDir   any `json:"wdir"` // Wind direction in degrees or VRB for variable winds
	WindSpeed int `json:"wspd"` // Wind speed in knots
	WindGust  int `json:"wgst"` // Wind gusts in knots
	//Visib string  `json:"visib"`
	Altim float64 `json:"altim"` // Altimeter setting in hectoPascals
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
	//MetarType  string       `json:"metarType"`
	//avWeatherMETAR string `json:"rawOb"` // Raw text of observation
	//MostRecent int          `json:"mostRecent"`
	//Lat        float64      `json:"lat"`
	//Lon        float64      `json:"lon"`
	//Elev       int          `json:"elev"`
	//Prior      int          `json:"prior"`
	//Name       string       `json:"name"`
	//Clouds     []cloudLayer `json:"clouds"`
}

/*
type cloudLayer struct {
	Base  int64  `json:"base"`
	Cover string `json:"cover"`
}
*/

// GetWindDirection returns the wind direction in degrees or VRB for variable winds.
func (m avWeatherMETAR) WindDirection() (vrb bool, direction int) {
	if d, ok := m.WindDir.(float64); ok {
		direction = int(d)
	} else {
		vrb = true
	}
	return
}

// getAltimeter returns the altimeter setting in inches Hg
func (m avWeatherMETAR) Altimeter() float64 {
	// Conversion formula (hectoPascal to Inch of Mercury): 29.92 * (hpa / 1013.2)
	return 0.02953 * m.Altim
}

const aviationWeatherCenterDataApi = `https://aviationweather.gov/api/data/metar?ids=%s&format=json`

func GetWeather(icao ...string) ([]METAR, error) {
	query := url.QueryEscape(strings.Join(icao, ","))
	requestUrl := fmt.Sprintf(aviationWeatherCenterDataApi, query)

	res, err := http.Get(requestUrl)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	av := make([]avWeatherMETAR, 0, len(icao))
	if err = json.NewDecoder(res.Body).Decode(&av); err != nil {
		return nil, err
	}

	// Convert to our representation
	metar := util.MapSlice(av, func(m avWeatherMETAR) METAR {
		metar := METAR{
			AirportICAO: m.IcaoId,
			Altimeter:   fmt.Sprintf("A%d", int(m.Altimeter()*100)),
		}
		metar.Wind.Variable, metar.Wind.Direction = m.WindDirection()
		metar.Wind.Speed = m.WindSpeed
		metar.Wind.Gust = m.WindGust

		return metar
	})

	return metar, nil
}
