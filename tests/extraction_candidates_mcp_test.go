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
	Blockers    []string `json:"blockers"`
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

// extFind returns the candidate whose own unit holds name: the symbols
// without the ones it depends on.
func extFind(t *testing.T, out extReportJSON, name string) extCandidateJSON {
	t.Helper()

	for _, c := range out.Candidates {
		if slices.Contains(c.Symbols, name) && !slices.Contains(c.DependsOn, name) {
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

// extBlocked runs extraction_candidates and then move_symbol dry_run on the
// symbols of the candidate holding name. It asserts the candidate lists a
// blocker containing want, and that the move is refused with every blocker's
// text in its message.
func extBlocked(t *testing.T, files map[string]string, name, want string) extCandidateJSON {
	t.Helper()

	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "extraction_candidates", Arguments: map[string]any{"package": "big"}})
	if err != nil || res.IsError {
		t.Fatalf("extraction_candidates: %v %s", err, errText(res))
	}

	var out extReportJSON

	err = json.Unmarshal([]byte(errText(res)), &out)
	if err != nil {
		t.Fatal(err)
	}

	c := extFind(t, out, name)
	if !slices.ContainsFunc(c.Blockers, func(b string) bool { return strings.Contains(b, want) }) {
		t.Fatalf("%s blockers = %q, want one containing %q", name, c.Blockers, want)
	}

	move, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "move_symbol", Arguments: map[string]any{
		"symbols": c.Symbols, "from_package": "big", "to_package": "dest", "dry_run": true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	refusal := strings.ReplaceAll(errText(move), dir+"/", "")
	if !move.IsError {
		t.Fatalf("move_symbol %v was not refused: %s", c.Symbols, refusal)
	}

	for _, b := range c.Blockers {
		if !strings.Contains(refusal, b) && !strings.Contains(b, "build configuration") {
			t.Errorf("refusal %q lacks blocker %q", refusal, b)
		}
	}

	return c
}

func TestExtractionCandidatesMCP_BlockerMethodInTestFile(t *testing.T) {
	extBlocked(t, map[string]string{
		"big/a.go":      "package big\n\ntype T struct{}\n",
		"big/a_test.go": "package big\n\nfunc (T) Helper() {}\n",
	}, "T", "its method Helper is declared in the test file big/a_test.go:3")
}

func TestExtractionCandidatesMCP_BlockerUnexportedFieldUsedByStayingCode(t *testing.T) {
	extBlocked(t, map[string]string{"big/a.go": `package big

type T struct{ n int }

func Use(t T) int { return t.n }
`}, "T", "unexported n moves with it")
}

func TestExtractionCandidatesMCP_BlockerUnexportedInterfaceMethod(t *testing.T) {
	extBlocked(t, map[string]string{"big/a.go": `package big

type I interface{ m() }

type T struct{}

func (T) m() {}
`}, "T", "implements I through the unexported method m")
}

func TestExtractionCandidatesMCP_BlockerGeneratedFile(t *testing.T) {
	extBlocked(t, map[string]string{
		"big/a.go":   "package big\n",
		"big/gen.go": "// Code generated by tool. DO NOT EDIT.\n\npackage big\n\nfunc Gen() int { return 1 }\n",
	}, "Gen", "in the generated file big/gen.go")
}

func TestExtractionCandidatesMCP_BlockerOutsideDefaultConfig(t *testing.T) {
	files := map[string]string{
		"big/a.go":      "package big\n\nfunc shared() int { return 1 }\n",
		"big/tagged.go": "//go:build neverset_a\n\npackage big\n\nfunc Tagged() int { return shared() }\n",
	}

	extBlocked(t, files, "shared", "outside the default build configuration")
	extBlocked(t, files, "Tagged", "outside the default build configuration")
}

func TestExtractionCandidatesMCP_BlockedSortAfterUnblockedInTier(t *testing.T) {
	out := extOK(t, map[string]string{
		"big/a.go":      "package big\n\ntype T struct {\n\ta int\n\tb int\n\tc int\n}\n\nfunc Free() int { return 1 }\n",
		"big/a_test.go": "package big\n\nfunc (T) Helper() {}\n",
	}, map[string]any{"package": "big"})

	if got := extFirstSymbols(out); !slices.EqualFunc(got, [][]string{{"Free"}, {"T"}}, slices.Equal) {
		t.Errorf("candidates %v, want the unblocked Free before the larger blocked T", got)
	}
}

func TestExtractionCandidatesMCP_MethodOnAliasBelongsToTarget(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": `package big

type B struct{}

type Al = B

func (Al) Bye() {}
`}, map[string]any{"package": "big"})

	b := extFind(t, out, "B")
	if !slices.Equal(b.Symbols, []string{"B"}) || b.Tier != 0 {
		t.Errorf("B = %+v, want a tier 0 candidate that does not depend on its alias", b)
	}

	for _, c := range out.Candidates {
		if slices.Contains(c.Symbols, "Bye") {
			t.Errorf("method listed as symbol: %+v", c)
		}
	}
}

func TestExtractionCandidatesMCP_TiesSortBySymbolName(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": "package big\n\nfunc f() {}; func e() {}; func d() {}; func c() {}; func b() {}; func a() {}\n"},
		map[string]any{"package": "big"})

	var got []string
	for _, c := range out.Candidates {
		got = append(got, c.Symbols...)
	}

	if want := []string{"a", "b", "c", "d", "e", "f"}; !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestExtractionCandidatesMCP_MultiNameVarIsOneCandidate(t *testing.T) {
	out := extOK(t, map[string]string{"big/a.go": "package big\n\nvar x, y = 1, 2\n\nvar z = 3\n"}, map[string]any{"package": "big"})

	if got, want := extFirstSymbols(out), [][]string{{"x", "y"}, {"z"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("candidates %v, want %v", got, want)
	}
}

func TestExtractionCandidatesMCP_InitAndMainAreNotCandidates(t *testing.T) {
	out := extOK(t, map[string]string{"cmdx/a.go": "package main\n\nfunc init() {}\n\nfunc main() { helper() }\n\nfunc helper() {}\n"},
		map[string]any{"package": "cmdx"})

	if got, want := extFirstSymbols(out), [][]string{{"helper"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("candidates %v, want only helper", got)
	}

	if c := out.Candidates[0]; c.UsedBy != 1 {
		t.Errorf("helper used_by = %d, want 1 (main)", c.UsedBy)
	}
}

func TestExtractionCandidatesMCP_ExportedNamesNeedNoExport(t *testing.T) {
	out := extOK(t, map[string]string{
		"big/a.go":      "package big\n\nfunc Helper() int { return 1 }\n\nfunc A() int { return Helper() }\n",
		"big/a_test.go": "package big\n\nimport \"testing\"\n\nfunc TestH(t *testing.T) { _ = Helper() }\n",
	}, map[string]any{"package": "big"})

	for _, name := range []string{"Helper", "A"} {
		c := extFind(t, out, name)
		if len(c.NeedsExport) != 0 {
			t.Errorf("%s needs_export = %v, want none", name, c.NeedsExport)
		}
	}
}

func TestExtractionCandidatesMCP_UnionOfEdgesAcrossConfigs(t *testing.T) {
	out := extOK(t, map[string]string{
		"big/a.go":      "package big\n\ntype T struct{}\n\nfunc helper() int { return 1 }\n",
		"big/tagged.go": "//go:build neverset_a\n\npackage big\n\nfunc (T) M() int { return helper() }\n",
	}, map[string]any{"package": "big"})

	c := extFind(t, out, "T")
	if !slices.Equal(c.DependsOn, []string{"helper"}) || len(c.Configs) != 0 || c.Lines != 3 {
		t.Errorf("T = %+v, want the tagged method to add the helper dependency and its line, declared in all configurations", c)
	}
}

func TestExtractionCandidatesMCP_PackageByDirAndImportPath(t *testing.T) {
	files := map[string]string{"big/a.go": extRankingSrc}

	want := extOK(t, files, map[string]any{"package": "big"})

	for _, pkg := range []string{"example.com/threadmcp/big", "./big"} {
		got := extOK(t, files, map[string]any{"package": pkg})
		if !slices.EqualFunc(extFirstSymbols(got), extFirstSymbols(want), slices.Equal) || len(got.Candidates) != 3 {
			t.Errorf("package %q = %v, want %v", pkg, extFirstSymbols(got), extFirstSymbols(want))
		}
	}
}
