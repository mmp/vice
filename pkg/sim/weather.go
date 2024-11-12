package sim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type METAR struct {
	//MetarId     int         `json:"metar_id"`
	IcaoId string `json:"icaoId"` // ICAO identifier
	//ReceiptTime string      `json:"receiptTime"`
	//ObsTime     int         `json:"obsTime"`
	//ReportTime  string      `json:"reportTime"`
	//Temp        float64     `json:"temp"`
	//Dewp        float64     `json:"dewp"`
	Wdir any `json:"wdir"` // Wind direction in degrees or VRB for variable winds
	Wspd int `json:"wspd"` // Wind speed in knots
	Wgst int `json:"wgst"` // Wind gusts in knots
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
	//RawMETAR string `json:"rawOb"` // Raw text of observation
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

const vrb = -1

// GetWindDirection returns the wind direction in degrees or VRB for variable winds.
func (m METAR) GetWindDirection() int {
	if windDir, ok := m.Wdir.(int); ok {
		return windDir
	} else {
		return vrb
	}
}

// getWindInfo returns the wind direction and speed in METAR text format.
func (m METAR) getWindInfo() string {
	if m.Wspd <= 0 {
		return "00000KT"
	}

	if dir, ok := m.Wdir.(int); !ok {
		return fmt.Sprintf("VRB%02dKT", m.Wspd)
	} else {
		wind := fmt.Sprintf("%03d%02d", dir, m.Wspd)

		// According to Federal Meteorological Handbook No. 1 (FCM-H1-2019)
		//   Gusts are indicated by rapid fluctuations in wind speed
		//   with a variation of 10 knots or more between peaks and lulls.
		// The Aviation Weather Center reports gust values according to the above or revised definitions.
		if m.Wgst > 0 {
			wind += fmt.Sprintf("G%02d", m.Wgst)
		}

		return wind + "KT"
	}
}

// getAltimeter returns the altimeter setting in inches Hg
func (m METAR) getAltimeter() float64 {
	// Conversion formula (hectoPascal to Inch of Mercury): 29.92 * (hpa / 1013.2)
	return 0.02953 * m.Altim
}

const aviationWeatherCenterDataApi = `https://aviationweather.gov/api/data/metar?ids=%s&format=json`

func getWeather(icao ...string) ([]METAR, error) {
	var query string
	if len(icao) == 1 {
		query = icao[0]
	} else {
		query = url.QueryEscape(strings.Join(icao, ","))
	}

	requestUrl := fmt.Sprintf(aviationWeatherCenterDataApi, query)

	res, err := http.Get(requestUrl)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	data := make([]METAR, 0, len(icao))
	if err = json.NewDecoder(res.Body).Decode(&data); err != nil {
		return nil, err
	}

	return data, nil
}
