package stt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// ReviewState is the persisted state of the transmission-review workflow
// (cmd/sttreview, cmd/stteval): a queue of records awaiting review and the
// set of already-processed record hashes.
type ReviewState struct {
	Queue []TestFile      `json:"queue"`
	Seen  map[string]bool `json:"seen"`
}

// LoadReviewState loads review state from path. A missing or unparseable
// file yields an empty state. Queue entries that are already Seen, and
// duplicates within the queue, are dropped.
func LoadReviewState(path string) *ReviewState {
	data, err := os.ReadFile(path)
	if err != nil {
		return &ReviewState{Seen: make(map[string]bool)}
	}
	var state ReviewState
	if err := json.Unmarshal(data, &state); err != nil {
		return &ReviewState{Seen: make(map[string]bool)}
	}
	if state.Seen == nil {
		state.Seen = make(map[string]bool)
	}

	seen := make(map[string]bool)
	var filtered []TestFile
	for _, e := range state.Queue {
		h := EntryHash(e)
		if !state.Seen[h] && !seen[h] {
			filtered = append(filtered, e)
			seen[h] = true
		}
	}
	state.Queue = filtered

	return &state
}

// Save persists the state to path, creating parent directories as needed.
func (s *ReviewState) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Ingest adds new entries to the queue, skipping already-seen ones and
// duplicates. Returns the number added.
func (s *ReviewState) Ingest(entries []TestFile) int {
	queued := make(map[string]bool)
	for _, e := range s.Queue {
		queued[EntryHash(e)] = true
	}

	added := 0
	for _, e := range entries {
		h := EntryHash(e)
		if !s.Seen[h] && !queued[h] {
			s.Queue = append(s.Queue, e)
			queued[h] = true
			added++
		}
	}
	return added
}

// MarkDone marks an entry as processed.
func (s *ReviewState) MarkDone(e TestFile) {
	s.Seen[EntryHash(e)] = true
}

// EntryHash computes the identity hash of a record: transcripts plus the
// stored output. This must remain stable — it is the key of the persisted
// Seen set.
func EntryHash(e TestFile) string {
	h := sha256.New()
	h.Write([]byte(e.Transcript + "|" + e.Callsign + "|" + e.Command))
	return hex.EncodeToString(h.Sum(nil))
}
