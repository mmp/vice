// stars/parsecmd.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Command Processing System
//
// This system provides a declarative way to specify commands that can be
// invoked either via keyboard entry or by clicking on the scope (slewing).
// Commands are specified using a simple syntax that combines literal text
// and typed matchers in square brackets, with alternatives separated by
// "|"s, e.g.  "Y[TRK_ACID] [FP_SP1]|Y[FP_SP1][SLEW]"
//

// userCommand defines a single command with its specification and handler.
//
// # Handler Function Argument Binding
//
// The handlerFunc field uses reflection-based argument binding with a
// flexible signature.  This allows handler function implementations to be
// highly compact.
//
// Handler functions may declare parameters in two groups:
//
// 1. Initial Arguments (optional, any order):
//   - *STARSPane: The STARS pane instance
//   - *panes.Context: The rendering/input context
//   - *Preferences: Current STARS user preferences (via sp.currentPrefs())
//
// 2. Typed Arguments (must match command spec types, any order):
//   - Arguments corresponding to [TYPE] matchers in the command specifier
//   - Types are determined by typeHandlers in cmdtypes.go
//
// Best practices:
//   - Only include initial arguments that are actually used in the function
//   - If a function only needs *Preferences, pass it directly rather than taking
//     *STARSPane and calling currentPrefs(). (Saves a line of code)
//
// The following return value types are allowed:
//   - () - no return value
//   - (error) - error only
//   - (CommandStatus) - status with optional command output and temporary command bindings
//   - (CommandStatus, error) - status and possible error
type userCommand struct {
	cmd           string    // Command specifier (e.g., "Y[TRK_ACID] [FP_SP1]")
	handlerFunc   any       // Handler function with flexible signature (see documentation above)
	matchers      []matcher // Sequence of matchers for this command
	numInitial    int       // Cached count of initial args (STARSPane, Context, Preferences)
	consumesClick bool      // True if the command is initiated from a mouse click
}

func makeUserCommand(c string, f any) userCommand {
	var err error
	uc := userCommand{cmd: c, handlerFunc: f}
	uc.matchers, err = makeMatchers(uc.cmd)
	if err != nil {
		panic(fmt.Sprintf("error for command %q: %v", c, err))
	}

	uc.numInitial, err = validateFunctionSignature(uc)
	if err != nil {
		panic(fmt.Sprintf("error for command %q: %v", c, err))
	}

	if len(uc.matchers) > 0 {
		uc.consumesClick = uc.matchers[len(uc.matchers)-1].consumesClick()
	}

	return uc
}

// Command registry - maps command mode to list of commands
var userCommands = make(map[CommandMode][]userCommand)

// Track registered command strings to detect duplicates
var registeredCommands = make(map[CommandMode]map[string]bool)

// registerCommand is used to register all of the supported STARS commands at startup time via
// init() functions in the cmd*.go files.
func registerCommand(m CommandMode, c string, f any) {
	if registeredCommands[m] == nil {
		registeredCommands[m] = make(map[string]bool)
	}

	for cmd := range splitCommands(c) {
		if registeredCommands[m][cmd] {
			panic(fmt.Sprintf("Duplicate command registration in mode %v: %s (from %s)", m, cmd, c))
		}
		userCommands[m] = append(userCommands[m], makeUserCommand(cmd, f))
		registeredCommands[m][cmd] = true
	}
}

// makeCommandHandlers creates a slice of userCommands from alternating command specifier strings
// and handler functions. It's used when running one command causes another set of commands to be
// installed temporarily (e.g. to process further keyboard input or mouse clicks from the user.)
func makeCommandHandlers(args ...any) []userCommand {
	if len(args)%2 != 0 {
		panic("makeCommandHandlers requires alternating spec strings and handler functions")
	}

	seen := make(map[string]bool)
	var handlers []userCommand

	for i := 0; i < len(args); i += 2 {
		spec, ok := args[i].(string)
		if !ok {
			panic(fmt.Sprintf("makeCommandHandlers: expected string at position %d, got %T", i, args[i]))
		}
		handler := args[i+1]

		for alt := range splitCommands(spec) {
			if seen[alt] {
				panic(fmt.Sprintf("makeCommandHandlers: duplicate command spec: %s", alt))
			}
			seen[alt] = true
			handlers = append(handlers, makeUserCommand(alt, handler))
		}
	}

	return handlers
}

func splitCommands(c string) iter.Seq[string] {
	return func(yield func(string) bool) {
		// Split command alternatives at the top level only (not within brackets); this ensures that
		// e.g., "M[TRK_ACID]|M[SLEW]" becomes two commands: "M[TRK_ACID]" and "M[SLEW]" but
		// "[FP_SP1|FP_ACID]" remains as one matcher with type alternatives.
		start, depth := 0, 0
		for pos, ch := range c {
			switch ch {
			case '[':
				depth++
			case ']':
				depth--
				if depth < 0 {
					panic(fmt.Sprintf("Unbalanced ] in command spec %q", c))
				}
			case '|':
				if depth == 0 {
					if !yield(c[start:pos]) {
						return
					}
					start = pos + 1
				}
				// Otherwise ignore it - it's within brackets
			}
		}
		// Pass through the final command or an empty command string if that's what was given
		// initially.
		if start < len(c) || (start == 0 && len(c) == 0) {
			yield(c[start:])
		}
		if depth != 0 {
			panic(fmt.Sprintf("Unbalanced [ in command spec %q", c))
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Parsing user input and matching commands

// CommandInput holds the current state during command matching.
type CommandInput struct {
	text string

	// Track / position information for click-based commands
	clickedTrack  *sim.Track
	hasClick      bool
	mousePosition [2]float32
	transforms    radar.ScopeTransformations

	// Ghost track information for CRDA commands
	clickedGhost *av.GhostTrack   // Ghost track if one was clicked (closer than track)
	ghosts       []*av.GhostTrack // All ghost tracks for the clicked aircraft
}

// matchCandidate represents a userCommand and the WIP state from attempting to match its
// specification with user input.
type matchCandidate struct {
	cmd       *userCommand
	matchers  []matcher                 // remaining matchers to process
	args      []any                     // accumulated argument values
	fpSpecs   []sim.FlightPlanSpecifier // accumulated FP specifiers (will be merged)
	remaining string                    // unconsumed input text
	advanced  bool                      // true if at least one matcher has succeeded
}

// advance returns a new matchCandidate with the first matcher consumed and state updated.
func (c matchCandidate) advance(m matcher, r matchResult) matchCandidate {
	next := matchCandidate{
		cmd:       c.cmd,
		matchers:  c.matchers[1:], // peel off first matcher
		args:      c.args,
		fpSpecs:   c.fpSpecs,
		remaining: r.remaining,
		advanced:  true,
	}

	// Accumulate values (handle FlightPlanSpecifiers separately - they will be merged)
	if m.goType() == fpSpecType {
		for _, v := range r.values {
			next.fpSpecs = append(next.fpSpecs, v.(sim.FlightPlanSpecifier))
		}
	} else {
		next.args = append(next.args, r.values...)
	}

	return next
}

// tryExecuteUserCommand attempts to execute a command using the registered commands
// for the current command mode.
func (sp *STARSPane) tryExecuteUserCommand(ctx *panes.Context, cmd string, clickedTrack *sim.Track, hasClick bool,
	mousePosition [2]float32, transforms radar.ScopeTransformations, clickedGhost *av.GhostTrack,
	ghosts []*av.GhostTrack) (CommandStatus, error, bool) {
	// Get commands for current mode
	cmds, ok := userCommands[sp.commandMode]
	if !ok || len(cmds) == 0 {
		return CommandStatus{}, nil, false
	}

	// For multiFunc mode, prepend the prefix to the command text
	text := cmd
	if sp.commandMode == CommandModeMultiFunc && sp.multiFuncPrefix != "" {
		text = sp.multiFuncPrefix + cmd
	}

	input := &CommandInput{
		text:          text,
		clickedTrack:  clickedTrack,
		hasClick:      hasClick,
		mousePosition: mousePosition,
		transforms:    transforms,
		clickedGhost:  clickedGhost,
		ghosts:        ghosts,
	}

	return sp.dispatchCommand(ctx, cmds, input)
}

// dispatchCommand is the main command dispatch function using greedy matching.
// It matches commands step-by-step, committing to the highest priority match at each step.
// Returns (status, error, handled) where handled indicates if any command matched.
func (sp *STARSPane) dispatchCommand(ctx *panes.Context, cmds []userCommand, input *CommandInput) (CommandStatus, error, bool) {
	candidates := util.MapSlice(cmds, func(cmd userCommand) matchCandidate {
		return matchCandidate{
			cmd:       &cmd,
			matchers:  cmd.matchers,
			remaining: input.text,
		}
	})

	var firstErr error
	if status, err, handled := sp.greedyMatchCommands(ctx, input, candidates, &firstErr); err != nil {
		return CommandStatus{}, err, true
	} else if handled {
		return status, nil, true
	} else if firstErr != nil {
		return CommandStatus{}, firstErr, true
	} else {
		return CommandStatus{}, nil, false
	}
}

// greedyMatchCommands recursively matches commands using greedy priority-based selection.
// Returns (status, error, handled) where handled indicates if any command matched and executed.
func (sp *STARSPane) greedyMatchCommands(ctx *panes.Context, input *CommandInput, candidates []matchCandidate,
	firstErr *error) (CommandStatus, error, bool) {
	// Try complete candidates
	for _, c := range util.FilterSlice(candidates, func(mc matchCandidate) bool {
		return len(mc.matchers) == 0 && mc.remaining == ""
	}) {
		if input.hasClick && !c.cmd.consumesClick {
			continue
		}

		// Prepare args
		args := c.args
		if len(c.fpSpecs) > 0 {
			// Merge all FlightPlanSpecifiers into one
			var merged sim.FlightPlanSpecifier
			for _, spec := range c.fpSpecs {
				merged.Merge(spec)
			}
			args = append(args, merged)
		}

		// Bind args and call command function
		boundArgs := c.cmd.bindArgs(sp, args)
		status, err := c.cmd.call(sp, ctx, boundArgs)

		if err == nil {
			return status, nil, true
		}

		if *firstErr == nil {
			*firstErr = err
		}
	}

	// Advance ongoing candidates, grouping by priority: lower priority -> higher precedence
	byPriority := make(map[int][]matchCandidate)
	for _, c := range util.FilterSlice(candidates, func(mc matchCandidate) bool { return len(mc.matchers) > 0 }) {
		m := c.matchers[0]
		r, err := m.match(sp, ctx, input, c.remaining)
		if err != nil {
			// Record the error but continue trying other candidates.
			// matched=true with error means "looks like this type but invalid".
			if *firstErr == nil {
				*firstErr = err
			}
			continue
		}
		if r != nil && r.matched {
			next := c.advance(m, *r)
			byPriority[r.priority] = append(byPriority[r.priority], next)
		}
	}

	// Try each priority group in order (lowest priority value first)
	for _, candidates := range util.SortedMap(byPriority) {
		if status, err, handled := sp.greedyMatchCommands(ctx, input, candidates, firstErr); err != nil {
			return status, err, false
		} else if handled {
			return status, nil, true
		}
	}

	return CommandStatus{}, nil, false
}

///////////////////////////////////////////////////////////////////////////
// Matcher Types and Interface

// matchResult holds the result of a match attempt.
type matchResult struct {
	values    []any
	remaining string // text remaining after match
	matched   bool
	priority  int // Negative for literals (longer = more negative), >= 0 for typed matchers
}

// matcher matches a portion of user input
type matcher interface {
	// match attempts to match at the current position.
	// Returns nil if no match, or a matchResult with the match details.
	match(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (*matchResult, error)

	// validate checks that type handlers exist and constraints are met.
	validate() error

	// goType returns the Go type this matcher produces, or nil for literal matchers.
	goType() reflect.Type

	// consumesClick returns whether this matcher consumes mouse clicks.
	consumesClick() bool

	// Generator returns a matchGenerator that produces random valid input for this matcher.
	// Used by the fuzz testing system.
	Generator() matchGenerator
}

///////////////////////////////////////////////////////////////////////////
// Parsing command specifications into matchers

// makeMatchers parses a command specification string into a slice of matchers.
// The spec should not contain top-level alternatives (those are split by registerCommand).
func makeMatchers(spec string) ([]matcher, error) {
	var matchers []matcher
	remaining := spec

	for len(remaining) > 0 {
		if remaining[0] == '[' {
			// Typed parameter
			end := strings.IndexByte(remaining, ']')
			if end == -1 {
				return nil, fmt.Errorf("unclosed [ in spec: %s", spec)
			}
			content := remaining[1:end]

			// Parse the bracketed content for modifiers and alternatives
			m, err := makeTypedMatcher(content)
			if err != nil {
				return nil, fmt.Errorf("invalid matcher [%s]: %w", content, err)
			}
			matchers = append(matchers, m)
			remaining = remaining[end+1:]
		} else {
			// Literal text - extract until we hit [ or end
			end := strings.IndexByte(remaining, '[')
			if end == -1 {
				end = len(remaining)
			}
			if end > 0 {
				matchers = append(matchers, literalMatcher{text: remaining[:end]})
				remaining = remaining[end:]
			}
		}
	}

	// Validate all matchers and check click consumer placement (only last may consume)
	for i, m := range matchers {
		if err := m.validate(); err != nil {
			return nil, err
		}
		if i < len(matchers)-1 && m.consumesClick() {
			return nil, fmt.Errorf("click consumer at position %d, but only the last matcher may consume clicks", i)
		}
	}

	return matchers, nil
}

// makeTypedMatcher parses the content within [...] into a matcher.
// Supports: TYPE, ?TYPE, *TYPE, TYPE1|TYPE2|... alternatives, and TYPE:N for character count.
func makeTypedMatcher(content string) (matcher, error) {
	if content == "" {
		return nil, fmt.Errorf("empty type name")
	}

	optional, greedy := content[0] == '?', content[0] == '*'
	if optional || greedy {
		content = content[1:]
	}

	// Split by | for alternatives
	alternatives := strings.Split(content, "|")

	// Construct appropriate matcher type
	var inner matcher
	if len(alternatives) == 1 {
		sm, err := makeSingleMatcher(alternatives[0])
		if err != nil {
			return nil, err
		}
		sm.optional = optional
		inner = sm
	} else {
		// Multiple alternatives - parse each into a matcher
		var matchers []matcher
		for _, alt := range alternatives {
			m, err := makeSingleMatcher(alt)
			if err != nil {
				return nil, err
			}
			matchers = append(matchers, m)
		}
		inner = alternativeMatcher{optional: optional, inner: matchers}
	}

	if greedy {
		return greedyMatcher{inner: inner}, nil
	}
	return inner, nil
}

// makeSingleMatcher parses a single matcher element (no alternatives).
// Handles: TYPE and TYPE:N formats.
func makeSingleMatcher(content string) (*singleTypedMatcher, error) {
	typeName, countStr, hasCount := strings.Cut(content, ":")
	if hasCount {
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return nil, fmt.Errorf("invalid character count in %s: %w", content, err)
		}
		if typeName == "" {
			return nil, fmt.Errorf("no type name before colon in %q", content)
		}
		if count <= 0 {
			return nil, fmt.Errorf("character count must be positive in %s", content)
		}
		return &singleTypedMatcher{typeName: typeName, charCount: count}, nil
	}
	return &singleTypedMatcher{typeName: content}, nil
}

// extractMatcherTypes returns the Go types for all typed parameters in a matcher slice.
// For matchers with alternatives, uses the first alternative's type.
// For greedy matchers, the type appears once (will be aggregated at runtime).
// Multiple FlightPlanSpecifier types are collapsed to a single one (they will be merged).
func extractMatcherTypes(matchers []matcher) []reflect.Type {
	var types []reflect.Type
	hasFPSpec := false

	for _, m := range matchers {
		switch m.goType() {
		case nil:
			continue // literalMatcher doesn't produce a value
		case fpSpecType:
			hasFPSpec = true
		default:
			types = append(types, m.goType())
		}
	}

	if hasFPSpec {
		types = append(types, fpSpecType)
	}
	return types
}

///////////////////////////////////////////////////////////////////////////
// matcher implementations

////////////////////////////////////////////////////////////

// literalMatcher matches literal text exactly.
type literalMatcher struct {
	text string
}

func (lm literalMatcher) match(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (*matchResult, error) {
	if !strings.HasPrefix(text, lm.text) {
		return nil, nil
	}
	return &matchResult{
		remaining: text[len(lm.text):],
		matched:   true,
		priority:  -len(lm.text), // Negative so literals beat typed matchers; longer = more negative = higher priority
	}, nil
}

func (literalMatcher) validate() error      { return nil }
func (literalMatcher) goType() reflect.Type { return nil }
func (literalMatcher) consumesClick() bool  { return false }
func (lm literalMatcher) Generator() matchGenerator {
	return &literalMatchGenerator{Text: lm.text}
}

////////////////////////////////////////////////////////////

// singleTypedMatcher matches a single type handler.
type singleTypedMatcher struct {
	optional  bool
	typeName  string
	charCount int // For [:N] - exact character count (0 means no limit)
}

func (sm singleTypedMatcher) match(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (*matchResult, error) {
	handler := getTypeParser(sm.typeName)

	var value any
	var remaining string
	var matched bool
	var err error

	if sm.charCount > 0 {
		if len(text) < sm.charCount {
			if sm.optional {
				return &matchResult{
					values:    []any{reflect.Zero(sm.goType()).Interface()},
					remaining: text,
					matched:   true,
				}, nil
			}
			return nil, nil
		}
		value, _, matched, err = handler.Parse(sp, ctx, input, text[:sm.charCount])
		remaining = text[sm.charCount:]
	} else {
		value, remaining, matched, err = handler.Parse(sp, ctx, input, text)
	}

	if err != nil {
		// Return error with matched status for definitive errors
		if matched {
			return &matchResult{remaining: text, matched: true}, err
		}
		return nil, err
	}
	if !matched {
		if sm.optional {
			return &matchResult{
				values:    []any{reflect.Zero(sm.goType()).Interface()},
				remaining: text,
				matched:   true,
			}, nil
		}
		return nil, nil
	}
	if err := checkReturnedType(handler, value); err != nil {
		return nil, err
	}
	return &matchResult{
		values:    []any{value},
		remaining: remaining,
		matched:   true,
		priority:  getTypePriority(sm.typeName),
	}, nil
}

func (sm singleTypedMatcher) validate() error {
	if getTypeParser(sm.typeName) == nil {
		return fmt.Errorf("unknown type: %s", sm.typeName)
	}
	if sm.charCount > 0 && getTypeParser(sm.typeName).ConsumesClick() {
		return fmt.Errorf("character count not supported for click-consuming type %s", sm.typeName)
	}
	return nil
}

func (sm singleTypedMatcher) goType() reflect.Type {
	return getTypeParser(sm.typeName).GoType()
}

func (sm singleTypedMatcher) consumesClick() bool {
	return getTypeParser(sm.typeName).ConsumesClick()
}

func (sm singleTypedMatcher) Generator() matchGenerator {
	gen := typeNameToGenerator(sm.typeName)
	if sm.optional {
		return &optionalMatchGenerator{Inner: gen}
	}
	return gen
}

// typeNameToGenerator returns the appropriate generator for a type parser name.
func typeNameToGenerator(typeName string) matchGenerator {
	switch typeName {
	// Track identifiers
	case "TRK_ACID":
		return &trackMatchGenerator{Style: "TRK_ACID"}
	case "TRK_BCN":
		return &trackMatchGenerator{Style: "TRK_BCN"}
	case "TRK_INDEX":
		return &trackMatchGenerator{Style: "TRK_INDEX"}
	case "TRK_INDEX_SUSPENDED":
		return &trackMatchGenerator{Style: "TRK_INDEX_SUSPENDED"}

	// Click consumers
	case "SLEW":
		return &slewMatchGenerator{}
	case "GHOST_SLEW":
		return &ghostSlewMatchGenerator{}
	case "POS", "POS_NORM", "POS_RAW":
		return &posMatchGenerator{}

	// Controller positions
	case "TCP1":
		return &tcpMatchGenerator{Style: "TCP1"}
	case "TCP2":
		return &tcpMatchGenerator{Style: "TCP2"}
	case "TCW":
		return &tcpMatchGenerator{Style: "TCW"}
	case "TCP_TRI":
		return &tcpMatchGenerator{Style: "TCP_TRI"}
	case "ARTCC":
		return &tcpMatchGenerator{Style: "ARTCC"}

	// Numbers
	case "#":
		return &numMatchGenerator{MinDigits: 1, MaxDigits: 1}
	case "##":
		return &numMatchGenerator{MinDigits: 2, MaxDigits: 2}
	case "NUM":
		return &numMatchGenerator{MinDigits: 1, MaxDigits: 5}
	case "FLOAT":
		return &floatMatchGenerator{Max: 100}
	case "TPA_FLOAT":
		return &floatMatchGenerator{Max: 9.9}

	// Identifiers
	case "SPC":
		return &spcMatchGenerator{}
	case "ACID":
		return &acidMatchGenerator{}
	case "BCN":
		return &beaconMatchGenerator{}
	case "BCN_BLOCK":
		return &beaconMatchGenerator{BlockOnly: true}
	case "AIRPORT_ID":
		return &airportMatchGenerator{}
	case "FIX":
		return &fixMatchGenerator{}
	case "TIME":
		return &timeMatchGenerator{}
	case "ALT_FILTER_6":
		return &altFilter6MatchGenerator{}
	case "CRDA_RUNWAY_ID":
		return &crdaRunwayMatchGenerator{}
	case "UNASSOC_FP":
		return &unassocFPMatchGenerator{}

	// Restriction areas
	case "QL_REGION":
		return &qlRegionMatchGenerator{}
	case "RA_INDEX":
		return &raIndexMatchGenerator{UserOnly: false}
	case "USER_RA_INDEX":
		return &raIndexMatchGenerator{UserOnly: true}
	case "RA_TEXT":
		return &raTextMatchGenerator{WithLocation: false, ClosedShape: false}
	case "RA_TEXT_AND_LOCATION":
		return &raTextMatchGenerator{WithLocation: true, ClosedShape: false}
	case "RA_CLOSED_TEXT":
		return &raTextMatchGenerator{WithLocation: false, ClosedShape: true}
	case "RA_CLOSED_TEXT_AND_LOCATION":
		return &raTextMatchGenerator{WithLocation: true, ClosedShape: true}
	case "RA_LOCATION":
		return &raLocationMatchGenerator{}
	case "QL_POSITIONS":
		return &qlPositionsMatchGenerator{}

	// Text handlers
	case "FIELD":
		return &fieldMatchGenerator{MinLen: 1, MaxLen: 8}
	case "ALL_TEXT":
		return &allTextMatchGenerator{}

	// Flight plan specifiers
	case "FP_ACID":
		return &fpSpecMatchGenerator{Style: "FP_ACID"}
	case "FP_BEACON":
		return &fpSpecMatchGenerator{Style: "FP_BEACON"}
	case "FP_SP1":
		return &fpSpecMatchGenerator{Style: "FP_SP1"}
	case "FP_TRI_SP1":
		return &fpSpecMatchGenerator{Style: "FP_TRI_SP1"}
	case "FP_PLUS_SP2":
		return &fpSpecMatchGenerator{Style: "FP_PLUS_SP2"}
	case "FP_ALT_A":
		return &fpSpecMatchGenerator{Style: "FP_ALT_A"}
	case "FP_ALT_P":
		return &fpSpecMatchGenerator{Style: "FP_ALT_P"}
	case "FP_ALT_R":
		return &fpSpecMatchGenerator{Style: "FP_ALT_R"}
	case "FP_TRI_ALT_A":
		return &fpSpecMatchGenerator{Style: "FP_TRI_ALT_A"}
	case "FP_PLUS_ALT_A":
		return &fpSpecMatchGenerator{Style: "FP_PLUS_ALT_A"}
	case "FP_PLUS2_ALT_R":
		return &fpSpecMatchGenerator{Style: "FP_PLUS2_ALT_R"}
	case "FP_TCP":
		return &fpSpecMatchGenerator{Style: "FP_TCP"}
	case "FP_NUM_ACTYPE":
		return &fpSpecMatchGenerator{Style: "FP_NUM_ACTYPE"}
	case "FP_NUM_ACTYPE4":
		return &fpSpecMatchGenerator{Style: "FP_NUM_ACTYPE4"}
	case "FP_ACTYPE":
		return &fpSpecMatchGenerator{Style: "FP_ACTYPE"}
	case "FP_COORD_TIME":
		return &fpSpecMatchGenerator{Style: "FP_COORD_TIME"}
	case "FP_FIX_PAIR":
		return &fpSpecMatchGenerator{Style: "FP_FIX_PAIR"}
	case "FP_EXIT_FIX":
		return &fpSpecMatchGenerator{Style: "FP_EXIT_FIX"}
	case "FP_FLT_TYPE":
		return &fpSpecMatchGenerator{Style: "FP_FLT_TYPE"}
	case "FP_RULES":
		return &fpSpecMatchGenerator{Style: "FP_RULES"}
	case "FP_RNAV":
		return &fpSpecMatchGenerator{Style: "FP_RNAV"}
	case "FP_VFR_FIXES":
		return &fpSpecMatchGenerator{Style: "FP_VFR_FIXES"}

	default:
		// Fallback to generic field generator
		return &fieldMatchGenerator{MinLen: 1, MaxLen: 8}
	}
}

func checkReturnedType(handler typeParser, value any) error {
	if value != nil {
		expected := handler.GoType()
		got := reflect.TypeOf(value)
		if expected.Kind() == reflect.Interface {
			if !got.Implements(expected) {
				return fmt.Errorf("internal error: type parser %s returned %s which doesn't implement %s",
					handler.Identifier(), got.String(), expected.String())
			}
		} else if got != expected {
			return fmt.Errorf("internal error: type parser %s returned wrong type: got %s, expected %s",
				handler.Identifier(), got.String(), expected.String())
		}
	}
	return nil
}

////////////////////////////////////////////////////////////

// alternativeMatcher tries multiple matchers, returning the best match by priority.
// Syntax: [TYPE1|TYPE2], [?TYPE1|TYPE2], [TYPE1:N|TYPE2]
type alternativeMatcher struct {
	optional bool
	inner    []matcher
}

func (am alternativeMatcher) match(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (*matchResult, error) {
	// Try alternatives, return best match by priority.
	// All alternatives return the same Go type (enforced by validate()).
	var bestResult *matchResult

	for _, m := range am.inner {
		result, err := m.match(sp, ctx, input, text)
		if err != nil {
			// Propagate definitive errors
			if result != nil && result.matched {
				return nil, err
			}
			continue
		}
		if result != nil && result.matched {
			if bestResult == nil || result.priority < bestResult.priority {
				bestResult = result
			}
		}
	}

	if bestResult != nil {
		return bestResult, nil
	}
	if am.optional {
		return &matchResult{
			values:    []any{reflect.Zero(am.goType()).Interface()},
			remaining: text,
			matched:   true,
		}, nil
	}
	return nil, nil
}

func (am alternativeMatcher) validate() error {
	for _, m := range am.inner {
		if err := m.validate(); err != nil {
			return err
		}
	}

	// Verify all alternatives return the same Go type and agree on click consumption
	firstType := am.inner[0].goType()
	for _, m := range am.inner[1:] {
		if m.goType() != firstType {
			return fmt.Errorf("alternatives must have same Go type: %v vs %v", firstType, m.goType())
		}
	}
	firstClick := am.inner[0].consumesClick()
	for _, m := range am.inner[1:] {
		if m.consumesClick() != firstClick {
			return fmt.Errorf("alternatives must all consume clicks or none")
		}
	}
	return nil
}

func (am alternativeMatcher) goType() reflect.Type {
	return am.inner[0].goType()
}

func (am alternativeMatcher) consumesClick() bool {
	return am.inner[0].consumesClick()
}

func (am alternativeMatcher) Generator() matchGenerator {
	var alts []matchGenerator
	for _, m := range am.inner {
		alts = append(alts, m.Generator())
	}
	gen := &altMatchGenerator{Alternatives: alts}
	if am.optional {
		return &optionalMatchGenerator{Inner: gen}
	}
	return gen
}

////////////////////////////////////////////////////////////

// greedyMatcher wraps another matcher to consume 0+ matches of space-delimited types.
// Syntax: [*TYPE], [*TYPE1|TYPE2]
type greedyMatcher struct {
	inner matcher
}

func (gm greedyMatcher) match(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (*matchResult, error) {
	var values []any
	var priority int
	firstMatch := true

	for {
		// Skip leading spaces only after the first match
		if !firstMatch {
			text = strings.TrimLeft(text, " ")
		}

		result, err := gm.inner.match(sp, ctx, input, text)
		if err != nil {
			return nil, err
		}
		if result == nil || !result.matched {
			break
		}

		values = append(values, result.values...)
		text = result.remaining
		if firstMatch {
			priority = result.priority
			firstMatch = false
		}
	}

	// Greedy always succeeds (it may match 0 times).
	// If nothing matched, provide a zero value of the expected type.
	if len(values) == 0 {
		values = []any{reflect.Zero(gm.goType()).Interface()}
	}
	return &matchResult{
		values:    values,
		remaining: text,
		matched:   true,
		priority:  priority,
	}, nil
}

func (gm greedyMatcher) validate() error {
	if gm.inner.goType() == nil {
		return fmt.Errorf("greedy matcher must wrap a value-producing matcher")
	}
	return gm.inner.validate()
}

func (gm greedyMatcher) goType() reflect.Type { return gm.inner.goType() }
func (gm greedyMatcher) consumesClick() bool  { return gm.inner.consumesClick() }
func (gm greedyMatcher) Generator() matchGenerator {
	return &greedyMatchGenerator{Inner: gm.inner.Generator()}
}

///////////////////////////////////////////////////////////////////////////
// Function Signature Validation and Argument Binding

// Common reflect types used in function binding
var (
	trackType      = reflect.TypeFor[*sim.Track]()
	trackStateType = reflect.TypeFor[*TrackState]()
	statusType     = reflect.TypeFor[CommandStatus]()
	errorInterface = reflect.TypeFor[error]()
)

// normalizeType maps *TrackState to *sim.Track for type comparison.
// This allows handlers to declare *TrackState parameters while the parser yields *sim.Track.
func normalizeType(t reflect.Type) reflect.Type {
	if t == trackStateType {
		return trackType
	}
	return t
}

// validateFunctionSignature validates the handler function matches the spec.
// Returns the count of initial arguments for caching in userCommand.
func validateFunctionSignature(cmd userCommand) (int, error) {
	funcType := reflect.TypeOf(cmd.handlerFunc)
	if funcType == nil || funcType.Kind() != reflect.Func {
		return 0, fmt.Errorf("handlerFunc must be a function")
	}

	numInitial, err := countInitialArgs(funcType)
	if err != nil {
		return 0, err
	}

	expectedTypes := extractMatcherTypes(cmd.matchers)
	if err := validateParamTypes(funcType, expectedTypes, numInitial); err != nil {
		return 0, err
	}
	if err := validateReturnTypes(funcType); err != nil {
		return 0, err
	}

	return numInitial, nil
}

// validateParamTypes checks that function parameters match expected types.
// Allows *TrackState in place of *sim.Track via normalizeType.
func validateParamTypes(funcType reflect.Type, expectedTypes []reflect.Type, numInitial int) error {
	expectedParamCount := numInitial + len(expectedTypes)
	if funcType.NumIn() != expectedParamCount {
		return fmt.Errorf("function has %d params, expected %d (%d initial + %d typed)",
			funcType.NumIn(), expectedParamCount, numInitial, len(expectedTypes))
	}

	// Build count of expected types
	expectedCount := make(map[reflect.Type]int)
	for _, t := range expectedTypes {
		expectedCount[t]++
	}

	// Build count of actual types (normalized)
	actualCount := make(map[reflect.Type]int)
	for i := numInitial; i < funcType.NumIn(); i++ {
		actualCount[normalizeType(funcType.In(i))]++
	}

	// Compare counts
	for t, count := range expectedCount {
		if actualCount[t] != count {
			return fmt.Errorf("expected %d params of type %v, got %d", count, t, actualCount[t])
		}
	}
	for t, count := range actualCount {
		if expectedCount[t] != count {
			return fmt.Errorf("unexpected param type %v (got %d)", t, count)
		}
	}

	return nil
}

// validateReturnTypes checks that function returns (), error, CommandStatus, or (CommandStatus, error).
func validateReturnTypes(funcType reflect.Type) error {
	numOut := funcType.NumOut()
	if numOut > 2 {
		return fmt.Errorf("function must return (), error, CommandStatus, or (CommandStatus, error), got %d return values", numOut)
	}
	if numOut == 1 {
		outType := funcType.Out(0)
		if outType != statusType && !outType.Implements(errorInterface) {
			return fmt.Errorf("single return value must be CommandStatus or error, got %v", outType)
		}
	}
	if numOut == 2 {
		if funcType.Out(0) != statusType {
			return fmt.Errorf("first return value must be CommandStatus, got %v", funcType.Out(0))
		}
		if !funcType.Out(1).Implements(errorInterface) {
			return fmt.Errorf("second return value must be error, got %v", funcType.Out(1))
		}
	}
	return nil
}

// initialArgTypes lists the allowed initial argument types for command handlers.
var initialArgTypes = []reflect.Type{
	reflect.TypeFor[*STARSPane](),
	reflect.TypeFor[*panes.Context](),
	reflect.TypeFor[*Preferences](),
}

// countInitialArgs counts how many initial arguments a function expects.
// Returns the count and any error if duplicates are found.
func countInitialArgs(funcType reflect.Type) (int, error) {
	seen := make(map[reflect.Type]bool)

	isInitialArgType := func(t reflect.Type) bool {
		return slices.Contains(initialArgTypes, t)
	}

	for i := range funcType.NumIn() {
		paramType := funcType.In(i)
		if isInitialArgType(paramType) {
			if seen[paramType] {
				return 0, fmt.Errorf("duplicate initial arg type: %v", paramType)
			}
			seen[paramType] = true
		} else {
			// Once we hit a non-initial type, all remaining must be non-initial
			for j := i; j < funcType.NumIn(); j++ {
				if isInitialArgType(funcType.In(j)) {
					return 0, fmt.Errorf("initial arg type %v found after non-initial types", funcType.In(j))
				}
			}
			break
		}
	}

	return len(seen), nil
}

// bindArgs binds extracted arguments to match the function signature order.
// If the function expects *TrackState but the extracted arg is *sim.Track, converts it.
func (cmd userCommand) bindArgs(sp *STARSPane, extractedArgs []any) []any {
	funcType := reflect.TypeOf(cmd.handlerFunc)

	// Build map of type -> extracted values
	argsByType := make(map[reflect.Type][]any)
	for _, arg := range extractedArgs {
		if arg != nil {
			argsByType[reflect.TypeOf(arg)] = append(argsByType[reflect.TypeOf(arg)], arg)
		}
	}

	// Build ordered args based on function signature (skip initial args)
	var orderedArgs []any
	typeUsage := make(map[reflect.Type]int)
	for i := cmd.numInitial; i < funcType.NumIn(); i++ {
		paramType := funcType.In(i)

		// Special case: function wants *TrackState but we have *sim.Track
		if paramType == trackStateType {
			idx := typeUsage[trackType]
			if idx < len(argsByType[trackType]) {
				trk := argsByType[trackType][idx].(*sim.Track)
				orderedArgs = append(orderedArgs, sp.TrackState[trk.ADSBCallsign])
			}
			typeUsage[trackType]++
		} else {
			idx := typeUsage[paramType]
			if idx < len(argsByType[paramType]) {
				orderedArgs = append(orderedArgs, argsByType[paramType][idx])
			}
			typeUsage[paramType]++
		}
	}

	return orderedArgs
}

// initialArgProviders maps initial arg types to functions that provide their values.
var initialArgProviders = map[reflect.Type]func(sp *STARSPane, ctx *panes.Context) reflect.Value{
	reflect.TypeFor[*STARSPane]():     func(sp *STARSPane, ctx *panes.Context) reflect.Value { return reflect.ValueOf(sp) },
	reflect.TypeFor[*panes.Context](): func(sp *STARSPane, ctx *panes.Context) reflect.Value { return reflect.ValueOf(ctx) },
	reflect.TypeFor[*Preferences]():   func(sp *STARSPane, ctx *panes.Context) reflect.Value { return reflect.ValueOf(sp.currentPrefs()) },
}

// call invokes the command handler function with the provided arguments.
// Handlers may return: (), error, CommandStatus, or (CommandStatus, error).
func (cmd userCommand) call(sp *STARSPane, ctx *panes.Context, args []any) (CommandStatus, error) {
	funcValue := reflect.ValueOf(cmd.handlerFunc)
	funcType := funcValue.Type()

	var callArgs []reflect.Value
	for i := range cmd.numInitial {
		paramType := funcType.In(i)
		if provider, ok := initialArgProviders[paramType]; ok {
			callArgs = append(callArgs, provider(sp, ctx))
		}
	}

	// Add typed command arguments
	for _, arg := range args {
		callArgs = append(callArgs, reflect.ValueOf(arg))
	}

	results := funcValue.Call(callArgs)

	// Handle return signatures: (), error, CommandStatus, (CommandStatus, error)
	switch len(results) {
	case 0:
		return CommandStatus{}, nil
	case 1:
		if results[0].Type() == statusType {
			return results[0].Interface().(CommandStatus), nil
		}
		// Must be error
		if results[0].IsNil() {
			return CommandStatus{}, nil
		}
		return CommandStatus{}, results[0].Interface().(error)
	default:
		status := results[0].Interface().(CommandStatus)
		if !results[1].IsNil() {
			return status, results[1].Interface().(error)
		}
		return status, nil
	}
}
