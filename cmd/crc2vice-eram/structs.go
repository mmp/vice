// cmd/crc2vice-eram/structs.go

package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type Output map[string]ERAMMap // ARTCC Map Category -> GeoMaps

type ARTCC struct {
	ID            string    `json:"id"`
	LastUpdatedAt time.Time `json:"lastUpdatedAt"`
	Facility      struct {
		ID              string `json:"id"`
		Type            string `json:"type"`
		Name            string `json:"name"`
		ChildFacilities []struct {
			ID              string `json:"id"`
			Type            string `json:"type"`
			Name            string `json:"name"`
			ChildFacilities []struct {
				ID                    string `json:"id"`
				Type                  string `json:"type"`
				Name                  string `json:"name"`
				ChildFacilities       []any  `json:"childFacilities"`
				TowerCabConfiguration struct {
					VideoMapID                string  `json:"videoMapId"`
					DefaultRotation           float32 `json:"defaultRotation"`
					DefaultZoomRange          float32 `json:"defaultZoomRange"`
					AircraftVisibilityCeiling float32 `json:"aircraftVisibilityCeiling"`
					TowerLocation             struct {
						Lat float64 `json:"lat"`
						Lon float64 `json:"lon"`
					} `json:"towerLocation"`
				} `json:"towerCabConfiguration"`
				AsdexConfiguration struct {
					VideoMapID              string  `json:"videoMapId"`
					DefaultRotation         float32 `json:"defaultRotation"`
					DefaultZoomRange        float32 `json:"defaultZoomRange"`
					TargetVisibilityRange   float32 `json:"targetVisibilityRange"`
					TargetVisibilityCeiling float32 `json:"targetVisibilityCeiling"`
					FixRules                []struct {
						ID            string `json:"id"`
						SearchPattern string `json:"searchPattern"`
						FixID         string `json:"fixId"`
					} `json:"fixRules"`
					UseDestinationIDAsFix bool `json:"useDestinationIdAsFix"`
					RunwayConfigurations  []struct {
						ID                   string   `json:"id"`
						Name                 string   `json:"name"`
						ArrivalRunwayIds     []string `json:"arrivalRunwayIds"`
						DepartureRunwayIds   []string `json:"departureRunwayIds"`
						HoldShortRunwayPairs []any    `json:"holdShortRunwayPairs"`
					} `json:"runwayConfigurations"`
					Positions []struct {
						ID        string `json:"id"`
						Name      string `json:"name"`
						RunwayIds []any  `json:"runwayIds"`
					} `json:"positions"`
					DefaultPositionID string `json:"defaultPositionId"`
					TowerLocation     struct {
						Lat float64 `json:"lat"`
						Lon float64 `json:"lon"`
					} `json:"towerLocation"`
				} `json:"asdexConfiguration,omitempty"`
				TdlsConfiguration struct {
					MandatorySid         bool `json:"mandatorySid"`
					MandatoryClimbout    bool `json:"mandatoryClimbout"`
					MandatoryClimbvia    bool `json:"mandatoryClimbvia"`
					MandatoryInitialAlt  bool `json:"mandatoryInitialAlt"`
					MandatoryDepFreq     bool `json:"mandatoryDepFreq"`
					MandatoryExpect      bool `json:"mandatoryExpect"`
					MandatoryContactInfo bool `json:"mandatoryContactInfo"`
					MandatoryLocalInfo   bool `json:"mandatoryLocalInfo"`
					Sids                 []struct {
						Name        string `json:"name"`
						ID          string `json:"id"`
						Transitions []struct {
							Name                string `json:"name"`
							ID                  string `json:"id"`
							FirstRoutePofloat32 string `json:"firstRoutePofloat32"`
							DefaultExpect       string `json:"defaultExpect"`
							DefaultDepFreq      string `json:"defaultDepFreq"`
							DefaultContactInfo  string `json:"defaultContactInfo"`
							DefaultLocalInfo    string `json:"defaultLocalInfo"`
						} `json:"transitions"`
					} `json:"sids"`
					Climbouts []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"climbouts"`
					Climbvias []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"climbvias"`
					InitialAlts []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"initialAlts"`
					DepFreqs []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"depFreqs"`
					Expects []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"expects"`
					ContactInfos []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"contactInfos"`
					LocalInfos []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"localInfos"`
				} `json:"tdlsConfiguration,omitempty"`
				FlightStripsConfiguration struct {
					StripBays []struct {
						ID            string  `json:"id"`
						Name          string  `json:"name"`
						NumberOfRacks float32 `json:"numberOfRacks"`
					} `json:"stripBays"`
					ExternalBays []struct {
						FacilityID string `json:"facilityId"`
						BayID      string `json:"bayId"`
					} `json:"externalBays"`
					DisplayDestinationAirportIds     bool `json:"displayDestinationAirportIds"`
					DisplayBarcodes                  bool `json:"displayBarcodes"`
					EnableArrivalStrips              bool `json:"enableArrivalStrips"`
					EnableSeparateArrDepPrfloat32ers bool `json:"enableSeparateArrDepPrfloat32ers"`
					LockSeparators                   bool `json:"lockSeparators"`
				} `json:"flightStripsConfiguration"`
				Positions []struct {
					ID                 string  `json:"id"`
					Name               string  `json:"name"`
					Starred            bool    `json:"starred"`
					RadioName          string  `json:"radioName"`
					Callsign           string  `json:"callsign"`
					Frequency          float32 `json:"frequency"`
					StarsConfiguration struct {
						Subset   float32 `json:"subset"`
						SectorID string  `json:"sectorId"`
						AreaID   string  `json:"areaId"`
						ColorSet string  `json:"colorSet"`
						TCPID    string  `json:"tcpId"`
					} `json:"starsConfiguration"`
					TransceiverIds []string `json:"transceiverIds"`
				} `json:"positions"`
				NeighboringFacilityIds []string `json:"neighboringFacilityIds"`
				NonNasFacilityIds      []any    `json:"nonNasFacilityIds"`
			} `json:"childFacilities"`
			StarsConfiguration struct {
				Areas []struct {
					ID               string `json:"id"`
					Name             string `json:"name"`
					VisibilityCenter struct {
						Lat float64 `json:"lat"`
						Lon float64 `json:"lon"`
					} `json:"visibilityCenter"`
					SurveillanceRange       float32  `json:"surveillanceRange"`
					UnderlyingAirports      []string `json:"underlyingAirports"`
					SsaAirports             []string `json:"ssaAirports"`
					TowerListConfigurations []struct {
						ID        string  `json:"id"`
						AirportID string  `json:"airportId"`
						Range     float32 `json:"range"`
					} `json:"towerListConfigurations"`
					LdbBeaconCodesInhibited          bool `json:"ldbBeaconCodesInhibited"`
					PdbGroundSpeedInhibited          bool `json:"pdbGroundSpeedInhibited"`
					DisplayRequestedAltInFdb         bool `json:"displayRequestedAltInFdb"`
					UseVfrPositionSymbol             bool `json:"useVfrPositionSymbol"`
					ShowDestinationDepartures        bool `json:"showDestinationDepartures"`
					ShowDestinationSatelliteArrivals bool `json:"showDestinationSatelliteArrivals"`
					ShowDestinationPrimaryArrivals   bool `json:"showDestinationPrimaryArrivals"`
				} `json:"areas"`
				float32ernalAirports []string `json:"float32ernalAirports"`
				BeaconCodeBanks      []struct {
					ID     string  `json:"id"`
					Type   string  `json:"type"`
					Subset float32 `json:"subset"`
					Start  float32 `json:"start"`
					End    float32 `json:"end"`
				} `json:"beaconCodeBanks"`
				Rpcs []struct {
					ID                    string  `json:"id"`
					Index                 float32 `json:"index"`
					AirportID             string  `json:"airportId"`
					PositionSymbolTie     string  `json:"positionSymbolTie"`
					PositionSymbolStagger string  `json:"positionSymbolStagger"`
					MasterRunway          struct {
						RunwayID                 string  `json:"runwayId"`
						HeadingTolerance         float32 `json:"headingTolerance"`
						NearSideHalfWidth        float32 `json:"nearSideHalfWidth"`
						FarSideHalfWidth         float32 `json:"farSideHalfWidth"`
						NearSideDistance         float64 `json:"nearSideDistance"`
						RegionLength             float32 `json:"regionLength"`
						TargetReferencePofloat32 struct {
							Lat float64 `json:"lat"`
							Lon float64 `json:"lon"`
						} `json:"targetReferencePofloat32"`
						TargetReferenceLineHeading       float32 `json:"targetReferenceLineHeading"`
						TargetReferenceLineLength        float32 `json:"targetReferenceLineLength"`
						TargetReferencePofloat32Altitude float32 `json:"targetReferencePofloat32Altitude"`
						ImageReferencePofloat32          struct {
							Lat float64 `json:"lat"`
							Lon float64 `json:"lon"`
						} `json:"imageReferencePofloat32"`
						ImageReferenceLineHeading float64 `json:"imageReferenceLineHeading"`
						ImageReferenceLineLength  float32 `json:"imageReferenceLineLength"`
						TieModeOffset             float64 `json:"tieModeOffset"`
						DescentPofloat32Distance  float64 `json:"descentPofloat32Distance"`
						DescentPofloat32Altitude  float32 `json:"descentPofloat32Altitude"`
						AbovePathTolerance        float32 `json:"abovePathTolerance"`
						BelowPathTolerance        float32 `json:"belowPathTolerance"`
						DefaultLeaderDirection    string  `json:"defaultLeaderDirection"`
						ScratchpadPatterns        []any   `json:"scratchpadPatterns"`
					} `json:"masterRunway"`
					SlaveRunway struct {
						RunwayID                 string  `json:"runwayId"`
						HeadingTolerance         float32 `json:"headingTolerance"`
						NearSideHalfWidth        float32 `json:"nearSideHalfWidth"`
						FarSideHalfWidth         float32 `json:"farSideHalfWidth"`
						NearSideDistance         float32 `json:"nearSideDistance"`
						RegionLength             float32 `json:"regionLength"`
						TargetReferencePofloat32 struct {
							Lat float64 `json:"lat"`
							Lon float64 `json:"lon"`
						} `json:"targetReferencePofloat32"`
						TargetReferenceLineHeading       float64 `json:"targetReferenceLineHeading"`
						TargetReferenceLineLength        float32 `json:"targetReferenceLineLength"`
						TargetReferencePofloat32Altitude float32 `json:"targetReferencePofloat32Altitude"`
						ImageReferencePofloat32          struct {
							Lat float64 `json:"lat"`
							Lon float64 `json:"lon"`
						} `json:"imageReferencePofloat32"`
						ImageReferenceLineHeading float32 `json:"imageReferenceLineHeading"`
						ImageReferenceLineLength  float32 `json:"imageReferenceLineLength"`
						TieModeOffset             float64 `json:"tieModeOffset"`
						DescentPofloat32Distance  float64 `json:"descentPofloat32Distance"`
						DescentPofloat32Altitude  float32 `json:"descentPofloat32Altitude"`
						AbovePathTolerance        float32 `json:"abovePathTolerance"`
						BelowPathTolerance        float32 `json:"belowPathTolerance"`
						DefaultLeaderDirection    string  `json:"defaultLeaderDirection"`
						ScratchpadPatterns        []any   `json:"scratchpadPatterns"`
					} `json:"slaveRunway"`
				} `json:"rpcs"`
				PrimaryScratchpadRules []struct {
					ID            string   `json:"id"`
					AirportIds    []string `json:"airportIds"`
					SearchPattern string   `json:"searchPattern"`
					Template      string   `json:"template"`
					MinAltitude   float32  `json:"minAltitude,omitempty"`
					MaxAltitude   float32  `json:"maxAltitude,omitempty"`
				} `json:"primaryScratchpadRules"`
				SecondaryScratchpadRules  []any `json:"secondaryScratchpadRules"`
				RnavPatterns              []any `json:"rnavPatterns"`
				Allow4CharacterScratchpad bool  `json:"allow4CharacterScratchpad"`
				StarsHandoffIds           []struct {
					ID            string  `json:"id"`
					FacilityID    string  `json:"facilityId"`
					HandoffNumber float32 `json:"handoffNumber"`
				} `json:"starsHandoffIds"`
				VideoMapIds []string `json:"videoMapIds"`
				MapGroups   []struct {
					ID     string   `json:"id"`
					MapIds []any    `json:"mapIds"`
					Tcps   []string `json:"tcps"`
				} `json:"mapGroups"`
				AtpaVolumes []struct {
					ID              string `json:"id"`
					AirportID       string `json:"airportId"`
					VolumeID        string `json:"volumeId"`
					Name            string `json:"name"`
					RunwayThreshold struct {
						Lat float64 `json:"lat"`
						Lon float64 `json:"lon"`
					} `json:"runwayThreshold"`
					Ceiling                          float32 `json:"ceiling"`
					Floor                            float32 `json:"floor"`
					MagneticHeading                  float32 `json:"magneticHeading"`
					MaximumHeadingDeviation          float32 `json:"maximumHeadingDeviation"`
					Length                           float32 `json:"length"`
					WidthLeft                        float32 `json:"widthLeft"`
					WidthRight                       float32 `json:"widthRight"`
					TwoPofloat32FiveApproachDistance float32 `json:"twoPofloat32FiveApproachDistance"`
					TwoPofloat32FiveApproachEnabled  bool    `json:"twoPofloat32FiveApproachEnabled"`
					Scratchpads                      []struct {
						ID               string `json:"id"`
						Entry            string `json:"entry"`
						ScratchPadNumber string `json:"scratchPadNumber"`
						Type             string `json:"type"`
					} `json:"scratchpads"`
					Tcps []struct {
						ID       string `json:"id"`
						TCP      string `json:"tcp"`
						TCPID    string `json:"tcpId"`
						ConeType string `json:"coneType"`
					} `json:"tcps"`
					TCPExclusions    []any `json:"tcpExclusions"`
					ExcludedTCPIds   []any `json:"excludedTcpIds"`
					LeaderDirections []any `json:"leaderDirections"`
				} `json:"atpaVolumes"`
				RecatEnabled           bool  `json:"recatEnabled"`
				Lists                  []any `json:"lists"`
				ConfigurationPlans     []any `json:"configurationPlans"`
				AutomaticConsolidation bool  `json:"automaticConsolidation"`
				Tcps                   []struct {
					Subset   float32 `json:"subset"`
					SectorID string  `json:"sectorId"`
					ID       string  `json:"id"`
				} `json:"tcps"`
			} `json:"starsConfiguration,omitempty"`
			FlightStripsConfiguration struct {
				StripBays []struct {
					ID            string  `json:"id"`
					Name          string  `json:"name"`
					NumberOfRacks float32 `json:"numberOfRacks"`
				} `json:"stripBays"`
				ExternalBays                     []any `json:"externalBays"`
				DisplayDestinationAirportIds     bool  `json:"displayDestinationAirportIds"`
				DisplayBarcodes                  bool  `json:"displayBarcodes"`
				EnableArrivalStrips              bool  `json:"enableArrivalStrips"`
				EnableSeparateArrDepPrfloat32ers bool  `json:"enableSeparateArrDepPrfloat32ers"`
				LockSeparators                   bool  `json:"lockSeparators"`
			} `json:"flightStripsConfiguration"`
			Positions []struct {
				ID                 string  `json:"id"`
				Name               string  `json:"name"`
				Starred            bool    `json:"starred"`
				RadioName          string  `json:"radioName"`
				Callsign           string  `json:"callsign"`
				Frequency          float32 `json:"frequency"`
				StarsConfiguration struct {
					Subset   float32 `json:"subset"`
					SectorID string  `json:"sectorId"`
					AreaID   string  `json:"areaId"`
					ColorSet string  `json:"colorSet"`
					TCPID    string  `json:"tcpId"`
				} `json:"starsConfiguration"`
				TransceiverIds []string `json:"transceiverIds"`
			} `json:"positions"`
			NeighboringFacilityIds []string `json:"neighboringFacilityIds"`
			NonNasFacilityIds      []any    `json:"nonNasFacilityIds"`
			TowerCabConfiguration  struct {
				VideoMapID                string  `json:"videoMapId"`
				DefaultRotation           float32 `json:"defaultRotation"`
				DefaultZoomRange          float32 `json:"defaultZoomRange"`
				AircraftVisibilityCeiling float32 `json:"aircraftVisibilityCeiling"`
				TowerLocation             struct {
					Lat float64 `json:"lat"`
					Lon float64 `json:"lon"`
				} `json:"towerLocation"`
			} `json:"towerCabConfiguration,omitempty"`
			AsdexConfiguration struct {
				VideoMapID              string  `json:"videoMapId"`
				DefaultRotation         float32 `json:"defaultRotation"`
				DefaultZoomRange        float32 `json:"defaultZoomRange"`
				TargetVisibilityRange   float32 `json:"targetVisibilityRange"`
				TargetVisibilityCeiling float32 `json:"targetVisibilityCeiling"`
				FixRules                []struct {
					ID            string `json:"id"`
					SearchPattern string `json:"searchPattern"`
					FixID         string `json:"fixId"`
				} `json:"fixRules"`
				UseDestinationIDAsFix bool `json:"useDestinationIdAsFix"`
				RunwayConfigurations  []struct {
					ID                   string   `json:"id"`
					Name                 string   `json:"name"`
					ArrivalRunwayIds     []string `json:"arrivalRunwayIds"`
					DepartureRunwayIds   []string `json:"departureRunwayIds"`
					HoldShortRunwayPairs []any    `json:"holdShortRunwayPairs"`
				} `json:"runwayConfigurations"`
				Positions []struct {
					ID        string `json:"id"`
					Name      string `json:"name"`
					RunwayIds []any  `json:"runwayIds"`
				} `json:"positions"`
				DefaultPositionID string `json:"defaultPositionId"`
				TowerLocation     struct {
					Lat float64 `json:"lat"`
					Lon float64 `json:"lon"`
				} `json:"towerLocation"`
			} `json:"asdexConfiguration,omitempty"`
			TdlsConfiguration struct {
				MandatorySid         bool `json:"mandatorySid"`
				MandatoryClimbout    bool `json:"mandatoryClimbout"`
				MandatoryClimbvia    bool `json:"mandatoryClimbvia"`
				MandatoryInitialAlt  bool `json:"mandatoryInitialAlt"`
				MandatoryDepFreq     bool `json:"mandatoryDepFreq"`
				MandatoryExpect      bool `json:"mandatoryExpect"`
				MandatoryContactInfo bool `json:"mandatoryContactInfo"`
				MandatoryLocalInfo   bool `json:"mandatoryLocalInfo"`
				Sids                 []struct {
					Name        string `json:"name"`
					ID          string `json:"id"`
					Transitions []struct {
						Name               string `json:"name"`
						ID                 string `json:"id"`
						DefaultExpect      string `json:"defaultExpect"`
						DefaultInitialAlt  string `json:"defaultInitialAlt"`
						DefaultContactInfo string `json:"defaultContactInfo"`
						DefaultLocalInfo   string `json:"defaultLocalInfo"`
					} `json:"transitions"`
				} `json:"sids"`
				Climbouts []struct {
					ID    string `json:"id"`
					Value string `json:"value"`
				} `json:"climbouts"`
				Climbvias   []any `json:"climbvias"`
				InitialAlts []struct {
					ID    string `json:"id"`
					Value string `json:"value"`
				} `json:"initialAlts"`
				DepFreqs []struct {
					ID    string `json:"id"`
					Value string `json:"value"`
				} `json:"depFreqs"`
				Expects []struct {
					ID    string `json:"id"`
					Value string `json:"value"`
				} `json:"expects"`
				ContactInfos []struct {
					ID    string `json:"id"`
					Value string `json:"value"`
				} `json:"contactInfos"`
				LocalInfos []struct {
					ID    string `json:"id"`
					Value string `json:"value"`
				} `json:"localInfos"`
				DefaultSidID string `json:"defaultSidId"`
			} `json:"tdlsConfiguration,omitempty"`
		} `json:"childFacilities"`
		EramConfiguration struct {
			NasID   string `json:"nasId"`
			GeoMaps []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				LabelLine1 string `json:"labelLine1"`
				LabelLine2 string `json:"labelLine2"`
				FilterMenu []struct {
					ID         string `json:"id"`
					LabelLine1 string `json:"labelLine1"`
					LabelLine2 string `json:"labelLine2"`
				} `json:"filterMenu"`
				BcgMenu     []string `json:"bcgMenu"`
				VideoMapIds []string `json:"videoMapIds"`
			} `json:"geoMaps"`
			EmergencyChecklist      []string `json:"emergencyChecklist"`
			PositionReliefChecklist []string `json:"positionReliefChecklist"`
			float32ernalAirports    []string `json:"float32ernalAirports"`
			BeaconCodeBanks         []struct {
				ID       string  `json:"id"`
				Category string  `json:"category"`
				Priority string  `json:"priority"`
				Subset   float32 `json:"subset"`
				Start    float32 `json:"start"`
				End      float32 `json:"end"`
			} `json:"beaconCodeBanks"`
			NeighboringStarsConfigurations []struct {
				ID                     string `json:"id"`
				FacilityID             string `json:"facilityId"`
				StarsID                string `json:"starsId"`
				SingleCharacterStarsID string `json:"singleCharacterStarsId,omitempty"`
				FieldEFormat           string `json:"fieldEFormat"`
				FieldELetter           string `json:"fieldELetter,omitempty"`
			} `json:"neighboringStarsConfigurations"`
			NeighboringCaatsConfigurations []any    `json:"neighboringCaatsConfigurations"`
			CoordinationFixes              []any    `json:"coordinationFixes"`
			ReferenceFixes                 []string `json:"referenceFixes"`
			AsrSites                       []struct {
				ID       string `json:"id"`
				AsrID    string `json:"asrId"`
				Location struct {
					Lat float64 `json:"lat"`
					Lon float64 `json:"lon"`
				} `json:"location"`
				Range   float32 `json:"range"`
				Ceiling float32 `json:"ceiling"`
			} `json:"asrSites"`
			ConflictAlertFloor float32 `json:"conflictAlertFloor"`
			AirportSingleChars []any   `json:"airportSingleChars"`
		} `json:"eramConfiguration"`
		Positions []struct {
			ID                string  `json:"id"`
			Name              string  `json:"name"`
			Starred           bool    `json:"starred"`
			RadioName         string  `json:"radioName"`
			Callsign          string  `json:"callsign"`
			Frequency         float32 `json:"frequency"`
			EramConfiguration struct {
				SectorID string `json:"sectorId"`
			} `json:"eramConfiguration"`
			TransceiverIds []string `json:"transceiverIds"`
		} `json:"positions"`
		NeighboringFacilityIds []string `json:"neighboringFacilityIds"`
		NonNasFacilityIds      []string `json:"nonNasFacilityIds"`
	} `json:"facility"`
	VisibilityCenters []struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"visibilityCenters"`
	AliasesLastUpdatedAt time.Time `json:"aliasesLastUpdatedAt"`
	VideoMaps            []struct {
		ID                      string    `json:"id"`
		Name                    string    `json:"name"`
		Tags                    []string  `json:"tags"`
		ShortName               string    `json:"shortName,omitempty"`
		SourceFileName          string    `json:"sourceFileName"`
		LastUpdatedAt           time.Time `json:"lastUpdatedAt"`
		StarsBrightnessCategory string    `json:"starsBrightnessCategory"`
		StarsID                 float32   `json:"starsId,omitempty"`
		StarsAlwaysVisible      bool      `json:"starsAlwaysVisible"`
		TdmOnly                 bool      `json:"tdmOnly"`
	} `json:"videoMaps"`
	Transceivers []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Location struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"location"`
		HeightMslMeters float32 `json:"heightMslMeters"`
		HeightAglMeters float32 `json:"heightAglMeters"`
	} `json:"transceivers"`
	AutoAtcRules []struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		Name              string `json:"name"`
		PositionID        string `json:"positionId"`
		PrecursorRules    []any  `json:"precursorRules"`
		ExclusionaryRules []any  `json:"exclusionaryRules"`
		Criteria          struct {
			RouteSubstrings        []string `json:"routeSubstrings"`
			ExcludeRouteSubstrings []any    `json:"excludeRouteSubstrings"`
			Departures             []any    `json:"departures"`
			Destinations           []string `json:"destinations"`
			ApplicableToJets       bool     `json:"applicableToJets"`
			ApplicableToTurboprops bool     `json:"applicableToTurboprops"`
			ApplicableToProps      bool     `json:"applicableToProps"`
		} `json:"criteria"`
		DescentCrossingRestriction struct {
			CrossingFix            string `json:"crossingFix"`
			CrossingFixName        string `json:"crossingFixName"`
			AltitudeConstrafloat32 struct {
				Value              float32 `json:"value"`
				TransitionLevel    float32 `json:"transitionLevel"`
				Constrafloat32Type string  `json:"constrafloat32Type"`
				IsLufl             bool    `json:"isLufl"`
			} `json:"altitudeConstrafloat32"`
			AltimeterStation struct {
				StationID   string `json:"stationId"`
				StationName string `json:"stationName"`
			} `json:"altimeterStation"`
		} `json:"descentCrossingRestriction,omitempty"`
		DescentRestriction struct {
			CrossingLine []struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
			} `json:"crossingLine"`
			AltitudeConstrafloat32 struct {
				Value              float32 `json:"value"`
				TransitionLevel    float32 `json:"transitionLevel"`
				Constrafloat32Type string  `json:"constrafloat32Type"`
				IsLufl             bool    `json:"isLufl"`
			} `json:"altitudeConstrafloat32"`
		} `json:"descentRestriction,omitempty"`
	} `json:"autoAtcRules"`
}

type Point2LL [2]float32

type GeoMap struct {
	Type     string `json:"type"`
	Features []struct {
		Type     string `json:"type"`
		Geometry struct {
			Type        string    `json:"type"`
			Coordinates []float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			IsLineDefaults bool      `json:"isLineDefaults"`
			Bcg            float32   `json:"bcg"`
			Filters        []float32 `json:"filters"`
			Style          string    `json:"style"`
			Thickness      float32   `json:"thickness"`
		} `json:"properties"`
	} `json:"features"`
}

type GeoJSON struct {
	Type     string           `json:"type"`
	Features []GeoJSONFeature `json:"features"`
}

type GeoJSONFeature struct {
	Type     string `json:"type"`
	Geometry struct {
		Type        string             `json:"type"`
		Coordinates GeoJSONCoordinates `json:"coordinates"`
	} `json:"geometry"`
	Properties *GeoJSONProperties `json:"properties"`
}

// GeoJSONProperties mirrors the per-feature properties used by CRC video maps.
// Fields are optional and may be absent; when absent they default to zero-values.
type GeoJSONProperties struct {
	// Defaults flags
	IsLineDefaults   bool `json:"isLineDefaults"`
	IsTextDefaults   bool `json:"isTextDefaults"`
	IsSymbolDefaults bool `json:"isSymbolDefaults"`

	// Common / line properties
	Bcg       int    `json:"bcg"`
	Filters   []int  `json:"filters"`
	Style     string `json:"style"`
	Thickness int    `json:"thickness"`

	// Text properties
	Size      int  `json:"size"`
	Underline bool `json:"underline"`
	Opaque    bool `json:"opaque"`
	XOffset   int  `json:"xOffset"`
	YOffset   int  `json:"yOffset"`
}

// UnmarshalJSON allows numeric fields to be provided as either numbers or numeric strings.
func (p *GeoJSONProperties) UnmarshalJSON(data []byte) error {
	type rawMap map[string]json.RawMessage
	var raw rawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Helper to decode bool fields (ignore errors, leave zero-value on failure)
	decodeBool := func(key string, dst *bool) {
		if b, ok := raw[key]; ok {
			_ = json.Unmarshal(b, dst)
		}
	}

	// Helper to decode string fields
	decodeString := func(key string, dst *string) {
		if b, ok := raw[key]; ok {
			_ = json.Unmarshal(b, dst)
		}
	}

	// Helper to decode an int that may be a JSON number or a quoted numeric string
	decodeInt := func(key string, dst *int) {
		b, ok := raw[key]
		if !ok {
			return
		}
		var n int
		if err := json.Unmarshal(b, &n); err == nil {
			*dst = n
			return
		}
		var s string
		if err := json.Unmarshal(b, &s); err == nil {
			s = strings.TrimSpace(s)
			if s == "" {
				return
			}
			if i, err := strconv.Atoi(s); err == nil {
				*dst = i
			}
		}
	}

	// Helper to decode []int which may be an array of numbers or strings, or a single number/string
	decodeIntSlice := func(key string, dst *[]int) {
		b, ok := raw[key]
		if !ok {
			return
		}
		var ints []int
		if err := json.Unmarshal(b, &ints); err == nil {
			*dst = ints
			return
		}
		var strs []string
		if err := json.Unmarshal(b, &strs); err == nil {
			out := make([]int, 0, len(strs))
			for _, s := range strs {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				if i, err := strconv.Atoi(s); err == nil {
					out = append(out, i)
				}
			}
			*dst = out
			return
		}
		// Try single value
		var single int
		if err := json.Unmarshal(b, &single); err == nil {
			*dst = []int{single}
			return
		}
		var singleStr string
		if err := json.Unmarshal(b, &singleStr); err == nil {
			if i, err := strconv.Atoi(strings.TrimSpace(singleStr)); err == nil {
				*dst = []int{i}
			}
		}
	}

	// Booleans
	decodeBool("isLineDefaults", &p.IsLineDefaults)
	decodeBool("isTextDefaults", &p.IsTextDefaults)
	decodeBool("isSymbolDefaults", &p.IsSymbolDefaults)

	// Common / line properties
	decodeInt("bcg", &p.Bcg)
	decodeIntSlice("filters", &p.Filters)
	decodeString("style", &p.Style)
	decodeInt("thickness", &p.Thickness)

	// Text properties
	decodeInt("size", &p.Size)
	decodeBool("underline", &p.Underline)
	decodeBool("opaque", &p.Opaque)
	decodeInt("xOffset", &p.XOffset)
	decodeInt("yOffset", &p.YOffset)

	return nil
}

// We only extract lines (at the moment at least) and so we only worry
// about [][2]float32s for coordinates. (For pofloat32s, this would be
// a single [2]float32 and for polygons, it would be [][][2]float32...)
type GeoJSONCoordinates []Point2LL

func (c *GeoJSONCoordinates) UnmarshalJSON(d []byte) error {
	*c = nil

	var coords []Point2LL
	if err := json.Unmarshal(d, &coords); err == nil {
		*c = coords
	}
	// Don't report any errors but assume that it's a pofloat32, polygon, ...
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Output structs

type ERAMMap struct {
	BcgName    string
	LabelLine1 string
	LabelLine2 string
	Name       string
	Lines      [][]Point2LL
}

type ERAMMapGroup struct {
	Maps       []ERAMMap
	LabelLine1 string
	LabelLine2 string
}

type ERAMMapGroups map[string]ERAMMapGroup
