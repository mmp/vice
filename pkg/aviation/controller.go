// pkg/aviation/controller.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/pkg/util"
)

type Controller struct {
	Position           string    // This is the key in the controllers map in JSON
	RadioName          string    `json:"radio_name"`
	Frequency          Frequency `json:"frequency"`
	TCP                string    `json:"sector_id"`       // e.g. N56, 2J, ...
	Scope              string    `json:"scope_char"`      // Optional. If unset, facility id is used for external, last char of sector id for local.
	FacilityIdentifier string    `json:"facility_id"`     // For example the "N" in "N4P" showing the N90 TRACON
	ERAMFacility       bool      `json:"eram_facility"`   // To weed out N56 and N4P being the same fac
	Facility           string    `json:"facility"`        // So we can get the STARS facility from a controller
	DefaultAirport     string    `json:"default_airport"` // only required if CRDA is a thing
}

func (c Controller) IsExternal() bool {
	return c.ERAMFacility || c.FacilityIdentifier != ""
}

func (c Controller) Id() string {
	if c.ERAMFacility {
		return c.TCP
	}
	return c.FacilityIdentifier + c.TCP
}

// split -> config
type SplitConfigurationSet map[string]SplitConfiguration

// callsign -> controller contig
type SplitConfiguration map[string]*MultiUserController

type MultiUserController struct {
	Primary          bool     `json:"primary"`
	BackupController string   `json:"backup"`
	Departures       []string `json:"departures"`
	Arrivals         []string `json:"arrivals"` // TEMPORARY for inbound flows transition
	InboundFlows     []string `json:"inbound_flows"`
}

///////////////////////////////////////////////////////////////////////////
// SplitConfigurations

func (sc SplitConfigurationSet) GetConfiguration(split string) (SplitConfiguration, error) {
	if len(sc) == 1 {
		// ignore split
		for _, config := range sc {
			return config, nil
		}
	}

	config, ok := sc[split]
	if !ok {
		return nil, fmt.Errorf("%s: split not found", split)
	}
	return config, nil
}

func (sc SplitConfigurationSet) GetPrimaryController(split string) (string, error) {
	configs, err := sc.GetConfiguration(split)
	if err != nil {
		return "", err
	}

	for callsign, mc := range configs {
		if mc.Primary {
			return callsign, nil
		}
	}

	return "", fmt.Errorf("No primary controller in split")
}

func (sc SplitConfigurationSet) Len() int {
	return len(sc)
}

func (sc SplitConfigurationSet) Splits() []string {
	return util.SortedMapKeys(sc)
}

///////////////////////////////////////////////////////////////////////////
// SplitConfiguration

// ResolveController takes a controller callsign and returns the signed-in
// controller that is responsible for that position (possibly just the
// provided callsign).
func (sc SplitConfiguration) ResolveController(id string, active func(id string) bool) (string, error) {
	origId := id
	i := 0
	for {
		if ctrl, ok := sc[id]; !ok {
			return "", fmt.Errorf("%s: failed to find controller in MultiControllers", id)
		} else if ctrl.Primary || active(id) {
			return id, nil
		} else {
			id = ctrl.BackupController
		}

		i++
		if i == 20 {
			return "", fmt.Errorf("%s: unable to find controller backup", origId)
		}
	}
}

func (sc SplitConfiguration) GetInboundController(group string) (string, error) {
	for callsign, ctrl := range sc {
		if ctrl.IsInboundController(group) {
			return callsign, nil
		}
	}

	return "", fmt.Errorf("%s: couldn't find inbound controller", group)
}

func (sc SplitConfiguration) GetDepartureController(airport, runway, sid string) (string, error) {
	for callsign, ctrl := range sc {
		if ctrl.IsDepartureController(airport, runway, sid) {
			return callsign, nil
		}
	}

	return "", fmt.Errorf("%s/%s: couldn't find departure controller", airport, sid)
}

///////////////////////////////////////////////////////////////////////////
// MultiUserController

func (c *MultiUserController) IsDepartureController(ap, rwy, sid string) bool {
	for _, d := range c.Departures {
		depAirport, depSIDRwy, ok := strings.Cut(d, "/")
		if ok { // have a runway or SID
			if ap == depAirport && (rwy == depSIDRwy || sid == depSIDRwy) {
				return true
			}
		} else { // no runway/SID, so only match airport
			if ap == depAirport {
				return true
			}
		}
	}
	return false
}

func (c *MultiUserController) IsInboundController(group string) bool {
	return slices.Contains(c.InboundFlows, group)
}
