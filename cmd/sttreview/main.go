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
	"time"
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
	WhisperModel      string                  `json:"whisper_model,omitempty"`
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

// FocusedField indicates which input field has keyboard focus.
type FocusedField int

const (
	FocusSearch FocusedField = iota
	FocusCorrection
)

// Disposition indicates what action was taken on an entry.
type Disposition int

const (
	DispositionNone Disposition = iota
	DispositionSaved
	DispositionSkipped
)

// AppState holds the runtime state of the application.
type AppState struct {
	entries            []LogEntry
	selectedIndex      int    // which entry is currently selected
	scrollOffset       int    // first visible entry index
	correction         string // editable correction (CALLSIGN COMMAND format)
	correctionEntryIdx int    // entry index that correction was initialized from
	cursorPos          int
	showContext        bool

	// Focus state
	focusedField FocusedField

	// Search state
	searchString    string // current search query
	searchCursorPos int    // cursor position in search string
	filteredIdx     []int  // indices into entries that match search

	// Disposition tracking
	disposition map[int]Disposition // entry index -> disposition
	savedFiles  map[int]string      // entry index -> saved filename (for deletion if changed)

	// Output directory
	outputDir string
}

// Action represents the result of handling an event.
type Action int

const (
	ActionNone Action = iota
	ActionQuit
)

const stateDir = "~/.sttreview"

func main() {
	outputDir := flag.String("output", "stt/failing_tests", "output directory for saved tests")
	ingestMode := flag.Bool("ingest", false, "ingest entries only, don't start review UI")
	showStatus := flag.Bool("status", false, "show queue status and exit")
	lifoMode := flag.Bool("lifo", false, "order entries by time, most recent first")
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

	// Order the queue: LIFO (most recent first) or random
	if *lifoMode {
		sort.Slice(persisted.Queue, func(i, j int) bool {
			ti, _ := time.Parse(time.RFC3339Nano, persisted.Queue[i].Time)
			tj, _ := time.Parse(time.RFC3339Nano, persisted.Queue[j].Time)
			return ti.After(tj)
		})
	} else {
		rand.Shuffle(len(persisted.Queue), func(i, j int) {
			persisted.Queue[i], persisted.Queue[j] = persisted.Queue[j], persisted.Queue[i]
		})
	}

	appState := &AppState{
		entries:      persisted.Queue,
		disposition:  make(map[int]Disposition),
		savedFiles:   make(map[int]string),
		outputDir:    *outputDir,
		focusedField: FocusCorrection,
	}
	// Initialize correction from first entry
	if len(appState.entries) > 0 {
		appState.initFromEntry(0)
	}
	// Build initial filter (empty search shows all)
	appState.updateSearchFilter()

	for {
		render(screen, appState)
		screen.Show()

		ev := screen.PollEvent()
		action := handleEvent(ev, appState, screen)

		if action == ActionQuit {
			// Save state: remove entries that have been processed from the queue
			var remaining []LogEntry
			for i, entry := range appState.entries {
				if appState.disposition[i] == DispositionNone {
					remaining = append(remaining, entry)
				} else {
					persisted.markDone(entry)
				}
			}
			persisted.Queue = remaining
			if err := persisted.save(); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
			}
			return
		}
	}
}

// initFromEntry initializes the correction field from an entry at the given index.
func (state *AppState) initFromEntry(entryIdx int) {
	if entryIdx < 0 || entryIdx >= len(state.entries) {
		return
	}
	entry := state.entries[entryIdx]
	callsign := strings.TrimSuffix(entry.Callsign, "/T")
	state.correction = callsign + " " + entry.Command
	state.cursorPos = len(state.correction)
	state.correctionEntryIdx = entryIdx
}

// updateSearchFilter updates the filtered indices based on the current search string.
func (state *AppState) updateSearchFilter() {
	if state.searchString == "" {
		// Show all entries
		state.filteredIdx = make([]int, len(state.entries))
		for i := range state.entries {
			state.filteredIdx[i] = i
		}
		return
	}

	searchLower := strings.ToLower(state.searchString)
	state.filteredIdx = nil

	for i, entry := range state.entries {
		// Match against transcript, callsign, command, or whisper model (case-insensitive)
		transcriptLower := strings.ToLower(entry.Transcript)
		callsignLower := strings.ToLower(entry.Callsign)
		commandLower := strings.ToLower(entry.Command)
		whisperModelLower := strings.ToLower(entry.WhisperModel)
		if strings.Contains(transcriptLower, searchLower) ||
			strings.Contains(callsignLower, searchLower) ||
			strings.Contains(commandLower, searchLower) ||
			strings.Contains(whisperModelLower, searchLower) {
			state.filteredIdx = append(state.filteredIdx, i)
		}
	}

	// Reset selection if it's now out of bounds
	if state.selectedIndex >= len(state.filteredIdx) {
		state.selectedIndex = 0
		state.scrollOffset = 0
	}
}

// getSelectedEntryIndex returns the actual entry index of the currently selected item.
// Returns -1 if no valid selection.
func (state *AppState) getSelectedEntryIndex() int {
	if len(state.filteredIdx) == 0 {
		return -1
	}
	if state.selectedIndex >= len(state.filteredIdx) {
		return -1
	}
	return state.filteredIdx[state.selectedIndex]
}

// getSelectedEntry returns the currently selected entry, or nil if none.
func (state *AppState) getSelectedEntry() *LogEntry {
	idx := state.getSelectedEntryIndex()
	if idx < 0 {
		return nil
	}
	return &state.entries[idx]
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

	// Filter out any entries that are already in the Seen set and deduplicate
	seen := make(map[string]bool)
	var filtered []LogEntry
	for _, e := range state.Queue {
		h := entryHash(e)
		if !state.Seen[h] && !seen[h] {
			filtered = append(filtered, e)
			seen[h] = true
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

// ingest adds new entries to the queue, skipping already-seen ones and duplicates.
func (s *PersistedState) ingest(entries []LogEntry) int {
	// Build set of hashes already in queue
	queued := make(map[string]bool)
	for _, e := range s.Queue {
		queued[entryHash(e)] = true
	}

	added := 0
	for _, e := range entries {
		h := entryHash(e)
		if !s.Seen[h] && !queued[h] {
			s.Queue = append(s.Queue, e)
			queued[h] = true
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
	// Ensure correction is synchronized with selected entry
	selectedIdx := state.getSelectedEntryIndex()
	if selectedIdx >= 0 && selectedIdx != state.correctionEntryIdx {
		state.initFromEntry(selectedIdx)
	}

	screen.Clear()
	width, height := screen.Size()

	// Styles
	styleDefault := tcell.StyleDefault.Foreground(tcell.ColorDarkBlue)
	styleHeader := tcell.StyleDefault.Bold(true).Reverse(true)
	styleSelected := tcell.StyleDefault.Foreground(tcell.ColorBlack).Bold(true)
	styleHelp := tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleColumn := tcell.StyleDefault.Foreground(tcell.ColorTeal)
	styleInvalid := tcell.StyleDefault.Foreground(tcell.ColorRed).Bold(true)
	styleContext := tcell.StyleDefault.Background(tcell.ColorWhite).Foreground(tcell.ColorBlack)
	styleContextLabel := tcell.StyleDefault.Background(tcell.ColorWhite).Foreground(tcell.ColorDarkBlue).Bold(true)
	styleSaved := tcell.StyleDefault.Foreground(tcell.ColorGreen)
	styleSkipped := tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleFocused := tcell.StyleDefault.Foreground(tcell.ColorDarkCyan).Bold(true)
	styleUnfocused := tcell.StyleDefault.Foreground(tcell.ColorGray)

	// Header
	totalCount := len(state.filteredIdx)
	var title string
	if state.searchString != "" {
		title = fmt.Sprintf(" STT Review [%d/%d matches] ", state.selectedIndex+1, totalCount)
	} else {
		title = fmt.Sprintf(" STT Review [%d/%d] ", state.selectedIndex+1, totalCount)
	}
	help := " [^K]=Skip [⏎]=Save "
	drawText(screen, 0, 0, width, styleHeader, title+strings.Repeat(" ", max(0, width-len(title)-len(help)))+help)

	// Search box row
	y := 1
	searchLabel := "Search: "
	searchStyle := styleUnfocused
	if state.focusedField == FocusSearch {
		searchStyle = styleFocused
	}
	drawText(screen, 0, y, len(searchLabel), searchStyle, searchLabel)

	// Draw search box content with cursor if focused
	searchDisplay := state.searchString
	if state.focusedField == FocusSearch {
		if state.searchCursorPos <= len(searchDisplay) {
			searchDisplay = searchDisplay[:state.searchCursorPos] + "_" + searchDisplay[state.searchCursorPos:]
		}
	}
	drawText(screen, len(searchLabel), y, width-len(searchLabel), searchStyle, searchDisplay)
	y++

	// Column layout
	indent := 4
	col1Width := 50 // Transcript
	col2Width := 30 // Callsign + Command
	col3Width := 25 // Correction

	// Adjust widths based on screen size
	totalColWidth := col1Width + col2Width + col3Width + 8 // separators
	if totalColWidth > width {
		col1Width = width - col2Width - col3Width - 8
		if col1Width < 30 {
			col1Width = 30
			col2Width = width - col1Width - col3Width - 8
		}
	}

	headerLine := fmt.Sprintf("   %-*s │ %-*s │ %-*s",
		col1Width, "TRANSCRIPT",
		col2Width, "CALLSIGN CMD",
		col3Width, "CORRECTION")
	drawText(screen, 0, y, width, styleColumn, headerLine)
	y++

	// Separator
	drawText(screen, 0, y, width, styleDefault, strings.Repeat("─", width))
	y++

	// Calculate visible area
	listStartY := y
	listEndY := height - 2 // Leave room for help line
	if state.showContext {
		// In context mode, only show the selected entry to ensure full transcript is visible
		state.scrollOffset = state.selectedIndex
		entryIdx := state.getSelectedEntryIndex()
		if entryIdx >= 0 {
			entry := state.entries[entryIdx]
			transcriptLines := wrapText(entry.Transcript, col1Width, col1Width-indent)
			listEndY = listStartY + len(transcriptLines)
		} else {
			listEndY = listStartY + 1
		}
	}
	visibleLines := listEndY - listStartY

	// Adjust scroll offset to keep selected item visible
	if state.selectedIndex < state.scrollOffset {
		state.scrollOffset = state.selectedIndex
	}
	if state.selectedIndex >= state.scrollOffset+visibleLines {
		state.scrollOffset = state.selectedIndex - visibleLines + 1
	}

	// Draw entries
	for i := state.scrollOffset; i < len(state.filteredIdx) && y < listEndY; i++ {
		entryIdx := state.filteredIdx[i]
		entry := state.entries[entryIdx]
		isSelected := i == state.selectedIndex

		// Determine disposition marker
		dispMarker := " "
		disp := state.disposition[entryIdx]
		if disp == DispositionSaved {
			dispMarker = "+"
		} else if disp == DispositionSkipped {
			dispMarker = "-"
		}

		// Choose style based on selection and disposition
		var lineStyle tcell.Style
		if isSelected {
			lineStyle = styleSelected
		} else if disp == DispositionSaved {
			lineStyle = styleSaved
		} else if disp == DispositionSkipped {
			lineStyle = styleSkipped
		} else {
			lineStyle = styleDefault
		}

		// Column content
		entryCallsign := strings.TrimSuffix(entry.Callsign, "/T")
		col2Display := entryCallsign + " " + entry.Command

		// Wrap transcript text
		transcriptLines := wrapText(entry.Transcript, col1Width, col1Width-indent)

		// For the selected entry, show the correction field
		var correctionDisplay string
		if isSelected {
			correctionDisplay = state.correction
			// Add cursor if correction field is focused
			if state.focusedField == FocusCorrection {
				if state.cursorPos <= len(correctionDisplay) {
					correctionDisplay = correctionDisplay[:state.cursorPos] + "_" + correctionDisplay[state.cursorPos:]
				}
			}
			if state.correction == " " {
				correctionDisplay = "(ignore)"
				if state.focusedField == FocusCorrection {
					correctionDisplay += "_"
				}
			}
		}

		// Draw first line
		prefix := dispMarker
		if isSelected {
			prefix = ">"
		}
		drawText(screen, 0, y, 1, lineStyle, prefix)
		drawText(screen, 1, y, 2, lineStyle, dispMarker+" ")
		drawText(screen, 3, y, col1Width, lineStyle, fmt.Sprintf("%-*s", col1Width, transcriptLines[0]))
		drawText(screen, 3+col1Width, y, 3, lineStyle, " │ ")
		drawText(screen, 3+col1Width+3, y, col2Width, lineStyle, fmt.Sprintf("%-*s", col2Width, truncate(col2Display, col2Width)))
		drawText(screen, 3+col1Width+3+col2Width, y, 3, lineStyle, " │ ")

		// Correction column - only show for selected entry
		if isSelected {
			corrStyle := lineStyle
			if state.focusedField == FocusCorrection {
				corrStyle = styleFocused
			}
			// Check validity
			if state.correction != "" && state.correction != " " {
				_, _, valid := parseCorrection(state.correction, entry)
				if !valid {
					corrStyle = styleInvalid
				}
			}
			drawText(screen, 3+col1Width+3+col2Width+3, y, col3Width, corrStyle, fmt.Sprintf("%-*s", col3Width, truncate(correctionDisplay, col3Width)))
		} else {
			drawText(screen, 3+col1Width+3+col2Width+3, y, col3Width, lineStyle, strings.Repeat(" ", col3Width))
		}
		y++

		// Draw continuation lines for transcript
		for j := 1; j < len(transcriptLines) && y < listEndY; j++ {
			contLine := fmt.Sprintf("   %s%-*s │ %-*s │ %-*s",
				strings.Repeat(" ", indent),
				col1Width-indent, transcriptLines[j],
				col2Width, "",
				col3Width, "")
			drawText(screen, 0, y, width, lineStyle, contLine)
			y++
		}
	}

	// Show context if enabled and we have a selected entry
	if state.showContext {
		entry := state.getSelectedEntry()
		if entry != nil {
			y++ // blank line
			maxY := height - 2
			entryCallsign := strings.TrimSuffix(entry.Callsign, "/T")

			// Show whisper model if available
			if entry.WhisperModel != "" && y < maxY {
				drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
				drawText(screen, 0, y, width, styleContextLabel, fmt.Sprintf(" Whisper Model: %s", entry.WhisperModel))
				y++
			}

			// Determine which callsign to show context for
			contextCallsign := entryCallsign
			if state.correction != "" && state.correction != " " {
				parts := strings.SplitN(strings.TrimSpace(state.correction), " ", 2)
				if len(parts) > 0 && isValidCallsign(parts[0], *entry) {
					contextCallsign = parts[0]
				}
			}

			ac := getAircraftForCallsign(contextCallsign, *entry)
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

				if len(ac.LAHSORunways) > 0 && y < maxY {
					drawText(screen, 0, y, width, styleContext, fmt.Sprintf(" LAHSO Runways: %-*s", width-17, strings.Join(ac.LAHSORunways, ", ")))
					y++
				}

				// Fixes (sorted alphabetically)
				if len(ac.Fixes) > 0 && y < maxY {
					drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
					drawText(screen, 0, y, width, styleContextLabel, " Fixes:")
					y++

					var fixNames []string
					for spoken := range ac.Fixes {
						fixNames = append(fixNames, spoken)
					}
					sort.Strings(fixNames)

					fixLine := " "
					for _, spoken := range fixNames {
						id := ac.Fixes[spoken]
						part := fmt.Sprintf("%s→%s  ", spoken, id)
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

					var apprNames []string
					for spoken := range ac.CandidateApproaches {
						apprNames = append(apprNames, spoken)
					}
					sort.Strings(apprNames)

					apprLine := " "
					for _, spoken := range apprNames {
						id := ac.CandidateApproaches[spoken]
						part := fmt.Sprintf("%s→%s  ", spoken, id)
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

				// Show approach fixes if there's an assigned approach or expect approach command
				if len(ac.ApproachFixes) > 0 && y < maxY {
					approachID := ac.AssignedApproach
					if approachID == "" {
						approachID = extractExpectApproachID(entry.Command)
					}
					if approachID != "" {
						if approachFixes, ok := ac.ApproachFixes[approachID]; ok && len(approachFixes) > 0 {
							drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
							drawText(screen, 0, y, width, styleContextLabel, fmt.Sprintf(" Approach %s Fixes:", approachID))
							y++

							var fixNames []string
							for spoken := range approachFixes {
								fixNames = append(fixNames, spoken)
							}
							sort.Strings(fixNames)

							fixLine := " "
							for _, spoken := range fixNames {
								id := approachFixes[spoken]
								part := fmt.Sprintf("%s→%s  ", spoken, id)
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
					}
				}
			}

			// Always show list of available callsigns (sorted alphabetically)
			if y < maxY {
				drawText(screen, 0, y, width, styleContext, strings.Repeat(" ", width))
				drawText(screen, 0, y, width, styleContextLabel, " All callsigns:")
				y++

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
		}
	}

	// Help footer
	helpY := height - 1
	contextHint := "[`]=Context "
	if state.showContext {
		contextHint = "[`]=Hide "
	}
	focusHint := "[Tab]=Switch "
	helpText := fmt.Sprintf(" %s%s[↑↓]=Select  [^K]=Skip  [Enter]=Save  [Space]=Ignore  [Esc]=Clear/Quit ", contextHint, focusHint)
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
		// Handle Tab to switch focus
		if ev.Key() == tcell.KeyTab {
			if state.focusedField == FocusSearch {
				state.focusedField = FocusCorrection
			} else {
				state.focusedField = FocusSearch
			}
			return ActionNone
		}

		// Handle Escape - clear search if focused and non-empty, otherwise quit
		if ev.Key() == tcell.KeyEscape {
			if state.focusedField == FocusSearch && state.searchString != "" {
				state.searchString = ""
				state.searchCursorPos = 0
				state.updateSearchFilter()
				return ActionNone
			}
			return ActionQuit
		}

		// Handle Up/Down for list navigation (always work regardless of focus)
		if ev.Key() == tcell.KeyUp {
			if state.selectedIndex > 0 {
				state.selectedIndex--
				// Initialize correction for newly selected entry
				state.initFromEntry(state.getSelectedEntryIndex())
			}
			return ActionNone
		}
		if ev.Key() == tcell.KeyDown {
			if state.selectedIndex < len(state.filteredIdx)-1 {
				state.selectedIndex++
				// Initialize correction for newly selected entry
				state.initFromEntry(state.getSelectedEntryIndex())
			}
			return ActionNone
		}

		// Handle input based on focused field
		if state.focusedField == FocusSearch {
			return handleSearchInput(ev, state)
		}
		return handleCorrectionInput(ev, state)
	}

	return ActionNone
}

// handleSearchInput handles keyboard input for the search field.
func handleSearchInput(ev *tcell.EventKey, state *AppState) Action {
	switch ev.Key() {
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if state.searchCursorPos > 0 {
			state.searchString = state.searchString[:state.searchCursorPos-1] + state.searchString[state.searchCursorPos:]
			state.searchCursorPos--
			state.updateSearchFilter()
		}
		return ActionNone

	case tcell.KeyDelete:
		if state.searchCursorPos < len(state.searchString) {
			state.searchString = state.searchString[:state.searchCursorPos] + state.searchString[state.searchCursorPos+1:]
			state.updateSearchFilter()
		}
		return ActionNone

	case tcell.KeyLeft:
		if state.searchCursorPos > 0 {
			state.searchCursorPos--
		}
		return ActionNone

	case tcell.KeyRight:
		if state.searchCursorPos < len(state.searchString) {
			state.searchCursorPos++
		}
		return ActionNone

	case tcell.KeyHome:
		state.searchCursorPos = 0
		return ActionNone

	case tcell.KeyEnd:
		state.searchCursorPos = len(state.searchString)
		return ActionNone

	case tcell.KeyEnter:
		// Enter in search just moves focus to correction
		state.focusedField = FocusCorrection
		return ActionNone

	case tcell.KeyRune:
		r := ev.Rune()
		// Backtick toggles context regardless of focus
		if r == '`' {
			state.showContext = !state.showContext
			return ActionNone
		}
		state.searchString = state.searchString[:state.searchCursorPos] + string(r) + state.searchString[state.searchCursorPos:]
		state.searchCursorPos++
		state.updateSearchFilter()
		return ActionNone
	}

	return ActionNone
}

// handleCorrectionInput handles keyboard input for the correction field.
func handleCorrectionInput(ev *tcell.EventKey, state *AppState) Action {
	entry := state.getSelectedEntry()
	if entry == nil {
		return ActionNone
	}
	entryIdx := state.getSelectedEntryIndex()

	switch ev.Key() {
	case tcell.KeyEnter:
		// Save entry
		savedFile, err := saveEntry(*entry, state.correction, state.outputDir)
		if err == nil {
			// If previously saved with different correction, delete old file
			if oldFile, ok := state.savedFiles[entryIdx]; ok && oldFile != savedFile {
				os.Remove(oldFile)
			}
			state.disposition[entryIdx] = DispositionSaved
			state.savedFiles[entryIdx] = savedFile
		}
		// Move to next entry
		if state.selectedIndex < len(state.filteredIdx)-1 {
			state.selectedIndex++
			state.initFromEntry(state.getSelectedEntryIndex())
		}
		return ActionNone

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if state.cursorPos > 0 {
			state.correction = state.correction[:state.cursorPos-1] + state.correction[state.cursorPos:]
			state.cursorPos--
		}
		return ActionNone

	case tcell.KeyDelete:
		if state.cursorPos < len(state.correction) {
			state.correction = state.correction[:state.cursorPos] + state.correction[state.cursorPos+1:]
		}
		return ActionNone

	case tcell.KeyLeft:
		if state.cursorPos > 0 {
			state.cursorPos--
		}
		return ActionNone

	case tcell.KeyRight:
		if state.cursorPos < len(state.correction) {
			state.cursorPos++
		}
		return ActionNone

	case tcell.KeyHome:
		state.cursorPos = 0
		return ActionNone

	case tcell.KeyEnd:
		state.cursorPos = len(state.correction)
		return ActionNone

	case tcell.KeyCtrlK:
		// Skip entry
		// If previously saved, delete the file
		if oldFile, ok := state.savedFiles[entryIdx]; ok {
			os.Remove(oldFile)
			delete(state.savedFiles, entryIdx)
		}
		state.disposition[entryIdx] = DispositionSkipped
		// Move to next entry
		if state.selectedIndex < len(state.filteredIdx)-1 {
			state.selectedIndex++
			state.initFromEntry(state.getSelectedEntryIndex())
		}
		return ActionNone

	case tcell.KeyRune:
		r := ev.Rune()
		// Backtick toggles context regardless of focus
		if r == '`' {
			state.showContext = !state.showContext
			return ActionNone
		}
		// Auto-uppercase
		r = unicode.ToUpper(r)
		state.correction = state.correction[:state.cursorPos] + string(r) + state.correction[state.cursorPos:]
		state.cursorPos++
		return ActionNone
	}

	return ActionNone
}

// saveEntry saves an entry to the output directory as a test case.
// Returns the path of the saved file.
func saveEntry(entry LogEntry, correction, outputDir string) (string, error) {
	// Parse correction if provided
	if correction != "" {
		callsign, command, valid := parseCorrection(correction, entry)
		if !valid {
			return "", fmt.Errorf("invalid callsign in correction: %s", callsign)
		}
		entry.Callsign = callsign
		entry.Command = command
	}

	// Create output directory if needed
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", err
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
		return "", err
	}

	return path, os.WriteFile(path, data, 0644)
}

// extractExpectApproachID extracts the approach ID from a command string containing an "E" command.
// Returns empty string if no expect approach command is found.
func extractExpectApproachID(command string) string {
	parts := strings.Fields(command)
	for _, part := range parts {
		if strings.HasPrefix(part, "E") && len(part) > 1 {
			approachID := part[1:]
			// Strip LAHSO suffix if present (e.g., "EI22L/LAHSO26" -> "I22L")
			if idx := strings.Index(approachID, "/LAHSO"); idx != -1 {
				approachID = approachID[:idx]
			}
			return approachID
		}
	}
	return ""
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
