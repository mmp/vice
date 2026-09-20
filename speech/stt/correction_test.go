package stt

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
)

func TestCorrectionTranscripts(t *testing.T) {
	aircraft := map[string]Aircraft{
		"United 123": {Callsign: "UAL123", State: "arrival", Altitude: 6000, LastAddressed: true},
		"Delta 456":  {Callsign: "DAL456", State: "arrival", Altitude: 6000},
	}
	for _, test := range []struct {
		transcript string
		want       string
	}{
		{"United one two three correction heading one two zero", "UAL123 CORRECTION H120"},
		{"correction United one two three heading one two zero", "UAL123 CORRECTION H120"},
		{"correction heading one two zero", "UAL123 CORRECTION H120"},
		{"negative heading one two zero", "UAL123 CORRECTION H120"},
		{"correction", ""},
		{"that was not for you", "UAL123 ROLLBACK"},
		{"United one two three that was not for you", "UAL123 ROLLBACK"},
		{"that was not for you Delta four five six heading one two zero", "DAL456 CORRECTION H120"},
		// Nothing for the aircraft it was meant for: all the controller has
		// said is that the previous transmission was wrong. Saying which aircraft
		// it was meant for retracts outright, so unlike a bare "correction" it
		// pulls the previous transmission back even when what follows is garbled.
		{"negative that was for Delta four five six", "UAL123 ROLLBACK"},
		{"negative that was for Delta four five six blorf", "UAL123 ROLLBACK"},
		{"negative that was for Delta four five six descend and maintain blorf",
			"DAL456 CORRECTION SAYAGAIN/ALTITUDE"},
		{"negative that was for Delta four five six turn left heading zero niner zero",
			"DAL456 CORRECTION L090"},
		{"United one two three heading one two zero correction Delta four five six heading one five zero", "DAL456 H150"},
		{"United one two three correction Delta four five six heading one five zero", "DAL456 H150"},
		{"United one two three heading one two zero correction one five zero", "UAL123 H150"},
		// A transmission that yields no instruction is not worth undoing the
		// previous one for, however it opened. Asking for the garbled part again
		// is not an instruction.
		{"correction Delta four five six", "DAL456 AGAIN"},
		{"United one two three correction blorf", "UAL123 AGAIN"},
		{"correction Delta four five six descend and maintain blorf", "DAL456 SAYAGAIN/ALTITUDE"},
		{"correction Delta four five six proceed direct blorf", "DAL456 SAYAGAIN/FIX"},
		// One instruction did come through, so the previous transmission goes.
		{"correction Delta four five six turn left heading zero niner zero descend and maintain blorf",
			"DAL456 CORRECTION L090 SAYAGAIN/ALTITUDE"},
		// A handoff is an instruction like any other.
		{"correction Delta four five six New York approach good day", "DAL456 CORRECTION FC"},
	} {
		t.Run(test.transcript, func(t *testing.T) {
			got, err := NewTranscriber(nil).DecodeTranscript(aircraft, test.transcript, "")
			if err != nil || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestUnaddressedCorrectionUsesAircraftContext(t *testing.T) {
	aircraft := map[string]Aircraft{
		"Southwest 2949": {
			Callsign: "SWA2949", State: "arrival", LastAddressed: true,
			CandidateApproaches: map[string]string{"ILS Runway 30": "I30"},
			Fixes:               map[string]string{"allxx": "ALLXX"},
		},
		"Delta 456": {
			Callsign: "DAL456", State: "arrival",
			CandidateApproaches: map[string]string{"ILS Runway 28": "I28"},
		},
	}
	for _, test := range []struct{ transcript, want string }{
		{"correction", ""},
		{"correction expect ILS runway three zero approach", "SWA2949 CORRECTION EI30"},
		{"correction direct allxx", "SWA2949 CORRECTION DALLXX"},
		{"correction New York approach good day", "SWA2949 CORRECTION FC"},
		{"correction blorf", "SWA2949 AGAIN"},
	} {
		got, err := NewTranscriber(nil).DecodeTranscript(aircraft, test.transcript, "")
		if err != nil || got != test.want {
			t.Errorf("%q: got %q, %v; want %q", test.transcript, got, err, test.want)
		}
	}
}

// TestCorrectionWithoutContext checks that a correction the decoder cannot attribute to
// an aircraft is dropped rather than guessed at.
func TestCorrectionWithoutContext(t *testing.T) {
	aircraft := map[string]Aircraft{
		"United 123": {Callsign: "UAL123", State: "arrival", Altitude: 6000},
	}
	for _, transcript := range []string{"correction heading one two zero", "that was not for you"} {
		got, err := NewTranscriber(nil).DecodeTranscript(aircraft, transcript, "")
		if err != nil || got != "" {
			t.Errorf("%q: got %q, %v; want %q", transcript, got, err, "")
		}
	}
}

// TestNegativeThatWasForNamesAnAircraft checks that a retraction still reaches the
// simulator when the decoder has not been told who wrongly took the previous
// transmission. The simulator retracts by frequency, so the aircraft the controller
// says it was meant for is a good enough carrier.
func TestNegativeThatWasForNamesAnAircraft(t *testing.T) {
	aircraft := map[string]Aircraft{
		"United 123": {Callsign: "UAL123", State: "arrival", Altitude: 6000},
		"Delta 456":  {Callsign: "DAL456", State: "arrival", Altitude: 6000},
	}
	got, err := NewTranscriber(nil).DecodeTranscript(aircraft, "negative that was for Delta four five six", "")
	if err != nil || got != "DAL456 ROLLBACK" {
		t.Fatalf("got %q, %v; want %q", got, err, "DAL456 ROLLBACK")
	}
}

// TestUnaddressedCorrectionKeepsAddressingForm checks that a correction naming no
// aircraft goes back to it in the form the controller has been addressing it.
func TestUnaddressedCorrectionKeepsAddressingForm(t *testing.T) {
	aircraft := map[string]Aircraft{
		"november one two three alpha brahvo": {
			Callsign: "N123AB", State: "arrival", Altitude: 6000,
			AddressingForm: sim.AddressingFormFull,
		},
		"skyhawk three alpha brahvo": {
			Callsign: "N123AB/T", State: "arrival", Altitude: 6000, LastAddressed: true,
			AddressingForm: sim.AddressingFormTypeTrailing3,
		},
	}
	got, err := NewTranscriber(nil).DecodeTranscript(aircraft, "correction heading one two zero", "")
	if err != nil || got != "N123AB/T CORRECTION H120" {
		t.Fatalf("got %q, %v; want %q", got, err, "N123AB/T CORRECTION H120")
	}
}

// TestBuildAircraftContextLastAddressed checks that a GA aircraft has at most one of
// its addressing forms flagged: lastAddressed ranges the context map, so flagging both
// would leave the form a correction goes back in to map iteration order.
func TestBuildAircraftContextLastAddressed(t *testing.T) {
	makeState := func(last av.ADSBCallsign) *sim.UserState {
		state := &sim.UserState{}
		state.CurrentConsolidation = map[sim.TCW]*sim.TCPConsolidation{"TEST": {PrimaryTCP: "TEST"}}
		state.Tracks = map[av.ADSBCallsign]*sim.Track{
			"N123AB": {
				RadarTrack:          av.RadarTrack{ADSBCallsign: "N123AB"},
				ControllerFrequency: "TEST",
				FlightPlan:          &sim.NASFlightPlan{AircraftType: "C172"},
			},
		}
		state.LastSTTCallsigns = map[sim.TCW]av.ADSBCallsign{"TEST": last}
		return state
	}

	for _, test := range []struct {
		last av.ADSBCallsign
		want []string
	}{
		{"", nil},
		{"N123AB", []string{"N123AB"}},
		{"N123AB/T", []string{"N123AB/T"}},
	} {
		ctx := NewTranscriber(nil).BuildAircraftContext(makeState(test.last), "TEST")
		if len(ctx) < 2 {
			t.Fatalf("last=%q: context has no type/trailing-three form: %v", test.last, ctx)
		}
		var flagged []string
		for _, ac := range ctx {
			if ac.LastAddressed && !slices.Contains(flagged, ac.Callsign) {
				flagged = append(flagged, ac.Callsign)
			}
		}
		slices.Sort(flagged)
		if !slices.Equal(flagged, test.want) {
			t.Errorf("last=%q: flagged %v, want %v", test.last, flagged, test.want)
		}
	}
}
