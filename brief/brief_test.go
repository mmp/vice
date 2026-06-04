// brief/brief_test.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package brief

import (
	"os"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"

	"github.com/yuin/goldmark/ast"
)

func TestMain(m *testing.M) {
	av.InitDB()
	os.Exit(m.Run())
}

// matchesAnyTCP reports whether any TCP in tcps matches the given pattern.
// Used to build the MatchesUserTCP callback in tests.
func matchesAnyTCP(tcps []string, pattern string) bool {
	for _, tcp := range tcps {
		if MatchesTCPPattern(tcp, pattern) {
			return true
		}
	}
	return false
}

// selectedConfig builds the SelectedConfig callback used by ParseOptions and
// ProcessConfigSelections from a group -> currently-selected-tag map.
func selectedConfig(selections map[string]string) func(string) string {
	return func(group string) string { return selections[group] }
}

func requireContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected %q to contain %q", got, want)
	}
}

func findConfigNodes(root ast.Node) []*ConfigurationsNode {
	var nodes []*ConfigurationsNode
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if cfg, ok := n.(*ConfigurationsNode); ok {
				nodes = append(nodes, cfg)
			}
		}
		return ast.WalkContinue, nil
	})
	return nodes
}

func findDocMapNodes(root ast.Node) []*DocMapNode {
	var nodes []*DocMapNode
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if dm, ok := n.(*DocMapNode); ok {
				nodes = append(nodes, dm)
			}
		}
		return ast.WalkContinue, nil
	})
	return nodes
}

// allErrors returns every error reported by a parse: the preprocessor-level
// list from ParseErrors() plus the inline *ErrorNode messages embedded in the
// AST.
func allErrors(p *ParsedMarkdown) []string {
	errs := append([]string(nil), p.ParseErrors()...)
	_ = ast.Walk(p.AST(), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if en, ok := n.(*ErrorNode); ok {
				errs = append(errs, en.Message)
			}
		}
		return ast.WalkContinue, nil
	})
	return errs
}

func TestMatchesTCPPattern(t *testing.T) {
	tests := []struct {
		name    string
		tcp     string
		pattern string
		want    bool
	}{
		{name: "exact match", tcp: "2K", pattern: "2K", want: true},
		{name: "exact mismatch", tcp: "2J", pattern: "2K", want: false},
		{name: "prefix wildcard", tcp: "5S", pattern: "5*", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesTCPPattern(tt.tcp, tt.pattern); got != tt.want {
				t.Fatalf("MatchesTCPPattern(%q, %q) = %v, want %v", tt.tcp, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestProcessConfigSelections(t *testing.T) {
	t.Run("returns input unchanged with no criteria", func(t *testing.T) {
		md := "alpha\n::: if jfk:4\nbeta\n::: endif jfk:4\ngamma"
		if got := ProcessConfigSelections(md, nil, nil, nil); got != md {
			t.Fatalf("unexpected rewrite:\n%s", got)
		}
	})

	t.Run("nested failing outer block suppresses matching inner block", func(t *testing.T) {
		md := strings.Join([]string{
			"start",
			"::: if outer:on",
			"outer",
			"::: if tcp:2*",
			"inner",
			"::: endif tcp:2*",
			"::: endif outer:on",
			"end",
		}, "\n")
		userTCPs := []string{"2J"}
		got := ProcessConfigSelections(md,
			selectedConfig(map[string]string{"other": "value"}),
			nil,
			func(pat string) bool { return matchesAnyTCP(userTCPs, pat) })
		if strings.Contains(got, "outer") || strings.Contains(got, "inner") {
			t.Fatalf("expected nested content to be skipped, got:\n%s", got)
		}
		if got != "start\nend" {
			t.Fatalf("unexpected output:\n%s", got)
		}
	})

	t.Run("tcp pattern matches any covered position", func(t *testing.T) {
		md := strings.Join([]string{
			"::: if tcp:1*",
			"lga",
			"::: endif tcp:1*",
			"::: if tcp:5*",
			"liberty",
			"::: endif tcp:5*",
			"::: if tcp:9*",
			"absent",
			"::: endif tcp:9*",
		}, "\n")
		// Controller signed in as 2G but consolidated owns 1D and 5A — both
		// tcp:1* and tcp:5* must match; tcp:9* must not.
		userTCPs := []string{"2G", "1D", "5A"}
		got := ProcessConfigSelections(md, nil, nil,
			func(pat string) bool { return matchesAnyTCP(userTCPs, pat) })
		requireContains(t, got, "lga")
		requireContains(t, got, "liberty")
		if strings.Contains(got, "absent") {
			t.Fatalf("tcp:9* should not match, got:\n%s", got)
		}
	})

	t.Run("selected configs airport and tcp conditions all participate", func(t *testing.T) {
		md := strings.Join([]string{
			"::: if dep:13",
			"selected",
			"::: endif dep:13",
			"::: if KJFK",
			"airport",
			"::: endif KJFK",
			"::: if tcp:2*",
			"tcp",
			"::: endif tcp:2*",
		}, "\n")
		airports := map[string]bool{"KJFK": true}
		userTCPs := []string{"2A"}
		got := ProcessConfigSelections(md,
			selectedConfig(map[string]string{"dep": "13"}),
			func(icao string) bool { return airports[icao] },
			func(pat string) bool { return matchesAnyTCP(userTCPs, pat) })
		for _, want := range []string{"selected", "airport", "tcp"} {
			requireContains(t, got, want)
		}
	})

	t.Run("mismatched and unclosed blocks inject explicit errors", func(t *testing.T) {
		md := strings.Join([]string{
			"::: if dep:13",
			"content",
			"::: endif dep:22",
			"::: if tcp:2*",
			"tail",
		}, "\n")
		got := ProcessConfigSelections(md,
			selectedConfig(map[string]string{"dep": "13"}), nil, nil)
		requireContains(t, got, `**ERROR: line 3: mismatched endif: expected "dep:13", got "dep:22"**`)
		// dep:13 is still open after the mismatched endif, plus tcp:2* on top.
		requireContains(t, got, `**ERROR: unclosed config block: "dep:13"**`)
		requireContains(t, got, `**ERROR: unclosed config block: "tcp:2*"**`)
	})

	t.Run("mismatched endif does not pop the stack", func(t *testing.T) {
		md := strings.Join([]string{
			"::: if dep:13",
			"first",
			"::: endif dep:22", // mismatched; should not pop dep:13
			"second",           // still inside dep:13 block
			"::: endif dep:13", // properly closes dep:13
			"after",
		}, "\n")
		got := ProcessConfigSelections(md,
			selectedConfig(map[string]string{"dep": "13"}), nil, nil)
		// Mismatched endif is flagged but doesn't cascade — both first and
		// second remain visible because dep:13 stayed active.
		requireContains(t, got, "first")
		requireContains(t, got, "second")
		requireContains(t, got, "after")
		if strings.Contains(got, "unclosed config block") {
			t.Fatalf("did not expect unclosed block error, got:\n%s", got)
		}
	})
}

func TestValidateConfigConditionSyntax(t *testing.T) {
	t.Run("validates syntax and unknown names", func(t *testing.T) {
		md := strings.Join([]string{
			"::: if dep:13",
			"good",
			"::: endif dep:13",
			"::: if UNKNOWN",
			"bad",
			"::: endif UNKNOWN",
			"::: endif stray",
			"::: if ",
		}, "\n")
		errs := validateConfigConditionSyntax(md)
		joined := strings.Join(errs, "\n")
		requireContains(t, joined, `undefined configuration "dep:13"`)
		requireContains(t, joined, `undefined configuration "UNKNOWN"`)
		requireContains(t, joined, `unexpected endif "stray"`)
		requireContains(t, joined, `empty ::: if condition`)
	})

	t.Run("flags unknown ::: directives", func(t *testing.T) {
		md := strings.Join([]string{
			"::: dif foo",
			"content",
			":::bogus",
		}, "\n")
		errs := validateConfigConditionSyntax(md)
		joined := strings.Join(errs, "\n")
		requireContains(t, joined, `line 1: unknown directive "::: dif foo"`)
		requireContains(t, joined, `line 3: unknown directive ":::bogus"`)
	})

	t.Run("permits forward references airports and tcp patterns", func(t *testing.T) {
		md := strings.Join([]string{
			"::: if dep:13,KJFK,tcp:2*",
			"ok",
			"::: endif dep:13,KJFK,tcp:2*",
			"```configurations dep",
			"13: South Flow",
			"```",
		}, "\n")
		if errs := validateConfigConditionSyntax(md); len(errs) != 0 {
			t.Fatalf("expected no errors, got %v", errs)
		}
	})
}

func TestGetInitialConfigSelectionsAndParseMarkdown(t *testing.T) {
	md := strings.Join([]string{
		"# Brief",
		"```configurations dep",
		"13: Depart 13",
		"22: Depart 22",
		"```",
		"```configurations arr",
		"4: Land 4",
		"31: Land 31",
		"```",
		"```configurations tcp",
		"x: bad",
		"```",
		"```configurations oops",
		"broken line",
		"13: valid after broken",
		"```",
	}, "\n")

	selections := GetInitialConfigSelections(md)
	if selections == nil {
		t.Fatal("expected initial selections")
	}
	if got := selections["dep"]; got != "13" {
		t.Fatalf("dep selection = %q, want %q", got, "13")
	}
	if got := selections["arr"]; got != "4" {
		t.Fatalf("arr selection = %q, want %q", got, "4")
	}
	if got := selections["oops"]; got != "13" {
		t.Fatalf("oops selection = %q, want %q", got, "13")
	}

	parsed := ParseMarkdown([]byte(md), ParseOptions{})
	errors := strings.Join(allErrors(parsed), "\n")
	requireContains(t, errors, `configuration group name "tcp" is reserved`)
	requireContains(t, errors, `invalid configuration format "broken line"`)

	nodes := findConfigNodes(parsed.AST())
	if len(nodes) != 3 {
		t.Fatalf("found %d config nodes, want 3", len(nodes))
	}
	if nodes[0].Options[0].Name != "13" {
		t.Fatalf("first dep option = %q", nodes[0].Options[0].Name)
	}
	if nodes[2].Options[0].Name != "13" {
		t.Fatalf("first oops option = %q", nodes[2].Options[0].Name)
	}
}

func TestUnnamedConfigurationsBlockRejected(t *testing.T) {
	md := strings.Join([]string{
		"```configurations",
		"13: Some option",
		"22: Other option",
		"```",
	}, "\n")

	parsed := ParseMarkdown([]byte(md), ParseOptions{})
	errors := strings.Join(allErrors(parsed), "\n")
	requireContains(t, errors, `configurations block requires a group name`)

	if nodes := findConfigNodes(parsed.AST()); len(nodes) != 0 {
		t.Fatalf("expected no config nodes for unnamed block, got %d", len(nodes))
	}
}

func TestDuplicateConfigurationsName(t *testing.T) {
	md := strings.Join([]string{
		"# Section A",
		"```configurations dep",
		"13: First Depart 13",
		"```",
		"# Section B",
		"```configurations dep",
		"22: Second Depart 22",
		"```",
	}, "\n")

	parsed := ParseMarkdown([]byte(md), ParseOptions{})
	errors := strings.Join(allErrors(parsed), "\n")
	requireContains(t, errors, `duplicate configurations block "dep"`)

	nodes := findConfigNodes(parsed.AST())
	if len(nodes) != 1 {
		t.Fatalf("expected only the first duplicate to materialize, got %d nodes", len(nodes))
	}
	if nodes[0].Options[0].Name != "13" {
		t.Fatalf("retained node option = %q, want %q", nodes[0].Options[0].Name, "13")
	}
}

func TestParseVideoMapContent(t *testing.T) {
	t.Run("ignores config control lines and parses annotations", func(t *testing.T) {
		content := strings.Join([]string{
			"::: if dep:13",
			"map: TEST MAP",
			"::: endif dep:13",
			"file: videomaps/test.gob.zst",
			"width: 80%",
			"point: N040.00.00.000,W073.00.00.000",
			"label: FIRST",
			"point: N041.00.00.000,W074.00.00.000",
			"label: SECOND",
			"arrow: N040.00.00.000,W073.00.00.000/d4000 N040.01.00.000,W073.01.00.000",
			"label: FLOW",
		}, "\n")

		docMap, err := parseVideoMapContent(content)
		if err != nil {
			t.Fatalf("parseVideoMapContent returned error: %v", err)
		}
		if got, want := docMap.Width, 80; got != want {
			t.Fatalf("width = %d, want %d", got, want)
		}
		if len(docMap.Maps) != 1 || docMap.Maps[0] != "TEST MAP" {
			t.Fatalf("unexpected maps: %#v", docMap.Maps)
		}
		if len(docMap.Annotations) != 3 {
			t.Fatalf("got %d annotations, want 3", len(docMap.Annotations))
		}
		if docMap.Annotations[0].Type != AnnotationPoint || docMap.Annotations[1].Type != AnnotationPoint {
			t.Fatalf("expected first two annotations to be points, got %#v", docMap.Annotations[:2])
		}
		if docMap.Annotations[2].Type != AnnotationArrow {
			t.Fatalf("expected third annotation to be arrow, got %v", docMap.Annotations[2].Type)
		}
		if ann := docMap.Annotations[2].Vertices[0].Annotation; ann == nil || ann.DescentAltitude != 4000 {
			t.Fatalf("expected descent annotation at first arrow vertex, got %#v", ann)
		}
	})

	t.Run("tolerates runs of whitespace in line/arrow/polygon point lists", func(t *testing.T) {
		content := strings.Join([]string{
			"file: videomaps/test.gob.zst",
			"map: TEST",
			"line: N040.00.00.000,W073.00.00.000   N041.00.00.000,W074.00.00.000\t\tN042.00.00.000,W075.00.00.000",
		}, "\n")

		docMap, err := parseVideoMapContent(content)
		if err != nil {
			t.Fatalf("parseVideoMapContent returned error: %v", err)
		}
		if len(docMap.Annotations) != 1 {
			t.Fatalf("got %d annotations, want 1", len(docMap.Annotations))
		}
		if got := len(docMap.Annotations[0].Vertices); got != 3 {
			t.Fatalf("got %d vertices, want 3", got)
		}
	})

	t.Run("returns parsing errors for malformed inputs", func(t *testing.T) {
		tests := []struct {
			name    string
			content string
			want    string
		}{
			{
				name: "invalid width",
				content: strings.Join([]string{
					"file: videomaps/test.gob.zst",
					"map: TEST MAP",
					"width: wide",
				}, "\n"),
				want: "invalid width value",
			},
			{
				name: "invalid extent",
				content: strings.Join([]string{
					"file: videomaps/test.gob.zst",
					"map: TEST MAP",
					"extent: onlyone",
				}, "\n"),
				want: "extent must have exactly 2 points",
			},
			{
				name: "invalid location",
				content: strings.Join([]string{
					"file: videomaps/test.gob.zst",
					"map: TEST MAP",
					"point: nowhere",
				}, "\n"),
				want: "invalid location",
			},
			{
				name: "invalid waypoint annotation",
				content: strings.Join([]string{
					"file: videomaps/test.gob.zst",
					"map: TEST MAP",
					"arrow: N040.00.00.000,W073.00.00.000/x4000 N040.01.00.000,W073.01.00.000",
				}, "\n"),
				want: "unknown annotation type",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := parseVideoMapContent(tt.content)
				if err == nil {
					t.Fatal("expected error")
				}
				requireContains(t, err.Error(), tt.want)
			})
		}
	})
}

func TestParseMarkdownTransformsNodes(t *testing.T) {
	md := strings.Join([]string{
		"```configurations dep",
		"13: Depart 13",
		"22: Depart 22",
		"```",
		"```videomap Sample Map",
		"map: TEST MAP",
		"file: videomaps/test.gob.zst",
		"arrow: N040.00.00.000,W073.00.00.000 N040.01.00.000,W073.01.00.000",
		"```",
	}, "\n")

	parsed := ParseMarkdown([]byte(md), ParseOptions{})
	if errs := allErrors(parsed); len(errs) != 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}

	cfgNodes := findConfigNodes(parsed.AST())
	if len(cfgNodes) != 1 {
		t.Fatalf("got %d config nodes, want 1", len(cfgNodes))
	}

	docMaps := findDocMapNodes(parsed.AST())
	if len(docMaps) != 1 {
		t.Fatalf("got %d doc map nodes, want 1", len(docMaps))
	}
	if docMaps[0].Label != "Sample Map" {
		t.Fatalf("doc map label = %q, want %q", docMaps[0].Label, "Sample Map")
	}
	if docMaps[0].DocMap.File != "videomaps/test.gob.zst" {
		t.Fatalf("doc map file = %q", docMaps[0].DocMap.File)
	}
}

func TestParseMarkdownVideoMapValidation(t *testing.T) {
	t.Run("valid brief de-duplicates referenced files", func(t *testing.T) {
		content := []byte(strings.Join([]string{
			"```configurations dep",
			"13: Depart 13",
			"```",
			"::: if dep:13",
			"shown",
			"::: endif dep:13",
			"```videomap One",
			"map: MAP-A",
			"file: videomaps/test.gob.zst",
			"arrow: N040.00.00.000,W073.00.00.000 N040.01.00.000,W073.01.00.000",
			"```",
			"```videomap Two",
			"map: MAP-B",
			"file: videomaps/test.gob.zst",
			"point: N040.02.00.000,W073.02.00.000",
			"label: POINT",
			"```",
		}, "\n"))

		parsed := ParseMarkdown(content, ParseOptions{})
		if errs := allErrors(parsed); len(errs) > 0 {
			t.Fatalf("unexpected parse errors: %v", errs)
		}
		if files := parsed.VideoMapFiles(); len(files) != 1 {
			t.Fatalf("expected single de-duplicated file entry, got %v", files)
		}
	})

	t.Run("zero-manifest validation still catches config syntax but skips map validation", func(t *testing.T) {
		content := []byte(strings.Join([]string{
			"::: if UNKNOWN",
			"bad",
			"::: endif UNKNOWN",
			"```videomap MissingMapsOkayHere",
			"map: NOT-CHECKED",
			"file: videomaps/missing.gob.zst",
			"point: N040.00.00.000,W073.00.00.000",
			"label: HERE",
			"```",
		}, "\n"))

		parsed := ParseMarkdown(content, ParseOptions{})
		if !slices.Contains(parsed.VideoMapFiles(), "videomaps/missing.gob.zst") {
			t.Fatalf("expected referenced file to be collected even without manifests, got %v", parsed.VideoMapFiles())
		}
		errs := strings.Join(allErrors(parsed), "\n")
		requireContains(t, errs, `undefined configuration "UNKNOWN"`)
		if strings.Contains(errs, "video map file") {
			t.Fatalf("expected map validation to be skipped, got: %s", errs)
		}
	})

	t.Run("reports annotation validation errors", func(t *testing.T) {
		content := []byte(strings.Join([]string{
			"```videomap Broken",
			"map: SOME-MAP",
			"file: videomaps/test.gob.zst",
			"line: N040.00.00.000,W073.00.00.000",
			"```",
		}, "\n"))

		parsed := ParseMarkdown(content, ParseOptions{})
		errs := strings.Join(allErrors(parsed), "\n")
		requireContains(t, errs, `line/arrow must have at least 2 points`)
	})
}

func TestParseVideoMapContentAirspace(t *testing.T) {
	t.Run("bare airspace draws all consolidated positions", func(t *testing.T) {
		content := strings.Join([]string{
			"map: TEST MAP",
			"file: videomaps/test.gob.zst",
			"airspace:",
		}, "\n")

		docMap, err := parseVideoMapContent(content)
		if err != nil {
			t.Fatalf("parseVideoMapContent returned error: %v", err)
		}
		if len(docMap.Annotations) != 1 {
			t.Fatalf("got %d annotations, want 1", len(docMap.Annotations))
		}
		ann := docMap.Annotations[0]
		if ann.Type != AnnotationAirspace {
			t.Fatalf("annotation type = %v, want AnnotationAirspace", ann.Type)
		}
		if len(ann.AirspaceTCPs) != 0 {
			t.Fatalf("expected empty AirspaceTCPs, got %v", ann.AirspaceTCPs)
		}
	})

	t.Run("airspace with TCP", func(t *testing.T) {
		content := strings.Join([]string{
			"map: TEST MAP",
			"file: videomaps/test.gob.zst",
			"airspace: 2J",
		}, "\n")

		docMap, err := parseVideoMapContent(content)
		if err != nil {
			t.Fatalf("parseVideoMapContent returned error: %v", err)
		}
		if len(docMap.Annotations) != 1 {
			t.Fatalf("got %d annotations, want 1", len(docMap.Annotations))
		}
		ann := docMap.Annotations[0]
		if ann.Type != AnnotationAirspace {
			t.Fatalf("annotation type = %v, want AnnotationAirspace", ann.Type)
		}
		if len(ann.AirspaceTCPs) != 1 || ann.AirspaceTCPs[0] != "2J" {
			t.Fatalf("AirspaceTCPs = %v, want [2J]", ann.AirspaceTCPs)
		}
	})

	t.Run("multiple airspace lines preserve order alongside other annotations", func(t *testing.T) {
		content := strings.Join([]string{
			"map: TEST MAP",
			"file: videomaps/test.gob.zst",
			"airspace: 2J",
			"point: N040.00.00.000,W073.00.00.000",
			"label: HERE",
			"airspace: 4J",
		}, "\n")

		docMap, err := parseVideoMapContent(content)
		if err != nil {
			t.Fatalf("parseVideoMapContent returned error: %v", err)
		}
		if len(docMap.Annotations) != 3 {
			t.Fatalf("got %d annotations, want 3", len(docMap.Annotations))
		}
		types := []AnnotationType{docMap.Annotations[0].Type, docMap.Annotations[1].Type, docMap.Annotations[2].Type}
		want := []AnnotationType{AnnotationAirspace, AnnotationPoint, AnnotationAirspace}
		for i := range types {
			if types[i] != want[i] {
				t.Fatalf("annotation %d type = %v, want %v", i, types[i], want[i])
			}
		}
		if docMap.Annotations[0].AirspaceTCPs[0] != "2J" {
			t.Fatalf("first airspace TCP = %q, want 2J", docMap.Annotations[0].AirspaceTCPs[0])
		}
		if docMap.Annotations[2].AirspaceTCPs[0] != "4J" {
			t.Fatalf("third airspace TCP = %q, want 4J", docMap.Annotations[2].AirspaceTCPs[0])
		}
	})
}
