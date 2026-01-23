package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/mmp/vice/stt"
)

// LogEntry represents an STT command log entry from the slog file.
type LogEntry struct {
	Time              string                  `json:"time"`
	Level             string                  `json:"level"`
	Msg               string                  `json:"msg"`
	Callstack         []string                `json:"callstack,omitempty"`
	Transcript        string                  `json:"transcript"`
	WhisperDurationMs float64                 `json:"whisper_duration_ms,omitempty"`
	Duration          int64                   `json:"duration,omitempty"`
	AudioDurationMs   float64                 `json:"audio_duration_ms,omitempty"`
	Processor         string                  `json:"processor,omitempty"`
	Callsign          string                  `json:"callsign"`
	Command           string                  `json:"command"`
	STTAircraft       map[string]stt.Aircraft `json:"stt_aircraft"`
	Logs              []string                `json:"logs,omitempty"`
}

// PersistedState stores the review queue and seen entries between sessions.
type PersistedState struct {
	Queue []LogEntry      `json:"queue"`
	Seen  map[string]bool `json:"seen"`
}

// AppState holds the runtime state of the application.
type AppState struct {
	entries      []LogEntry
	currentIndex int
	correction   string // editable correction (CALLSIGN COMMAND format)
	cursorPos    int
	showContext  bool

	// Search state
	searchMode     bool   // true when actively typing search
	searchString   string // current search query
	searchLocked   bool   // true when search is locked (after Enter)
	filteredIdx    []int  // indices into entries that match search
	filteredCursor int    // position within filteredIdx
}

// Action represents the result of handling an event.
type Action int

const (
	ActionNone Action = iota
	ActionQuit
	ActionSkip
	ActionSave
)

const stateDir = "~/.sttreview"

func main() {
	outputDir := flag.String("output", "stt/tests", "output directory for saved tests")
	ingestMode := flag.Bool("ingest", false, "ingest entries only, don't start review UI")
	showStatus := flag.Bool("status", false, "show queue status and exit")
	flag.Parse()

	// Load persisted state
	persisted := loadState()

	// Handle -status
	if *showStatus {
		fmt.Printf("Queue: %d entries to review\n", len(persisted.Queue))
		fmt.Printf("Seen: %d entries processed\n", len(persisted.Seen))
		return
	}

	// Ingest from file if provided
	if len(flag.Args()) > 0 {
		entries, err := loadEntriesFromFile(flag.Args()[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading file: %v\n", err)
			os.Exit(1)
		}
		added := persisted.ingest(entries)
		fmt.Printf("Ingested %d new entries (%d already seen)\n", added, len(entries)-added)
		if err := persisted.save(); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
			os.Exit(1)
		}
		if *ingestMode {
			return
		}
	}

	if len(persisted.Queue) == 0 {
		fmt.Println("No entries to review. Use: sttreview <logfile>")
		return
	}

	// Start UI
	screen, err := tcell.NewScreen()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating screen: %v\n", err)
		os.Exit(1)
	}
	if err := screen.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing screen: %v\n", err)
		os.Exit(1)
	}
	defer screen.Fini()

	screen.SetStyle(tcell.StyleDefault.
		Background(tcell.ColorReset).
		Foreground(tcell.ColorReset))

	// Shuffle the queue for random order
	rand.Shuffle(len(persisted.Queue), func(i, j int) {
		persisted.Queue[i], persisted.Queue[j] = persisted.Queue[j], persisted.Queue[i]
	})

	appState := &AppState{entries: persisted.Queue}
	// Initialize correction from first entry
	if len(appState.entries) > 0 {
		appState.initFromEntry(appState.entries[0])
	}

	for {
		render(screen, appState)
		screen.Show()

		ev := screen.PollEvent()
		action := handleEvent(ev, appState, screen)

		switch action {
		case ActionQuit:
			// Save remaining queue
			persisted.Queue = appState.entries[appState.currentIndex:]
			if err := persisted.save(); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
			}
			return
		case ActionSkip:
			currentEntry := getCurrentEntry(appState)
			if currentEntry == nil {
				continue
			}
			persisted.markDone(*currentEntry)
			advanceToNextEntry(appState)
			if err := persisted.save(); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
			}
		case ActionSave:
			currentEntry := getCurrentEntry(appState)
			if currentEntry == nil {
				continue
			}
			if err := saveEntry(*currentEntry, appState.correction, *outputDir); err != nil {
				// Show error briefly - for now just continue
				_ = err
			}
			persisted.markDone(*currentEntry)
			advanceToNextEntry(appState)
			if err := persisted.save(); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
			}
		}

		// Check if done (only when not in locked search mode)
		if !appState.searchLocked && appState.currentIndex >= len(appState.entries) {
			// Clear the queue since all entries have been processed
			persisted.Queue = nil
			if err := persisted.save(); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
			}
			return
		}
	}
}

// initFromEntry initializes the correction field from an entry.
func (state *AppState) initFromEntry(entry LogEntry) {
	callsign := strings.TrimSuffix(entry.Callsign, "/T")
	state.correction = callsign + " " + entry.Command
	state.cursorPos = len(state.correction)
}

// updateSearchFilter updates the filtered indices based on the current search string.
func (state *AppState) updateSearchFilter() {
	if state.searchString == "" {
		state.filteredIdx = nil
		state.filteredCursor = 0
		return
	}

	searchLower := strings.ToLower(state.searchString)
	state.filteredIdx = nil

	for i := state.currentIndex; i < len(state.entries); i++ {
		entry := state.entries[i]
		// Match against transcript, callsign, or command (case-insensitive)
		transcriptLower := strings.ToLower(entry.Transcript)
		callsignLower := strings.ToLower(entry.Callsign)
		commandLower := strings.ToLower(entry.Command)
		if strings.Contains(transcriptLower, searchLower) ||
			strings.Contains(callsignLower, searchLower) ||
			strings.Contains(commandLower, searchLower) {
			state.filteredIdx = append(state.filteredIdx, i)
		}
	}
	state.filteredCursor = 0
}

// clearSearch clears search state and reverts to showing all entries.
func (state *AppState) clearSearch() {
	state.searchMode = false
	state.searchString = ""
	state.searchLocked = false
	state.filteredIdx = nil
	state.filteredCursor = 0
}

// getCurrentEntry returns a pointer to the current entry based on search state.
func getCurrentEntry(state *AppState) *LogEntry {
	if state.searchLocked && len(state.filteredIdx) > 0 {
		if state.filteredCursor < len(state.filteredIdx) {
			return &state.entries[state.filteredIdx[state.filteredCursor]]
		}
		return nil
	}
	if state.currentIndex < len(state.entries) {
		return &state.entries[state.currentIndex]
	}
	return nil
}

// advanceToNextEntry moves to the next entry based on search state.
func advanceToNextEntry(state *AppState) {
	if state.searchLocked && len(state.filteredIdx) > 0 {
		// In locked search mode, advance within filtered list
		state.filteredCursor++
		if state.filteredCursor < len(state.filteredIdx) {
			state.initFromEntry(state.entries[state.filteredIdx[state.filteredCursor]])
		} else {
			// Reached end of filtered list - clear search and continue
			state.clearSearch()
		}
	} else {
		// Normal mode
		state.currentIndex++
		if state.currentIndex < len(state.entries) {
			state.initFromEntry(state.entries[state.currentIndex])
		}
	}
}

// expandPath expands ~ to home directory.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// loadState loads persisted state from disk.
func loadState() *PersistedState {
	path := expandPath(stateDir + "/state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return &PersistedState{Seen: make(map[string]bool)}
	}
	var state PersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return &PersistedState{Seen: make(map[string]bool)}
	}
	if state.Seen == nil {
		state.Seen = make(map[string]bool)
	}

	// Filter out any entries that are already in the Seen set
	var filtered []LogEntry
	for _, e := range state.Queue {
		if !state.Seen[entryHash(e)] {
			filtered = append(filtered, e)
		}
	}
	state.Queue = filtered

	return &state
}

// save persists the state to disk.
func (s *PersistedState) save() error {
	dir := expandPath(stateDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), data, 0644)
}

// ingest adds new entries to the queue, skipping already-seen ones.
func (s *PersistedState) ingest(entries []LogEntry) int {
	added := 0
	for _, e := range entries {
		h := entryHash(e)
		if !s.Seen[h] {
			s.Queue = append(s.Queue, e)
			added++
		}
	}
	return added
}

// markDone marks an entry as processed.
func (s *PersistedState) markDone(e LogEntry) {
	s.Seen[entryHash(e)] = true
}

// entryHash computes a unique hash for an entry.
func entryHash(e LogEntry) string {
	h := sha256.New()
	h.Write([]byte(e.Transcript + "|" + e.Callsign + "|" + e.Command))
	return hex.EncodeToString(h.Sum(nil))
}

// loadEntriesFromFile parses a slog file and extracts STT command entries.
func loadEntriesFromFile(path string) ([]LogEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large JSON objects
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var currentJSON strings.Builder
	braceCount := 0
	inString := false
	escaped := false

	for scanner.Scan() {
		line := scanner.Text()

		// Track JSON object boundaries
		for _, ch := range line {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' && inString {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			if ch == '{' {
				braceCount++
			} else if ch == '}' {
				braceCount--
			}
		}

		currentJSON.WriteString(line)
		currentJSON.WriteString("\n")

		// When we've closed all braces, we have a complete JSON object
		if braceCount == 0 && currentJSON.Len() > 0 {
			jsonStr := strings.TrimSpace(currentJSON.String())
			if jsonStr != "" {
				var entry LogEntry
				if err := json.Unmarshal([]byte(jsonStr), &entry); err == nil {
					if entry.Msg == "STT command" && entry.Transcript != "" {
						entries = append(entries, entry)
					}
				}
			}
			currentJSON.Reset()
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}

// wrapText breaks text into lines, breaking at spaces, with optional indent for continuation.
func wrapText(text string, firstLineWidth, contLineWidth int) []string {
	if len(text) <= firstLineWidth {
		return []string{text}
	}

	var lines []string
	remaining := text
	isFirstLine := true

	for len(remaining) > 0 {
		lineWidth := contLineWidth
		if isFirstLine {
			lineWidth = firstLineWidth
		}

		if len(remaining) <= lineWidth {
			lines = append(lines, remaining)
			break
		}

		// Find last space within lineWidth
		breakAt := lineWidth
		for i := lineWidth; i > 0; i-- {
			if remaining[i] == ' ' {
				breakAt = i
				break
			}
		}

		// If no space found, force break at lineWidth
		if breakAt == lineWidth && remaining[lineWidth] != ' ' {
			// Look for any space
			found := false
			for i := lineWidth - 1; i > 0; i-- {
				if remaining[i] == ' ' {
					breakAt = i
					found = true
					break
				}
			}
			if !found {
				breakAt = lineWidth
			}
		}

		lines = append(lines, remaining[:breakAt])
		remaining = strings.TrimLeft(remaining[breakAt:], " ")
		isFirstLine = false
	}

	return lines
}

// isValidCallsign checks if the callsign exists in the entry's aircraft context.
func isValidCallsign(callsign string, entry LogEntry) bool {
	if callsign == "" {
		return false
	}
	for key, ac := range entry.STTAircraft {
		if ac.Callsign == callsign || key == callsign {
			return true
		}
	}
	return false
}

// getAircraftForCallsign finds the aircraft context for a callsign.
func getAircraftForCallsign(callsign string, entry LogEntry) *stt.Aircraft {
	for key, ac := range entry.STTAircraft {
		if ac.Callsign == callsign || key == callsign {
			return &ac
		}
	}
	return nil
}

// parseCorrection parses the correction string into callsign and command.
// Returns (callsign, command, valid).
// - If correction is empty, returns original values.
// - If correction is a single space, returns empty callsign and command (ignore transmission).
// - Otherwise, parses as "CALLSIGN COMMAND" format.
func parseCorrection(correction string, entry LogEntry) (string, string, bool) {
	if correction == "" {
		return strings.TrimSuffix(entry.Callsign, "/T"), entry.Command, true
	}

	// Single space means "ignore this transmission"
	if correction == " " {
		return "", "", true
	}

	// Split on first space to get callsign and command
	parts := strings.SplitN(strings.TrimSpace(correction), " ", 2)
	if len(parts) == 0 {
		return "", "", false
	}

	callsign := parts[0]
	command := ""
	if len(parts) > 1 {
		command = parts[1]
	}

	// Validate callsign
	valid := isValidCallsign(callsign, entry)
	return callsign, command, valid
}

// render draws the UI.
func render(screen tcell.Screen, state *AppState) {
	screen.Clear()
	width, height := screen.Size()

	// Styles
	styleDefault := tcell.StyleDefault.Foreground(tcell.ColorDarkBlue)
	styleHeader := tcell.StyleDefault.Bold(true).Reverse(true)
	styleCurrent := tcell.StyleDefault.Foreground(tcell.ColorBlack).Bold(true)
	styleHelp := tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleColumn := tcell.StyleDefault.Foreground(tcell.ColorTeal)
	styleInvalid := tcell.StyleDefault.Foreground(tcell.ColorRed).Bold(true)
	styleContext := tcell.StyleDefault.Background(tcell.ColorWhite).Foreground(tcell.ColorBlack)
	styleContextLabel := tcell.StyleDefault.Background(tcell.ColorWhite).Foreground(tcell.ColorDarkBlue).Bold(true)

	// Header
	var title string
	if state.searchLocked {
		matchCount := len(state.filteredIdx)
		title = fmt.Sprintf(" STT Review [%d/%d] Search: %q (%d matches) ",
			state.filteredCursor+1, matchCount, state.searchString, matchCount)
	} else if state.searchMode {
		title = fmt.Sprintf(" STT Review - Search: %s_ ", state.searchString)
	} else {
		title = fmt.Sprintf(" STT Review [%d/%d] ", state.currentIndex+1, len(state.entries))
	}
	help := " [-]=Skip [\u23ce]=Save "
	if state.searchMode {
		help = " [Esc]=Cancel [Enter]=Lock "
	} else if state.searchLocked {
		help = " [Esc]=Clear Search "
	}
	drawText(screen, 0, 0, width, styleHeader, title+strings.Repeat(" ", max(0, width-len(title)-len(help)))+help)

	// Handle search mode with no matches
	if state.searchMode && state.searchString != "" && len(state.filteredIdx) == 0 {
		drawText(screen, 0, 3, width, tcell.StyleDefault.Foreground(tcell.ColorRed),
			" No matches found for: "+state.searchString)
		return
	}

	// Determine current entry based on search state
	var entry LogEntry
	var displayEntries []LogEntry
	var displayIndex int

	if state.searchLocked && len(state.filteredIdx) > 0 {
		// Locked search: show filtered entries
		displayIndex = state.filteredCursor
		entry = state.entries[state.filteredIdx[displayIndex]]
		displayEntries = make([]LogEntry, len(state.filteredIdx))
		for i, idx := range state.filteredIdx {
			displayEntries[i] = state.entries[idx]
		}
	} else if state.searchMode && len(state.filteredIdx) > 0 {
		// Active search with matches: show filtered entries
		displayIndex = 0
		entry = state.entries[state.filteredIdx[0]]
		displayEntries = make([]LogEntry, len(state.filteredIdx))
		for i, idx := range state.filteredIdx {
			displayEntries[i] = state.entries[idx]
		}
	} else if state.searchMode {
		// Active search with empty query: show all from current
		if state.currentIndex >= len(state.entries) {
			return
		}
		displayIndex = 0
		entry = state.entries[state.currentIndex]
		displayEntries = state.entries[state.currentIndex:]
	} else {
		// Normal mode: show all entries from currentIndex
		if state.currentIndex >= len(state.entries) {
			return
		}
		displayIndex = 0
		entry = state.entries[state.currentIndex]
		displayEntries = state.entries[state.currentIndex:]
	}

	// Column 2: callsign + command (read-only, from entry)
	entryCallsign := strings.TrimSuffix(entry.Callsign, "/T")
	col2Display := entryCallsign + " " + entry.Command

	// Calculate col2Width based on content - find max width needed
	col2Width := len(col2Display)
	// Check upcoming entries too
	for i := 1; i < 20 && displayIndex+i < len(displayEntries); i++ {
		nextEntry := displayEntries[displayIndex+i]
		nextCallsign := strings.TrimSuffix(nextEntry.Callsign, "/T")
		nextCol2 := nextCallsign + " " + nextEntry.Command
		if len(nextCol2) > col2Width {
			col2Width = len(nextCol2)
		}
	}
	// Add some padding and ensure minimum width for header
	col2Width = max(col2Width+2, len("CALLSIGN CMD")+2)

	// Column widths
	indent := 4
	col3Width := 25                                // Fixed width for correction column
	col1Width := width - col2Width - col3Width - 6 // 6 for separators and prefix
	if col1Width < 30 {
		col1Width = 30
		col2Width = width - col1Width - col3Width - 6
	}

	headerLine := fmt.Sprintf(" %-*s \u2502 %-*s \u2502 %-*s",
		col1Width, "TRANSCRIPT",
		col2Width, "CALLSIGN CMD",
		col3Width, "CORRECTION")
	drawText(screen, 0, 1, width, styleColumn, headerLine)

	// Separator
	drawText(screen, 0, 2, width, styleDefault, strings.Repeat("\u2500", width))

	y := 3

	// Column 3: correction (editable) or search input
	var correctionDisplay string
	correctionStyle := styleCurrent
	styleSearch := tcell.StyleDefault.Foreground(tcell.ColorYellow).Bold(true)

	if state.searchMode {
		// Show search input with "/" prefix
		correctionDisplay = "/" + state.searchString + "_"
		correctionStyle = styleSearch
	} else if state.correction == " " {
		// Special display for "ignore" (single space)
		correctionDisplay = "(ignore)_"
	} else {
		correctionDisplay = state.correction
		// Add cursor
		if state.cursorPos <= len(correctionDisplay) {
			before := correctionDisplay[:state.cursorPos]
			after := correctionDisplay[state.cursorPos:]
			correctionDisplay = before + "_" + after
		}

		// Check if correction is valid (if non-empty)
		if state.correction != "" {
			_, _, valid := parseCorrection(state.correction, entry)
			if !valid {
				correctionStyle = styleInvalid
			}
		}
	}

	// Wrap transcript text
	transcriptLines := wrapText(entry.Transcript, col1Width, col1Width-indent)

	// Draw first line
	firstTranscript := fmt.Sprintf("%-*s", col1Width, transcriptLines[0])

	drawText(screen, 0, y, 1, styleCurrent, ">")
	drawText(screen, 1, y, col1Width, styleCurrent, firstTranscript)
	drawText(screen, col1Width+1, y, 3, styleCurrent, " \u2502 ")
	// Column 2: callsign + command (read-only)
	drawText(screen, col1Width+4, y, col2Width, styleCurrent, fmt.Sprintf("%-*s", col2Width, col2Display))
	drawText(screen, col1Width+4+col2Width, y, 3, styleCurrent, " \u2502 ")
	// Column 3: correction
	drawText(screen, col1Width+4+col2Width+3, y, col3Width, correctionStyle, fmt.Sprintf("%-*s", col3Width, truncate(correctionDisplay, col3Width)))
	y++

	// Draw continuation lines for transcript
	for i := 1; i < len(transcriptLines); i++ {
		contLine := fmt.Sprintf(" %s%-*s \u2502 %-*s \u2502 %-*s",
			strings.Repeat(" ", indent),
			col1Width-indent, transcriptLines[i],
			col2Width, "",
			col3Width, "")
		drawText(screen, 0, y, width, styleCurrent, contLine)
		y++
	}

	// Show context or upcoming entries
	y++ // blank line
	if state.showContext {
		// Draw context on white background
		maxY := height - 2

		// Always show context for the entry's original callsign
		ac := getAircraftForCallsign(entryCallsign, entry)
		if ac != nil {
			// Aircraft info
			drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
			drawText(screen, 0, y, width, styleContextLabel, fmt.Sprintf(" Aircraft: %s (%s)", ac.Callsign, ac.AircraftType))
			y++

			if y < maxY {
				info := fmt.Sprintf(" State: %s   Altitude: %d", ac.State, ac.Altitude)
				if ac.SID != "" {
					info += fmt.Sprintf("   SID: %s", ac.SID)
				}
				if ac.STAR != "" {
					info += fmt.Sprintf("   STAR: %s", ac.STAR)
				}
				drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, info))
				y++
			}

			// Controller info
			if y < maxY && (ac.ControllerFrequency != "" || ac.TrackingController != "") {
				ctrlInfo := " "
				if ac.ControllerFrequency != "" {
					ctrlInfo += fmt.Sprintf("Frequency: %s", ac.ControllerFrequency)
				}
				if ac.TrackingController != "" {
					if ctrlInfo != " " {
						ctrlInfo += "   "
					}
					ctrlInfo += fmt.Sprintf("Tracking: %s", ac.TrackingController)
				}
				drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, ctrlInfo))
				y++
			}

			if ac.AssignedApproach != "" && y < maxY {
				drawText(screen, 0, y, width, styleContext, fmt.Sprintf(" Assigned Approach: %-*s", width-20, ac.AssignedApproach))
				y++
			}

			// Fixes (sorted alphabetically)
			if len(ac.Fixes) > 0 && y < maxY {
				drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
				drawText(screen, 0, y, width, styleContextLabel, " Fixes:")
				y++

				// Sort fix names
				var fixNames []string
				for spoken := range ac.Fixes {
					fixNames = append(fixNames, spoken)
				}
				sort.Strings(fixNames)

				fixLine := " "
				for _, spoken := range fixNames {
					id := ac.Fixes[spoken]
					part := fmt.Sprintf("%s\u2192%s  ", spoken, id)
					if len(fixLine)+len(part) > width-2 {
						if y < maxY {
							drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, fixLine))
							y++
						}
						fixLine = "   " + part
					} else {
						fixLine += part
					}
				}
				if fixLine != " " && fixLine != "   " && y < maxY {
					drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, fixLine))
					y++
				}
			}

			// Approaches (sorted alphabetically)
			if len(ac.CandidateApproaches) > 0 && y < maxY {
				drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
				drawText(screen, 0, y, width, styleContextLabel, " Approaches:")
				y++

				// Sort approach names
				var apprNames []string
				for spoken := range ac.CandidateApproaches {
					apprNames = append(apprNames, spoken)
				}
				sort.Strings(apprNames)

				apprLine := " "
				for _, spoken := range apprNames {
					id := ac.CandidateApproaches[spoken]
					part := fmt.Sprintf("%s\u2192%s  ", spoken, id)
					if len(apprLine)+len(part) > width-2 {
						if y < maxY {
							drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, apprLine))
							y++
						}
						apprLine = "   " + part
					} else {
						apprLine += part
					}
				}
				if apprLine != " " && apprLine != "   " && y < maxY {
					drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, apprLine))
					y++
				}
			}
		}

		// Always show list of available callsigns (sorted alphabetically)
		if y < maxY {
			drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
			drawText(screen, 0, y, width, styleContextLabel, " All callsigns:")
			y++

			// Collect unique callsigns and sort
			callsignSet := make(map[string]bool)
			for _, a := range entry.STTAircraft {
				callsignSet[a.Callsign] = true
			}
			var callsigns []string
			for cs := range callsignSet {
				callsigns = append(callsigns, cs)
			}
			sort.Strings(callsigns)

			csLine := " "
			for _, cs := range callsigns {
				part := cs + "  "
				if len(csLine)+len(part) > width-2 {
					if y < maxY {
						drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, csLine))
						y++
					}
					csLine = " " + part
				} else {
					csLine += part
				}
			}
			if csLine != " " && y < maxY {
				drawText(screen, 0, y, width, styleContext, fmt.Sprintf("%-*s", width, csLine))
				y++
			}
		}

		// Fill remaining context area with white
		for y < maxY {
			drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
			y++
		}
	} else {
		// Show upcoming entries
		maxY := height - 2
		entryIdx := 1

		for y < maxY && displayIndex+entryIdx < len(displayEntries) {
			nextEntry := displayEntries[displayIndex+entryIdx]

			// Wrap transcript text
			nextTranscriptLines := wrapText(nextEntry.Transcript, col1Width, col1Width-indent)

			// Draw first line - column 2 shows callsign + command
			nextCallsign := strings.TrimSuffix(nextEntry.Callsign, "/T")
			nextCol2 := nextCallsign + " " + nextEntry.Command
			line := fmt.Sprintf(" %-*s \u2502 %-*s \u2502 %-*s",
				col1Width, nextTranscriptLines[0],
				col2Width, nextCol2,
				col3Width, "")
			drawText(screen, 0, y, width, styleDefault, line)
			y++

			// Draw continuation lines
			for i := 1; i < len(nextTranscriptLines) && y < maxY; i++ {
				contLine := fmt.Sprintf(" %s%-*s \u2502 %-*s \u2502 %-*s",
					strings.Repeat(" ", indent),
					col1Width-indent, nextTranscriptLines[i],
					col2Width, "",
					col3Width, "")
				drawText(screen, 0, y, width, styleDefault, contLine)
				y++
			}

			entryIdx++
		}
	}

	// Help footer
	helpY := height - 1
	var helpText string
	if state.searchMode {
		helpText = " Type to search  [Enter]=Lock results  [Esc]=Cancel search "
	} else if state.searchLocked {
		contextHint := "[`]=Context "
		if state.showContext {
			contextHint = "[`]=Hide "
		}
		helpText = fmt.Sprintf(" %s[-]=Skip  [Enter]=Save  [Esc]=Clear search  [/]=New search ", contextHint)
	} else {
		contextHint := "[`]=Context "
		if state.showContext {
			contextHint = "[`]=Hide "
		}
		helpText = fmt.Sprintf(" %s[-]=Skip  [Enter]=Save  [Space]=Ignore  [Esc]=Quit  [/]=Search ", contextHint)
	}
	drawText(screen, 0, helpY, width, styleHelp, helpText)
}

// drawText draws a string at the given position.
func drawText(screen tcell.Screen, x, y, maxWidth int, style tcell.Style, text string) {
	col := 0
	for _, r := range text {
		if col >= maxWidth {
			break
		}
		screen.SetContent(x+col, y, r, nil, style)
		col++
	}
	// Fill remaining space
	for col < maxWidth {
		screen.SetContent(x+col, y, ' ', nil, style)
		col++
	}
}

// truncate truncates a string to fit within maxLen.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// handleEvent processes a tcell event and returns the appropriate action.
func handleEvent(ev tcell.Event, state *AppState, screen tcell.Screen) Action {
	switch ev := ev.(type) {
	case *tcell.EventResize:
		screen.Sync()
		return ActionNone

	case *tcell.EventKey:
		// Handle search mode
		if state.searchMode {
			return handleSearchEvent(ev, state)
		}

		// Handle locked search - Escape clears it
		if state.searchLocked {
			if ev.Key() == tcell.KeyEscape {
				state.clearSearch()
				return ActionNone
			}
			// Other keys work normally on the filtered list
		}

		switch ev.Key() {
		case tcell.KeyEscape:
			return ActionQuit

		case tcell.KeyEnter:
			return ActionSave

		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if state.cursorPos > 0 {
				state.correction = state.correction[:state.cursorPos-1] + state.correction[state.cursorPos:]
				state.cursorPos--
			}

		case tcell.KeyDelete:
			if state.cursorPos < len(state.correction) {
				state.correction = state.correction[:state.cursorPos] + state.correction[state.cursorPos+1:]
			}

		case tcell.KeyLeft:
			if state.cursorPos > 0 {
				state.cursorPos--
			}

		case tcell.KeyRight:
			if state.cursorPos < len(state.correction) {
				state.cursorPos++
			}

		case tcell.KeyHome:
			state.cursorPos = 0

		case tcell.KeyEnd:
			state.cursorPos = len(state.correction)

		case tcell.KeyRune:
			r := ev.Rune()
			if r == '`' {
				// Toggle context display
				state.showContext = !state.showContext
				return ActionNone
			}
			if r == '-' && !state.searchLocked {
				return ActionSkip
			}
			// '/' at start of empty correction enters search mode
			if r == '/' && state.correction == "" && state.cursorPos == 0 {
				state.searchMode = true
				state.searchString = ""
				return ActionNone
			}
			// Auto-uppercase
			r = unicode.ToUpper(r)
			state.correction = state.correction[:state.cursorPos] + string(r) + state.correction[state.cursorPos:]
			state.cursorPos++
		}
	}

	return ActionNone
}

// handleSearchEvent handles keyboard events while in search mode.
func handleSearchEvent(ev *tcell.EventKey, state *AppState) Action {
	switch ev.Key() {
	case tcell.KeyEscape:
		// Clear search and exit search mode
		state.clearSearch()
		return ActionNone

	case tcell.KeyEnter:
		// Lock the search results
		if state.searchString != "" && len(state.filteredIdx) > 0 {
			state.searchMode = false
			state.searchLocked = true
			// Initialize correction for the first filtered entry
			if len(state.filteredIdx) > 0 {
				state.initFromEntry(state.entries[state.filteredIdx[0]])
			}
		} else {
			// No matches or empty search - just exit search mode
			state.clearSearch()
		}
		return ActionNone

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(state.searchString) > 0 {
			state.searchString = state.searchString[:len(state.searchString)-1]
			state.updateSearchFilter()
		}
		return ActionNone

	case tcell.KeyRune:
		r := ev.Rune()
		state.searchString += string(r)
		state.updateSearchFilter()
		return ActionNone
	}

	return ActionNone
}

// saveEntry saves an entry to the output directory as a test case.
// If correction is provided, it should be in "CALLSIGN COMMAND" format.
func saveEntry(entry LogEntry, correction, outputDir string) error {
	// Parse correction if provided
	if correction != "" {
		callsign, command, valid := parseCorrection(correction, entry)
		if !valid {
			return fmt.Errorf("invalid callsign in correction: %s", callsign)
		}
		entry.Callsign = callsign
		entry.Command = command
	}

	// Create output directory if needed
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	// Generate filename from transcript
	filename := sanitizeFilename(entry.Transcript) + ".json"
	path := filepath.Join(outputDir, filename)

	// Handle collision by adding suffix
	base := sanitizeFilename(entry.Transcript)
	for i := 1; fileExists(path); i++ {
		path = filepath.Join(outputDir, fmt.Sprintf("%s_%d.json", base, i))
	}

	// Marshal with indentation
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// sanitizeFilename creates a safe filename from a transcript.
func sanitizeFilename(transcript string) string {
	// Convert to lowercase and replace spaces with underscores
	s := strings.ToLower(transcript)
	s = strings.ReplaceAll(s, " ", "_")

	// Remove non-alphanumeric characters except underscores
	reg := regexp.MustCompile(`[^a-z0-9_]`)
	s = reg.ReplaceAllString(s, "")

	// Truncate to reasonable length
	if len(s) > 50 {
		s = s[:50]
	}

	// Handle empty result
	if s == "" {
		s = "entry"
	}

	return s
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
