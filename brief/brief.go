// brief/brief.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package brief

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	goldmarkutil "github.com/yuin/goldmark/util"
)

// VideoMapBlock represents an annotated video map embedded in scenario
// brief markdown using a fenced "videomap" code block.
type VideoMapBlock struct {
	File        string
	Maps        []string
	Annotations []MapAnnotation
	Extent      [2]math.Point2LL // Optional extent (upper-left, lower-right)
	Width       int              // Width as percentage (1-100), default 100
}

// AnnotationType specifies the type of map annotation.
type AnnotationType int

const (
	AnnotationLine AnnotationType = iota
	AnnotationArrow
	AnnotationPolygon
	AnnotationFix
	AnnotationPoint
	AnnotationAirspace // Per-controller airspace boundaries
)

// WaypointAnnotation holds an annotation associated with a waypoint.
type WaypointAnnotation struct {
	ClimbAltitude   int // Altitude to climb to (0 if not specified)
	DescentAltitude int // Altitude to descend to (0 if not specified)
}

// AnnotatedVertex holds a coordinate point and its optional annotation.
type AnnotatedVertex struct {
	Point      math.Point2LL
	Annotation *WaypointAnnotation // nil if there is no annotation
}

// MapAnnotation represents a drawable element on a video map.
type MapAnnotation struct {
	Type         AnnotationType    // Type of annotation (line, arrow, polygon, fix, point, airspace)
	Vertices     []AnnotatedVertex // Coordinates with optional annotations (single vertex for fix/point)
	Label        []string          // Multi-line text label
	AirspaceTCPs []string          // For AnnotationAirspace; empty = user's consolidated positions
}

// Validate runs structural checks that do not depend on caller-side state:
// file present, at least one map specified, and per-annotation vertex counts.
// Cross-reference checks (map existence in the named file, airspace TCP
// existence) are driven by callbacks at ParseMarkdown time; see HasMap and
// HasAirspaceTCP on ParseOptions.
func (m *VideoMapBlock) Validate() []string {
	var errs []string
	if m.File == "" {
		errs = append(errs, "no video map file specified")
		return errs
	}
	if len(m.Maps) == 0 {
		errs = append(errs, "no maps specified")
		return errs
	}
	for i, ann := range m.Annotations {
		prefix := fmt.Sprintf("Annotation %d: ", i)
		switch ann.Type {
		case AnnotationLine, AnnotationArrow:
			if len(ann.Vertices) < 2 {
				errs = append(errs, prefix+"line/arrow must have at least 2 points")
			}
		case AnnotationPolygon:
			if len(ann.Vertices) < 3 {
				errs = append(errs, prefix+"polygon must have at least 3 points")
			}
		case AnnotationFix:
			if len(ann.Vertices) != 1 {
				errs = append(errs, prefix+"fix annotation must have exactly 1 vertex")
			}
		case AnnotationPoint:
			if len(ann.Vertices) != 1 {
				errs = append(errs, prefix+"point annotation must have exactly 1 vertex")
			}
		}
	}
	return errs
}

///////////////////////////////////////////////////////////////////////////
// Goldmark extension for parsing VideoMapBlock in markdown

// getLineNumber converts a byte offset in source to a line number (1-indexed).
func getLineNumber(source []byte, offset int) int {
	return 1 + bytes.Count(source[:min(offset, len(source))], []byte{'\n'})
}

// DocMapExtension is a goldmark extension that transforms links with
// VideoMapBlock targets into custom AST nodes.
type DocMapExtension struct {
	videoMapFiles []string
}

// NewDocMapExtension creates a new DocMapExtension.
func NewDocMapExtension() *DocMapExtension {
	return &DocMapExtension{}
}

// Extend implements goldmark.Extender.
func (e *DocMapExtension) Extend(m goldmark.Markdown) {
	docMapTransformer := &docMapTransformer{videoMapFiles: &e.videoMapFiles}
	captionTransformer := &tableCaptionTransformer{}
	configTransformer := &configurationTransformer{}

	m.Parser().AddOptions(
		parser.WithASTTransformers(
			goldmarkutil.Prioritized(docMapTransformer, 100),
			goldmarkutil.Prioritized(configTransformer, 99),
			goldmarkutil.Prioritized(captionTransformer, 98),
		),
	)
}

// parseWaypointAnnotation parses climb/descent annotations from a waypoint string.
// Format: /c<altitude> or /d<altitude>
// Example: PUCKY/d16000 means to indicate issuing a descent to 16,000 at PUCKY
func parseWaypointAnnotation(annotationStr string) (WaypointAnnotation, error) {
	var ann WaypointAnnotation
	var haveClimb, haveDescent bool

	// Split by "/" to get individual parts
	parts := strings.Split(annotationStr, "/")

	parseAlt := func(s string) (int, error) {
		alt, err := strconv.Atoi(s)
		if err != nil {
			return 0, err
		}
		if alt < 0 {
			return 0, fmt.Errorf("altitude must be non-negative: %d", alt)
		}
		return alt, nil
	}

	for _, part := range parts {
		if len(part) < 2 {
			return ann, fmt.Errorf("malformed annotation %q (expected c<alt> or d<alt>)", part)
		}

		ty, altStr := part[0], part[1:]
		switch ty {
		case 'c', 'C':
			if haveClimb {
				return ann, fmt.Errorf("duplicate climb annotation")
			}
			alt, err := parseAlt(altStr)
			if err != nil {
				return ann, fmt.Errorf("invalid climb altitude %q: %w", altStr, err)
			}
			ann.ClimbAltitude = alt
			haveClimb = true

		case 'd', 'D':
			if haveDescent {
				return ann, fmt.Errorf("duplicate descent annotation")
			}
			alt, err := parseAlt(altStr)
			if err != nil {
				return ann, fmt.Errorf("invalid descent altitude %q: %w", altStr, err)
			}
			ann.DescentAltitude = alt
			haveDescent = true

		default:
			return ann, fmt.Errorf("unknown annotation type: %c (expected 'c' or 'd')", ty)
		}
	}

	return ann, nil
}

// parseVideoMapContent parses the videomap code block content in key:value format.
// Format:
//
//	file: videomaps/FILE.gob.zst
//	map: MAP_NAME1
//	map: MAP_NAME2
//	width: 50%
//	extent: LOC LOC
//
//	line: lat,lon, lat,lon, ...
//	label: LABEL LINE 1
//	label: LABEL LINE 2
//
//	arrow: LOC LOC
//	label: ARROW LABEL
//
//	polygon: LOC LOC LOC ...
//	label: POLYGON LABEL
//
//	fix: LOC
//	label: FIX LABEL
//
//	point: LOC
//	label: POINT LABEL
//
// LOC location specifications may be explicit lat/long coordinates,
// waypoint/fix names, or airport identifiers.
func parseVideoMapContent(content string) (*VideoMapBlock, error) {
	docMap := &VideoMapBlock{
		Width: 100, // Default to 100%
	}

	var currentAnnotation *MapAnnotation

	// Helper to parse a location string as lat/long, fix, or airport
	parseLocation := func(locStr string) (math.Point2LL, error) {
		// Try parsing as lat/long first
		if p, err := math.ParseLatLong([]byte(locStr)); err == nil {
			return p, nil
		}

		// Try as waypoint/fix
		if p, ok := av.DB.LookupWaypoint(locStr); ok {
			return p, nil
		}

		// Try as airport. LookupAirport handles the 3-letter K/P/T prefix
		// fallback used for US airports.
		if ap, ok := av.DB.LookupAirport(locStr); ok {
			return ap.Location, nil
		}

		return math.Point2LL{}, fmt.Errorf("invalid location %q (not a valid lat/long, fix, or airport)", locStr)
	}

	// Helper to finalize and append current annotation
	flushAnnotation := func() {
		if currentAnnotation == nil {
			return
		}
		if len(currentAnnotation.Vertices) > 0 {
			docMap.Annotations = append(docMap.Annotations, *currentAnnotation)
		}
		currentAnnotation = nil
	}

	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}

		// Skip config control directives. ProcessConfigSelections strips well-formed
		// ::: if/endif lines before we get here; anything still present (because
		// preprocessing was skipped, or because the directive is malformed) is
		// reported by validateConfigConditionSyntax at the brief level, so we just
		// avoid misparsing them as key: value.
		if strings.HasPrefix(line, ":::") {
			continue
		}

		// Parse key: value
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed line: %q", line)
		}

		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])

		switch key {
		case "file":
			docMap.File = value

		case "map":
			// Multiple map: lines specify multiple maps
			docMap.Maps = append(docMap.Maps, value)

		case "width":
			// Parse width as percentage (e.g., "50%" or "50")
			widthStr := strings.TrimSuffix(value, "%")
			widthStr = strings.TrimSpace(widthStr)
			width, err := strconv.Atoi(widthStr)
			if err != nil {
				return nil, fmt.Errorf("invalid width value %q: %w", value, err)
			}
			if width < 1 || width > 100 {
				return nil, fmt.Errorf("width %d out of range (must be 1-100)", width)
			}
			docMap.Width = width

		case "extent":
			// Parse two space-separated points
			points := strings.Fields(value)
			if len(points) != 2 {
				return nil, fmt.Errorf("extent must have exactly 2 points separated by space, got %d", len(points))
			}
			pt1, err := parseLocation(points[0])
			if err != nil {
				return nil, fmt.Errorf("invalid extent first point %q: %w", points[0], err)
			}
			pt2, err := parseLocation(points[1])
			if err != nil {
				return nil, fmt.Errorf("invalid extent second point %q: %w", points[1], err)
			}
			docMap.Extent = [2]math.Point2LL{pt1, pt2}

		case "line", "arrow", "polygon":
			flushAnnotation()
			// Start a new LINE / ARROW / POLYGON annotation
			var annotationType AnnotationType
			switch key {
			case "line":
				annotationType = AnnotationLine
			case "arrow":
				annotationType = AnnotationArrow
			case "polygon":
				annotationType = AnnotationPolygon
			}
			currentAnnotation = &MapAnnotation{Type: annotationType}
			// Parse coordinates with optional climb/descent annotations
			// Format: "lat,lon/d16000 lat,lon PUCKY/c18000"
			// strings.Fields tolerates runs of whitespace so a brief author's
			// alignment spaces don't break the videomap.
			for _, pstr := range strings.Fields(value) {
				// Split coordinate from annotation
				coordStr, annotationStr, _ := strings.Cut(pstr, "/")

				// Parse the coordinate (supports lat/long, fix, or airport)
				p, err := parseLocation(coordStr)
				if err != nil {
					return nil, err
				}

				// Parse annotation if present
				var annotationPtr *WaypointAnnotation
				if annotationStr != "" {
					annotation, err := parseWaypointAnnotation(annotationStr)
					if err != nil {
						return nil, fmt.Errorf("invalid annotation in %q: %w", pstr, err)
					}
					annotationPtr = &annotation
				}

				// Append as AnnotatedVertex
				currentAnnotation.Vertices = append(currentAnnotation.Vertices, AnnotatedVertex{
					Point:      p,
					Annotation: annotationPtr,
				})
			}

		case "fix":
			flushAnnotation()
			// Start a new FIX annotation
			currentAnnotation = &MapAnnotation{Type: AnnotationFix}
			// Parse and resolve the fix/airport location
			p, err := parseLocation(value)
			if err != nil {
				return nil, fmt.Errorf("fix: %w", err)
			}
			// Store as a single vertex
			currentAnnotation.Vertices = []AnnotatedVertex{{Point: p}}

		case "point":
			flushAnnotation()
			// Start a new POINT annotation
			currentAnnotation = &MapAnnotation{Type: AnnotationPoint}
			// Parse the point location (supports lat/long, fix, or airport)
			p, err := parseLocation(value)
			if err != nil {
				return nil, fmt.Errorf("point: %w", err)
			}
			// Store as a single vertex
			currentAnnotation.Vertices = []AnnotatedVertex{{Point: p}}

		case "airspace":
			flushAnnotation()
			// Append an AIRSPACE annotation directly: labels aren't supported,
			// so there's no reason to keep it open as currentAnnotation. Empty
			// value = user's consolidated positions; otherwise the value names
			// a single TCP.
			ann := MapAnnotation{Type: AnnotationAirspace}
			if value != "" {
				ann.AirspaceTCPs = []string{value}
			}
			docMap.Annotations = append(docMap.Annotations, ann)

		case "label":
			if currentAnnotation == nil {
				return nil, fmt.Errorf("label: with no preceding annotation")
			}
			currentAnnotation.Label = append(currentAnnotation.Label, value)

		default:
			return nil, fmt.Errorf("unknown videomap key %q", key)
		}
	}

	// Don't forget the last annotation
	flushAnnotation()

	return docMap, nil
}

// docMapTransformer transforms code blocks with language "videomap" into DocMapNode.
type docMapTransformer struct {
	videoMapFiles *[]string
}

// Transform implements parser.ASTTransformer.
func (t *docMapTransformer) Transform(node *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()

	// Two-pass approach: collect nodes first, then transform
	// This is necessary because goldmark's ast.Walk uses NextSibling() for iteration.
	// When ReplaceChild is called, it sets the replaced node's NextSibling to nil,
	// causing the walk to terminate prematurely and skip remaining siblings.
	type videomapBlock struct {
		codeBlock *ast.FencedCodeBlock
		title     string
		lineNum   int
	}
	var blocksToTransform []videomapBlock

	// First pass: collect all videomap code blocks without modifying the AST
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		codeBlock, ok := n.(*ast.FencedCodeBlock)
		if !ok || codeBlock.Info == nil {
			return ast.WalkContinue, nil
		}

		// Check if language is "videomap"
		language := string(codeBlock.Language(source))
		if language != "videomap" {
			return ast.WalkContinue, nil
		}

		// Get line number for error reporting
		var lineNum int
		if codeBlock.Lines().Len() > 0 {
			lineNum = getLineNumber(source, codeBlock.Lines().At(0).Start)
		}

		// Extract the title from the info string (everything after "videomap")
		infoBytes := codeBlock.Info.Value(source)
		info := string(infoBytes)
		title := ""
		if len(info) > len("videomap") {
			title = strings.TrimSpace(info[len("videomap"):])
		}

		blocksToTransform = append(blocksToTransform, videomapBlock{
			codeBlock: codeBlock,
			title:     title,
			lineNum:   lineNum,
		})

		return ast.WalkContinue, nil
	})

	// Second pass: transform all collected blocks
	for _, block := range blocksToTransform {
		// Parse the code block content as key:value format
		content := block.codeBlock.Lines().Value(source)

		errPrefix := fmt.Sprintf("line %d: videomap block %q: ", block.lineNum, block.title)

		docMap, err := parseVideoMapContent(string(content))
		if err != nil {
			// Leave the FencedCodeBlock in place so the broken source still
			// renders; flag the failure inline after it.
			insertErrorsAfter(block.codeBlock, errPrefix+err.Error())
			continue
		}

		if file := docMap.File; file != "" && !slices.Contains(*t.videoMapFiles, file) {
			*t.videoMapFiles = append(*t.videoMapFiles, file)
		}

		// Replace the code block with the DocMapNode
		parent := block.codeBlock.Parent()
		if parent == nil {
			continue
		}
		docMapNode := &DocMapNode{DocMap: docMap, Label: block.title}
		docMapNode.SetBlankPreviousLines(block.codeBlock.HasBlankPreviousLines())
		parent.ReplaceChild(parent, block.codeBlock, docMapNode)

		// Validation errors are reported next to the rendered map.
		validateErrs := docMap.Validate()
		msgs := make([]string, len(validateErrs))
		for i, verr := range validateErrs {
			msgs[i] = errPrefix + verr
		}
		insertErrorsAfter(docMapNode, msgs...)
	}
}

// DocMapNode is a custom AST node representing a VideoMapBlock.
// It's a block-level node that replaces inline link syntax.
type DocMapNode struct {
	ast.BaseBlock
	DocMap *VideoMapBlock
	Label  string
}

func (n *DocMapNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *DocMapNode) Kind() ast.NodeKind {
	return kindDocMapNode
}

var kindDocMapNode = ast.NewNodeKind("DocMapNode")

///////////////////////////////////////////////////////////////////////////
// Table caption support

// TableCaption represents a caption for a table.
type TableCaption struct {
	ast.BaseBlock
	Caption string
}

func (n *TableCaption) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *TableCaption) Kind() ast.NodeKind {
	return kindTableCaption
}

var kindTableCaption = ast.NewNodeKind("TableCaption")

///////////////////////////////////////////////////////////////////////////
// Inline parse-error node

// ErrorNode carries a parse-time error message that is inserted into the AST
// at the point of discovery so it can be rendered inline next to the offending
// block instead of in a separate top-of-document list.
type ErrorNode struct {
	ast.BaseBlock
	Message string
}

func (n *ErrorNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *ErrorNode) Kind() ast.NodeKind {
	return kindErrorNode
}

var kindErrorNode = ast.NewNodeKind("ErrorNode")

// insertErrorsAfter inserts one ErrorNode per message as successive siblings
// after anchor, preserving message order. Returns the last node inserted (or
// anchor if msgs is empty), suitable for chaining further insertions.
func insertErrorsAfter(anchor ast.Node, msgs ...string) ast.Node {
	parent := anchor.Parent()
	if parent == nil {
		return anchor
	}
	for _, m := range msgs {
		errNode := &ErrorNode{Message: m}
		parent.InsertAfter(parent, anchor, errNode)
		anchor = errNode
	}
	return anchor
}

// tableCaptionTransformer finds paragraphs starting with ": " that immediately
// follow tables and converts them to TableCaption nodes.
type tableCaptionTransformer struct{}

// Transform implements parser.ASTTransformer.
func (t *tableCaptionTransformer) Transform(node *ast.Document, reader text.Reader, pc parser.Context) {
	// Two-pass approach: collect nodes first, then transform
	// This is necessary because goldmark's ast.Walk uses NextSibling() for iteration.
	// When ReplaceChild is called, it sets the replaced node's NextSibling to nil,
	// causing the walk to terminate prematurely and skip remaining siblings.
	type captionToTransform struct {
		para        *ast.Paragraph
		captionText string
	}
	var captionsToTransform []captionToTransform

	// First pass: collect all caption paragraphs without modifying the AST
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		// Look for table nodes
		if table, ok := n.(*extast.Table); ok {
			// Check if the next sibling is a paragraph
			nextSibling := table.NextSibling()
			if para, ok := nextSibling.(*ast.Paragraph); ok {
				// Extract paragraph text
				var text strings.Builder
				for child := para.FirstChild(); child != nil; child = child.NextSibling() {
					if textNode, ok := child.(*ast.Text); ok {
						text.Write(textNode.Segment.Value(reader.Source()))
					}
				}

				if captionText, ok := strings.CutPrefix(text.String(), ": "); ok {
					captionsToTransform = append(captionsToTransform, captionToTransform{
						para:        para,
						captionText: strings.TrimSpace(captionText),
					})
				}
			}
		}

		return ast.WalkContinue, nil
	})

	// Second pass: transform all collected captions
	for _, ct := range captionsToTransform {
		parent := ct.para.Parent()
		parent.ReplaceChild(parent, ct.para, &TableCaption{Caption: ct.captionText})
	}
}

///////////////////////////////////////////////////////////////////////////
// Configuration transformer

// configurationTransformer handles parsing of configuration blocks and sections.
type configurationTransformer struct{}

// parseConfigOptions parses configuration options from content in "name: label" format.
// Returns the parsed options and any errors encountered.
func parseConfigOptions(content string) ([]ConfigOption, []string) {
	var options []ConfigOption
	var errors []string
	seen := make(map[string]struct{})

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			errors = append(errors, fmt.Sprintf("invalid configuration format %q, expected 'name: label'", line))
			continue
		}

		name := strings.TrimSpace(parts[0])
		label := strings.TrimSpace(parts[1])

		if name == "" {
			errors = append(errors, fmt.Sprintf("configuration has empty name: %q", line))
			continue
		}
		if label == "" {
			errors = append(errors, fmt.Sprintf("configuration has empty label: %q", line))
			continue
		}
		if strings.Contains(name, ",") {
			errors = append(errors, fmt.Sprintf("configuration name cannot contain commas: %q", name))
			continue
		}
		// ':' would break the ::: if matching downstream.
		if strings.Contains(name, ":") {
			errors = append(errors, fmt.Sprintf("configuration name cannot contain colons: %q", name))
			continue
		}
		if _, ok := seen[name]; ok {
			errors = append(errors, fmt.Sprintf("duplicate configuration name %q", name))
			continue
		}
		seen[name] = struct{}{}

		options = append(options, ConfigOption{Name: name, Label: label})
	}

	return options, errors
}

func isValidConfigConditionName(name string, definedConfigs map[string]bool) bool {
	if definedConfigs[name] {
		return true
	}

	// Airport ICAO codes. These are evaluated dynamically against the scenario.
	if _, ok := av.DB.LookupAirport(name); ok {
		return true
	}

	// TCP patterns. These are evaluated dynamically against the user's TCP.
	if tcp, ok := strings.CutPrefix(name, "tcp:"); ok {
		if len(tcp) >= 1 && len(tcp) <= 3 {
			return !strings.ContainsFunc(tcp, func(r rune) bool {
				return !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '*')
			})
		}
	}

	return false
}

// validateConfigConditionSyntax checks raw markdown for malformed ::: if/endif blocks
// and references to unknown configuration names. Returns a list of error messages.
func validateConfigConditionSyntax(markdown string) []string {
	var errs []string
	lines := strings.Split(markdown, "\n")
	definedConfigs := make(map[string]bool)

	var inFence bool
	var fenceInfo string
	var currentConfigGroup string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				inFence = true
				fenceInfo = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				currentConfigGroup = ""
				if rest, ok := strings.CutPrefix(fenceInfo, "configurations"); ok {
					currentConfigGroup = strings.TrimSpace(rest)
				}
			} else {
				inFence = false
				fenceInfo = ""
				currentConfigGroup = ""
			}
			continue
		}

		if inFence && currentConfigGroup != "" {
			if trimmed == "" {
				continue
			}
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				name := strings.TrimSpace(parts[0])
				if name != "" {
					definedName := name
					if currentConfigGroup != "" {
						definedName = currentConfigGroup + ":" + name
					}
					definedConfigs[definedName] = true
				}
			}
			continue
		}
	}

	type conditionFrame struct {
		condition string
		lineNum   int
	}
	var stack []conditionFrame

	for lineIdx, line := range lines {
		trimmed := strings.TrimSpace(line)

		if cond, ok := strings.CutPrefix(trimmed, "::: if"); ok {
			cond = strings.TrimSpace(cond)
			if cond == "" {
				errs = append(errs, fmt.Sprintf("line %d: empty ::: if condition", lineIdx+1))
				stack = append(stack, conditionFrame{lineNum: lineIdx + 1})
				continue
			}

			configNames := strings.Split(cond, ",")
			validNames := 0
			for _, name := range configNames {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				validNames++
				if !isValidConfigConditionName(name, definedConfigs) {
					errs = append(errs, fmt.Sprintf("line %d: config section references undefined configuration %q", lineIdx+1, name))
				}
			}
			if validNames == 0 {
				errs = append(errs, fmt.Sprintf("line %d: empty ::: if condition", lineIdx+1))
			}
			stack = append(stack, conditionFrame{condition: cond, lineNum: lineIdx + 1})
		} else if cond, ok := strings.CutPrefix(trimmed, "::: endif"); ok {
			cond := strings.TrimSpace(cond)
			if len(stack) == 0 {
				errs = append(errs, fmt.Sprintf("line %d: unexpected endif %q with no matching if", lineIdx+1, cond))
				continue
			}

			top := stack[len(stack)-1]
			if top.condition != cond {
				// Mismatched endif: leave the if on the stack so subsequent
				// endifs aren't silently mismatched too.
				errs = append(errs, fmt.Sprintf("line %d: mismatched endif: expected %q, got %q", lineIdx+1, top.condition, cond))
				continue
			}
			stack = stack[:len(stack)-1]
		} else if strings.HasPrefix(trimmed, ":::") {
			errs = append(errs, fmt.Sprintf(`line %d: unknown directive %q (expected "::: if" or "::: endif")`, lineIdx+1, trimmed))
		}
	}

	for _, frame := range stack {
		errs = append(errs, fmt.Sprintf("line %d: unclosed config block: %q", frame.lineNum, frame.condition))
	}
	return errs
}

// Transform implements parser.ASTTransformer.
func (t *configurationTransformer) Transform(node *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	// Two-pass approach for configuration blocks
	// This is necessary because goldmark's ast.Walk uses NextSibling() for iteration.
	// When ReplaceChild is called, it sets the replaced node's NextSibling to nil,
	// causing the walk to terminate prematurely and skip remaining siblings.
	type configBlockToTransform struct {
		codeBlock *ast.FencedCodeBlock
		name      string
		lineNum   int
	}
	var configBlocks []configBlockToTransform

	// First walk, first pass: collect every configurations code block (including
	// malformed ones with an invalid or missing group name) without modifying
	// the AST. Name-validity checks happen in the second pass, where it is safe
	// to insert ErrorNodes adjacent to the offending block.
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		codeBlock, ok := n.(*ast.FencedCodeBlock)
		if !ok || codeBlock.Info == nil {
			return ast.WalkContinue, nil
		}

		// Check if this is a configurations block
		info := string(codeBlock.Info.Value(source))
		configName, ok := strings.CutPrefix(info, "configurations")
		if !ok {
			return ast.WalkContinue, nil
		}
		configName = strings.TrimSpace(configName)

		// Get line number for error reporting
		var lineNum int
		if codeBlock.Lines().Len() > 0 {
			lineNum = getLineNumber(source, codeBlock.Lines().At(0).Start)
		}

		configBlocks = append(configBlocks, configBlockToTransform{
			codeBlock: codeBlock,
			name:      configName,
			lineNum:   lineNum,
		})

		return ast.WalkContinue, nil
	})

	// Second pass: validate + transform each block, attaching ErrorNodes inline.
	seenNames := make(map[string]int) // name -> line of first definition
	for _, block := range configBlocks {
		// Validate that "tcp" is not used as a config group name.
		if block.name == "tcp" {
			insertErrorsAfter(block.codeBlock,
				fmt.Sprintf("line %d: configuration group name \"tcp\" is reserved for TCP-based config blocks", block.lineNum))
			continue
		}

		// Require a non-empty group identifier. Without one the block's option
		// names are not prefixed (e.g. `::: if 13` instead of `::: if dep:13`),
		// and validateConfigConditionSyntax only collects defined names from
		// named blocks — leaving any `::: if` referencing an unnamed block's
		// option to fail as "undefined."
		if block.name == "" {
			insertErrorsAfter(block.codeBlock,
				fmt.Sprintf("line %d: configurations block requires a group name (e.g. ```configurations:dep)", block.lineNum))
			continue
		}

		content := block.codeBlock.Lines().Value(source)
		options, errs := parseConfigOptions(string(content))

		optionErrMsgs := make([]string, len(errs))
		for i, err := range errs {
			optionErrMsgs[i] = fmt.Sprintf("line %d: %s", block.lineNum, err)
		}

		if len(options) == 0 {
			emptyMsg := fmt.Sprintf("line %d: configurations block is empty or has no valid options", block.lineNum)
			insertErrorsAfter(block.codeBlock, append(optionErrMsgs, emptyMsg)...)
			continue
		}

		if firstLine, dup := seenNames[block.name]; dup {
			dupMsg := fmt.Sprintf("line %d: duplicate configurations block %q (first defined at line %d)", block.lineNum, block.name, firstLine)
			insertErrorsAfter(block.codeBlock, append(optionErrMsgs, dupMsg)...)
			continue
		}
		seenNames[block.name] = block.lineNum

		configNode := NewConfigurationsNode(block.name, options)
		parent := block.codeBlock.Parent()
		if parent == nil {
			continue
		}
		configNode.SetBlankPreviousLines(block.codeBlock.HasBlankPreviousLines())
		parent.ReplaceChild(parent, block.codeBlock, configNode)

		// Per-option parse errors are reported next to the rendered radio
		// buttons (some options survived if we got here).
		insertErrorsAfter(configNode, optionErrMsgs...)
	}
}

// GetInitialConfigSelections parses the brief to discover its configurations
// blocks and returns a map from each group name to its default (first) option
// name.
func GetInitialConfigSelections(markdown string) map[string]string {
	if markdown == "" {
		return nil
	}

	parsed := ParseMarkdown([]byte(markdown), ParseOptions{})
	if parsed == nil {
		return nil
	}

	configs := make(map[string]string)
	_ = ast.Walk(parsed.AST(), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if configNode, ok := n.(*ConfigurationsNode); ok {
				if len(configNode.Options) > 0 {
					configs[configNode.Name] = configNode.Options[0].Name
				}
			}
		}
		return ast.WalkContinue, nil
	})

	return configs
}

///////////////////////////////////////////////////////////////////////////
// Text preprocessing for config selection

// MatchesTCPPattern reports whether a controller TCP matches a pattern.
// Patterns may end in '*' for a prefix wildcard ("5*" matches "5A", "5B",
// etc.); otherwise the pattern matches the TCP exactly. Exported so callers
// can implement ParseOptions.MatchesUserTCP with consistent semantics.
func MatchesTCPPattern(tcp, pattern string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(tcp, prefix)
	}
	return tcp == pattern
}

// ProcessConfigSelections preprocesses markdown by removing the contents of
// `::: if NAME[,NAME...]` blocks whose condition does not match. Match logic
// (OR across the comma-separated names):
//   - NAME is `group:tag` and `selectedConfig(group) == tag`
//   - NAME is an ICAO code for which `isActiveAirport(NAME)` returns true
//   - NAME is `tcp:PATTERN` and `matchesUserTCP(PATTERN)` returns true
//
// Any callback may be nil; nil treats that branch as a non-match. When all
// callbacks are nil, the input is returned unchanged.
func ProcessConfigSelections(markdown string, selectedConfig func(group string) string,
	isActiveAirport func(string) bool, matchesUserTCP func(string) bool) string {
	if selectedConfig == nil && isActiveAirport == nil && matchesUserTCP == nil {
		return markdown
	}

	lines := strings.Split(markdown, "\n")
	var result []string

	// Config blocks are hierarchical: if an outer block's condition fails,
	// everything inside is skipped, including any nested config blocks.
	// skipDepth tracks how many levels deep we are inside failing blocks; a
	// value of 0 means emission is enabled.
	skipDepth := 0

	// Stack of condition strings from ::: if lines, for validating matching ::: endif
	var condStack []string

	for lineIdx, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for config section start: ::: if NAME or ::: if NAME1,NAME2,...
		if trimmed == "::: if" || strings.HasPrefix(trimmed, "::: if ") {
			condText := strings.TrimSpace(strings.TrimPrefix(trimmed, "::: if"))

			condStack = append(condStack, condText)

			if skipDepth > 0 {
				// Already inside a failing block; skip this nested block too
				skipDepth++
				continue
			}

			matches := func(name string) bool {
				if name = strings.TrimSpace(name); name == "" {
					return false
				}
				if pattern, ok := strings.CutPrefix(name, "tcp:"); ok {
					return matchesUserTCP != nil && matchesUserTCP(pattern)
				}
				if group, tag, ok := strings.Cut(name, ":"); ok {
					return selectedConfig != nil && selectedConfig(group) == tag
				}
				return isActiveAirport != nil && isActiveAirport(name)
			}
			if !util.SeqContainsFunc(strings.SplitSeq(condText, ","), matches) {
				skipDepth = 1
			}
			// Don't emit the ::: if line itself
		} else if trimmed == "::: endif" || strings.HasPrefix(trimmed, "::: endif ") {
			endifCond := strings.TrimSpace(strings.TrimPrefix(trimmed, "::: endif"))

			// Validate that the endif matches the innermost open if. On a
			// mismatch, leave the if on the stack and don't change skip/active
			// depth, so subsequent endifs match what the user wrote without
			// cascading errors from a silent pop.
			if len(condStack) == 0 {
				result = append(result, fmt.Sprintf("**ERROR: line %d: unexpected endif %q with no matching if**", lineIdx+1, endifCond))
			} else if top := condStack[len(condStack)-1]; top != endifCond {
				result = append(result, fmt.Sprintf("**ERROR: line %d: mismatched endif: expected %q, got %q**", lineIdx+1, top, endifCond))
			} else {
				condStack = condStack[:len(condStack)-1]
				if skipDepth > 0 {
					skipDepth--
				}
			}
			// Don't emit the ::: endif line itself
		} else if strings.HasPrefix(trimmed, ":::") {
			result = append(result, fmt.Sprintf(`**ERROR: line %d: unknown directive %q (expected "::: if" or "::: endif")**`, lineIdx+1, trimmed))
		} else if skipDepth == 0 {
			result = append(result, line)
		}
	}

	for _, cond := range condStack {
		result = append(result, fmt.Sprintf("**ERROR: unclosed config block: %q**", cond))
	}

	return strings.Join(result, "\n")
}

///////////////////////////////////////////////////////////////////////////
// Configuration nodes

// ConfigOption represents a single configuration option with internal name and display label.
type ConfigOption struct {
	Name  string // Option tag (the part after "group:" in `::: if group:tag`)
	Label string // Display label shown in UI
}

// ConfigurationsNode represents a list of configuration options.
// Rendered as radio buttons. Names must be unique within a brief.
type ConfigurationsNode struct {
	ast.BaseBlock
	Name    string         // Name identifier for this configuration block
	Options []ConfigOption // Configuration options
}

// NewConfigurationsNode creates a new ConfigurationsNode.
func NewConfigurationsNode(name string, options []ConfigOption) *ConfigurationsNode {
	return &ConfigurationsNode{
		Name:    name,
		Options: options,
	}
}

func (n *ConfigurationsNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func (n *ConfigurationsNode) Kind() ast.NodeKind {
	return kindConfigurationsNode
}

var kindConfigurationsNode = ast.NewNodeKind("ConfigurationsNode")

///////////////////////////////////////////////////////////////////////////
// Cached Parsed Markdown

// ParsedMarkdown holds a parsed markdown AST and any errors collected
// during parsing and validation.
type ParsedMarkdown struct {
	source        []byte
	ast           ast.Node
	parseErrors   []string
	videoMapFiles []string
}

// ParseOptions configures the validation and filtering passes ParseMarkdown
// runs. Every field is optional; a nil callback disables the corresponding
// check or match. Selection state, airport activity, and TCP coverage are all
// supplied as caller-defined callbacks so the brief package never needs to
// know how the client tracks them.
type ParseOptions struct {
	// SelectedConfig returns the currently-selected option tag for the given
	// configurations group (e.g. "13" for group "dep"). An empty return value
	// means no option is selected, which never matches a `::: if dep:tag`
	// condition. nil treats no group as selected.
	SelectedConfig func(group string) string

	// IsActiveAirport reports whether the given ICAO is an airport with
	// non-VFR traffic in the current scenario. Used to match `::: if KJFK`
	// blocks. nil treats no airports as active.
	IsActiveAirport func(icao string) bool

	// MatchesUserTCP reports whether any TCP the user covers matches the
	// supplied pattern (e.g. "1*", "2A"). Used to match `::: if tcp:1*`
	// blocks. nil treats no patterns as matching.
	MatchesUserTCP func(tcpPattern string) bool
}

// ParseMarkdown parses markdown content, runs the line-based ::: if/endif
// preprocessor with the supplied selection state, and parses the result with
// goldmark. Block-scoped errors are embedded as *ErrorNode siblings of the
// offending block in the returned AST so they can be rendered inline.
// Preprocessor-level errors (those produced before goldmark sees the source,
// where no AST node exists yet) are returned by ParseErrors().
func ParseMarkdown(content []byte, o ParseOptions) *ParsedMarkdown {
	var parseErrors []string

	// Config condition syntax (independent of goldmark). Validate on the raw
	// content before preprocessing strips lines.
	parseErrors = append(parseErrors, validateConfigConditionSyntax(string(content))...)

	// Preprocess based on selections.
	content = []byte(ProcessConfigSelections(string(content), o.SelectedConfig, o.IsActiveAirport, o.MatchesUserTCP))

	ext := NewDocMapExtension()
	md := goldmark.New(goldmark.WithExtensions(ext, extension.Table))
	parsed := md.Parser().Parse(text.NewReader(content))

	// Walk the parsed AST to flag any fenced code blocks that aren't `videomap`
	// or `configurations`. Successful matches were already replaced by custom
	// AST nodes upstream; an offending FencedCodeBlock here gets an ErrorNode
	// inserted next to it so the user sees the warning inline.
	type unknownBlock struct {
		cb       *ast.FencedCodeBlock
		language string
		line     int
	}
	var unknownBlocks []unknownBlock
	_ = ast.Walk(parsed, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		cb, ok := n.(*ast.FencedCodeBlock)
		if !ok {
			return ast.WalkContinue, nil
		}
		language := string(cb.Language(content))
		if language == "videomap" || language == "configurations" {
			return ast.WalkContinue, nil
		}
		var line int
		if cb.Lines().Len() > 0 {
			line = getLineNumber(content, cb.Lines().At(0).Start)
		}
		unknownBlocks = append(unknownBlocks, unknownBlock{cb: cb, language: language, line: line})
		return ast.WalkContinue, nil
	})
	for _, ub := range unknownBlocks {
		insertErrorsAfter(ub.cb,
			fmt.Sprintf(`line %d: unknown fenced code block %q (expected "videomap" or "configurations")`,
				ub.line, ub.language))
	}

	return &ParsedMarkdown{
		source:        content,
		ast:           parsed,
		parseErrors:   parseErrors,
		videoMapFiles: ext.videoMapFiles,
	}
}

// Source returns the (possibly preprocessed) markdown content the AST was
// built from.
func (p *ParsedMarkdown) Source() []byte {
	return p.source
}

// AST returns the parsed AST.
func (p *ParsedMarkdown) AST() ast.Node {
	return p.ast
}

// ParseErrors returns preprocessor-level errors that have no AST representation
// (malformed ::: if/endif structure, undefined config references, etc.).
// Block-scoped errors are embedded in the AST as *ErrorNode siblings of the
// offending block; walk the AST to surface those.
func (p *ParsedMarkdown) ParseErrors() []string {
	return p.parseErrors
}

// VideoMapFiles returns the distinct video map file paths referenced by
// videomap blocks in the (selection-filtered) brief, in source order.
func (p *ParsedMarkdown) VideoMapFiles() []string {
	return p.videoMapFiles
}
