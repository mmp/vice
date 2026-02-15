// stars/fuzz.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// for race checking:
// % go run -race ./cmd/vice -runserver
// % go run ./cmd/vice -starsrandoms -server localhost
//
// alternatively, just:
// % go run ./cmd/vice -starsrandoms
// or:
// % while go run ./cmd/vice -starsrandoms; do :; done

package stars

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// Fuzz testing default values.
const (
	DefaultFuzzMutationRate     = 0.1
	DefaultFuzzTotalFrames      = 3600 // 1 hour of simulated time at 1 sim step / second
	DefaultFuzzCommandsPerFrame = 10
	DefaultFuzzSimRate          = 20   // Max sim speed
	DefaultFuzzLogInterval      = 6000 // Commands between periodic logs
)

func fuzzLog(format string, args ...any) {
	// fmt.Printf(format, args...)
}

// SelectRandomScenario picks a random scenario from the server's catalog
// and returns a NewSimRequest ready for use with ConnectionManager.CreateNewSim.
func SelectRandomScenario(srv *client.Server) (server.NewSimRequest, error) {
	catalogs := srv.GetScenarioCatalogs()

	type scenarioChoice struct {
		tracon       string
		groupName    string
		scenarioName string
		spec         *server.ScenarioSpec
	}

	var choices []scenarioChoice
	for tracon, facilityCatalogs := range catalogs {
		for groupName, catalog := range facilityCatalogs {
			for scenarioName, spec := range catalog.Scenarios {
				choices = append(choices, scenarioChoice{
					tracon:       tracon,
					groupName:    groupName,
					scenarioName: scenarioName,
					spec:         spec,
				})
			}
		}
	}

	if len(choices) == 0 {
		return server.NewSimRequest{}, errors.New("no scenarios available")
	}

	choice := rand.SampleSlice(rand.Make(), choices)
	fuzzLog("Starting STARS fuzz testing: %s/%s\n", choice.tracon, choice.scenarioName)

	return server.NewSimRequest{
		Facility:     choice.tracon,
		GroupName:    choice.groupName,
		ScenarioName: choice.scenarioName,
		ScenarioSpec: choice.spec,
	}, nil
}

// FuzzConfig controls fuzz test behavior.
type FuzzConfig struct {
	MutationRate     float32 // Probability of mutating valid input (default 0.1)
	Seed             uint64  // Random seed (0 = time-based)
	TotalFrames      int     // Total frames to run (0 = DefaultFuzzTotalFrames)
	CommandsPerFrame int     // Commands to execute per frame (0 = DefaultFuzzCommandsPerFrame)
}

// FuzzController orchestrates fuzz testing of STARS commands.
type FuzzController struct {
	sp            *STARSPane
	specs         []CommandSpec
	targetGenSpec *CommandSpec // The [ALL_TEXT] target-gen spec for pilot commands
	r             *rand.Rand
	lg            *log.Logger
	seed          uint64

	// Config
	mutationRate     float32
	totalFrames      int
	commandsPerFrame int

	// State
	frameCount  int
	initialized bool

	// Statistics
	commandsTried   int
	commandsHandled int
	commandsError   int
	byMode          map[CommandMode]*ModeStats
}

// ModeStats tracks statistics per command mode.
type ModeStats struct {
	Tried   int
	Handled int
	Errors  int
}

// NewFuzzController creates a new FuzzController for testing STARS commands.
func NewFuzzController(sp *STARSPane, cfg FuzzConfig, lg *log.Logger) *FuzzController {
	seed := cfg.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}

	mutationRate := cfg.MutationRate
	if mutationRate <= 0 {
		mutationRate = DefaultFuzzMutationRate
	}

	totalFrames := cfg.TotalFrames
	if totalFrames <= 0 {
		totalFrames = DefaultFuzzTotalFrames
	}

	commandsPerFrame := cfg.CommandsPerFrame
	if commandsPerFrame <= 0 {
		commandsPerFrame = DefaultFuzzCommandsPerFrame
	}

	allSpecs := BuildCommandSpecs()

	// Filter out dangerous/unwanted commands:
	// - "P" = pause sim
	// - "/..." = chat messages (we want aircraft commands, not chat)
	var specs []CommandSpec
	for _, spec := range allSpecs {
		if spec.OrigSpec == "P" {
			continue
		}
		if len(spec.OrigSpec) > 0 && spec.OrigSpec[0] == '/' {
			continue
		}
		specs = append(specs, spec)
	}

	// Find the target-gen [ALL_TEXT] spec for pilot commands
	var targetGenSpec *CommandSpec
	for i := range specs {
		if specs[i].Mode == CommandModeTargetGen && specs[i].OrigSpec == "[ALL_TEXT]" {
			targetGenSpec = &specs[i]
			break
		}
	}

	r := rand.Make()
	r.Seed(seed)
	fc := &FuzzController{
		sp:               sp,
		specs:            specs,
		targetGenSpec:    targetGenSpec,
		r:                r,
		lg:               lg,
		seed:             seed,
		mutationRate:     mutationRate,
		totalFrames:      totalFrames,
		commandsPerFrame: commandsPerFrame,
		byMode:           make(map[CommandMode]*ModeStats),
	}

	fuzzLog("[FUZZ] Initialized with seed: %d, %d command specs\n", seed, len(fc.specs))
	if targetGenSpec != nil {
		fuzzLog("[FUZZ] Found target-gen spec for pilot commands\n")
	}
	return fc
}

// Seed returns the random seed used by this controller.
func (fc *FuzzController) Seed() uint64 {
	return fc.seed
}

// ExecuteRandomCommand generates and executes a random command.
func (fc *FuzzController) ExecuteRandomCommand(ctx *panes.Context) {
	fc.commandsTried++

	// Build generator context with current state
	genCtx := &GeneratorContext{
		SP: fc.sp,
	}

	// Select a target track for aircraft commands
	var targetTrack *sim.Track
	if len(fc.sp.visibleTracks) > 0 {
		trk := fc.sp.visibleTracks[fc.r.Intn(len(fc.sp.visibleTracks))]
		genCtx.TargetTrack = &trk
		targetTrack = &trk
	}

	// Filter to tracks the user actually controls for target-gen commands.
	// TracksFromACIDSuffix requires the user to control the ControllerFrequency position,
	// so we must filter the same way to ensure commands will actually execute.
	var controlledTracks []sim.Track
	var seenControllers = make(map[string]bool)
	for _, trk := range fc.sp.visibleTracks {
		if trk.ControllerFrequency != "" {
			seenControllers[string(trk.ControllerFrequency)] = true
			// Only include tracks where user controls the position
			if ctx.UserControlsPosition(trk.ControllerFrequency) || ctx.TCWIsPrivileged(ctx.UserTCW) {
				controlledTracks = append(controlledTracks, trk)
			}
		}
	}
	if fc.commandsTried%DefaultFuzzLogInterval == 0 {
		fuzzLog("[FUZZ] UserTCW=%q, privileged=%v, seen controllers: %v, controlled tracks: %d, visible: %d\n",
			ctx.UserTCW, ctx.TCWIsPrivileged(ctx.UserTCW), seenControllers, len(controlledTracks), len(fc.sp.visibleTracks))
	}

	// 10% of the time, force a target-gen pilot command if we have controlled tracks and a spec
	forceTargetGen := fc.r.Float32() < 0.1 && fc.targetGenSpec != nil && len(controlledTracks) > 0

	var spec CommandSpec
	if forceTargetGen {
		spec = *fc.targetGenSpec
		// For target-gen, select from controlled tracks
		trk := controlledTracks[fc.r.Intn(len(controlledTracks))]
		genCtx.TargetTrack = &trk
		targetTrack = &trk
	} else {
		// Select a random command spec
		spec = fc.specs[fc.r.Intn(len(fc.specs))]
	}
	genCtx.CommandMode = spec.Mode

	// Generate input
	result := spec.Generate(fc.r, genCtx)
	if forceTargetGen && targetTrack != nil && targetTrack.IsAssociated() {
		result.Text = string(targetTrack.FlightPlan.ACID) + " " + result.Text
	}

	// Optionally mutate (but not forced target-gen commands - we want those to succeed)
	if !forceTargetGen && fc.r.Float32() < fc.mutationRate {
		result.Text = fc.mutate(result.Text)
	}

	// Set command mode
	fc.sp.commandMode = spec.Mode

	// Log the command being run
	fuzzLog("[FUZZ] mode=%v cmd=%q click=%v\n", spec.Mode, result.Text, result.NeedsClick)

	// Build transforms (needed for position parsing)
	ps := fc.sp.currentPrefs()
	ctr := ps.DefaultCenter
	if ps.UseUserCenter {
		ctr = ps.UserCenter
	}
	transforms := radar.GetScopeTransformations(ctx.PaneExtent, ctx.MagneticVariation, ctx.NmPerLongitude,
		ctr, float32(ps.Range), 0)

	// Execute command
	_, err, handled := fc.sp.tryExecuteUserCommand(
		ctx, result.Text, result.Track, result.NeedsClick,
		[2]float32{float32(fc.r.Intn(int(ctx.PaneExtent.Width()))), float32(fc.r.Intn(int(ctx.PaneExtent.Height())))}, // random mouse pos
		transforms, nil, nil,
	)

	// Update stats
	modeStats := fc.byMode[spec.Mode]
	if modeStats == nil {
		modeStats = &ModeStats{}
		fc.byMode[spec.Mode] = modeStats
	}
	modeStats.Tried++

	if handled {
		fc.commandsHandled++
		modeStats.Handled++
	}
	if err != nil {
		fc.commandsError++
		modeStats.Errors++
	}

	// Clear transient handlers to avoid accumulation
	fc.sp.transientCommandHandlers = nil
}

// mutate randomly modifies the input string.
func (fc *FuzzController) mutate(text string) string {
	if len(text) == 0 {
		return text
	}

	switch fc.r.Intn(5) {
	case 0: // Delete random character
		i := fc.r.Intn(len(text))
		return text[:i] + text[i+1:]
	case 1: // Insert random character
		i := fc.r.Intn(len(text) + 1)
		ch := fc.randomChar()
		return text[:i] + string(ch) + text[i:]
	case 2: // Replace random character
		i := fc.r.Intn(len(text))
		return text[:i] + string(fc.randomChar()) + text[i+1:]
	case 3: // Truncate
		return text[:fc.r.Intn(len(text))]
	case 4: // Append garbage
		var sb string
		for range 1 + fc.r.Intn(5) {
			sb += string(fc.randomChar())
		}
		return text + sb
	}
	return text
}

// randomChar returns a random alphanumeric character.
func (fc *FuzzController) randomChar() byte {
	if fc.r.Float32() < 0.7 {
		return 'A' + byte(fc.r.Intn(26))
	}
	return '0' + byte(fc.r.Intn(10))
}

// PrintStatistics outputs the fuzz testing statistics.
func (fc *FuzzController) PrintStatistics() {
	fmt.Printf("\n=== STARS Fuzz Test Results ===\n")
	fmt.Printf("Seed:              %d\n", fc.seed)
	fmt.Printf("Commands tried:    %d\n", fc.commandsTried)
	if fc.commandsTried > 0 {
		fmt.Printf("Commands handled:  %d (%.1f%%)\n",
			fc.commandsHandled, 100*float64(fc.commandsHandled)/float64(fc.commandsTried))
		fmt.Printf("Commands error:    %d (%.1f%%)\n",
			fc.commandsError, 100*float64(fc.commandsError)/float64(fc.commandsTried))
	}

	fmt.Printf("\nPer-mode breakdown:\n")
	for mode, stats := range util.SortedMap(fc.byMode) {
		if stats.Tried > 0 {
			fmt.Printf("  %v: tried=%d handled=%d (%.1f%%) errors=%d\n",
				CommandMode(mode).PreviewString(fc.sp), stats.Tried, stats.Handled,
				100*float64(stats.Handled)/float64(stats.Tried), stats.Errors)
		}
	}
}

// ExecuteFrame runs one frame of fuzz testing. It handles initialization
// on the first frame and executes the configured number of commands.
// Returns true if testing should continue.
func (fc *FuzzController) ExecuteFrame(ctx *panes.Context, c *client.ControlClient) bool {
	if !fc.initialized {
		c.SetSimRate(DefaultFuzzSimRate)
		fuzzLog("[FUZZ] Set sim rate to %dx\n", DefaultFuzzSimRate)
		fc.initialized = true
	}

	for range fc.commandsPerFrame {
		fc.ExecuteRandomCommand(ctx)
	}
	fc.frameCount++

	return fc.frameCount < fc.totalFrames
}

// ShouldContinue returns true if fuzz testing should continue.
func (fc *FuzzController) ShouldContinue() bool {
	return fc.frameCount < fc.totalFrames
}

// TotalFrames returns the configured total number of frames.
func (fc *FuzzController) TotalFrames() int {
	return fc.totalFrames
}

// FrameCount returns the number of frames executed so far.
func (fc *FuzzController) FrameCount() int {
	return fc.frameCount
}

///////////////////////////////////////////////////////////////////////////

// matchGenerator generates random input for a matcher.
type matchGenerator interface {
	// Generate returns a text fragment and whether a click is needed.
	Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult
}

// GeneratorResult holds the result of generating input for a matcher.
type GeneratorResult struct {
	Text       string     // Text fragment to include
	NeedsClick bool       // Whether a track/position click is needed
	Track      *sim.Track // If NeedsClick, optionally the track to click on
}

// GeneratorContext provides access to simulation state for generators.
type GeneratorContext struct {
	SP          *STARSPane  // Access to visible tracks, prefs, etc.
	TargetTrack *sim.Track  // Currently selected track for aircraft commands
	CommandMode CommandMode // Current command mode (affects ALL_TEXT generation)
}

// CommandSpec holds a parsed command specification ready for generation.
type CommandSpec struct {
	Mode       CommandMode
	Generators []matchGenerator
	OrigSpec   string // For debugging
}

// BuildCommandSpecs builds CommandSpecs from the registered userCommands.
func BuildCommandSpecs() []CommandSpec {
	var specs []CommandSpec
	for mode, cmds := range userCommands {
		for _, cmd := range cmds {
			spec := CommandSpec{
				Mode:     mode,
				OrigSpec: cmd.cmd,
			}
			for _, m := range cmd.matchers {
				spec.Generators = append(spec.Generators, m.Generator())
			}
			specs = append(specs, spec)
		}
	}
	return specs
}

// Generate generates a complete input string for this command spec.
func (cs *CommandSpec) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	var parts []string
	var needsClick bool
	var track *sim.Track

	for _, g := range cs.Generators {
		result := g.Generate(r, ctx)
		if result.Text != "" {
			parts = append(parts, result.Text)
		}
		if result.NeedsClick {
			needsClick = true
			track = result.Track
		}
	}

	return GeneratorResult{
		Text:       strings.Join(parts, ""),
		NeedsClick: needsClick,
		Track:      track,
	}
}

///////////////////////////////////////////////////////////////////////////
// Generator implementations

// literalMatchGenerator generates literal text.
type literalMatchGenerator struct {
	Text string
}

func (g *literalMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	return GeneratorResult{Text: g.Text}
}

// altMatchGenerator tries multiple alternative generators and picks one randomly.
type altMatchGenerator struct {
	Alternatives []matchGenerator
}

func (g *altMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	if len(g.Alternatives) == 0 {
		return GeneratorResult{}
	}
	idx := r.Intn(len(g.Alternatives))
	return g.Alternatives[idx].Generate(r, ctx)
}

// greedyMatchGenerator generates 0 or more matches of the inner generator.
type greedyMatchGenerator struct {
	Inner matchGenerator
}

func (g *greedyMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Generate 0-3 matches
	count := r.Intn(4)
	if count == 0 {
		return GeneratorResult{}
	}

	var parts []string
	var needsClick bool
	var track *sim.Track

	for range count {
		result := g.Inner.Generate(r, ctx)
		if result.Text != "" {
			parts = append(parts, result.Text)
		}
		if result.NeedsClick {
			needsClick = true
			track = result.Track
		}
	}

	return GeneratorResult{
		Text:       strings.Join(parts, " "),
		NeedsClick: needsClick,
		Track:      track,
	}
}

// optionalMatchGenerator wraps a generator to make it optional (50% chance of generating).
type optionalMatchGenerator struct {
	Inner matchGenerator
}

func (g *optionalMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	if r.Float32() < 0.5 {
		return GeneratorResult{}
	}
	return g.Inner.Generate(r, ctx)
}

///////////////////////////////////////////////////////////////////////////
// Typed generators

// numMatchGenerator generates numbers.
type numMatchGenerator struct {
	MinDigits int
	MaxDigits int
}

func (g *numMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	digits := g.MinDigits
	if g.MaxDigits > g.MinDigits {
		digits += r.Intn(g.MaxDigits - g.MinDigits + 1)
	}
	if digits <= 0 {
		digits = 1
	}

	max := 1
	for range digits {
		max *= 10
	}
	n := r.Intn(max)
	return GeneratorResult{Text: fmt.Sprintf("%0*d", digits, n)}
}

// trackMatchGenerator generates track identifiers.
type trackMatchGenerator struct {
	Style    string // "TRK_ACID", "TRK_BCN", "TRK_INDEX", "TRK_INDEX_SUSPENDED"
	Optional bool
}

func (g *trackMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	if g.Optional && r.Float32() < 0.3 {
		return GeneratorResult{}
	}

	// Filter to associated tracks
	var candidates []sim.Track
	for _, trk := range ctx.SP.visibleTracks {
		if trk.IsAssociated() {
			candidates = append(candidates, trk)
		}
	}

	if len(candidates) == 0 {
		// Generate a random ACID if no tracks available
		return GeneratorResult{Text: randomACID(r)}
	}

	trk := candidates[r.Intn(len(candidates))]
	switch g.Style {
	case "TRK_BCN":
		return GeneratorResult{Text: trk.Squawk.String()}
	case "TRK_INDEX":
		return GeneratorResult{Text: fmt.Sprintf("%d", trk.FlightPlan.ListIndex)}
	case "TRK_INDEX_SUSPENDED":
		return GeneratorResult{Text: fmt.Sprintf("%d", trk.FlightPlan.CoastSuspendIndex)}
	default: // TRK_ACID
		return GeneratorResult{Text: string(trk.FlightPlan.ACID)}
	}
}

// slewMatchGenerator signals that a track click is needed.
type slewMatchGenerator struct{}

func (g *slewMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	var track *sim.Track
	if len(ctx.SP.visibleTracks) > 0 {
		// Pick a random track
		trk := ctx.SP.visibleTracks[r.Intn(len(ctx.SP.visibleTracks))]
		track = &trk
	}
	return GeneratorResult{NeedsClick: true, Track: track}
}

// ghostSlewMatchGenerator signals that a ghost track click is needed.
type ghostSlewMatchGenerator struct{}

func (g *ghostSlewMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// For ghost slews, we still need a click but may not have a ghost track
	return GeneratorResult{NeedsClick: true}
}

// posMatchGenerator signals that a position click is needed.
type posMatchGenerator struct{}

func (g *posMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	return GeneratorResult{NeedsClick: true}
}

// acidMatchGenerator generates aircraft identifiers.
type acidMatchGenerator struct {
	FromExisting bool // If true, pick from existing tracks
}

func (g *acidMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	if g.FromExisting && len(ctx.SP.visibleTracks) > 0 {
		trk := ctx.SP.visibleTracks[r.Intn(len(ctx.SP.visibleTracks))]
		if trk.IsAssociated() {
			return GeneratorResult{Text: string(trk.FlightPlan.ACID)}
		}
	}
	return GeneratorResult{Text: randomACID(r)}
}

// beaconMatchGenerator generates beacon codes.
type beaconMatchGenerator struct {
	BlockOnly bool // If true, generate 2-digit block only
}

func (g *beaconMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	if g.BlockOnly {
		// 2-digit octal block (00-77)
		return GeneratorResult{Text: fmt.Sprintf("%02o", r.Intn(64))}
	}
	// 4-digit octal beacon code
	return GeneratorResult{Text: fmt.Sprintf("%04o", r.Intn(4096))}
}

// tcpMatchGenerator generates controller position IDs.
type tcpMatchGenerator struct {
	Style string // "TCP1", "TCP2", "TCW", "TCP_TRI", "ARTCC"
}

func (g *tcpMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	switch g.Style {
	case "TCP1":
		// Single letter A-Z
		return GeneratorResult{Text: string('A' + rune(r.Intn(26)))}
	case "TCP2", "TCW":
		// Digit + letter (1A-9Z)
		return GeneratorResult{Text: fmt.Sprintf("%d%c", 1+r.Intn(9), 'A'+rune(r.Intn(26)))}
	case "TCP_TRI":
		// Triangle + digit + letter
		return GeneratorResult{Text: fmt.Sprintf("%s%d%c", STARSTriangleCharacter, 1+r.Intn(9), 'A'+rune(r.Intn(26)))}
	case "ARTCC":
		// Letter + 2 digits or just "C"
		if r.Float32() < 0.2 {
			return GeneratorResult{Text: "C"}
		}
		return GeneratorResult{Text: fmt.Sprintf("%c%02d", 'A'+rune(r.Intn(26)), r.Intn(100))}
	default:
		return GeneratorResult{Text: "1A"}
	}
}

// spcMatchGenerator generates sector position codes.
type spcMatchGenerator struct{}

func (g *spcMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// 2-character SPC (we don't validate, just generate something plausible)
	return GeneratorResult{Text: fmt.Sprintf("%c%c", 'A'+rune(r.Intn(26)), 'A'+rune(r.Intn(26)))}
}

// airportMatchGenerator generates airport IDs.
type airportMatchGenerator struct{}

func (g *airportMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	airports := collectAirports(ctx.SP.visibleTracks)
	if len(airports) > 0 {
		return GeneratorResult{Text: airports[r.Intn(len(airports))]}
	}
	// Generate random 3-letter code
	return GeneratorResult{Text: fmt.Sprintf("%c%c%c", 'A'+rune(r.Intn(26)), 'A'+rune(r.Intn(26)), 'A'+rune(r.Intn(26)))}
}

// fixMatchGenerator generates navigation fix names.
type fixMatchGenerator struct{}

func (g *fixMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	fixes := collectFixes(ctx.SP.visibleTracks)
	if len(fixes) > 0 {
		return GeneratorResult{Text: fixes[r.Intn(len(fixes))]}
	}
	// No real fixes available - generate placeholder text
	return GeneratorResult{Text: randomField(r, 3+r.Intn(3))}
}

// timeMatchGenerator generates 4-digit times.
type timeMatchGenerator struct{}

func (g *timeMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	hour := r.Intn(24)
	minute := r.Intn(60)
	return GeneratorResult{Text: fmt.Sprintf("%02d%02d", hour, minute)}
}

// floatMatchGenerator generates floating point numbers.
type floatMatchGenerator struct {
	Max float32
}

func (g *floatMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	max := g.Max
	if max <= 0 {
		max = 100
	}
	val := r.Float32() * max
	return GeneratorResult{Text: fmt.Sprintf("%.1f", val)}
}

// fieldMatchGenerator generates a generic text field.
type fieldMatchGenerator struct {
	MinLen int
	MaxLen int
}

func (g *fieldMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	minLen := g.MinLen
	if minLen <= 0 {
		minLen = 1
	}
	maxLen := g.MaxLen
	if maxLen <= 0 {
		maxLen = 8
	}
	length := minLen + r.Intn(maxLen-minLen+1)
	return GeneratorResult{Text: randomField(r, length)}
}

// allTextMatchGenerator generates text for [ALL_TEXT] matchers.
// For CommandModeTargetGen, generates aircraft control commands.
type allTextMatchGenerator struct {
	ForTargetGen bool
}

func (g *allTextMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Check if we should generate aircraft commands
	if g.ForTargetGen || ctx.CommandMode == CommandModeTargetGen || ctx.CommandMode == CommandModeTargetGenLock {
		return GeneratorResult{Text: generateAircraftCommand(r, ctx)}
	}
	// Otherwise generate random text
	return GeneratorResult{Text: randomField(r, 1+r.Intn(10))}
}

// fpSpecMatchGenerator generates flight plan specifier components.
type fpSpecMatchGenerator struct {
	Style string
}

func (g *fpSpecMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	switch g.Style {
	case "FP_ACID":
		return GeneratorResult{Text: randomACID(r)}
	case "FP_BEACON":
		opts := []string{"+", "/", "/1", "/2", "/3", "/4", fmt.Sprintf("%04o", r.Intn(4096))}
		return GeneratorResult{Text: opts[r.Intn(len(opts))]}
	case "FP_SP1":
		return GeneratorResult{Text: randomScratchpad(r)}
	case "FP_TRI_SP1":
		return GeneratorResult{Text: STARSTriangleCharacter + randomScratchpad(r)}
	case "FP_PLUS_SP2":
		return GeneratorResult{Text: "+" + randomScratchpad(r)}
	case "FP_ALT_A", "FP_ALT_P", "FP_ALT_R":
		alt := 10 + r.Intn(400) // 1000 to 40000 feet in hundreds
		return GeneratorResult{Text: fmt.Sprintf("%03d", alt)}
	case "FP_TRI_ALT_A":
		alt := 10 + r.Intn(400)
		return GeneratorResult{Text: STARSTriangleCharacter + fmt.Sprintf("%03d", alt)}
	case "FP_PLUS_ALT_A":
		alt := 10 + r.Intn(400)
		return GeneratorResult{Text: "+" + fmt.Sprintf("%03d", alt)}
	case "FP_PLUS2_ALT_R":
		alt := 10 + r.Intn(400)
		return GeneratorResult{Text: "++" + fmt.Sprintf("%03d", alt)}
	case "FP_TCP":
		return GeneratorResult{Text: fmt.Sprintf("%d%c", 1+r.Intn(9), 'A'+rune(r.Intn(26)))}
	case "FP_NUM_ACTYPE":
		var comp []string
		if r.Bool() {
			comp = append(comp, strconv.Itoa(1+r.Intn(7)))
		}
		types := []string{"B738", "A320", "C172", "E170", "B77W", "A359"}
		comp = append(comp, types[r.Intn(len(types))])
		if r.Bool() {
			comp = append(comp, string('A'+rune(r.Intn(26))))
		}
		return GeneratorResult{Text: strings.Join(comp, "/")}
	case "FP_NUM_ACTYPE4":
		var comp []string
		if r.Bool() {
			comp = append(comp, strconv.Itoa(1+r.Intn(7)))
		}
		types := []string{"F16*", "A320", "C172", "B2**", "B77W", "A359"}
		comp = append(comp, types[r.Intn(len(types))])
		if r.Bool() {
			comp = append(comp, string('A'+rune(r.Intn(26))))
		}
		return GeneratorResult{Text: strings.Join(comp, "/")}
	case "FP_ACTYPE":
		var comp []string
		types := []string{"B738", "A320", "C172", "E170", "B77W", "A359"}
		comp = append(comp, types[r.Intn(len(types))])
		if r.Bool() {
			comp = append(comp, string('A'+rune(r.Intn(26))))
		}
		return GeneratorResult{Text: strings.Join(comp, "/")}
	case "FP_COORD_TIME":
		hour := r.Intn(24)
		minute := r.Intn(60)
		return GeneratorResult{Text: fmt.Sprintf("%02d%02dE", hour, minute)}
	case "FP_FIX_PAIR":
		fix1 := getRandomFix(r, ctx)
		fix2 := getRandomFix(r, ctx)
		if fix1 == "" || fix2 == "" {
			return GeneratorResult{Text: randomField(r, 5) + "*" + randomField(r, 5)}
		}
		return GeneratorResult{Text: fix1 + "*" + fix2}
	case "FP_EXIT_FIX":
		fix := getRandomFix(r, ctx)
		if fix == "" {
			return GeneratorResult{Text: "*" + randomField(r, 3)}
		}
		if len(fix) > 3 {
			fix = fix[:3]
		}
		return GeneratorResult{Text: "*" + fix}
	case "FP_FLT_TYPE":
		types := []string{"A", "P", "E"}
		return GeneratorResult{Text: types[r.Intn(len(types))]}
	case "FP_RULES":
		rules := []string{".V", ".P", ".E", "."}
		return GeneratorResult{Text: rules[r.Intn(len(rules))]}
	case "FP_RNAV":
		return GeneratorResult{Text: "R"}
	case "FP_VFR_FIXES":
		fix1 := getRandomFix(r, ctx)
		fix2 := getRandomFix(r, ctx)
		if fix1 == "" || fix2 == "" {
			return GeneratorResult{Text: randomField(r, 3) + "*" + randomField(r, 3)}
		}
		if len(fix1) > 3 {
			fix1 = fix1[:3]
		}
		if len(fix2) > 3 {
			fix2 = fix2[:3]
		}
		return GeneratorResult{Text: fix1 + "*" + fix2}
	default:
		return GeneratorResult{Text: randomField(r, 3)}
	}
}

// raTextMatchGenerator generates restriction area text.
type raTextMatchGenerator struct {
	WithLocation bool
	ClosedShape  bool
}

func (g *raTextMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	text := randomField(r, 3+r.Intn(10))

	// Optionally add modifiers. △ (blink) is always valid; + (shade) and *N (color)
	// are only valid for closed shapes (circles/polygons).
	if r.Float32() < 0.2 {
		text += " " + STARSTriangleCharacter // blink
	}
	if g.ClosedShape && r.Float32() < 0.2 {
		text += " +" // shade
	}
	if g.ClosedShape && r.Float32() < 0.2 {
		text += " *" + string('1'+byte(r.Intn(8))) // color 1-8
	}

	if g.WithLocation {
		fixes := collectFixes(ctx.SP.visibleTracks)
		if len(fixes) > 0 {
			text += " " + fixes[r.Intn(len(fixes))]
		}
	}
	return GeneratorResult{Text: text}
}

// raLocationMatchGenerator generates restriction area locations.
type raLocationMatchGenerator struct{}

func (g *raLocationMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	fixes := collectFixes(ctx.SP.visibleTracks)
	if len(fixes) > 0 && r.Float32() < 0.7 {
		return GeneratorResult{Text: fixes[r.Intn(len(fixes))]}
	}

	// Generate lat/long within 1 degree of the radar scope center
	ps := ctx.SP.currentPrefs()
	ctr := ps.DefaultCenter
	if ps.UseUserCenter {
		ctr = ps.UserCenter
	}

	// Random offset within ±1 degree
	latOffset := (r.Float32()*2 - 1) // -1 to +1
	longOffset := (r.Float32()*2 - 1)

	lat := ctr.Latitude() + latOffset
	long := ctr.Longitude() + longOffset

	// Convert to degrees/minutes/seconds
	latDeg := int(lat)
	latMin := int((lat - float32(latDeg)) * 60)
	latSec := int(((lat-float32(latDeg))*60 - float32(latMin)) * 60)
	if latMin < 0 {
		latMin = -latMin
	}
	if latSec < 0 {
		latSec = -latSec
	}

	longDeg := int(-long) // West is positive in our format
	longMin := int((-long - float32(longDeg)) * 60)
	longSec := int(((-long-float32(longDeg))*60 - float32(longMin)) * 60)
	if longMin < 0 {
		longMin = -longMin
	}
	if longSec < 0 {
		longSec = -longSec
	}

	return GeneratorResult{Text: fmt.Sprintf("%02d%02d%02dN/%03d%02d%02dW", latDeg, latMin, latSec, longDeg, longMin, longSec)}
}

// raIndexMatchGenerator generates restriction area indices.
type raIndexMatchGenerator struct {
	UserOnly bool
}

func (g *raIndexMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Generate index 1-99
	return GeneratorResult{Text: fmt.Sprintf("%d", 1+r.Intn(99))}
}

// qlRegionMatchGenerator generates quicklook region IDs.
type qlRegionMatchGenerator struct{}

func (g *qlRegionMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Generate 1-3 character region ID
	length := 1 + r.Intn(3)
	return GeneratorResult{Text: randomField(r, length)}
}

// fdamRegionMatchGenerator generates FDAM region IDs.
type fdamRegionMatchGenerator struct{}

func (g *fdamRegionMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Generate 1-5 character region ID
	length := 1 + r.Intn(5)
	return GeneratorResult{Text: randomField(r, length)}
}

// qlPositionsMatchGenerator generates quicklook position lists.
type qlPositionsMatchGenerator struct{}

func (g *qlPositionsMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Generate 1-3 position specs
	count := 1 + r.Intn(3)
	var parts []string
	for range count {
		if r.Float32() < 0.5 {
			// Single letter
			parts = append(parts, string('A'+rune(r.Intn(26))))
		} else {
			// Digit + letter
			parts = append(parts, fmt.Sprintf("%d%c", 1+r.Intn(9), 'A'+rune(r.Intn(26))))
		}
	}
	return GeneratorResult{Text: strings.Join(parts, " ")}
}

// altFilter6MatchGenerator generates 6-digit altitude filters.
type altFilter6MatchGenerator struct{}

func (g *altFilter6MatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	low := r.Intn(400)  // 0-399 (0-39900 feet)
	high := r.Intn(400) // 0-399 (0-39900 feet)
	if low > high {
		low, high = high, low
	}
	return GeneratorResult{Text: fmt.Sprintf("%03d%03d", low, high)}
}

// crdaRegionMatchGenerator generates CRDA region IDs.
type crdaRegionMatchGenerator struct{}

func (g *crdaRegionMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Generate runway-style names for now; region names can be arbitrary.
	num := 1 + r.Intn(36)
	suffixes := []string{"", "L", "R", "C"}
	return GeneratorResult{Text: fmt.Sprintf("%02d%s", num, suffixes[r.Intn(len(suffixes))])}
}

// unassocFPMatchGenerator generates identifiers for unassociated flight plans.
type unassocFPMatchGenerator struct{}

func (g *unassocFPMatchGenerator) Generate(r *rand.Rand, ctx *GeneratorContext) GeneratorResult {
	// Generate a random ACID - we don't track unassociated flight plans in GeneratorContext
	return GeneratorResult{Text: randomACID(r)}
}

///////////////////////////////////////////////////////////////////////////
// Helper functions

// collectAirports extracts unique airports from visible tracks.
func collectAirports(tracks []sim.Track) []string {
	airports := make(map[string]bool)
	for _, trk := range tracks {
		if trk.IsAssociated() {
			if ap := trk.FlightPlan.ArrivalAirport; ap != "" {
				airports[ap] = true
			}
		}
	}
	result := make([]string, 0, len(airports))
	for ap := range airports {
		result = append(result, ap)
	}
	return result
}

// collectFixes extracts fix names from visible track routes.
func collectFixes(tracks []sim.Track) []string {
	fixSet := make(map[string]bool)
	for _, trk := range tracks {
		if trk.IsAssociated() {
			for _, fix := range extractFixesFromRoute(trk.FlightPlan.Route) {
				fixSet[fix] = true
			}
		}
	}
	result := make([]string, 0, len(fixSet))
	for fix := range fixSet {
		result = append(result, fix)
	}
	return result
}

func randomACID(r *rand.Rand) string {
	// Generate 3-7 character ACID starting with letter
	length := 3 + r.Intn(5)
	var sb strings.Builder
	sb.WriteByte('A' + byte(r.Intn(26)))
	for i := 1; i < length; i++ {
		if r.Float32() < 0.5 {
			sb.WriteByte('A' + byte(r.Intn(26)))
		} else {
			sb.WriteByte('0' + byte(r.Intn(10)))
		}
	}
	return sb.String()
}

func randomScratchpad(r *rand.Rand) string {
	// Generate 2-3 character scratchpad
	length := 2 + r.Intn(2)
	var sb strings.Builder
	for range length {
		if r.Float32() < 0.7 {
			sb.WriteByte('A' + byte(r.Intn(26)))
		} else {
			sb.WriteByte('0' + byte(r.Intn(10)))
		}
	}
	return sb.String()
}

func randomField(r *rand.Rand, length int) string {
	var sb strings.Builder
	for range length {
		if r.Float32() < 0.7 {
			sb.WriteByte('A' + byte(r.Intn(26)))
		} else {
			sb.WriteByte('0' + byte(r.Intn(10)))
		}
	}
	return sb.String()
}

// getRandomFix returns a random fix from visible tracks, or empty string if none available.
func getRandomFix(r *rand.Rand, ctx *GeneratorContext) string {
	fixes := collectFixes(ctx.SP.visibleTracks)
	if len(fixes) == 0 {
		return ""
	}
	return fixes[r.Intn(len(fixes))]
}

///////////////////////////////////////////////////////////////////////////
// Aircraft command generation for CommandModeTargetGen

func generateAircraftCommand(r *rand.Rand, ctx *GeneratorContext) string {
	// Generate 1-3 commands
	count := 1 + r.Intn(3)
	var cmds []string
	for range count {
		cmds = append(cmds, generateOneAircraftCommand(r, ctx))
	}
	return strings.Join(cmds, " ")
}

func generateOneAircraftCommand(r *rand.Rand, ctx *GeneratorContext) string {
	type weightedCmd struct {
		weight int
		gen    func(*rand.Rand, *GeneratorContext) string
	}

	cmdTypes := []weightedCmd{
		// Altitude
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return "A" }}, // discretion
		{6, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("A%d", 20+r.Intn(400)) }},
		{6, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("C%d", 20+r.Intn(400)) }},
		{6, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("D%d", 20+r.Intn(400)) }},

		// Heading
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return "H" }}, // present heading
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("H%03d", 1+r.Intn(360)) }},
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("L%03d", 1+r.Intn(360)) }},
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("R%03d", 1+r.Intn(360)) }},
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("T%dL", 5+r.Intn(36)*5) }},
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("T%dR", 5+r.Intn(36)*5) }},

		// Speed
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return "S" }}, // cancel
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return "SMIN" }},
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return "SMAX" }},
		{6, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("S%d", 150+r.Intn(200)) }},

		// Direct fix
		{10, func(r *rand.Rand, ctx *GeneratorContext) string {
			if ctx.TargetTrack != nil && ctx.TargetTrack.IsAssociated() {
				route := ctx.TargetTrack.FlightPlan.Route
				fixes := extractFixesFromRoute(route)
				if len(fixes) > 0 {
					return "D" + fixes[r.Intn(len(fixes))]
				}
			}
			fixes := collectFixes(ctx.SP.visibleTracks)
			if len(fixes) > 0 {
				return "D" + fixes[r.Intn(len(fixes))]
			}
			return fmt.Sprintf("H%03d", 1+r.Intn(360))
		}},

		// Expect approach
		{8, func(r *rand.Rand, ctx *GeneratorContext) string {
			appr := getValidApproach(r, ctx)
			if appr == "" {
				return fmt.Sprintf("A%d", 20+r.Intn(200))
			}
			return "E" + appr
		}},

		// Cleared approach
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { // straight-in
			appr := getValidApproach(r, ctx)
			if appr == "" {
				return fmt.Sprintf("C%d", 20+r.Intn(200))
			}
			return "CSI" + appr
		}},
		{6, func(r *rand.Rand, ctx *GeneratorContext) string { // normal
			appr := getValidApproach(r, ctx)
			if appr == "" {
				return fmt.Sprintf("C%d", 20+r.Intn(200))
			}
			return "C" + appr
		}},

		// Intercept localizer
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "I" }},

		// Climb/descend via
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return "CVS" }},
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return "DVS" }},

		// Expedite
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return "EC" }},
		{3, func(r *rand.Rand, ctx *GeneratorContext) string { return "ED" }},

		// Squawk commands
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "SQS" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "SQA" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "SQON" }},
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return fmt.Sprintf("SQ%04o", r.Intn(4096)) }},

		// Contact commands
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return "TO" }},
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return "FC" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string {
			return fmt.Sprintf("CT%d%c", 1+r.Intn(9), 'A'+rune(r.Intn(26)))
		}},

		// Ident
		{2, func(r *rand.Rand, ctx *GeneratorContext) string { return "ID" }},

		// Misc commands
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "RON" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "X" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "SS" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "SH" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "SA" }},
		{1, func(r *rand.Rand, ctx *GeneratorContext) string { return "GA" }},
	}

	ct, _ := rand.SampleWeighted(r, cmdTypes, func(w weightedCmd) int { return w.weight })
	return ct.gen(r, ctx)
}

// getValidApproach returns a real approach name from the target track's arrival airport.
// Returns empty string if no valid approach can be found.
func getValidApproach(r *rand.Rand, ctx *GeneratorContext) string {
	// Get arrival airport from target track
	if ctx.TargetTrack == nil || !ctx.TargetTrack.IsAssociated() {
		return ""
	}

	arrivalAirport := ctx.TargetTrack.ArrivalAirport
	if arrivalAirport == "" {
		return ""
	}

	// Look up airport in database
	ap, ok := av.DB.Airports[arrivalAirport]
	if !ok || len(ap.Approaches) == 0 {
		return ""
	}

	// Collect approach names
	var approaches []string
	for name := range ap.Approaches {
		approaches = append(approaches, name)
	}

	return approaches[r.Intn(len(approaches))]
}

func extractFixesFromRoute(route string) []string {
	// Simple extraction: split on common delimiters and filter
	parts := strings.FieldsFunc(route, func(c rune) bool {
		return c == '.' || c == '/' || c == ' '
	})
	var fixes []string
	for _, p := range parts {
		// Only keep 3-5 letter names that look like fixes
		if len(p) >= 3 && len(p) <= 5 {
			isAlpha := true
			for _, c := range p {
				if c < 'A' || c > 'Z' {
					isAlpha = false
					break
				}
			}
			if isAlpha {
				fixes = append(fixes, p)
			}
		}
	}
	return fixes
}
