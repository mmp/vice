// scrapemva.go
// download the latest XML MVA charts from the FAA

package main

import (
	"fmt"
	"strings"

	"github.com/gocolly/colly/v2"
)

func main() {
	c := colly.NewCollector()

	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		link := e.Attr("href")
		if strings.HasSuffix(link, ".xml") {
			c.Visit(link)
		}
	})

	c.OnResponse(func(r *colly.Response) {
		if !strings.HasSuffix(r.FileName(), ".xml") {
			return
		}

		if err := r.Save(r.FileName()); err != nil {
			fmt.Printf("%s: %v\n", r.FileName(), err)
		} else {
			fmt.Printf("%s: saved %d bytes\n", r.FileName(), len(r.Body))
		}
	})

	c.Visit("https://www.faa.gov/air_traffic/flight_info/aeronav/digital_products/mva_mia/mva/")
}
