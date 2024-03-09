// scrapeartccs.go

// Scrapes AirNav to figure out which ARTCC covers each ATCT/TRACON.
// Result was subsequently manually tuned up.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
)

type TRACON struct {
	Name  string
	ARTCC string
}

func main() {
	var out map[string]TRACON
	f, err := os.Open("tracons.json")
	if err != nil {
		// First time
		f, err = os.Create("tracons.json")
		if err != nil {
			panic(err)
		}
		out = make(map[string]TRACON)
		for t, name := range tracons {
			out[t] = TRACON{Name: name, ARTCC: "TODO"}
		}
	} else if err := json.NewDecoder(f).Decode(&out); err != nil {
		panic(err)
	}
	f.Close()

	for name, tracon := range out {
		if tracon.ARTCC != "TODO" {
			continue
		}
		if strings.ContainsAny(name, "0123456789") {
			continue
		}

		c := colly.NewCollector(
			colly.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15"))

		c.OnRequest(func(r *colly.Request) {
			r.Headers.Set("Access-Control-Allow-Origin", "*")
			r.Headers.Set("Accept", "*/*")
			r.Headers.Set("Sec-Fetch-Site", "same-origin")
			r.Headers.Set("Accept-Language", "en-US,en;q=0.9")
			r.Headers.Set("Accept-Encoding", "gzip, deflate, br")
			r.Headers.Set("Sec-Fetch-Mode", "cors")
			r.Headers.Set("Access-Control-Allow-Credentials", "true")
			r.Headers.Set("Connection", "keep-alive")
			r.Headers.Set("Sec-Fetch-Dest", "empty")
		})

		c.OnHTML("tr", func(e *colly.HTMLElement) {
			if strings.HasPrefix(e.Text, "ARTCC:") {
				fmt.Printf("Got ARTCC: %s\n", e.Text[8:])
				out[name] = TRACON{
					Name:  out[name].Name,
					ARTCC: strings.TrimSpace(e.Text[8:]),
				}
			}
		})

		url := "https://www.airnav.com/airport/K" + name
		fmt.Printf("Visiting " + url + "\n")
		c.Visit(url)

		// Save the progress
		f, err := os.Create("tracons.json")
		if err != nil {
			panic(err)
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "    ")
		if err := enc.Encode(out); err != nil {
			panic(err)
		}
		f.Close()

		time.Sleep(36 * time.Second)
	}

}

// https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/tracon
var tracons = map[string]string{
	"AAC": "Academy Tower",
	"ABE": "Allentown Tower",
	"ABI": "Abilene Tower",
	"ABQ": "Albuquerque Tower",
	"ACT": "Waco Tower",
	"ACY": "Atlantic City Tower",
	"AGS": "Augusta Tower",
	"ALB": "Albany Tower",
	"ALO": "Waterloo Tower",
	"AMA": "Amarillo Tower",
	"ASE": "Aspen Tower",
	"AUS": "Austin Tower",
	"AVL": "Asheville Tower",
	"AVP": "Wilkes-Barre Tower",
	"AZO": "Kalamazoo Tower",
	"BFL": "Bakersfield Tower",
	"BGM": "Binghamton Tower",
	"BGR": "Bangor Tower",
	"BHM": "Birmingham Tower",
	"BIL": "Billings Tower",
	"BIS": "Bismarck Tower",
	"BNA": "Nashville Tower",
	"BOI": "Boise Tower",
	"BTR": "Baton Rouge Tower",
	"BTV": "Burlington Tower",
	"BUF": "Buffalo Tower",
	"CAE": "Columbia Tower",
	"CAK": "Akron-Canton Tower",
	"CHA": "Chatanooga Tower",
	"CHS": "Charleston Tower",
	"CID": "Cedar Rapids Tower",
	"CKB": "Clarksburg Tower",
	"CLE": "Cleveland Tower",
	"CLT": "Charlotte Tower",
	"CMH": "Columbus Tower",
	"CMI": "Champaign Tower",
	"COS": "Colorado Springs Tower",
	"CPR": "Casper Tower",
	"CRP": "Corpus Christi Tower",
	"CRW": "Charleston Tower",
	"CVG": "Cincinnati Tower",
	"DAB": "Daytona Beach Tower",
	"DAY": "Dayton Tower",
	"DLH": "Duluth Tower",
	"DSM": "Des Moines Tower",
	"ELM": "Elmira Tower",
	"ELP": "El Paso Tower",
	"ERI": "Erie Tower",
	"EUG": "Eugene Tower",
	"EVV": "Evansville Tower",
	"FAI": "Fairbanks Tower",
	"FAR": "Fargo Tower",
	"FAT": "Fresno Tower",
	"FAY": "Fayetteville Tower",
	"FLO": "Florence Tower",
	"FNT": "Flint Tower",
	"FSD": "Sioux Falls Tower",
	"FSM": "Fort Smith Tower",
	"FWA": "Fort Wayne Tower",
	"GEG": "Spokane Tower",
	"GGG": "Longview Tower",
	"GPT": "Gulfport Tower",
	"GRB": "Green Bay Tower",
	"GRR": "Grand Rapids Tower",
	"GSO": "Greensboro Tower",
	"GSP": "Greer Tower",
	"GTF": "Great Falls Tower",
	"HLN": "Helena Tower",
	"HSV": "Huntsville Tower",
	"HTS": "Huntington Tower",
	"HUF": "Terre Haute/Hulman ATCT/TRACON",
	"ICT": "Wichita Tower",
	"ILM": "Wilmington Tower",
	"IND": "Indianapolis Tower",
	"ITO": "Hilo Tower",
	"JAN": "Jackson Tower",
	"JAX": "Jacksonville Tower",
	"LAN": "Lansing Tower",
	"LBB": "Lubbock Tower",
	"LCH": "Lake Charles Tower",
	"LEX": "Lexington Tower",
	"LFT": "Lafayette Tower",
	"LIT": "Little Rock Tower",
	"MAF": "Midland Tower",
	"MBS": "Saginaw Tower",
	"MCI": "Kansas City Tower",
	"MDT": "Harrisburg Intl Tower",
	"MFD": "Mansfield Tower",
	"MGM": "Montgomery Tower",
	"MIA": "Miami Tower",
	"MKE": "Milwaukee Tower",
	"MKG": "Muskegon Tower",
	"MLI": "Quad City Tower",
	"MLU": "Monroe Tower",
	"MOB": "Mobile Tower",
	"MSN": "Madison Tower",
	"MSY": "New Orleans Tower",
	"MWH": "Grant County Tower",
	"MYR": "Myrtle Beach Tower",
	"OKC": "Oklahoma City Tower",
	"ORF": "Norfolk Tower",
	"PBI": "Palm Beach Tower",
	"PHL": "Philadelphia Tower",
	"PIA": "Peoria Tower",
	"PIT": "Pittsburgh Tower",
	"PSC": "Pasco Tower",
	"PVD": "Providence Tower",
	"PWM": "Portland Tower",
	"RDG": "Reading Tower",
	"RDU": "Raleigh-Durham Tower",
	"RFD": "Rockford Tower",
	"ROA": "Roanoke Tower",
	"ROC": "Rochester Tower",
	"ROW": "Roswell Tower",
	"RST": "Rochester Tower",
	"RSW": "Fort Myers Tower",
	"SAT": "San Antonio Tower",
	"SAV": "Savannah Tower",
	"SBA": "Santa Barbara Tower",
	"SBN": "South Bend Tower",
	"SDF": "Standiford Tower",
	"SGF": "Springfield Tower",
	"SHV": "Shreveport Tower",
	"SPI": "Springfield Tower",
	"SUX": "Sioux Gateway Tower",
	"SYR": "Syracuse Tower",
	"TLH": "Tallahassee Tower",
	"TOL": "Toledo Tower",
	"TPA": "Tampa Tower",
	"TRI": "Tri-Cities Tower",
	"TUL": "Tulsa Tower",
	"TWF": "Twin Falls Tower",
	"TYS": "Knoxville Tower",
	"YNG": "Youngstown Tower",
	"A11": "Anchorage TRACON",
	"A80": "Atlanta TRACON",
	"A90": "Boston TRACON",
	"C90": "Chicago TRACON",
	"D01": "Denver TRACON",
	"D10": "Dallas/Ft Worth TRACON",
	"D21": "Detroit TRACON",
	"F11": "Central Florida TRACON",
	"I90": "Houston TRACON",
	"L30": "Las Vegas TRACON",
	"M03": "Memphis TRACON",
	"M98": "Minneapolis TRACON",
	"N90": "New York TRACON",
	"NCT": "NorCal TRACON",
	"NMM": "Meridian TRACON",
	"P31": "Pensacola TRACON",
	"P50": "Phoenix TRACON",
	"P80": "Portland TRACON",
	"PCT": "Potomac TRACON",
	"R90": "Omaha TRACON",
	"S46": "Seattle TRACON",
	"S56": "Salt Lake City TRACON",
	"SCT": "SoCal TRACON",
	"T75": "St Louis TRACON",
	"U90": "Tucson TRACON",
	"Y90": "Yankee TRACON",
}
