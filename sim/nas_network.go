// sim/nas_network.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"log/slog"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// NASNetwork manages all facility computers in a simulation.
// It is the top-level routing fabric for inter-facility messages.
type NASNetwork struct {
	ERAMComputers  map[string]*ERAMComputer  // ARTCC code -> computer (e.g. "ZNY")
	STARSComputers map[string]*STARSComputer // TRACON code -> computer (e.g. "N90")

	// parentERAM maps TRACON code -> ARTCC code for hierarchy lookup
	parentERAM map[string]string
}

// NewNASNetwork creates an empty NAS network.
func NewNASNetwork() *NASNetwork {
	return &NASNetwork{
		ERAMComputers:  make(map[string]*ERAMComputer),
		STARSComputers: make(map[string]*STARSComputer),
		parentERAM:     make(map[string]string),
	}
}

// PrimarySTARS returns the STARSComputer for the given facility.
func (n *NASNetwork) PrimarySTARS(facility string) *STARSComputer {
	return n.STARSComputers[facility]
}

// PrimaryERAM returns the ERAMComputer for the given facility's parent ARTCC.
func (n *NASNetwork) PrimaryERAM(facility string) *ERAMComputer {
	if artcc, ok := n.parentERAM[facility]; ok {
		return n.ERAMComputers[artcc]
	}
	// If the facility itself is an ARTCC
	return n.ERAMComputers[facility]
}

// STARSFor returns the STARSComputer for the given facility code, or nil
// if no such STARS exists in the network.
func (n *NASNetwork) STARSFor(facility string) *STARSComputer {
	return n.STARSComputers[facility]
}

// ERAMFor returns the ERAMComputer for the given facility code, or nil.
// If the code is a TRACON, returns the parent ERAM.
func (n *NASNetwork) ERAMFor(facility string) *ERAMComputer {
	if ec, ok := n.ERAMComputers[facility]; ok {
		return ec
	}
	if artcc, ok := n.parentERAM[facility]; ok {
		return n.ERAMComputers[artcc]
	}
	return nil
}

// Route delivers a NAS message to the correct facility computer's inbox.
func (n *NASNetwork) Route(msg NASMessage) {
	if sc, ok := n.STARSComputers[msg.ToFacility]; ok {
		sc.Inbox = append(sc.Inbox, msg)
		return
	}
	if ec, ok := n.ERAMComputers[msg.ToFacility]; ok {
		ec.Inbox = append(ec.Inbox, msg)
		return
	}
	// Unknown facility - silently drop
}

// isARTCC returns true if the facility code looks like an ARTCC identifier
// (3 characters starting with Z, e.g. "ZNY", "ZDC", "ZBW").
func isARTCC(facility string) bool {
	return len(facility) == 3 && strings.HasPrefix(facility, "Z")
}

// scenarioAirportKeys returns the ICAO codes of the scenario airports.
func scenarioAirportKeys(airports map[string]*av.Airport) []string {
	result := make([]string, 0, len(airports))
	for icao := range airports {
		result = append(result, icao)
	}
	return result
}

// buildScenarioAirportToTRACONMap builds a map from scenario airport ICAO
// to TRACON code, using geographic proximity to determine which TRACON
// each airport belongs to.
func buildScenarioAirportToTRACONMap(artcc string, scenarioAirports map[string]*av.Airport) map[string]string {
	result := make(map[string]string)
	for icao := range scenarioAirports {
		for traconCode, tracon := range av.DB.TRACONs {
			if tracon.ARTCC != artcc {
				continue
			}
			if ap, ok := av.DB.Airports[icao]; ok {
				if math.NMDistance2LL(ap.Location, tracon.Center()) <= float32(tracon.Radius) {
					result[icao] = traconCode
					break
				}
			}
		}
	}
	return result
}

// InitializeNetwork sets up the full NAS network for a sim based on the
// primary facility and its handoff topology. scenarioAirports contains only
// the airports defined in the active scenario, used to filter which airports
// the primary TRACON and ERAM know about.
func InitializeNetwork(
	facility string,
	scenarioAirports map[string]*av.Airport,
	localCodePool *av.LocalSquawkCodePool,
	fixPairs []FixPairDefinition,
	fixPairAssignments []FixPairAssignment,
	topology *HandoffTopology,
	lg *slog.Logger,
) *NASNetwork {
	net := NewNASNetwork()

	// Determine ARTCC for primary facility
	tracon, ok := av.DB.TRACONs[facility]
	if !ok {
		lg.Error("unknown TRACON in av.DB", slog.String("facility", facility))
		// Fall back: create minimal network
		primarySTARS := makeSTARSComputer(facility)
		net.STARSComputers[facility] = primarySTARS
		return net
	}
	artcc := tracon.ARTCC

	// 1. Create primary STARS computer
	primarySTARS := makeSTARSComputer(facility)
	primarySTARS.FixPairs = fixPairs
	primarySTARS.FixPairAssignments = fixPairAssignments
	primarySTARS.Airports = scenarioAirportKeys(scenarioAirports)
	net.STARSComputers[facility] = primarySTARS
	net.parentERAM[facility] = artcc

	// 2. Create parent ERAM computer
	parentERAM := makeERAMComputer(artcc, localCodePool)
	parentERAM.Airports = buildScenarioAirportToTRACONMap(artcc, scenarioAirports)
	parentERAM.Children[facility] = primarySTARS
	primarySTARS.ParentERAM = parentERAM
	net.ERAMComputers[artcc] = parentERAM

	// 3. Create neighbor computers from handoff topology
	if topology != nil {
		for _, neighbor := range topology.NeighboringFacilities {
			if isARTCC(neighbor) {
				// Peer ARTCC
				if _, exists := net.ERAMComputers[neighbor]; !exists {
					peerERAM := makeERAMComputer(neighbor, nil)
					peerERAM.Airports = make(map[string]string) // stub: no airports
					net.ERAMComputers[neighbor] = peerERAM
					parentERAM.Peers[neighbor] = peerERAM
					peerERAM.Peers[artcc] = parentERAM
					lg.Debug("created peer ERAM", slog.String("artcc", neighbor))
				}
			} else {
				// Neighbor TRACON
				if _, exists := net.STARSComputers[neighbor]; !exists {
					neighborSTARS := makeSTARSComputer(neighbor)
					net.STARSComputers[neighbor] = neighborSTARS

					// Determine parent ARTCC for this neighbor
					if nTracon, ok := av.DB.TRACONs[neighbor]; ok {
						nArtcc := nTracon.ARTCC
						net.parentERAM[neighbor] = nArtcc

						// Neighbor TRACONs get empty airport lists so ERAM
						// won't distribute FPs to them (they're not active).
						neighborSTARS.Airports = nil

						// Create the ERAM for this neighbor's ARTCC if needed
						if _, exists := net.ERAMComputers[nArtcc]; !exists {
							nERAM := makeERAMComputer(nArtcc, nil)
							nERAM.Airports = make(map[string]string) // stub: no airports
							net.ERAMComputers[nArtcc] = nERAM
							// Wire as peer to the primary ARTCC if different
							if nArtcc != artcc {
								parentERAM.Peers[nArtcc] = nERAM
								nERAM.Peers[artcc] = parentERAM
							}
						}

						// Wire child/parent
						nERAM := net.ERAMComputers[nArtcc]
						nERAM.Children[neighbor] = neighborSTARS
						neighborSTARS.ParentERAM = nERAM

						lg.Debug("created neighbor STARS",
							slog.String("tracon", neighbor),
							slog.String("artcc", nArtcc))
					}
				}
			}
		}
	}

	return net
}
