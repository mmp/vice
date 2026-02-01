package stt

import "sort"

// CallsignPattern represents a declarative callsign matching rule.
type CallsignPattern struct {
	Name            string                                    // Human-readable name for debugging
	Template        string                                    // Original template string
	Priority        int                                       // Higher priority patterns are tried first
	Matchers        []callsignMatcher                         // Parsed from template
	MinScore        float64                                   // Minimum score to accept a match
	FixedConfidence float64                                   // If > 0, use this confidence instead of computed
	ScoringFunc     func(result *callsignMatchResult) float64 // Custom scoring function
	Condition       func(Aircraft) bool                       // Pre-filter aircraft (returns true to include)
	RequireUnique   bool                                      // Only accept if exactly one aircraft matches
}

// callsignPatterns holds all registered callsign patterns.
var callsignPatterns []CallsignPattern

// CallsignPatternOption configures a callsign pattern registration.
type CallsignPatternOption func(*CallsignPattern)

// WithCallsignName sets a human-readable name for the pattern.
func WithCallsignName(name string) CallsignPatternOption {
	return func(p *CallsignPattern) {
		p.Name = name
	}
}

// WithCallsignPriority sets the pattern priority (higher = tried first).
func WithCallsignPriority(priority int) CallsignPatternOption {
	return func(p *CallsignPattern) {
		p.Priority = priority
	}
}

// WithCallsignMinScore sets the minimum score threshold for accepting a match.
func WithCallsignMinScore(score float64) CallsignPatternOption {
	return func(p *CallsignPattern) {
		p.MinScore = score
	}
}

// WithCallsignConfidence sets a fixed confidence value for matches from this pattern.
func WithCallsignConfidence(conf float64) CallsignPatternOption {
	return func(p *CallsignPattern) {
		p.FixedConfidence = conf
	}
}

// WithCallsignScoring sets a custom scoring function.
func WithCallsignScoring(fn func(*callsignMatchResult) float64) CallsignPatternOption {
	return func(p *CallsignPattern) {
		p.ScoringFunc = fn
	}
}

// WithCallsignRequire sets a condition function to pre-filter aircraft.
func WithCallsignRequire(fn func(Aircraft) bool) CallsignPatternOption {
	return func(p *CallsignPattern) {
		p.Condition = fn
	}
}

// WithCallsignRequireUnique requires exactly one aircraft to match.
func WithCallsignRequireUnique() CallsignPatternOption {
	return func(p *CallsignPattern) {
		p.RequireUnique = true
	}
}

// RegisterCallsignPattern registers a callsign matching pattern.
func RegisterCallsignPattern(template string, opts ...CallsignPatternOption) {
	pattern := CallsignPattern{
		Template: template,
		Priority: 50, // Default priority
	}

	for _, opt := range opts {
		opt(&pattern)
	}

	// Generate name from template if not set
	if pattern.Name == "" {
		pattern.Name = generatePatternName(template)
	}

	// Parse the template into matchers
	matchers, err := parseCallsignTemplate(template)
	if err != nil {
		panic("failed to parse callsign template " + template + ": " + err.Error())
	}
	pattern.Matchers = matchers

	callsignPatterns = append(callsignPatterns, pattern)
}

// sortedCallsignPatterns returns patterns sorted by priority (highest first).
func sortedCallsignPatterns() []CallsignPattern {
	sorted := make([]CallsignPattern, len(callsignPatterns))
	copy(sorted, callsignPatterns)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})
	return sorted
}

// filterAircraftByCondition returns aircraft that pass the pattern's condition.
func filterAircraftByCondition(aircraft map[string]Aircraft, condition func(Aircraft) bool) map[string]Aircraft {
	if condition == nil {
		return aircraft
	}
	filtered := make(map[string]Aircraft)
	for key, ac := range aircraft {
		if condition(ac) {
			filtered[key] = ac
		}
	}
	return filtered
}
