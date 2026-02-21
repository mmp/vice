package stt

import (
	"fmt"
	"reflect"
	"sync"
)

// sttCommand represents a registered command with its template and handler.
type sttCommand struct {
	name           string    // Human-readable name for debugging
	template       string    // Original template string
	matchers       []matcher // Parsed matchers
	handler        any       // Handler function
	priority       int       // Higher priority wins when multiple match
	thenVariant    string    // Output format for "then" variant (e.g., "TD%d")
	sayAgainOnFail bool      // If true, emit SAYAGAIN when type parser fails
}

// sttCommands holds all registered commands.
var sttCommands []sttCommand

var initOnce sync.Once

func Init() {
	initOnce.Do(func() {
		registerAllCallsignPatterns()
		registerAllCommands()
	})
}

// CommandOption configures a command registration.
type CommandOption func(*sttCommand)

// WithPriority sets the command priority.
func WithPriority(p int) CommandOption {
	return func(c *sttCommand) {
		c.priority = p
	}
}

// WithThenVariant sets the format for "then" sequenced commands.
func WithThenVariant(format string) CommandOption {
	return func(c *sttCommand) {
		c.thenVariant = format
	}
}

// WithName sets a human-readable name for debugging.
func WithName(name string) CommandOption {
	return func(c *sttCommand) {
		c.name = name
	}
}

// WithSayAgainOnFail enables emitting SAYAGAIN when a type parser fails.
// Use this for commands where the controller clearly requested something
// specific (like "expect approach") and we should ask for clarification.
func WithSayAgainOnFail() CommandOption {
	return func(c *sttCommand) {
		c.sayAgainOnFail = true
	}
}

// registerSTTCommand registers a command with a template string and handler function.
//
// Template syntax:
//   - `word` - Literal keyword (fuzzy matched)
//   - `word1|word2` - Keyword alternatives
//   - `[words]` - Optional literal words
//   - `{altitude}` - Altitude parameter
//   - `{heading}` - Heading parameter (1-360)
//   - `{speed}` - Speed parameter (100-400 knots)
//   - `{fix}` - Navigation fix (fuzzy matched)
//   - `{approach}` - Approach name (fuzzy matched)
//   - `{squawk}` - Squawk code (4 digits)
//   - `{degrees}` - Turn degrees (1-45)
//   - `{sid}` - SID name
//   - `{star}` - STAR name
//   - `{num:min-max}` - Number in range
//   - `{skip}` - Skip tokens until next matcher
//   - `[word {type}]` - Optional section with typed param (param is *T, nil if absent)
//
// Handler signatures:
//   - func() string - No parameters
//   - func(val int) string - Single integer parameter
//   - func(val1 int, val2 *int) string - Required and optional parameters
//   - func(fix string, alt int) string - String and integer parameters
//
// The handler must return a string (the command output).
func registerSTTCommand(template string, handler any, opts ...CommandOption) {
	cmd := sttCommand{
		template: template,
		handler:  handler,
		priority: 5, // Default priority
	}

	for _, opt := range opts {
		opt(&cmd)
	}

	// Generate name from template if not set
	if cmd.name == "" {
		cmd.name = generatePatternName(template)
	}

	// Parse the template into matchers
	matchers, err := parseTemplate(template)
	if err != nil {
		panic(fmt.Sprintf("failed to parse template %q: %v", template, err))
	}
	cmd.matchers = matchers

	// Validate handler signature matches template parameters
	if err := validateHandler(handler, matchers); err != nil {
		panic(fmt.Sprintf("handler validation failed for template %q: %v", template, err))
	}

	sttCommands = append(sttCommands, cmd)
}

// validateHandler checks that the handler function signature matches the template parameters.
func validateHandler(handler any, matchers []matcher) error {
	handlerType := reflect.TypeOf(handler)
	if handlerType.Kind() != reflect.Func {
		return fmt.Errorf("handler must be a function, got %T", handler)
	}

	// Check return type
	if handlerType.NumOut() != 1 || handlerType.Out(0).Kind() != reflect.String {
		return fmt.Errorf("handler must return exactly one string")
	}

	// Count typed parameters from matchers
	var expectedParams []reflect.Type
	for _, m := range matchers {
		if tm, ok := m.(*typedMatcher); ok {
			if om, ok2 := tm.inner.(*optionalGroupMatcher); ok2 {
				// Optional group - collect all typed matchers, they become pointers
				for _, inner := range om.matchers {
					if tm2, ok3 := inner.(*typedMatcher); ok3 {
						expectedParams = append(expectedParams, reflect.PointerTo(tm2.parser.goType()))
					}
				}
			} else {
				expectedParams = append(expectedParams, tm.parser.goType())
			}
		} else if om, ok := m.(*optionalGroupMatcher); ok {
			// Optional group at top level - collect typed matchers as pointers
			for _, inner := range om.matchers {
				if tm, ok2 := inner.(*typedMatcher); ok2 {
					expectedParams = append(expectedParams, reflect.PointerTo(tm.parser.goType()))
				}
			}
		}
	}

	// Check parameter count
	if handlerType.NumIn() != len(expectedParams) {
		return fmt.Errorf("handler expects %d params, but template has %d typed params",
			handlerType.NumIn(), len(expectedParams))
	}

	// Check parameter types
	for i := 0; i < handlerType.NumIn(); i++ {
		handlerParam := handlerType.In(i)
		expectedParam := expectedParams[i]
		if handlerParam != expectedParam {
			return fmt.Errorf("param %d: handler expects %v, but template has %v",
				i, handlerParam, expectedParam)
		}
	}

	return nil
}
