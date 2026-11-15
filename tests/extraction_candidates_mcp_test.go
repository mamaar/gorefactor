package tests_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type extCandidateJSON struct {
	Symbols     []string `json:"symbols"`
	DependsOn   []string `json:"depends_on"`
	Tier        int      `json:"tier"`
	Lines       int      `json:"lines"`
	UsedBy      int      `json:"used_by"`
	NeedsExport []string `json:"needs_export"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Configs     []string `json:"configs"`
}

type extReportJSON struct {
	Candidates   []extCandidateJSON `json:"candidates"`
	TotalCount   int                `json:"total_count"`
	BuildConfigs []string           `json:"build_configs"`
}

// callExtractionCandidates runs extraction_candidates on package big of an
// inline workspace and returns the decoded report, or the error text.
func callExtractionCandidates(t *testing.T, files map[string]string, args map[string]any) (extReportJSON, string) {
	t.Helper()

	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "extraction_candidates", Arguments: args})
	if err != nil {
		t.Fatalf("extraction_candidates: %v", err)
	}

	if res.IsError {
		return extReportJSON{}, errText(res)
	}

	var out extReportJSON

	err = json.Unmarshal([]byte(errText(res)), &out)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}

	if out.TotalCount != len(out.Candidates) {
		t.Errorf("total_count %d, want %d", out.TotalCount, len(out.Candidates))
	}

	return out, ""
}

func extOK(t *testing.T, files map[string]string, args map[string]any) extReportJSON {
	t.Helper()

	out, msg := callExtractionCandidates(t, files, args)
	if msg != "" {
		t.Fatalf("extraction_candidates error: %s", msg)
	}

	return out
}

// extFind returns the candidate whose symbols include name.
func extFind(t *testing.T, out extReportJSON, name string) extCandidateJSON {
	t.Helper()

	for _, c := range out.Candidates {
		if slices.Contains(c.Symbols, name) {
			return c
		}
	}

	t.Fatalf("no candidate contains %s: %+v", name, out.Candidates)

	return extCandidateJSON{}
}

func extFirstSymbols(out extReportJSON) [][]string {
	var got [][]string
	for _, c := range out.Candidates {
		got = append(got, c.Symbols)
	}

	return got
}

const extRankingSrc = `package big

func Short() int { return 1 }

func Long() int {
	x := 1
	y := 2
	return x + y
}

func Both() int { return Short() + Long() }
`

func TestExtractionCandidatesMCP_RanksTierThenLines(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": extRankingSrc}, map[string]any{"package": "big"})

	want := [][]string{{"Long"}, {"Short"}, {"Both", "Long", "Short"}}
	if got := extFirstSymbols(out); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("candidates %v, want %v", got, want)
	}

	if out.Candidates[0].Tier != 0 || out.Candidates[0].Lines <= out.Candidates[1].Lines {
		t.Errorf("Long = %+v, Short = %+v: want tier 0 and more lines first", out.Candidates[0], out.Candidates[1])
	}

	both := out.Candidates[2]
	if both.Tier != 2 || !slices.Equal(both.DependsOn, []string{"Long", "Short"}) || both.File != "big/a.go" || both.Line != 11 {
		t.Errorf("Both = %+v", both)
	}
}

func TestExtractionCandidatesMCP_FuncCallingHelper(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": `package big

func UsesHelper() int { return help() }

func help() int { return 2 }
`}, map[string]any{"package": "big"})

	c := extFind(t, out, "UsesHelper")
	if c.Tier != 1 || !slices.Equal(c.Symbols, []string{"UsesHelper", "help"}) || !slices.Equal(c.DependsOn, []string{"help"}) {
		t.Errorf("UsesHelper = %+v", c)
	}
}

func TestExtractionCandidatesMCP_TypeMovesWithItsMethods(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": `package big

const limit = 3

type T struct{}

func (T) M() int { return limit }
`}, map[string]any{"package": "big"})

	for _, c := range out.Candidates {
		if slices.Contains(c.Symbols, "M") || slices.Contains(c.Symbols, "T.M") {
			t.Errorf("method listed as symbol: %+v", c)
		}
	}

	c := extFind(t, out, "T")
	if !slices.Equal(c.Symbols, []string{"T", "limit"}) || !slices.Equal(c.DependsOn, []string{"limit"}) || c.Tier != 1 {
		t.Errorf("T = %+v", c)
	}

	if c.Lines != 3 {
		t.Errorf("T lines = %d, want 3 (type and method)", c.Lines)
	}
}

func TestExtractionCandidatesMCP_IotaGroupIsOneCandidate(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": `package big

const (
	A = iota
	B
	C
)
`}, map[string]any{"package": "big"})

	want := [][]string{{"A", "B", "C"}}
	if got := extFirstSymbols(out); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("candidates %v, want %v", got, want)
	}
}

func TestExtractionCandidatesMCP_CycleIncludesEachOther(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": `package big

func a() int { return b() }

func b() int { return a() }
`}, map[string]any{"package": "big"})

	if len(out.Candidates) != 2 {
		t.Fatalf("candidates %+v, want one per function", out.Candidates)
	}

	a, b := out.Candidates[0], out.Candidates[1] // same tier and lines: by line
	if a.Tier != 1 || b.Tier != 1 {
		t.Errorf("tiers %d, %d, want 1, 1", a.Tier, b.Tier)
	}

	if !slices.Equal(a.DependsOn, []string{"b"}) || !slices.Equal(b.DependsOn, []string{"a"}) {
		t.Errorf("depends_on a=%v b=%v", a.DependsOn, b.DependsOn)
	}
}

func TestExtractionCandidatesMCP_TestsAreNotCandidatesButNeedExport(t *testing.T) {
	out := extOK(t, map[string]string{
		"big/a.go": `package big

func solo() int { return 1 }
`,
		"big/a_test.go": `package big

import "testing"

func testHelper() int { return solo() }

func TestSolo(t *testing.T) { _ = testHelper() }
`,
	}, map[string]any{"package": "big"})

	if len(out.Candidates) != 1 {
		t.Fatalf("candidates %+v, want only solo", out.Candidates)
	}

	c := out.Candidates[0]
	if !slices.Equal(c.Symbols, []string{"solo"}) || c.Tier != 0 || c.UsedBy != 0 {
		t.Errorf("solo = %+v, want a tier 0 candidate no production code uses", c)
	}

	if !slices.Equal(c.NeedsExport, []string{"solo"}) {
		t.Errorf("needs_export = %v, want [solo]", c.NeedsExport)
	}
}

func TestExtractionCandidatesMCP_NeedsExportAndUsedBy(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": `package big

func helper() int { return 1 }

func A() int { return helper() }

func B() int { return helper() }
`}, map[string]any{"package": "big"})

	h := extFind(t, out, "helper")
	if h.UsedBy != 2 || !slices.Equal(h.NeedsExport, []string{"helper"}) {
		t.Errorf("helper used_by=%d needs_export=%v, want 2, [helper]", h.UsedBy, h.NeedsExport)
	}

	a := extFind(t, out, "A")
	if !slices.Equal(a.Symbols, []string{"A", "helper"}) || a.UsedBy != 1 || !slices.Equal(a.NeedsExport, []string{"helper"}) {
		t.Errorf("A = %+v, want B counted as user of the set and helper exported", a)
	}

	if b := extFind(t, out, "B"); len(b.NeedsExport) != 1 {
		t.Errorf("B = %+v", b)
	}
}

func TestExtractionCandidatesMCP_ReferenceUnderBuildTagCounts(t *testing.T) {
	out := extOK(t, map[string]string{
		"big/a.go": `package big

func shared() int { return 1 }
`,
		"big/tagged.go": `//go:build neverset_a

package big

func Tagged() int { return shared() }
`,
	}, map[string]any{"package": "big"})

	if len(out.BuildConfigs) != 2 {
		t.Fatalf("build_configs = %v, want 2", out.BuildConfigs)
	}

	tagged := extFind(t, out, "Tagged")
	if tagged.Tier != 1 || !slices.Equal(tagged.DependsOn, []string{"shared"}) || len(tagged.Configs) != 1 {
		t.Errorf("Tagged = %+v, want a tier 1 candidate declared in one configuration", tagged)
	}

	shared := extFind(t, out, "shared")
	if shared.UsedBy != 1 || len(shared.Configs) != 0 {
		t.Errorf("shared = %+v, want used_by 1 from the tagged configuration, declared in all", shared)
	}
}

func TestExtractionCandidatesMCP_MaxDependenciesAndErrors(t *testing.T) {
	files := map[string]string{"big/a.go": extRankingSrc}

	zero := 0

	out := extOK(t, files, map[string]any{"package": "big", "max_dependencies": zero})
	if got, want := extFirstSymbols(out), [][]string{{"Long"}, {"Short"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("max_dependencies 0 = %v, want %v", got, want)
	}

	_, msg := callExtractionCandidates(t, files, map[string]any{"package": "nope"})
	if !strings.Contains(msg, `package "nope" not found`) {
		t.Errorf("unknown package error = %q", msg)
	}

	_, msg = callExtractionCandidates(t, files, map[string]any{"package": "big", "max_dependencies": -1})
	if !strings.Contains(msg, "max_dependencies must not be negative, got -1") {
		t.Errorf("negative error = %q", msg)
	}
}
