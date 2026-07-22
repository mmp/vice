// cmd/stteval evaluates the unreviewed transmissions in cmd/sttreview's
// queue against the current STT decoder. It partitions them into trusted
// entries (current output agrees with the stored output and passes
// consistency checks), suspects (disagreement or a failed check), and
// unusable records; selects the trusted entries that add statement
// coverage over the existing stt/tests corpus; and stages new test files
// and a suspects review queue for human sign-off.
//
// Phases (run in order; each reads the earlier phases' outputs from
// -evaldir):
//
//	-phase decode    decode every queued entry, write report.jsonl
//	-phase coverage  per-entry coverage novelty + greedy selection
//	                 (requires a binary built with
//	                 go build -cover -covermode=atomic -coverpkg=github.com/mmp/vice/stt)
//	-phase emit      write staged-tests/, staged-index.json, suspects.json
//	-phase apply     -accepted <file>: install accepted staged tests in
//	                 stt/tests/, divert the rest to suspects.json, mark
//	                 all processed entries Seen in the main state
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/coverage"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/stt"
)

type reportRow struct {
	Hash       string   `json:"hash"`
	Transcript string   `json:"transcript"`
	Stored     string   `json:"stored"`
	Current    string   `json:"current"`
	Agree      bool     `json:"agree"`
	Flags      []string `json:"flags,omitempty"`
	Class      string   `json:"class"` // trusted | suspect | unusable
}

type selectedRow struct {
	Hash  string `json:"hash"`
	Novel int    `json:"novel"`
}

// diagnosis is one suspect's review note: an optional proposed correction
// ("CALLSIGN CMDS", or a single space for "expected = silence") and the
// reason behind it. The apply phase attaches these to the suspects queue.
type diagnosis struct {
	Suggestion string `json:"suggestion"`
	Reason     string `json:"reason"`
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func main() {
	phase := flag.String("phase", "", "decode | coverage | emit | apply")
	statePath := flag.String("state", "~/.sttreview/state.json", "review state file")
	evalDir := flag.String("evaldir", "~/.sttreview/eval", "directory for evaluation outputs")
	testsDir := flag.String("tests", "stt/tests", "existing corpus directory")
	acceptedPath := flag.String("accepted", "", "file listing accepted staged-test filenames (apply phase)")
	diagnosesPath := flag.String("diagnoses", "", "JSON object {transcript: {suggestion, reason}} to annotate suspect reviews (apply phase)")
	flag.Parse()

	av.InitDB()

	sp, ed := expandPath(*statePath), expandPath(*evalDir)
	if err := os.MkdirAll(ed, 0755); err != nil {
		fatalf("creating evaldir: %v", err)
	}

	var err error
	switch *phase {
	case "decode":
		err = phaseDecode(sp, ed)
	case "coverage":
		err = phaseCoverage(sp, ed, *testsDir)
	case "emit":
		err = phaseEmit(sp, ed, *testsDir)
	case "apply":
		err = phaseApply(sp, ed, *testsDir, expandPath(*acceptedPath), expandPath(*diagnosesPath))
	default:
		fatalf("unknown -phase %q (want decode, coverage, emit, or apply)", *phase)
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// ---------------------------------------------------------------- decode

func phaseDecode(statePath, evalDir string) error {
	state := stt.LoadReviewState(statePath)
	if len(state.Queue) == 0 {
		return fmt.Errorf("empty queue in %s", statePath)
	}

	provider := stt.NewTranscriber(nil)
	var rows []reportRow
	flagCounts := make(map[string]int)
	classCounts := make(map[string]int)

	for _, e := range state.Queue {
		row := reportRow{
			Hash:       stt.EntryHash(e),
			Transcript: e.Transcript,
			Stored:     e.Expected(),
		}
		if len(e.STTAircraft) == 0 {
			row.Class = "unusable"
		} else {
			aircraft := e.BuildAircraftMap()
			current, err := provider.DecodeTranscript(aircraft, e.Transcript, "")
			if err != nil {
				return fmt.Errorf("decoding %q: %v", e.Transcript, err)
			}
			row.Current = current
			row.Agree = stt.CommandsEquivalent(row.Stored, current, aircraft)
			row.Flags = consistencyFlags(e.Transcript, current)
			if row.Agree && len(row.Flags) == 0 {
				row.Class = "trusted"
			} else {
				row.Class = "suspect"
			}
		}
		classCounts[row.Class]++
		for _, f := range row.Flags {
			flagCounts[f]++
		}
		rows = append(rows, row)
	}

	if err := writeJSONL(filepath.Join(evalDir, "report.jsonl"), rows); err != nil {
		return err
	}

	fmt.Printf("decoded %d entries\n", len(rows))
	for _, c := range slices.Sorted(maps.Keys(classCounts)) {
		fmt.Printf("  %-10s %d\n", c, classCounts[c])
	}
	disagree := 0
	for _, r := range rows {
		if r.Class != "unusable" && !r.Agree {
			disagree++
		}
	}
	fmt.Printf("disagree with stored output: %d\n", disagree)
	fmt.Println("flags:")
	for _, f := range slices.Sorted(maps.Keys(flagCounts)) {
		fmt.Printf("  %-22s %d\n", f, flagCounts[f])
	}
	return nil
}

// keywordChecks lists transcript keywords that promise a command of some
// kind and the predicate over the output commands that satisfies them.
var keywordChecks = []struct {
	word string
	ok   func(cmds []string) bool
}{
	{"heading", catIn("heading", "depart_heading")},
	{"descend", anyOf(catIn("altitude", "crossing"), literal("DVS"))},
	{"climb", anyOf(catIn("altitude", "crossing"), literal("CVS"))},
	{"speed", catIn("speed", "crossing")},
	{"knots", catIn("speed", "crossing")},
	{"maintain", anyOf(catIn("altitude", "speed", "crossing", "heading"), literal("VISSEP"))},
	{"squawk", squawkOK},
	{"ident", squawkOK},
	{"direct", catIn("navigation", "cleared_approach", "expect_approach", "depart_heading")},
	{"contact", contactOK},
	{"tower", contactOK},
	{"cleared", catIn("cleared_approach", "navigation", "altitude")},
	{"expect", catIn("expect_approach")},
	{"traffic", anyOf(catIn("traffic", "advisory", "wake"), literal("VISSEP", "RST"))},
}

func catIn(cats ...string) func([]string) bool {
	return func(cmds []string) bool {
		return slices.ContainsFunc(cmds, func(c string) bool {
			return slices.Contains(cats, stt.CommandCategory(c))
		})
	}
}

func literal(names ...string) func([]string) bool {
	return func(cmds []string) bool {
		return slices.ContainsFunc(cmds, func(c string) bool { return slices.Contains(names, c) })
	}
}

func anyOf(preds ...func([]string) bool) func([]string) bool {
	return func(cmds []string) bool {
		for _, p := range preds {
			if p(cmds) {
				return true
			}
		}
		return false
	}
}

func contactOK(cmds []string) bool {
	return slices.ContainsFunc(cmds, func(c string) bool {
		return c == "TO" || strings.HasPrefix(c, "TO/") || c == "FC" || c == "MF"
	})
}

func squawkOK(cmds []string) bool {
	return slices.ContainsFunc(cmds, func(c string) bool {
		return strings.HasPrefix(c, "SQ") || c == "ID"
	})
}

// ackWords end a transmission that only acknowledges; the expected output
// is silence even though the callsign digits go unechoed.
var ackWords = map[string]bool{
	"roger": true, "wilco": true, "copy": true, "affirm": true,
	"affirmative": true, "hello": true, "hey": true,
}

// radarish words precede "contact" in "radar contact" and its garbles;
// such a "contact" is informational, not a frequency-change instruction.
var radarish = map[string]bool{
	"radar": true, "rate": true, "read": true, "reader": true,
	"rare": true, "wait": true, "of": true, "route": true,
}

// consistencyFlags applies transcript-vs-output plausibility checks to the
// current decoder output.
func consistencyFlags(transcript, current string) []string {
	tokens := stt.Tokenize(stt.NormalizeTranscript(transcript))
	var flags []string

	fields := strings.Fields(current)
	var cmds []string
	if len(fields) > 1 {
		cmds = fields[1:]
	}

	nonFiller := 0
	for _, t := range tokens {
		if !stt.IsFillerWord(strings.ToLower(t.Text)) {
			nonFiller++
		}
	}
	if current == "" && nonFiller >= 5 {
		flags = append(flags, "silence-on-content")
	}
	if strings.Contains(current, "SAYAGAIN") {
		flags = append(flags, "sayagain")
	}

	// Keyword checks: an exact command keyword in the transcript should be
	// reflected by a command of the corresponding kind.
	for _, kc := range keywordChecks {
		for i, t := range tokens {
			if strings.ToLower(t.Text) != kc.word {
				continue
			}
			if kc.word == "contact" && i > 0 && radarish[strings.ToLower(tokens[i-1].Text)] {
				continue
			}
			if !kc.ok(cmds) {
				flags = append(flags, "keyword-"+kc.word)
			}
			break
		}
	}

	// Number check: every multi-digit number in the transcript should
	// appear (allowing for the standard encodings) somewhere in the output.
	// An acknowledgment-only transmission is correctly answered with
	// silence, its unechoed callsign digits notwithstanding.
	if current == "" && slices.ContainsFunc(tokens, func(t stt.Token) bool {
		return ackWords[strings.ToLower(t.Text)]
	}) {
		return flags
	}
	outRuns := digitRuns(current)
	correctionAt := slices.IndexFunc(tokens, func(t stt.Token) bool {
		return strings.EqualFold(t.Text, "correction")
	})
	hasContact := contactOK(cmds)
	for i, t := range tokens {
		// Frequency digits ("one two six point eight") are not echoed in
		// FC/TO outputs; skip numbers adjacent to "point".
		if (i > 0 && strings.EqualFold(tokens[i-1].Text, "point")) ||
			(i+1 < len(tokens) && strings.EqualFold(tokens[i+1].Text, "point")) {
			continue
		}
		// Altimeter readings are informational and stripped by design.
		if (i > 0 && strings.EqualFold(tokens[i-1].Text, "altimeter")) ||
			(i > 1 && strings.EqualFold(tokens[i-2].Text, "altimeter")) {
			continue
		}
		// Content before a "correction" is retracted.
		if correctionAt >= 0 && i < correctionAt {
			continue
		}
		// Runway numbers are absorbed into approach IDs; localizer/approach
		// references likewise ("intercept runway two two localizer" -> I).
		if i > 0 && strings.EqualFold(tokens[i-1].Text, "runway") {
			continue
		}
		if i+1 < len(tokens) {
			next := strings.ToLower(tokens[i+1].Text)
			if next == "localizer" || next == "approach" {
				continue
			}
		}
		// A frequency spoken without "point" ("tower one three three
		// niner") is not echoed by TO/FC outputs.
		if hasContact && len(t.Text) >= 3 && t.Text[0] == '1' {
			continue
		}
		var cands []string
		switch t.Type {
		case stt.TokenNumber:
			if len(t.Text) < 2 {
				continue
			}
			cands = append(cands, t.Text, t.Text+"0")
			if strings.HasSuffix(t.Text, "0") {
				cands = append(cands, strings.TrimSuffix(t.Text, "0"))
			}
			if t.Value >= 1000 && t.Value%100 == 0 {
				cands = append(cands, strconv.Itoa(t.Value/100))
			}
		case stt.TokenAltitude:
			// Altitudes are encoded in hundreds of feet in commands.
			cands = append(cands, strconv.Itoa(t.Value))
		default:
			continue
		}
		matched := false
		for _, c := range cands {
			for _, r := range outRuns {
				// Subsequence covers dropped-middle-digit callsign matches
				// ("three nine" resolving to ASA389).
				if strings.Contains(r, c) || strings.Contains(c, r) || isSubsequence(c, r) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			flags = append(flags, "unconsumed-number")
			break
		}
	}

	return flags
}

// isSubsequence reports whether sub's characters appear in s in order.
func isSubsequence(sub, s string) bool {
	i := 0
	for j := 0; j < len(s) && i < len(sub); j++ {
		if s[j] == sub[i] {
			i++
		}
	}
	return i == len(sub)
}

// digitRuns returns the maximal runs of digits in s.
func digitRuns(s string) []string {
	var runs []string
	start := -1
	for i := 0; i <= len(s); i++ {
		digit := i < len(s) && s[i] >= '0' && s[i] <= '9'
		if digit && start < 0 {
			start = i
		} else if !digit && start >= 0 {
			runs = append(runs, s[start:i])
			start = -1
		}
	}
	return runs
}

// ---------------------------------------------------------------- coverage

func phaseCoverage(statePath, evalDir, testsDir string) error {
	// Verify this binary is instrumented before doing any work.
	if err := coverage.ClearCounters(); err != nil {
		return fmt.Errorf("not a coverage-instrumented build (rebuild with go build -cover -covermode=atomic -coverpkg=github.com/mmp/vice/stt): %v", err)
	}

	rows, err := readReport(evalDir)
	if err != nil {
		return err
	}
	state := stt.LoadReviewState(statePath)
	byHash := make(map[string]stt.TestFile)
	for _, e := range state.Queue {
		byHash[stt.EntryHash(e)] = e
	}

	provider := stt.NewTranscriber(nil)
	scratch, err := os.MkdirTemp("", "stteval-cov")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)

	// Baseline: the union of what the existing corpus covers.
	files, err := filepath.Glob(filepath.Join(testsDir, "*.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no corpus files in %s", testsDir)
	}
	if err := coverage.ClearCounters(); err != nil {
		return err
	}
	for _, f := range files {
		tf, err := stt.LoadTestFile(f)
		if err != nil {
			return fmt.Errorf("%s: %v", f, err)
		}
		if _, err := provider.DecodeTranscript(tf.BuildAircraftMap(), tf.Transcript, ""); err != nil {
			return fmt.Errorf("%s: %v", f, err)
		}
	}
	baseline, err := snapshotCovered(scratch, "baseline")
	if err != nil {
		return err
	}
	fmt.Printf("baseline: %d corpus files cover %d statements\n", len(files), len(baseline))

	// Per-candidate novel coverage.
	novel := make(map[string]map[string]bool) // hash -> novel statement set
	trusted := 0
	for _, row := range rows {
		if row.Class != "trusted" {
			continue
		}
		trusted++
		e, ok := byHash[row.Hash]
		if !ok {
			return fmt.Errorf("entry %s in report but not in queue; re-run -phase decode", row.Hash[:12])
		}
		if err := coverage.ClearCounters(); err != nil {
			return err
		}
		if _, err := provider.DecodeTranscript(e.BuildAircraftMap(), e.Transcript, ""); err != nil {
			return err
		}
		covered, err := snapshotCovered(scratch, row.Hash[:16])
		if err != nil {
			return err
		}
		nv := make(map[string]bool)
		for stmt := range covered {
			if !baseline[stmt] {
				nv[stmt] = true
			}
		}
		if len(nv) > 0 {
			novel[row.Hash] = nv
		}
	}
	fmt.Printf("candidates: %d trusted, %d with novel statements\n", trusted, len(novel))

	// Greedy set cover over the novel statements.
	var selected []selectedRow
	chosen := make(map[string]bool)
	for {
		bestHash, bestGain := "", 0
		for h, nv := range novel {
			gain := 0
			for stmt := range nv {
				if !chosen[stmt] {
					gain++
				}
			}
			if gain > bestGain || (gain == bestGain && gain > 0 && h < bestHash) {
				bestHash, bestGain = h, gain
			}
		}
		if bestGain == 0 {
			break
		}
		for stmt := range novel[bestHash] {
			chosen[stmt] = true
		}
		selected = append(selected, selectedRow{Hash: bestHash, Novel: bestGain})
		delete(novel, bestHash)
	}

	if err := writeJSONL(filepath.Join(evalDir, "selected.jsonl"), selected); err != nil {
		return err
	}
	fmt.Printf("selected %d entries adding %d statements of coverage\n", len(selected), len(chosen))
	return nil
}

// snapshotCovered writes the current coverage counters to a fresh directory
// under scratch and returns the set of covered statement IDs.
func snapshotCovered(scratch, name string) (map[string]bool, error) {
	dir := filepath.Join(scratch, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := coverage.WriteMetaDir(dir); err != nil {
		return nil, err
	}
	if err := coverage.WriteCountersDir(dir); err != nil {
		return nil, err
	}

	txt := filepath.Join(dir, "cov.txt")
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+dir, "-o="+txt)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("covdata: %v: %s", err, out)
	}

	covered := make(map[string]bool)
	f, err := os.Open(txt)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: file.go:12.34,56.78 nstmts count. The instrumented build
		// also covers this package itself (the coverage runtime requires
		// the main package instrumented); only decoder statements count.
		if !strings.HasPrefix(line, "github.com/mmp/vice/stt/") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 0 || line[i+1:] == "0" {
			continue
		}
		j := strings.LastIndexByte(line[:i], ' ')
		if j < 0 {
			continue
		}
		covered[line[:j]] = true
	}
	return covered, scanner.Err()
}

// ---------------------------------------------------------------- emit

func phaseEmit(statePath, evalDir, testsDir string) error {
	rows, err := readReport(evalDir)
	if err != nil {
		return err
	}
	var selected []selectedRow
	if err := readJSONL(filepath.Join(evalDir, "selected.jsonl"), &selected); err != nil {
		return err
	}
	state := stt.LoadReviewState(statePath)
	byHash := make(map[string]stt.TestFile)
	for _, e := range state.Queue {
		byHash[stt.EntryHash(e)] = e
	}
	rowByHash := make(map[string]reportRow)
	for _, r := range rows {
		rowByHash[r.Hash] = r
	}

	// Staged tests: selected trusted entries, expected = current output.
	stagedDir := filepath.Join(evalDir, "staged-tests")
	if err := os.RemoveAll(stagedDir); err != nil {
		return err
	}
	if err := os.MkdirAll(stagedDir, 0755); err != nil {
		return err
	}
	index := make(map[string]string) // filename -> hash
	taken := make(map[string]bool)
	for _, sel := range selected {
		e, ok := byHash[sel.Hash]
		if !ok {
			return fmt.Errorf("selected entry %s not in queue", sel.Hash[:12])
		}
		e.Callsign, e.Command = splitOutput(rowByHash[sel.Hash].Current)
		name := stagedName(e.Transcript, testsDir, taken)
		taken[name] = true
		index[name] = sel.Hash
		if err := writeTestFile(filepath.Join(stagedDir, name), e); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(evalDir, "staged-index.json"), index); err != nil {
		return err
	}

	// Suspects queue: every suspect, output refreshed to the current
	// decoder's. Suggested is filled in by the diagnosis pass.
	suspects := &stt.ReviewState{Seen: make(map[string]bool)}
	for _, row := range rows {
		if row.Class != "suspect" {
			continue
		}
		e := byHash[row.Hash]
		e.Callsign, e.Command = splitOutput(row.Current)
		suspects.Queue = append(suspects.Queue, e)
	}
	if err := suspects.Save(filepath.Join(evalDir, "suspects.json")); err != nil {
		return err
	}

	fmt.Printf("staged %d tests in %s\n", len(index), stagedDir)
	fmt.Printf("wrote %d suspects to %s\n", len(suspects.Queue), filepath.Join(evalDir, "suspects.json"))
	return nil
}

// splitOutput splits a decoder output "CALLSIGN CMDS..." into its callsign
// and command halves.
func splitOutput(output string) (callsign, command string) {
	callsign, command, _ = strings.Cut(output, " ")
	return
}

// stagedName picks a corpus filename for a transcript, avoiding both the
// existing corpus and names already taken this run.
func stagedName(transcript, testsDir string, taken map[string]bool) string {
	base := stt.SanitizeTestFilename(transcript)
	name := base + ".json"
	for i := 1; taken[name] || fileExists(filepath.Join(testsDir, name)); i++ {
		name = fmt.Sprintf("%s_%d.json", base, i)
	}
	return name
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ---------------------------------------------------------------- apply

func phaseApply(statePath, evalDir, testsDir, acceptedPath, diagnosesPath string) error {
	if acceptedPath == "" {
		return fmt.Errorf("-phase apply requires -accepted <file>")
	}
	acceptedData, err := os.ReadFile(acceptedPath)
	if err != nil {
		return err
	}
	accepted := make(map[string]bool)
	for _, line := range strings.Fields(string(acceptedData)) {
		accepted[filepath.Base(line)] = true
	}

	// Optional per-suspect diagnoses, keyed by transcript (the suspects'
	// stored output was rewritten to the current decoder output during
	// emit, so the entry hash no longer matches the report). Each carries an
	// optional suggested correction and a human-readable reason.
	diagnoses := make(map[string]diagnosis)
	if diagnosesPath != "" {
		if err := readJSON(diagnosesPath, &diagnoses); err != nil {
			return err
		}
	}

	rows, err := readReport(evalDir)
	if err != nil {
		return err
	}
	var index map[string]string
	if err := readJSON(filepath.Join(evalDir, "staged-index.json"), &index); err != nil {
		return err
	}
	for name := range accepted {
		if _, ok := index[name]; !ok {
			return fmt.Errorf("accepted file %q is not a staged test", name)
		}
	}

	// Back up the main state before mutating anything.
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(statePath+".bak", stateData, 0644); err != nil {
		return err
	}

	// Install accepted staged tests; divert doubted ones to the suspects.
	suspectsPath := filepath.Join(evalDir, "suspects.json")
	suspects := stt.LoadReviewState(suspectsPath)

	// Attach diagnoses to the suspects: the reason is shown for every
	// diagnosed entry; the suggested correction (present only when the
	// diagnosis proposed a change) prefills the review edit field.
	prefilled, noted := 0, 0
	for i := range suspects.Queue {
		if d, ok := diagnoses[suspects.Queue[i].Transcript]; ok {
			if d.Suggestion != "" {
				suspects.Queue[i].Suggested = d.Suggestion
				prefilled++
			}
			if d.Reason != "" {
				suspects.Queue[i].Reason = d.Reason
				noted++
			}
		}
	}

	installed, doubted := 0, 0
	for _, name := range slices.Sorted(maps.Keys(index)) {
		src := filepath.Join(evalDir, "staged-tests", name)
		tf, err := stt.LoadTestFile(src)
		if err != nil {
			return fmt.Errorf("%s: %v", src, err)
		}
		if accepted[name] {
			if err := writeTestFile(filepath.Join(testsDir, name), tf); err != nil {
				return err
			}
			installed++
		} else {
			suspects.Queue = append(suspects.Queue, tf)
			doubted++
		}
		if err := os.Remove(src); err != nil {
			return err
		}
	}
	if err := suspects.Save(suspectsPath); err != nil {
		return err
	}

	// Mark every processed entry Seen and drain the main queue.
	state := stt.LoadReviewState(statePath)
	processed := make(map[string]bool)
	for _, r := range rows {
		processed[r.Hash] = true
		state.Seen[r.Hash] = true
	}
	before := len(state.Queue)
	state.Queue = slices.DeleteFunc(state.Queue, func(e stt.TestFile) bool {
		return processed[stt.EntryHash(e)]
	})
	if err := state.Save(statePath); err != nil {
		return err
	}

	fmt.Printf("installed %d tests in %s, diverted %d to suspects\n", installed, testsDir, doubted)
	fmt.Printf("suspects queue: %d entries (%d with a suggested correction, %d with a note) in %s\n", len(suspects.Queue), prefilled, noted, suspectsPath)
	fmt.Printf("main queue drained %d -> %d entries (backup at %s)\n", before, len(state.Queue), statePath+".bak")
	return nil
}

// ---------------------------------------------------------------- I/O

func writeTestFile(path string, tf stt.TestFile) error {
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func readReport(evalDir string) ([]reportRow, error) {
	var rows []reportRow
	err := readJSONL(filepath.Join(evalDir, "report.jsonl"), &rows)
	return rows, err
}

func writeJSONL[T any](path string, rows []T) error {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

func readJSONL[T any](path string, rows *[]T) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var r T
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		*rows = append(*rows, r)
	}
	return scanner.Err()
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
