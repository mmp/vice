// cmd/vice/brief.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/brief"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

// drawScenarioBriefSection draws the scenario brief as a collapsing header section inside the
// scenario info window.
func drawScenarioBriefSection(mgr *client.ConnectionManager, config *Config, c *client.ControlClient,
	p platform.Platform, lg *log.Logger) {
	if c.State.ScenarioBrief == "" {
		return
	}

	if !imgui.CollapsingHeaderBoolPtr(c.State.Facility+" Procedures", nil) {
		return
	}

	imgui.Indent()

	// Check for Control-R to reload the brief.
	if imgui.IsKeyPressedBool(imgui.KeyR) && imgui.CurrentIO().KeyCtrl() {
		if err := c.ReloadScenarioBrief(); err != nil {
			lg.Errorf("Failed to reload scenario brief: %v", err)
		} else {
			// Force regeneration below
			ui.brief.markdown = ""
		}
	}

	// Parse and cache markdown if needed.
	if ui.brief.markdown != c.State.ScenarioBrief {
		ui.brief.markdown = c.State.ScenarioBrief
		ui.brief.selectedConfigs = brief.GetInitialConfigSelections(c.State.ScenarioBrief)
		ui.brief.closedHeadings = make(map[string]struct{})
		ui.brief.disabledAirspaceTCPs = make(map[string]map[string]bool)

		// Clear caches
		ui.brief.parsed = nil
		ui.brief.inlineTokensCache = nil
		if ui.brief.videoMapCache == nil {
			ui.brief.videoMapCache = newVideoMapCache()
		}
	}

	// Render the markdown using cached brief and selections
	if ui.brief.markdown != "" {
		renderMarkdown(ui.brief.markdown, ui.brief.selectedConfigs, ui.brief.closedHeadings,
			p, config.UIFontSize, ui.brief.videoMapCache, &c.State, c, lg)
	}

	imgui.Unindent()
}

// Constants for map drawing
const (
	mapPaddingFactor     = float32(0.1)  // Expand map bounds by 10%
	maxMapAspectRatio    = float32(1.8)  // Cap rendered map aspect ratio (the longer side ≤ this × the shorter)
	minMapExtentNm       = float32(2.0)  // Minimum width/height in NM when fitting to content (handles single-point annotations)
	arrowheadSize        = float32(10.0) // Arrow wing length in pixels
	arrowheadAngle       = float32(0.4)  // Arrow wing angle in radians
	fixCircleRadius      = float32(5.0)  // Fix marker radius in pixels
	waypointCircleRadius = float32(4.0)  // Waypoint marker radius in pixels
	labelOffsetPixels    = float32(8.0)  // Label offset from shape in pixels
	fixLabelOffset       = float32(15.0) // Fix label offset in pixels
)

// Color constants for map drawing
var (
	videoMapColor   = imgui.Vec4{X: 0.3, Y: 0.3, Z: 0.4, W: 1.0} // Dark blue/gray for map lines
	annotationColor = imgui.Vec4{X: 0.0, Y: 0.6, Z: 0.0, W: 1.0} // Mid-green for annotations
	mapTextColor    = imgui.Vec4{X: 1.0, Y: 1.0, Z: 1.0, W: 1.0} // White for text
	mapBgColor      = imgui.Vec4{X: 0.1, Y: 0.1, Z: 0.1, W: 1.0} // Dark gray background
	errorTextColor  = imgui.Vec4{X: 1.0, Y: 0.3, Z: 0.3, W: 1.0} // Red for errors
)

// calculateColumnWidths scans a table and returns the maximum text width for each column
func calculateColumnWidths(tableNode *extast.Table, source []byte) []float32 {
	// First, count columns from first row
	numCols := 0
	if firstChild := tableNode.FirstChild(); firstChild != nil {
		if header, ok := firstChild.(*extast.TableHeader); ok {
			for c := header.FirstChild(); c != nil; c = c.NextSibling() {
				numCols++
			}
		} else if row, ok := firstChild.(*extast.TableRow); ok {
			for c := row.FirstChild(); c != nil; c = c.NextSibling() {
				numCols++
			}
		}
	}

	if numCols == 0 {
		return nil
	}

	columnWidths := make([]float32, numCols)

	// Walk all rows and calculate max width for each column
	for child := tableNode.FirstChild(); child != nil; child = child.NextSibling() {
		var cells []ast.Node
		if header, ok := child.(*extast.TableHeader); ok {
			for c := header.FirstChild(); c != nil; c = c.NextSibling() {
				cells = append(cells, c)
			}
		} else if row, ok := child.(*extast.TableRow); ok {
			for c := row.FirstChild(); c != nil; c = c.NextSibling() {
				cells = append(cells, c)
			}
		}

		for colIdx, cell := range cells {
			if colIdx >= numCols {
				break
			}
			text := extractText(cell, source)
			textWidth := imgui.CalcTextSize(text).X
			columnWidths[colIdx] = max(columnWidths[colIdx], textWidth)
		}
	}

	// Add padding to each column
	for i := range columnWidths {
		columnWidths[i] += 10 // padding on each side
	}

	return columnWidths
}

const briefDrawWidth = 800

// renderMarkdown renders the given markdown using imgui.
func renderMarkdown(source string, selectedConfigs map[string]string, closedHeadings map[string]struct{},
	p platform.Platform, baseFontSize int, mapCache *videoMapCache, state *client.SimState,
	controlClient *client.ControlClient, lg *log.Logger) {
	if state == nil {
		return
	}

	// Re-parse the brief only on explicit triggers (new content, Ctrl-R, a radio-button click) or
	// ~1/second when a fresh state update advances GenerationIndex.
	if ui.brief.parsed == nil || state.GenerationIndex > ui.brief.parseGen {
		ui.brief.parsed = brief.ParseMarkdown([]byte(source), brief.ParseOptions{
			SelectedConfig: func(group string) string { return selectedConfigs[group] },
			IsActiveAirport: func(icao string) bool {
				return slices.ContainsFunc(state.DepartureRunways,
					func(rwy sim.DepartureRunway) bool { return rwy.Airport == icao }) ||
					slices.ContainsFunc(state.ArrivalRunways,
						func(rwy sim.ArrivalRunway) bool { return rwy.Airport == icao })
			},
			MatchesUserTCP: func(pattern string) bool {
				tcps := state.GetPositionsForTCW(state.UserTCW)
				return slices.ContainsFunc(tcps, func(tcp sim.ControlPosition) bool {
					return brief.MatchesTCPPattern(string(tcp), pattern)
				})
			},
		})
		ui.brief.parseGen = state.GenerationIndex

		// inlineTokensCache is keyed by ast.Node pointers from the parse just replaced; drop it so
		// we don't return tokens for stale nodes.
		ui.brief.inlineTokensCache = nil
	}
	if ui.brief.parsed == nil {
		return
	}

	// Display preprocessor-level errors at the top. Block-scoped errors are embedded in the AST as
	// *brief.ErrorNode and render inline.
	if errs := ui.brief.parsed.ParseErrors(); len(errs) > 0 {
		imgui.PushStyleColorVec4(imgui.ColText, errorTextColor)
		imgui.Text("Brief preprocessor errors:")
		for _, err := range errs {
			imgui.BulletText(err)
		}
		imgui.PopStyleColor()
		imgui.Spacing()
		imgui.Separator()
		imgui.Spacing()
	}

	// Use preprocessed markdown for rendering
	preprocessedSource := ui.brief.parsed.Source()
	preprocessedNode := ui.brief.parsed.AST()

	dpiScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	width := dpiScale * briefDrawWidth

	// Fonts used by renderInline for styled inline content (paragraphs and list items). The regular
	// variant matches what imgui.Text would use; the others let *italic*, **bold**, ***bold
	// italic***, and `code` render with actual style switching instead of literal markup.
	inlineFonts := briefInlineFonts{
		regular:    renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: baseFontSize}),
		italic:     renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoItalic, Size: baseFontSize}),
		bold:       renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoBold, Size: baseFontSize}),
		boldItalic: renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoBoldItalic, Size: baseFontSize}),
		mono:       renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoMono, Size: baseFontSize}),
	}

	// Track which heading levels are currently collapsed
	var skippingUntilLevel int

	// Track which tables have been successfully started with BeginTable
	tablesStarted := make(map[ast.Node]bool)
	drewTableHeader := make(map[ast.Node]bool)
	tableColumnWidths := make(map[ast.Node][]float32)
	tableHeaderColumnIndex := make(map[ast.Node]int)
	tableWraps := make(map[ast.Node]bool) // Table is in stretch+wrap mode
	lastTableTotalWidth := float32(0)     // Track last table width for caption centering

	// Track context during walk
	var listDepth int
	var inListItem bool
	var level2Indented bool

	// headingIDStack tracks the levels of headings for which we've pushed
	// imgui IDs. This creates a hierarchical ID context so that widgets
	// under identically-named subsections (e.g. "## LGA Area" under two
	// different top-level sections) get unique imgui IDs.
	var headingIDStack []int
	var headingTextStack []string

	imgui.PushIDStr("##brief")
	defer imgui.PopID()
	defer func() {
		// Ensure we unindent at the end if still indented
		if level2Indented {
			imgui.Unindent()
		}
		// Pop any remaining heading IDs
		for range headingIDStack {
			imgui.PopID()
		}
	}()

	// Walk the preprocessed AST and render each node
	_ = ast.Walk(preprocessedNode, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		// If we're skipping content, check if this is a heading that ends the skip
		if skippingUntilLevel > 0 {
			if entering {
				if heading, ok := n.(*ast.Heading); ok && heading.Level <= skippingUntilLevel {
					// Found a heading at same or higher level - stop skipping
					skippingUntilLevel = 0
				} else {
					// Still skipping
					return ast.WalkSkipChildren, nil
				}
			} else {
				// Also skip on leaving to maintain balanced push/pop
				return ast.WalkContinue, nil
			}
		}

		switch n := n.(type) {
		case *ast.Document:
			// Nothing to do for document node
			return ast.WalkContinue, nil

		case *ast.Heading:
			if entering {
				// Manage indentation based on heading level transitions
				if n.Level == 1 && level2Indented {
					// Transitioning from level 2 back to level 1 - unindent
					imgui.Unindent()
					level2Indented = false
				} else if n.Level == 2 && !level2Indented {
					// Entering level 2 section - indent
					imgui.Indent()
					level2Indented = true
				}

				text := extractText(n, preprocessedSource)

				// Pop IDs for headings at same or deeper level, then push a new ID for this
				// heading's section. This ensures that identically-named subsections under
				// different parents get unique imgui IDs.
				for len(headingIDStack) > 0 && headingIDStack[len(headingIDStack)-1] >= n.Level {
					imgui.PopID()
					headingIDStack = headingIDStack[:len(headingIDStack)-1]
					headingTextStack = headingTextStack[:len(headingTextStack)-1]
				}
				parentPath := strings.Join(headingTextStack, "\x1f")
				sectionKey := text
				if parentPath != "" {
					sectionKey = parentPath + "\x1f" + text
				}
				imgui.PushIDStr(sectionKey)
				headingIDStack = append(headingIDStack, n.Level)
				headingTextStack = append(headingTextStack, text)

				// Top two levels are collapsible, expanded by default
				if n.Level <= 2 {
					_, isClosed := closedHeadings[sectionKey]
					isOpen := !isClosed

					// Set the state for this item based on our map; use CondAlways to ensure our
					// map always controls the state
					imgui.SetNextItemOpenV(isOpen, imgui.CondAlways)

					// Draw the collapsing header
					headerOpen := imgui.CollapsingHeaderBoolPtr(text, nil)

					if !headerOpen {
						// If collapsed, skip all content until next heading at same/higher level
						skippingUntilLevel = n.Level
					}

					// Update the map: if closed, add to map; if open, remove from map
					if headerOpen {
						delete(closedHeadings, sectionKey)
					} else {
						closedHeadings[sectionKey] = struct{}{}
					}
				} else {
					imgui.Text(text)
					imgui.Spacing()
				}
			}
			return ast.WalkSkipChildren, nil

		case *ast.Paragraph:
			if entering {
				// Calculate wrap width based on list depth
				indentWidth := float32(listDepth * 20) // Approximate indent per level
				wrapWidth := width - indentWidth

				renderInline(n, preprocessedSource, inlineFonts, wrapWidth)

				// If this was the first content in a list item, clear the flag
				if inListItem {
					inListItem = false
				} else {
					// Only add spacing if not in a list item (to avoid extra space after first paragraph)
					if listDepth == 0 {
						imgui.Spacing()
					}
				}
				return ast.WalkSkipChildren, nil
			}

		case *ast.List:
			if entering {
				listDepth++
				// A list should never be inline with a bullet, even if it's the first content
				if inListItem {
					inListItem = false
					// Break the line to ensure nested list doesn't appear inline with parent bullet
					imgui.Dummy(imgui.Vec2{X: 0, Y: 0})
				} else if listDepth > 1 {
					imgui.Spacing()
				}
			} else {
				listDepth--
				// Add spacing after list only if at top level
				if listDepth == 0 {
					imgui.Spacing()
				}
			}

		case *ast.ListItem:
			if entering {
				imgui.Indent()

				// Add bullet or number
				parent := n.Parent()
				if list, ok := parent.(*ast.List); ok {
					if list.IsOrdered() {
						// Ordered list - show number
						itemNum := 1
						for c := parent.FirstChild(); c != nil && c != n; c = c.NextSibling() {
							itemNum++
						}
						imgui.Text(fmt.Sprintf("%d.", itemNum))
					} else {
						// Unordered list - show bullet
						imgui.Bullet()
					}
				}

				imgui.SameLine()

				// Check if this list item has direct text content (no Paragraph wrapper)
				// This can happen with tight lists
				hasTextChild := false
				for child := n.FirstChild(); child != nil; child = child.NextSibling() {
					if _, ok := child.(*ast.Text); ok {
						hasTextChild = true
						break
					}
					if _, ok := child.(*ast.Paragraph); ok {
						break // Has paragraph, normal flow will handle it
					}
				}

				if hasTextChild {
					// Render inline children directly (handles emphasis and code spans)
					indentWidth := float32(listDepth * 20)
					wrapWidth := width - indentWidth
					renderInline(n, preprocessedSource, inlineFonts, wrapWidth)
					return ast.WalkSkipChildren, nil
				}

				inListItem = true // Next content should be inline with bullet
			}

			if !entering {
				imgui.Unindent()
			}

		case *ast.Text:
			// Text nodes outside paragraphs (e.g., in headings, list items without paragraph wrapper)
			if entering {
				text := string(n.Segment.Value(preprocessedSource))
				if inListItem {
					inListItem = false
				}
				imgui.Text(text)
			}

		case *ast.CodeBlock, *ast.FencedCodeBlock:
			if entering {
				// Render code block using monospace font
				text := extractText(n, preprocessedSource)
				imgui.PushFont(&inlineFonts.mono.Ifont, float32(inlineFonts.mono.Size))
				imgui.TextUnformatted(text)
				imgui.PopFont()
				imgui.Spacing()
				return ast.WalkSkipChildren, nil
			}

		case *ast.ThematicBreak:
			if entering {
				imgui.Separator()
				imgui.Spacing()
			}

		case *ast.Blockquote:
			if entering {
				imgui.Indent()
				// TODO: Could add a vertical line or background color
			} else {
				imgui.Unindent()
			}

		case *brief.DocMapNode:
			if entering {
				// Draw the annotated map inline
				imgui.Spacing()

				// Calculate actual map width based on percentage
				mapWidth := width
				if n.DocMap.Width > 0 && n.DocMap.Width < 100 {
					mapWidth = width * float32(n.DocMap.Width) / 100.0
				}

				// Save the current cursor X position to restore after drawing
				savedCursorX := imgui.CursorPosX()

				// Center the map if it's smaller than available width
				if mapWidth < width {
					centerOffset := (width - mapWidth) / 2.0
					imgui.SetCursorPosX(imgui.CursorPosX() + centerOffset)
				}

				disabledTCPs := ui.brief.disabledAirspaceTCPs[n.Label]
				mapErrs := drawAnnotatedMap(n.DocMap, n.Label, mapWidth, mapCache, state, controlClient, inlineFonts.italic, disabledTCPs, p, lg)

				// Restore cursor X position before rendering any error so it
				// aligns with the brief's main column rather than with the
				// centered map label.
				imgui.SetCursorPosX(savedCursorX)

				// When more than one TCP's airspace would draw, offer a
				// 3-column checkbox table so the user can toggle individual
				// TCPs off. All start enabled; "disabled" is the persisted set.
				if airspaceTCPs := collectMapAirspaceTCPs(n.DocMap, state); len(airspaceTCPs) > 1 {
					if disabledTCPs == nil {
						disabledTCPs = make(map[string]bool)
						ui.brief.disabledAirspaceTCPs[n.Label] = disabledTCPs
					}
					imgui.PushIDStr(fmt.Sprintf("airspace-tcps-%s", n.Label))
					if imgui.BeginTableV(fmt.Sprintf("##airspace-tcp-table-%s", n.Label), 3,
						imgui.TableFlagsNoSavedSettings|imgui.TableFlagsSizingStretchProp, imgui.Vec2{}, 0) {
						for i, tcp := range airspaceTCPs {
							if i%3 == 0 {
								imgui.TableNextRow()
							}
							imgui.TableNextColumn()
							enabled := !disabledTCPs[tcp]
							if imgui.Checkbox(tcp, &enabled) {
								if enabled {
									delete(disabledTCPs, tcp)
								} else {
									disabledTCPs[tcp] = true
								}
							}
						}
						imgui.EndTable()
					}
					imgui.PopID()
				}

				if len(mapErrs) > 0 {
					imgui.PushStyleColorVec4(imgui.ColText, errorTextColor)
					for _, err := range mapErrs {
						imgui.BulletText(err.Error())
					}
					imgui.PopStyleColor()
				}
				imgui.Spacing()
			}
			return ast.WalkSkipChildren, nil

		case *brief.ConfigurationsNode:
			if entering {
				// Render radio buttons for configuration options in a table
				imgui.Spacing()

				// Find which option is currently selected by name
				sel := selectedConfigs[n.Name]
				selectedIndex := int32(0)
				for i, opt := range n.Options {
					if opt.Name == sel {
						selectedIndex = int32(i)
						break
					}
				}

				// Create unique ID for this configurations node using its name
				imgui.PushIDStr(fmt.Sprintf("config-%s", n.Name))

				// Render radio buttons in a 3-column table
				const ncol = 3
				if imgui.BeginTableV(fmt.Sprintf("##config-table-%s", n.Name), ncol,
					imgui.TableFlagsNoSavedSettings|imgui.TableFlagsSizingStretchProp, imgui.Vec2{}, 0) {
					for i, option := range n.Options {
						if i%ncol == 0 {
							imgui.TableNextRow()
						}
						imgui.TableNextColumn()
						if imgui.RadioButtonIntPtr(option.Label, &selectedIndex, int32(i)) {
							// Update the selections and force a re-parse in the next frame.
							selectedConfigs[n.Name] = option.Name
							ui.brief.parsed = nil
							ui.brief.inlineTokensCache = nil
						}
					}
					imgui.EndTable()
				}

				imgui.PopID()
				imgui.Spacing()
			}
			return ast.WalkSkipChildren, nil

		case *extast.Table:
			if entering {
				// Calculate column widths by scanning table content
				columnWidths := calculateColumnWidths(n, preprocessedSource)
				numCols := len(columnWidths)

				if numCols > 0 {
					imgui.Spacing()
					// Store column widths for use when setting up columns
					tableColumnWidths[n] = columnWidths
					tableHeaderColumnIndex[n] = 0

					// Calculate total content width
					var totalWidth float32
					for _, w := range columnWidths {
						totalWidth += w
					}

					// Store for caption centering
					lastTableTotalWidth = totalWidth

					// Base flags for all tables
					flags := imgui.TableFlagsNoSavedSettings | imgui.TableFlagsRowBg | imgui.TableFlagsBorders

					if totalWidth <= width {
						// Content fits - size columns to their content, center
						// the table, and don't expand to fill available width.
						flags |= imgui.TableFlagsSizingFixedFit | imgui.TableFlagsNoHostExtendX
						centerOffset := (width - totalWidth) / 2.0
						imgui.SetCursorPosX(imgui.CursorPosX() + centerOffset)
					} else {
						// Content exceeds width - stretch columns
						// proportionally to their natural width and wrap cell
						// text. (ScrollX would need an explicit outer_size.y
						// and produces a poor reading experience for prose.)
						flags |= imgui.TableFlagsSizingStretchProp
						tableWraps[n] = true
					}

					if imgui.BeginTableV(fmt.Sprintf("##table-%p", n), int32(numCols), flags, imgui.Vec2{X: 0, Y: 0}, 0.0) {
						tablesStarted[n] = true
					}
				}
			} else if tablesStarted[n] {
				// Only call EndTable if we successfully called BeginTable
				imgui.EndTable()
				imgui.Spacing()
				delete(tablesStarted, n)
				delete(tableColumnWidths, n)
				delete(tableHeaderColumnIndex, n)
				delete(tableWraps, n)
				delete(drewTableHeader, n)
			}

		case *extast.TableRow:
			if entering {
				// Find parent table to check if it was started
				var tableNode *extast.Table
				for p := n.Parent(); p != nil; p = p.Parent() {
					if t, ok := p.(*extast.Table); ok {
						tableNode = t
						break
					}
				}
				if tableNode != nil && tablesStarted[tableNode] {
					imgui.TableNextRow()
				}
			}
			return ast.WalkContinue, nil

		case *extast.TableCell:
			if entering {
				// Find the parent table node to check if it was started
				var tableNode *extast.Table
				for p := n.Parent(); p != nil; p = p.Parent() {
					if t, ok := p.(*extast.Table); ok {
						tableNode = t
						break
					}
				}

				// Only render cell if parent table was successfully started
				if tableNode != nil && tablesStarted[tableNode] {
					// Check if this is a header cell
					// Header cells have TableHeader as parent, body cells have TableRow as parent
					isHeader := false
					if parent := n.Parent(); parent != nil {
						if _, ok := parent.(*extast.TableHeader); ok {
							isHeader = true
						}
					}

					text := extractText(n, preprocessedSource)
					wrap := tableWraps[tableNode]
					if isHeader {
						// Set column sizing: stretch with column width as
						// weight when wrapping, otherwise fixed pixel width.
						colIdx := tableHeaderColumnIndex[tableNode]
						columnWidths := tableColumnWidths[tableNode]
						if colIdx < len(columnWidths) {
							colFlag := imgui.TableColumnFlagsWidthFixed
							if wrap {
								colFlag = imgui.TableColumnFlagsWidthStretch
							}
							imgui.TableSetupColumnV(text, colFlag, columnWidths[colIdx], 0)
							tableHeaderColumnIndex[tableNode] = colIdx + 1
						} else {
							imgui.TableSetupColumn(text)
						}
					} else {
						if !drewTableHeader[tableNode] {
							imgui.TableHeadersRow()
							drewTableHeader[tableNode] = true
						}

						imgui.TableNextColumn()
						if wrap {
							imgui.TextWrapped(text)
						} else {
							imgui.Text(text)
						}
					}
				}

				return ast.WalkSkipChildren, nil
			}

		case *brief.TableCaption:
			if entering {
				// Render caption centered in gray italic below the table
				inlineFonts.italic.ImguiPush()
				captionWidth := imgui.CalcTextSize(n.Caption).X

				// Center the caption relative to the table width (or available width if table is wider)
				tableWidth := lastTableTotalWidth
				if tableWidth <= 0 {
					tableWidth = width
				}

				// Center caption relative to table
				if captionWidth < tableWidth {
					// If table was centered, caption should align with it
					if tableWidth < width {
						// Table was centered, so center caption within table
						tableCenterOffset := (width - tableWidth) / 2.0
						captionCenterOffset := (tableWidth - captionWidth) / 2.0
						imgui.SetCursorPosX(imgui.CursorPosX() + tableCenterOffset + captionCenterOffset)
					} else {
						// Table spans full width, center caption within available width
						centerOffset := (width - captionWidth) / 2.0
						imgui.SetCursorPosX(imgui.CursorPosX() + centerOffset)
					}
				}

				imgui.PushStyleColorU32(imgui.ColText, 0xff999999)
				imgui.TextUnformatted(n.Caption)
				imgui.PopStyleColor()
				imgui.PopFont()
				imgui.Spacing()
			}
			return ast.WalkSkipChildren, nil

		case *brief.ErrorNode:
			if entering {
				imgui.PushStyleColorVec4(imgui.ColText, errorTextColor)
				imgui.BulletText(n.Message)
				imgui.PopStyleColor()
			}
			return ast.WalkSkipChildren, nil

		default:
			// For unhandled node types, continue walking
			return ast.WalkContinue, nil
		}

		return ast.WalkContinue, nil
	})
}

// drawCenteredLabel draws a text label centered horizontally within the given width.
// If font is non-nil it is pushed for both width measurement and drawing.
func drawCenteredLabel(label string, width, initialCursorX float32, font *renderer.Font) {
	if label == "" {
		return
	}
	if font != nil {
		font.ImguiPush()
		defer imgui.PopFont()
	}
	textSize := imgui.CalcTextSize(label)
	imgui.SetCursorPosX(initialCursorX + (width-textSize.X)/2)
	imgui.Text(label)
	// After Text(), cursor moves to new line - restore X position
	imgui.SetCursorPosX(initialCursorX)
}

// findVideoMapsFromLibrary extracts the requested video maps by name from a library.
// The returned slice carries VideoMap by value; the struct's heavy payload
// (Lines/Symbols/Labels) is slice-backed and shared with the library, so the copy is cheap.
func findVideoMapsFromLibrary(library *av.MapLibrary, mapNames []string) ([]av.STARSMap, []error) {
	if library == nil {
		return nil, nil
	}

	videoMaps := make([]av.STARSMap, 0, len(mapNames))
	var errs []error
	for _, mapName := range mapNames {
		if m, ok := library.Maps[mapName]; ok {
			videoMaps = append(videoMaps, m)
		} else {
			errs = append(errs, fmt.Errorf("%s: video map not found in library", mapName))
		}
	}
	return videoMaps, errs
}

// videoMapCache holds cached video map libraries to avoid reloading on every frame.
type videoMapCache struct {
	libraries map[string]*av.MapLibrary // file path -> loaded library
	errors    map[string]error          // file path -> load error
}

// newVideoMapCache creates a new empty video map cache.
func newVideoMapCache() *videoMapCache {
	return &videoMapCache{
		libraries: make(map[string]*av.MapLibrary),
		errors:    make(map[string]error),
	}
}

// loadVideoMapLibrary memoizes ControlClient.LoadVideoMapLibrary so a brief
// only does the local-load / RPC dance once per file per session. Returns a
// nil library (without error) if the brief block has no file or no map names.
func loadVideoMapLibrary(briefMap *brief.VideoMapBlock, cache *videoMapCache,
	controlClient *client.ControlClient) (*av.MapLibrary, error) {
	if briefMap.File == "" || len(briefMap.Maps) == 0 {
		return nil, nil
	}

	if lib, ok := cache.libraries[briefMap.File]; ok {
		return lib, nil
	}
	if err, ok := cache.errors[briefMap.File]; ok {
		return nil, err
	}

	lib, err := controlClient.LoadVideoMapLibrary(briefMap.File)
	if err != nil {
		cache.errors[briefMap.File] = err
		return nil, err
	}
	cache.libraries[briefMap.File] = lib
	return lib, nil
}

// briefInlineFonts groups the proportional + monospace fonts used by renderInline to style inline
// markdown emphasis and code spans.
type briefInlineFonts struct {
	regular, italic, bold, boldItalic, mono *renderer.Font
}

// pickFont selects the appropriate font for the current emphasis state.  Mono takes priority (code
// spans aren't styled bold/italic). Otherwise the bold and italic flags combine into one of the
// four proportional variants.
func (f briefInlineFonts) pickFont(bold, italic, mono bool) *renderer.Font {
	switch {
	case mono:
		return f.mono
	case bold && italic:
		return f.boldItalic
	case bold:
		return f.bold
	case italic:
		return f.italic
	default:
		return f.regular
	}
}

// inlineToken is a single word (plus optional trailing whitespace) drawn
// in a particular font. forceBreak is set for hard line breaks.
type inlineToken struct {
	text       string
	font       *renderer.Font
	forceBreak bool
}

// collectInlineTokens walks the inline subtree of n and returns a flat
// list of word-sized tokens carrying their font. Each token has any
// trailing whitespace from the source attached, so adjacent tokens
// render without gaps.
func collectInlineTokens(n ast.Node, source []byte, fonts briefInlineFonts) []inlineToken {
	var tokens []inlineToken

	// Track current emphasis state by counting active wrappers. Goldmark
	// gives us properly-nested Emphasis nodes, so a counter handles
	// arbitrary nesting (***x*** parses as a bold containing an italic).
	var boldDepth, italicDepth, monoDepth int

	emit := func(text string) {
		if text == "" {
			return
		}
		font := fonts.pickFont(boldDepth > 0, italicDepth > 0, monoDepth > 0)
		// Split into whitespace-terminated tokens. Each token keeps its trailing whitespace so
		// inter-word spacing is preserved.
		for len(text) > 0 {
			// Find end of next word (run of non-space chars), then end of trailing space run.
			i := 0
			for i < len(text) && text[i] != ' ' && text[i] != '\t' && text[i] != '\n' {
				i++
			}
			j := i
			for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\n') {
				j++
			}
			tokens = append(tokens, inlineToken{text: text[:j], font: font})
			text = text[j:]
		}
	}

	_ = ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		switch node := node.(type) {
		case *ast.Text:
			if entering {
				emit(string(node.Segment.Value(source)))
				if node.HardLineBreak() {
					tokens = append(tokens, inlineToken{forceBreak: true})
				} else if node.SoftLineBreak() {
					emit(" ")
				}
			}

		case *ast.String:
			if entering {
				emit(string(node.Value))
			}

		case *ast.Emphasis:
			if entering {
				if node.Level >= 2 {
					boldDepth++
				} else {
					italicDepth++
				}
			} else {
				if node.Level >= 2 {
					boldDepth--
				} else {
					italicDepth--
				}
			}

		case *ast.CodeSpan:
			// Render the contents in monospace with no backticks.
			if entering {
				monoDepth++
			} else {
				monoDepth--
			}
		}
		return ast.WalkContinue, nil
	})

	return tokens
}

// inlineRender holds the per-paragraph result of analyzing inline content: either a flowing
// single-font string (fast path) or the per-token slice (slow path, used when the paragraph has
// emphasis or hard breaks).
type inlineRender struct {
	tokens []inlineToken // populated when fastText == "" fastText, if non-empty, is the
	// concatenation of every token's text for a paragraph that uses one font end-to-end and has no
	// forceBreak tokens.  Rendering it goes through imgui.TextUnformatted under a PushTextWrapPos —
	// one cgo call per paragraph instead of ~5 per visible word.
	fastText string
	fastFont *renderer.Font
}

// buildInlineRender runs collectInlineTokens and decides whether the paragraph qualifies for the
// single-AddText fast path.
func buildInlineRender(n ast.Node, source []byte, fonts briefInlineFonts) inlineRender {
	tokens := collectInlineTokens(n, source, fonts)
	if len(tokens) == 0 {
		return inlineRender{}
	}
	// Fast path eligibility: every token uses the same font and there is no forceBreak (hard line
	// break). In practice this matches most paragraphs in briefs.
	first := tokens[0].font
	ineligible := slices.ContainsFunc(tokens, func(tok inlineToken) bool { return tok.forceBreak || tok.font != first })
	if ineligible {
		return inlineRender{tokens: tokens}
	}
	var b strings.Builder
	for _, tok := range tokens {
		b.WriteString(tok.text)
	}
	// Trim trailing whitespace so the wrap-pos accounting and the final imgui.NewLine match the
	// slow path's appearance.
	return inlineRender{
		fastText: strings.TrimRight(b.String(), " \t\n"),
		fastFont: first,
	}
}

// renderInline renders the inline children of n. For paragraphs without emphasis, it hands the
// whole string to imgui under a PushTextWrapPos — one cgo per paragraph. Otherwise it falls back to
// the per-token loop with manual word-wrap so adjacent runs in different fonts still flow
// correctly.
func renderInline(n ast.Node, source []byte, fonts briefInlineFonts, wrapWidth float32) {
	// The result is a pure function of (n, source, fonts); the AST and source
	// are stable per parse, and fonts only change on a brief reload (which
	// clears the cache via ui.inlineTokensCache = nil).
	if ui.brief.inlineTokensCache == nil {
		ui.brief.inlineTokensCache = make(map[ast.Node]inlineRender)
	}
	r, ok := ui.brief.inlineTokensCache[n]
	if !ok {
		r = buildInlineRender(n, source, fonts)
		ui.brief.inlineTokensCache[n] = r
	}

	if r.fastText != "" {
		// Fast path: single font, no hard breaks. Let imgui handle wrapping.
		startX := imgui.CursorPosX()
		imgui.PushFont(&r.fastFont.Ifont, float32(r.fastFont.Size))
		imgui.PushTextWrapPosV(startX + wrapWidth)
		imgui.TextUnformatted(r.fastText)
		imgui.PopTextWrapPos()
		imgui.PopFont()
		return
	}

	tokens := r.tokens
	if len(tokens) == 0 {
		return
	}

	// Zero ItemSpacing inside the paragraph so wrapped lines stack at
	// exactly lineHeight (matching what imgui.Text does with its own
	// internal wrap). We pop and emit the final NewLine outside the
	// override so the gap to the next block element uses the normal
	// item spacing.
	imgui.PushStyleVarVec2(imgui.StyleVarItemSpacing, imgui.Vec2{X: 0, Y: 0})

	startX := imgui.CursorPosX()
	maxX := startX + wrapWidth
	lineEmpty := true

	for _, tok := range tokens {
		if tok.forceBreak {
			imgui.NewLine()
			imgui.SetCursorPosX(startX)
			lineEmpty = true
			continue
		}

		// Split the token into its visible word and trailing whitespace.
		word := tok.text
		k := len(word)
		for k > 0 {
			c := word[k-1]
			if c != ' ' && c != '\t' && c != '\n' {
				break
			}
			k--
		}
		visible := word[:k]
		trailing := word[k:]

		// Skip a whitespace-only token at the start of a line: nothing
		// to draw, and it would visually indent the wrapped line.
		if visible == "" && lineEmpty {
			continue
		}

		imgui.PushFont(&tok.font.Ifont, float32(tok.font.Size))
		visibleW := imgui.CalcTextSize(visible).X
		imgui.PopFont()

		// Wrap before this word if it won't fit and we're not at column 0.
		if !lineEmpty && imgui.CursorPosX()+visibleW > maxX {
			imgui.NewLine()
			imgui.SetCursorPosX(startX)
			lineEmpty = true
		}

		// Draw the word, then the trailing whitespace (if any) on the
		// same line. Use SameLineV(0,0) so imgui doesn't insert its own
		// item spacing between adjacent runs.
		imgui.PushFont(&tok.font.Ifont, float32(tok.font.Size))
		if visible != "" {
			imgui.TextUnformatted(visible)
			imgui.SameLineV(0, 0)
		}
		if trailing != "" {
			imgui.TextUnformatted(trailing)
			imgui.SameLineV(0, 0)
		}
		imgui.PopFont()

		lineEmpty = false
	}

	// Restore normal item spacing, then terminate the paragraph with a
	// NewLine so the next block element gets the usual vertical gap.
	imgui.PopStyleVar()
	imgui.NewLine()
}

// extractText extracts text content from a node and its children.
func extractText(n ast.Node, source []byte) string {
	var buf strings.Builder

	writeEmphasisMarker := func(level int) {
		if level == 1 {
			buf.WriteByte('*')
		} else if level == 2 {
			buf.WriteString("**")
		}
	}

	_ = ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			// Emit closing marker for emphasis nodes on the way back up.
			if emph, ok := node.(*ast.Emphasis); ok {
				writeEmphasisMarker(emph.Level)
			}
			return ast.WalkContinue, nil
		}

		switch node := node.(type) {
		case *ast.Text:
			segment := node.Segment
			buf.Write(segment.Value(source))
			if node.SoftLineBreak() {
				buf.WriteByte(' ')
			} else if node.HardLineBreak() {
				buf.WriteByte('\n')
			}

		case *ast.String:
			buf.Write(node.Value)

		case *ast.CodeBlock, *ast.FencedCodeBlock:
			// Code blocks store their content in Lines()
			lines := node.Lines()
			for i := range lines.Len() {
				line := lines.At(i)
				buf.Write(line.Value(source))
			}
			return ast.WalkSkipChildren, nil

		case *ast.CodeSpan:
			// Extract code span text
			buf.WriteByte('`')
			for c := node.FirstChild(); c != nil; c = c.NextSibling() {
				if text, ok := c.(*ast.Text); ok {
					buf.Write(text.Segment.Value(source))
				}
			}
			buf.WriteByte('`')

		case *ast.Emphasis:
			writeEmphasisMarker(node.Level)
		}

		return ast.WalkContinue, nil
	})

	return buf.String()
}

///////////////////////////////////////////////////////////////////////////

// formatAltitudeRange formats a floor/ceiling pair as "low-high" in hundreds of feet, matching the
// convention used for ControllerAirspaceVolume.Label.
func formatAltitudeRange(low, high int) string {
	if low == high {
		return fmt.Sprintf("%d", low/100)
	}
	return fmt.Sprintf("%d-%d", low/100, high/100)
}

// resolveAirspaceTCPs returns the TCPs an airspace annotation should draw.  An explicit list in the
// annotation is used as-is; otherwise the user's consolidated positions are returned.
func resolveAirspaceTCPs(ann brief.MapAnnotation, state *client.SimState) []sim.ControlPosition {
	if len(ann.AirspaceTCPs) > 0 {
		tcps := make([]sim.ControlPosition, len(ann.AirspaceTCPs))
		for i, t := range ann.AirspaceTCPs {
			tcps[i] = sim.ControlPosition(t)
		}
		return tcps
	}
	return state.GetPositionsForTCW(state.UserTCW)
}

// collectMapAirspaceTCPs returns the sorted union of TCPs across all
// AnnotationAirspace entries in briefMap. Used to drive the per-map
// "which TCPs to show" checkbox row.
func collectMapAirspaceTCPs(briefMap *brief.VideoMapBlock, state *client.SimState) []string {
	seen := make(map[string]struct{})
	for _, ann := range briefMap.Annotations {
		if ann.Type != brief.AnnotationAirspace {
			continue
		}
		for _, tcp := range resolveAirspaceTCPs(ann, state) {
			seen[string(tcp)] = struct{}{}
		}
	}
	return util.SortedMapKeys(seen)
}

// briefMapProjection captures the lat/lon → canvas mapping for a brief video map. lat/lon points
// project to nm offsets from `center`, then are rotated by -magVar so magnetic north points up.
// `bounds` is the visible region expressed in those rotated-nm coordinates; storing bounds in the
// rotated frame (rather than as a lat/lon bbox) keeps the canvas tight to the actual content even
// when the content's principal axis is diagonal.
type briefMapProjection struct {
	center   math.Point2LL
	nmPerLon float32
	// sin/cos of magVar (cached so equal-comparison still works on the
	// struct and we don't recompute per call).
	sinMagVar, cosMagVar float32
	bounds               math.Extent2D // in rotated-nm coordinates
}

func (p briefMapProjection) latLonToRotNm(ll math.Point2LL) (float32, float32) {
	dx := (ll[0] - p.center[0]) * p.nmPerLon
	dy := (ll[1] - p.center[1]) * math.NMPerLatitude
	return p.cosMagVar*dx + p.sinMagVar*dy, p.cosMagVar*dy - p.sinMagVar*dx
}

func (p briefMapProjection) rotNmToLatLon(rotX, rotY float32) math.Point2LL {
	dx := p.cosMagVar*rotX - p.sinMagVar*rotY
	dy := p.cosMagVar*rotY + p.sinMagVar*rotX
	return math.Point2LL{p.center[0] + dx/p.nmPerLon, p.center[1] + dy/math.NMPerLatitude}
}

// calculateMapProjection builds the lat/lon → canvas projection for a
// brief video map. If briefMap.Extent is set, the projection spans that
// explicit lat/lon rectangle (rotated bounding box). Otherwise the
// projection is fit to the content (annotations + airspace, falling back
// to video map lines), padded, and aspect-capped.
func calculateMapProjection(briefMap *brief.VideoMapBlock, videoMaps []av.STARSMap, state *client.SimState) (briefMapProjection, error) {
	useExplicitExtent := briefMap.Extent != [2]math.Point2LL{}

	center := state.GetInitialCenter()
	proj := briefMapProjection{
		center:    center,
		nmPerLon:  math.NMPerLongitudeAt(center),
		sinMagVar: math.Sin(math.Radians(state.MagneticVariation)),
		cosMagVar: math.Cos(math.Radians(state.MagneticVariation)),
	}

	// Build the rotated-nm bbox of all content in a single pass. Tight: no
	// wasted space from the axis-aligned-lat/lon-bbox-of-rotated-rectangle
	// effect.
	rotBounds := math.EmptyExtent2D()
	sawContent := false
	extend := func(ll math.Point2LL) {
		rotX, rotY := proj.latLonToRotNm(ll)
		rotBounds = math.Union(rotBounds, [2]float32{rotX, rotY})
		sawContent = true
	}

	if useExplicitExtent {
		e := math.Extent2DFromP2LLs([]math.Point2LL{briefMap.Extent[0], briefMap.Extent[1]})
		extend(math.Point2LL{e.P0[0], e.P0[1]})
		extend(math.Point2LL{e.P1[0], e.P0[1]})
		extend(math.Point2LL{e.P0[0], e.P1[1]})
		extend(math.Point2LL{e.P1[0], e.P1[1]})
	} else {
		sawAnnotation := false
		for _, ann := range briefMap.Annotations {
			for _, v := range ann.Vertices {
				extend(v.Point)
				sawAnnotation = true
			}
			if ann.Type == brief.AnnotationAirspace {
				for _, tcp := range resolveAirspaceTCPs(ann, state) {
					for _, vols := range state.Airspace[tcp] {
						for _, vol := range vols {
							for _, pts := range vol.Boundaries {
								for _, p := range pts {
									extend(p)
									sawAnnotation = true
								}
							}
						}
					}
				}
			}
		}
		if !sawAnnotation {
			for _, vm := range videoMaps {
				b := vm.Bounds()
				extend(math.Point2LL{b.P0[0], b.P0[1]})
				extend(math.Point2LL{b.P1[0], b.P0[1]})
				extend(math.Point2LL{b.P0[0], b.P1[1]})
				extend(math.Point2LL{b.P1[0], b.P1[1]})
			}
		}
	}

	if !sawContent {
		return briefMapProjection{}, fmt.Errorf("invalid map bounds")
	}

	if !useExplicitExtent {
		// Enforce a minimum extent so a single fix:/point: annotation (which
		// produces a zero-area bbox) still renders with sensible margin
		// instead of failing the bounds check below.
		if w := rotBounds.Width(); w < minMapExtentNm {
			d := (minMapExtentNm - w) / 2
			rotBounds.P0[0] -= d
			rotBounds.P1[0] += d
		}
		if h := rotBounds.Height(); h < minMapExtentNm {
			d := (minMapExtentNm - h) / 2
			rotBounds.P0[1] -= d
			rotBounds.P1[1] += d
		}

		// Padding (10% per side) and an aspect-ratio cap so highly
		// elongated content doesn't produce an absurdly tall or wide pane.
		padX := rotBounds.Width() * mapPaddingFactor
		padY := rotBounds.Height() * mapPaddingFactor
		rotBounds.P0[0] -= padX
		rotBounds.P1[0] += padX
		rotBounds.P0[1] -= padY
		rotBounds.P1[1] += padY

		w, h := rotBounds.Width(), rotBounds.Height()
		if w > 0 && h > 0 {
			if w*maxMapAspectRatio < h {
				d := (h/maxMapAspectRatio - w) / 2
				rotBounds.P0[0] -= d
				rotBounds.P1[0] += d
			} else if h*maxMapAspectRatio < w {
				d := (w/maxMapAspectRatio - h) / 2
				rotBounds.P0[1] -= d
				rotBounds.P1[1] += d
			}
		}
	}

	if rotBounds.Width() <= 0 || rotBounds.Height() <= 0 {
		return briefMapProjection{}, fmt.Errorf("invalid map bounds")
	}

	proj.bounds = rotBounds
	return proj, nil
}

// vertexAnnotationText returns the formatted "↑ N" or "↓ N" altitude label
// for a waypoint annotation, or "" if it carries no altitude. The line/arrow
// draw path and expandBoundsForLabels both call this so the displayed text
// (and therefore the measured width) can't drift between measure and draw.
func vertexAnnotationText(ann *brief.WaypointAnnotation) string {
	if ann == nil {
		return ""
	}
	if ann.ClimbAltitude > 0 {
		return fmt.Sprintf("%s %d", renderer.FontAwesomeIconArrowUp, ann.ClimbAltitude)
	}
	if ann.DescentAltitude > 0 {
		return fmt.Sprintf("%s %d", renderer.FontAwesomeIconArrowDown, ann.DescentAltitude)
	}
	return ""
}

// labelTopLeftAndSize returns ann's label top-left pixel position (under the
// supplied lat/lon → pixel transform) and its (maxLineWidth, totalHeight)
// pixel extent. Returns false if ann has no label or no usable anchor.
// drawAnnotatedMap and expandBoundsForLabels both call this so positions
// stay in sync between the measure pass and the draw pass.
func labelTopLeftAndSize(ann brief.MapAnnotation, latLonToScreen func(math.Point2LL) imgui.Vec2) (imgui.Vec2, imgui.Vec2, bool) {
	if len(ann.Label) == 0 {
		return imgui.Vec2{}, imgui.Vec2{}, false
	}
	var w float32
	for _, line := range ann.Label {
		w = max(w, imgui.CalcTextSize(line).X)
	}
	size := imgui.Vec2{X: w, Y: float32(len(ann.Label)) * imgui.TextLineHeight()}

	switch ann.Type {
	case brief.AnnotationPoint:
		if len(ann.Vertices) == 0 {
			return imgui.Vec2{}, size, false
		}
		c := latLonToScreen(ann.Vertices[0].Point)
		return imgui.Vec2{X: c.X - size.X/2, Y: c.Y - size.Y/2}, size, true
	case brief.AnnotationFix:
		if len(ann.Vertices) == 0 {
			return imgui.Vec2{}, size, false
		}
		c := latLonToScreen(ann.Vertices[0].Point)
		return imgui.Vec2{X: c.X + fixLabelOffset, Y: c.Y + fixLabelOffset}, size, true
	case brief.AnnotationLine, brief.AnnotationArrow, brief.AnnotationPolygon:
		if len(ann.Vertices) == 0 {
			return imgui.Vec2{}, size, false
		}
		b := math.EmptyExtent2D()
		for _, v := range ann.Vertices {
			p := latLonToScreen(v.Point)
			b = math.Union(b, [2]float32{p.X, p.Y})
		}
		c := b.Center()
		return imgui.Vec2{X: c[0] + labelOffsetPixels, Y: c[1] + labelOffsetPixels}, size, true
	}
	return imgui.Vec2{}, size, false
}

// expandBoundsForLabels enlarges proj.bounds (in rotated-nm space) so that
// every annotation's label, positioned via labelTopLeftAndSize, falls inside
// the canvas. width is the rendered canvas width in pixels; canvas height is
// derived from proj.bounds. Three iterations converge well under one pixel
// since growing bounds shifts label pixel positions inward.
func expandBoundsForLabels(proj briefMapProjection, anns []brief.MapAnnotation, width float32) briefMapProjection {
	if width <= 0 {
		return proj
	}
	for range 3 {
		b := proj.bounds
		if b.Width() <= 0 || b.Height() <= 0 {
			return proj
		}
		mapHeight := width * b.Height() / b.Width()

		toCanvas := func(ll math.Point2LL) imgui.Vec2 {
			rotX, rotY := proj.latLonToRotNm(ll)
			return imgui.Vec2{
				X: (rotX - b.P0[0]) / b.Width() * width,
				Y: (1.0 - (rotY-b.P0[1])/b.Height()) * mapHeight,
			}
		}

		var overL, overR, overT, overB float32
		accumulate := func(pos, size imgui.Vec2) {
			overL = max(overL, -pos.X)
			overR = max(overR, pos.X+size.X-width)
			overT = max(overT, -pos.Y)
			overB = max(overB, pos.Y+size.Y-mapHeight)
		}
		lineHeight := imgui.TextLineHeight()
		for _, ann := range anns {
			if pos, size, ok := labelTopLeftAndSize(ann, toCanvas); ok {
				accumulate(pos, size)
			}
			// Per-vertex altitude labels (only line/arrow paths draw them).
			if ann.Type != brief.AnnotationLine && ann.Type != brief.AnnotationArrow {
				continue
			}
			for _, v := range ann.Vertices {
				text := vertexAnnotationText(v.Annotation)
				if text == "" {
					continue
				}
				c := toCanvas(v.Point)
				accumulate(
					imgui.Vec2{X: c.X + labelOffsetPixels, Y: c.Y},
					imgui.Vec2{X: imgui.CalcTextSize(text).X, Y: lineHeight},
				)
			}
		}
		if overL <= 0 && overR <= 0 && overT <= 0 && overB <= 0 {
			return proj
		}

		// Expand the rotated-nm bounds asymmetrically by the per-side
		// pixel overflow, converted back to rotated-nm.
		proj.bounds.P0[0] -= overL * b.Width() / width
		proj.bounds.P1[0] += overR * b.Width() / width
		proj.bounds.P1[1] += overT * b.Height() / mapHeight // canvas top = larger rotated Y
		proj.bounds.P0[1] -= overB * b.Height() / mapHeight
	}
	return proj
}

// drawArrowhead draws an arrowhead at the end of a line.
func drawArrowhead(drawList *imgui.DrawList, p1, p2 imgui.Vec2, color uint32) {
	// Calculate direction vector
	dx := p2.X - p1.X
	dy := p2.Y - p1.Y
	length := math.Sqrt(dx*dx + dy*dy)
	if length == 0 {
		return
	}

	// Normalize direction
	dx /= length
	dy /= length

	// Calculate arrowhead wing points using rotation
	sc := math.SinCos(arrowheadAngle)
	sin, cos := sc[0], sc[1]

	wing1 := imgui.Vec2{
		X: p2.X - arrowheadSize*(dx*cos-dy*sin),
		Y: p2.Y - arrowheadSize*(dx*sin+dy*cos),
	}

	wing2 := imgui.Vec2{
		X: p2.X - arrowheadSize*(dx*cos+dy*sin),
		Y: p2.Y - arrowheadSize*(dy*cos-dx*sin),
	}

	drawList.AddLine(p2, wing1, color)
	drawList.AddLine(p2, wing2, color)
}

// drawBriefMap issues every brief videomap draw — video-map lines, all
// annotations including airspace, and the hovered-airspace tooltip — directly
// into drawList in screen coordinates. Airspace draws were previously split
// out (back when the rest was baked into a cached imgui.DrawList); now that
// everything is rebuilt every frame there's no benefit to keeping them apart.
func drawBriefMap(drawList *imgui.DrawList, briefMap *brief.VideoMapBlock, videoMaps []av.STARSMap,
	state *client.SimState, disabledTCPs map[string]bool, cursorOverCanvas bool, cursorLatLon math.Point2LL, latLonToScreen func(math.Point2LL) imgui.Vec2) []error {
	videoMapColorU32 := imgui.ColorU32Vec4(videoMapColor)
	annotationColorU32 := imgui.ColorU32Vec4(annotationColor)
	highlightColorU32 := imgui.ColorU32Vec4(imgui.Vec4{X: 0.4, Y: 1.0, Z: 0.4, W: 1.0})
	textColorU32 := imgui.ColorU32Vec4(mapTextColor)

	type airspaceHit struct {
		tcp string
		vol av.ControllerAirspaceVolume
	}
	var airspaceHits []airspaceHit

	appendLabel := func(lines []string, pos imgui.Vec2) {
		labelY := pos.Y
		for _, labelLine := range lines {
			drawList.AddTextVec2(imgui.Vec2{X: pos.X, Y: labelY}, textColorU32, labelLine)
			labelY += imgui.TextLineHeight()
		}
	}

	// Reused across all polyline draws; AddPolyline takes a pointer to a
	// contiguous Vec2 array, so we project into this buffer once per polyline
	// rather than calling AddLine per segment. Pre-size to fit the longest
	// polyline across all sources so each `polyBuf = polyBuf[:0]` + appends
	// hits the existing backing array without growslice churn.
	maxPolyLen := 0
	for _, vm := range videoMaps {
		for _, line := range vm.Lines {
			maxPolyLen = max(maxPolyLen, len(line.Points))
		}
	}
	for _, ann := range briefMap.Annotations {
		maxPolyLen = max(maxPolyLen, len(ann.Vertices))
		if ann.Type == brief.AnnotationAirspace {
			for _, tcp := range resolveAirspaceTCPs(ann, state) {
				for _, vols := range state.Airspace[tcp] {
					for _, vol := range vols {
						for _, pts := range vol.Boundaries {
							maxPolyLen = max(maxPolyLen, len(pts))
						}
					}
				}
			}
		}
	}
	polyBuf := make([]imgui.Vec2, 0, maxPolyLen)
	drawPolyline := func(pts []math.Point2LL, color uint32, flags imgui.DrawFlags) {
		if len(pts) < 2 {
			return
		}
		polyBuf = polyBuf[:0]
		for _, p := range pts {
			polyBuf = append(polyBuf, latLonToScreen(p))
		}
		drawList.AddPolyline(&polyBuf[0], int32(len(polyBuf)), color, flags, 1.0)
	}
	drawAnnVertexPolyline := func(verts []brief.AnnotatedVertex, color uint32, flags imgui.DrawFlags) {
		if len(verts) < 2 {
			return
		}
		polyBuf = polyBuf[:0]
		for _, v := range verts {
			polyBuf = append(polyBuf, latLonToScreen(v.Point))
		}
		drawList.AddPolyline(&polyBuf[0], int32(len(polyBuf)), color, flags, 1.0)
	}

	for _, vm := range videoMaps {
		for _, line := range vm.Lines {
			drawPolyline(line.Points, videoMapColorU32, imgui.DrawFlagsNone)
		}
	}

	var errors []error
	for _, ann := range briefMap.Annotations {
		switch ann.Type {
		case brief.AnnotationLine, brief.AnnotationArrow:
			drawAnnVertexPolyline(ann.Vertices, annotationColorU32, imgui.DrawFlagsNone)
			for _, vertex := range ann.Vertices {
				text := vertexAnnotationText(vertex.Annotation)
				if text == "" {
					continue
				}
				pos := latLonToScreen(vertex.Point)
				drawList.AddCircleFilled(pos, waypointCircleRadius, annotationColorU32)
				drawList.AddTextVec2(imgui.Vec2{X: pos.X + labelOffsetPixels, Y: pos.Y}, textColorU32, text)
			}
			if ann.Type == brief.AnnotationArrow && len(ann.Vertices) >= 2 {
				lastIdx := len(ann.Vertices) - 1
				p1 := latLonToScreen(ann.Vertices[lastIdx-1].Point)
				p2 := latLonToScreen(ann.Vertices[lastIdx].Point)
				drawArrowhead(drawList, p1, p2, annotationColorU32)
			}
			if pos, _, ok := labelTopLeftAndSize(ann, latLonToScreen); ok {
				appendLabel(ann.Label, pos)
			}

		case brief.AnnotationPolygon:
			drawAnnVertexPolyline(ann.Vertices, annotationColorU32, imgui.DrawFlagsClosed)
			if pos, _, ok := labelTopLeftAndSize(ann, latLonToScreen); ok {
				appendLabel(ann.Label, pos)
			}

		case brief.AnnotationFix:
			if len(ann.Vertices) == 0 {
				continue
			}
			centerPt := latLonToScreen(ann.Vertices[0].Point)
			drawList.AddCircle(centerPt, fixCircleRadius, annotationColorU32)
			if pos, _, ok := labelTopLeftAndSize(ann, latLonToScreen); ok {
				appendLabel(ann.Label, pos)
			}

		case brief.AnnotationPoint:
			// "point:" only establishes a label anchor — no marker is drawn.
			if len(ann.Vertices) == 0 {
				continue
			}
			if pos, size, ok := labelTopLeftAndSize(ann, latLonToScreen); ok {
				labelY := pos.Y
				for _, labelLine := range ann.Label {
					lineWidth := imgui.CalcTextSize(labelLine).X
					labelX := pos.X + (size.X-lineWidth)/2
					drawList.AddTextVec2(imgui.Vec2{X: labelX, Y: labelY}, textColorU32, labelLine)
					labelY += imgui.TextLineHeight()
				}
			}

		case brief.AnnotationAirspace:
			for _, tcp := range resolveAirspaceTCPs(ann, state) {
				if disabledTCPs[string(tcp)] {
					continue
				}
				if tcpAirspace, ok := state.Airspace[tcp]; !ok {
					errors = append(errors, fmt.Errorf("%s: no airspace defined for TCP", tcp))
				} else {
					for _, vols := range tcpAirspace {
						for _, vol := range vols {
							hovered := cursorOverCanvas && vol.ContainsPoint(cursorLatLon)
							color := annotationColorU32
							thickness := float32(1.0)
							if hovered {
								color = highlightColorU32
								thickness = 2.5
								airspaceHits = append(airspaceHits, airspaceHit{tcp: string(tcp), vol: vol})
							}
							for _, pts := range vol.Boundaries {
								if len(pts) < 2 {
									continue
								}
								polyBuf = polyBuf[:0]
								for _, p := range pts {
									polyBuf = append(polyBuf, latLonToScreen(p))
								}
								// PointInPolygon2LL (the hover hit test above) treats
								// boundaries as closed, so close the ring here unless
								// it self-closes.
								flags := imgui.DrawFlagsClosed
								if pts[0] == pts[len(pts)-1] {
									flags = imgui.DrawFlagsNone
								}
								drawList.AddPolyline(&polyBuf[0], int32(len(polyBuf)), color, flags, thickness)
							}
						}
					}
				}
			}
		}
	}

	if len(airspaceHits) > 0 {
		sort.SliceStable(airspaceHits, func(i, j int) bool {
			if airspaceHits[i].vol.LowerLimit != airspaceHits[j].vol.LowerLimit {
				return airspaceHits[i].vol.LowerLimit < airspaceHits[j].vol.LowerLimit
			}
			return airspaceHits[i].tcp < airspaceHits[j].tcp
		})
		imgui.BeginTooltip()
		for _, h := range airspaceHits {
			imgui.Text(fmt.Sprintf("%-4s %s", h.tcp, formatAltitudeRange(h.vol.LowerLimit, h.vol.UpperLimit)))
		}
		imgui.EndTooltip()
	}

	return errors
}

// drawAnnotatedMap draws a video map with annotations inline in the brief.
// Returns a non-nil error when the map can't be drawn (missing file, failed
// projection, etc.); the caller renders that error against its own cursor
// position so it aligns with the brief's main column rather than with the
// centered map label.
func drawAnnotatedMap(briefMap *brief.VideoMapBlock, label string, width float32, cache *videoMapCache, state *client.SimState,
	controlClient *client.ControlClient, labelFont *renderer.Font, disabledTCPs map[string]bool, p platform.Platform, lg *log.Logger) []error {
	initialCursorX := imgui.CursorPosX()
	drawCenteredLabel(label, width, initialCursorX, labelFont)

	videoMapLibrary, err := loadVideoMapLibrary(briefMap, cache, controlClient)
	if err != nil {
		return []error{fmt.Errorf("loading video map: %w", err)}
	}

	videoMaps, errs := findVideoMapsFromLibrary(videoMapLibrary, briefMap.Maps)

	// Build the lat/lon → canvas projection (rotated by -magVar so magnetic
	// north points up, matching STARSPane).
	proj, err := calculateMapProjection(briefMap, videoMaps, state)
	if err != nil {
		return append(errs, err)
	}
	proj = expandBoundsForLabels(proj, briefMap.Annotations, width)

	b := proj.bounds
	mapHeight := width * b.Height() / b.Width()
	canvasSize := imgui.Vec2{X: width, Y: mapHeight}
	canvasPos := imgui.CursorScreenPos()
	canvasMax := imgui.Vec2{X: canvasPos.X + canvasSize.X, Y: canvasPos.Y + canvasSize.Y}

	imgui.PushStyleVarVec2(imgui.StyleVarWindowPadding, imgui.Vec2{})
	childFlags := imgui.WindowFlagsNoScrollbar | imgui.WindowFlagsNoScrollWithMouse
	if !imgui.BeginChildStrV(fmt.Sprintf("##map-%s", label), canvasSize, 0, childFlags) {
		imgui.EndChild()
		imgui.PopStyleVar()
		return errs
	}

	drawList := imgui.WindowDrawList()

	bgColor := imgui.ColorU32Vec4(mapBgColor)
	drawList.AddRectFilled(canvasPos, canvasMax, bgColor)

	// Disable line antialiasing for the brief map's polyline draws: AA fattens
	// each segment from 2 verts to 6, which dominates the secondary viewport's
	// vertex upload + draw cost (the cgo'd RenderPlatformWindowsDefault). The
	// brief is informational; jaggies don't matter. Restored before EndChild so
	// nothing else in the window inherits the change.
	const aaLineFlags = imgui.DrawListFlagsAntiAliasedLines | imgui.DrawListFlagsAntiAliasedLinesUseTex
	prevFlags := drawList.Flags()
	drawList.SetFlags(prevFlags &^ aaLineFlags)
	defer drawList.SetFlags(prevFlags)

	latLonToScreen := func(ll math.Point2LL) imgui.Vec2 {
		rotX, rotY := proj.latLonToRotNm(ll)
		return imgui.Vec2{
			X: canvasPos.X + (rotX-b.P0[0])/b.Width()*canvasSize.X,
			Y: canvasPos.Y + (1.0-(rotY-b.P0[1])/b.Height())*canvasSize.Y,
		}
	}
	screenToLatLon := func(pt imgui.Vec2) math.Point2LL {
		normX := (pt.X - canvasPos.X) / canvasSize.X
		normY := (pt.Y - canvasPos.Y) / canvasSize.Y
		rotX := b.P0[0] + normX*b.Width()
		rotY := b.P0[1] + (1.0-normY)*b.Height()
		return proj.rotNmToLatLon(rotX, rotY)
	}

	cursorOverCanvas := imgui.IsWindowHovered()
	var cursorLatLon math.Point2LL
	if cursorOverCanvas {
		cursorLatLon = screenToLatLon(imgui.MousePos())
	}

	errs = append(errs, drawBriefMap(drawList, briefMap, videoMaps, state, disabledTCPs,
		cursorOverCanvas, cursorLatLon, latLonToScreen)...)

	imgui.Dummy(canvasSize)

	if imgui.IsWindowHovered() && imgui.IsMouseReleased(imgui.MouseButtonLeft) {
		io := imgui.CurrentIO()
		if io.KeyCtrl() && io.KeyShift() {
			latLong := screenToLatLon(imgui.MousePos())
			p.GetClipboard().SetClipboard(strings.ReplaceAll(latLong.DMSString(), " ", ""))
		}
	}

	imgui.EndChild()
	imgui.PopStyleVar()

	imgui.Spacing()
	return errs
}
