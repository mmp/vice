// cmd/checkwx/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// checkwx reports the weather that the scenarios use but that resources/wx
// lacks, as happens when scenarios are added without re-running the weather
// pipeline. It is run from the top of the repository.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

var scrapedFile = flag.String("scraped", "cmd/wxscrape/metar-airports.txt", "`file` listing the airports wxscrape fetches METAR for")

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	b, err := os.ReadFile(*scrapedFile)
	if err != nil {
		return err
	}
	scraped := strings.Fields(string(b))

	db.InitDB()
	fac, err := scenario.WXFacilities(log.New(false, "warn", ""))
	if err != nil {
		return err
	}

	r, err := readBundled(wx.METARFilename)
	if err != nil {
		return err
	}
	cm, err := wx.LoadCompressedMETAR(r)
	if err != nil {
		return fmt.Errorf("%s: %w", wx.METARFilename, err)
	}

	r, err = readBundled(wx.ManifestPath("atmos"))
	if err != nil {
		return err
	}
	manifest, err := wx.LoadManifest(r)
	if err != nil {
		return fmt.Errorf("%s: %w", wx.ManifestPath("atmos"), err)
	}

	// Re-running the pipeline only brings in METAR for the airports that
	// wxscrape fetches, whether for themselves or for a substitute.
	airports := util.FilterSlice(fac.Airports, func(ap string) bool {
		if donor, ok := wx.METARSubstitutes[ap]; ok {
			ap = donor
		}
		return slices.Contains(scraped, ap)
	})
	missingMETAR := util.FilterSlice(airports, func(ap string) bool { return !cm.HasAirport(ap) })

	facilities := slices.Concat(fac.TRACONs, fac.ARTCCs)
	missingAtmos := util.FilterSlice(facilities, func(f string) bool {
		_, ok := manifest.GetTimestamps(f)
		return !ok
	})

	if len(missingMETAR) == 0 && len(missingAtmos) == 0 {
		fmt.Printf("resources/wx has METAR for all %d fetched scenario airports and atmospheric data for all %d facilities\n",
			len(airports), len(facilities))
		return nil
	}

	msg := "resources/wx is missing weather that the scenarios use; run cmd/wxingest/cloudrun/run.sh to update it"
	if len(missingMETAR) > 0 {
		msg += "\n  METAR: " + strings.Join(missingMETAR, " ")
	}
	if len(missingAtmos) > 0 {
		msg += "\n  atmospheric data: " + strings.Join(missingAtmos, " ")
	}
	return errors.New(msg)
}

// readBundled returns a reader for the file at path within resources/wx.
func readBundled(path string) (*bytes.Reader, error) {
	b, err := fs.ReadFile(util.GetResourcesFS(), "wx/"+path)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}
