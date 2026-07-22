package stt

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
)

// TestFile is the on-disk record for one STT transmission: the shape of
// the "STT command" entries in the vice slog, of the corpus files in
// stt/tests/ and stt/failing_tests/, and of the entries in cmd/sttreview's
// review queue. Callsign and Command hold the expected decoder output
// (both empty means the expected output is silence).
type TestFile struct {
	Time              string              `json:"time"`
	Level             string              `json:"level"`
	Msg               string              `json:"msg"`
	Callstack         []string            `json:"callstack,omitempty"`
	Transcript        string              `json:"transcript"`
	WhisperDurationMs float64             `json:"whisper_duration_ms,omitempty"`
	Duration          int64               `json:"duration,omitempty"`
	AudioDurationMs   float64             `json:"audio_duration_ms,omitempty"`
	Processor         string              `json:"processor,omitempty"`
	WhisperModel      string              `json:"whisper_model,omitempty"`
	Callsign          string              `json:"callsign"`
	Command           string              `json:"command"`
	Suggested         string              `json:"suggested,omitempty"` // reviewer-prefill correction, if any
	Reason            string              `json:"reason,omitempty"`    // why the entry is a suspect / the suggestion (review note)
	STTAircraft       map[string]Aircraft `json:"stt_aircraft"`
	Logs              []string            `json:"logs,omitempty"`
}

// LoadTestFile reads and parses one test-file JSON.
func LoadTestFile(path string) (TestFile, error) {
	var tf TestFile
	data, err := os.ReadFile(path)
	if err != nil {
		return tf, err
	}
	err = json.Unmarshal(data, &tf)
	return tf, err
}

// Expected returns the expected decoder output: "CALLSIGN COMMANDS", or ""
// when both are empty (the transmission expects silence).
func (tf TestFile) Expected() string {
	if tf.Callsign == "" && tf.Command == "" {
		return ""
	}
	return strings.TrimSpace(tf.Callsign + " " + tf.Command)
}

var testFilenameStrip = regexp.MustCompile(`[^a-z0-9_]`)

// SanitizeTestFilename derives a test-file basename (without extension)
// from a transcript: lowercased, spaces to underscores, other punctuation
// dropped, truncated to 50 characters.
func SanitizeTestFilename(transcript string) string {
	s := strings.ToLower(transcript)
	s = strings.ReplaceAll(s, " ", "_")
	s = testFilenameStrip.ReplaceAllString(s, "")
	if len(s) > 50 {
		s = s[:50]
	}
	if s == "" {
		s = "entry"
	}
	return s
}

// BuildAircraftMap converts the stored per-aircraft context into the map
// DecodeTranscript expects, mirroring the production context
// initialization in provider.go: type-addressed callsigns get a /T
// suffix, and the assigned approach's fixes are merged into Fixes.
func (tf TestFile) BuildAircraftMap() map[string]Aircraft {
	aircraft := make(map[string]Aircraft, len(tf.STTAircraft))
	for key, ac := range tf.STTAircraft {
		if ac.AddressingForm == sim.AddressingFormTypeTrailing3 && !strings.HasSuffix(ac.Callsign, "/T") {
			ac.Callsign += "/T"
		}
		if ac.AssignedApproach != "" && len(ac.ApproachFixes) > 0 {
			telephony := av.GetApproachTelephony(ac.AssignedApproach)
			if code, ok := ac.CandidateApproaches[telephony]; ok {
				if approachFixes, ok := ac.ApproachFixes[code]; ok {
					fixes := make(map[string]string, len(ac.Fixes)+len(approachFixes))
					for spoken, fix := range ac.Fixes {
						fixes[spoken] = fix
					}
					for spoken, fix := range approachFixes {
						if _, exists := fixes[spoken]; !exists {
							fixes[spoken] = fix
						}
					}
					ac.Fixes = fixes
				}
			}
		}
		aircraft[key] = ac
	}
	return aircraft
}
