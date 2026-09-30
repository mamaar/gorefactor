package tests_test

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const mbdSrc = "package src\n\nfunc Shared() int { return 1 }\n"

func mbdWorkspace(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"src/src.go": mbdSrc,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Shared() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Shared() }\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	return writeInlineWorkspace(t, files)
}

func TestMoveByDependencies_SymbolBreakingTaggedFileIsSkippedAndNamed(t *testing.T) {
	dir := mbdWorkspace(t, map[string]string{
		"a/a_tagx.go": "//go:build tagx\n\npackage a\n\nimport \"example.com/threadmcp/src\"\n\nfunc Tagged() int { return src.Shared() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	for _, want := range []string{"Shared", "a/a_tagx.go", "tags=tagx"} {
		if !strings.Contains(string(sites), want) {
			t.Errorf("skipped_sites = %s, want it to mention %q", sites, want)
		}
	}
	assertFileUnchanged(t, dir, "src/src.go", mbdSrc)
	goVets(t, dir, "", "tagx")
}

func TestMoveByDependencies_SymbolWithNoConfigProblemIsMovedAndBuilds(t *testing.T) {
	dir := mbdWorkspace(t, map[string]string{
		"a/a_tagx.go": "//go:build tagx\n\npackage a\n\nfunc Tagged() int { return A() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	if _, ok := out["skipped_sites"]; ok {
		t.Errorf("skipped_sites = %v, want none", out["skipped_sites"])
	}
	if configs, _ := out["build_configs"].([]any); len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	assertFileContains(t, dir, "shared/shared.go", "func Shared() int")
	assertFileContains(t, dir, "a/a.go", "shared.Shared()")
	goBuilds(t, dir, "", "tagx")
}

func TestMoveByDependencies_TwoSymbolsFromOnePackageMoveInOneCall(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc One() int { return 1 }\n\nfunc Two() int { return 2 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.One() + src.Two() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.One() + src.Two() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	if _, ok := out["skipped_sites"]; ok {
		t.Errorf("skipped_sites = %v, want none", out["skipped_sites"])
	}
	assertFileContains(t, dir, "shared/shared.go", "func One() int")
	assertFileContains(t, dir, "shared/shared.go", "func Two() int")
	for _, rel := range []string{"a/a.go", "b/b.go"} {
		assertFileContains(t, dir, rel, "shared.One() + shared.Two()")
		if got := fileText(t, dir, rel); strings.Contains(got, "threadmcp/src\"") {
			t.Errorf("%s still imports src:\n%s", rel, got)
		}
	}
	goVets(t, dir, "")
}

func TestMoveByDependencies_SymbolsFromTwoPackagesMoveToOneTargetInOneCall(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src1/src1.go": "package src1\n\nfunc One() int { return 1 }\n",
		"src2/src2.go": "package src2\n\nfunc Two() int { return 2 }\n",
		"a/a.go":       "package a\n\nimport (\n\t\"example.com/threadmcp/src1\"\n\t\"example.com/threadmcp/src2\"\n)\n\nfunc A() int { return src1.One() + src2.Two() }\n",
		"b/b.go":       "package b\n\nimport (\n\t\"example.com/threadmcp/src1\"\n\t\"example.com/threadmcp/src2\"\n)\n\nfunc B() int { return src1.One() + src2.Two() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	if _, ok := out["skipped_sites"]; ok {
		t.Errorf("skipped_sites = %v, want none", out["skipped_sites"])
	}
	assertFileContains(t, dir, "shared/shared.go", "func One() int")
	assertFileContains(t, dir, "shared/shared.go", "func Two() int")
	for _, rel := range []string{"a/a.go", "b/b.go"} {
		assertFileContains(t, dir, rel, "shared.One() + shared.Two()")
	}
	goVets(t, dir, "")
}

func TestMoveByDependencies_RefusedCandidateIsReportedAndTheOthersMove(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":  "package src\n\nfunc One() int { return 1 }\n\nfunc Two() int { return 2 }\n\nfunc Three() int { return 3 }\n",
		"a/a.go":      "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.One() + src.Two() + src.Three() }\n",
		"b/b.go":      "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.One() + src.Two() + src.Three() }\n",
		"a/a_tagx.go": "//go:build tagx\n\npackage a\n\nimport \"example.com/threadmcp/src\"\n\nfunc Tagged() int { return src.Two() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	for _, want := range []string{"Two not moved", "a/a_tagx.go"} {
		if !strings.Contains(string(sites), want) {
			t.Errorf("skipped_sites = %s, want it to mention %q", sites, want)
		}
	}
	for _, name := range []string{"One", "Three"} {
		assertFileContains(t, dir, "shared/shared.go", "func "+name+"() int")
	}
	if got := fileText(t, dir, "shared/shared.go"); strings.Contains(got, "func Two") {
		t.Errorf("Two moved although a build configuration needs it in src:\n%s", got)
	}
	assertFileContains(t, dir, "src/src.go", "func Two() int")
	goVets(t, dir, "", "tagx")
}

func TestMoveByDependencies_TypeWithMethodsAndFunctionFromSamePackageMoveTogether(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\ntype Box struct{ N int }\n\nfunc (b Box) Get() int { return b.N }\n\nfunc Make() int { return 1 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Box{N: src.Make()}.Get() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Box{N: src.Make()}.Get() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	if _, ok := out["skipped_sites"]; ok {
		t.Errorf("skipped_sites = %v, want none", out["skipped_sites"])
	}
	assertFileContains(t, dir, "shared/shared.go", "type Box struct")
	assertFileContains(t, dir, "shared/shared.go", "func (b Box) Get() int")
	assertFileContains(t, dir, "shared/shared.go", "func Make() int")
	assertFileContains(t, dir, "a/a.go", "shared.Box{N: shared.Make()}.Get()")
	goVets(t, dir, "")
}

func TestMoveByDependencies_DryRunReturnsCombinedDiffAndWritesNothing(t *testing.T) {
	files := map[string]string{
		"shared/shared.go": "package shared\n",
		"src/src.go":       "package src\n\nfunc One() int { return 1 }\n\nfunc Two() int { return 2 }\n",
		"a/a.go":           "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.One() + src.Two() }\n",
		"b/b.go":           "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.One() + src.Two() }\n",
	}
	dir := writeInlineWorkspace(t, maps.Clone(files))
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared", "dry_run": true})

	diff, _ := json.Marshal(out)
	for _, want := range []string{"func One()", "func Two()", "shared.One"} {
		if !strings.Contains(string(diff), want) {
			t.Errorf("dry_run output lacks %q:\n%s", want, diff)
		}
	}
	for rel, content := range files {
		assertFileUnchanged(t, dir, rel, content)
	}
}

func TestMoveByDependencies_SymbolUsingAnotherMovedPackageSymbolIsSkippedAndTheRestMove(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src1/src1.go": "package src1\n\nimport \"example.com/threadmcp/src2\"\n\nfunc One() int { return src2.Two() }\n",
		"src2/src2.go": "package src2\n\nfunc Two() int { return 2 }\n",
		"a/a.go":       "package a\n\nimport (\n\t\"example.com/threadmcp/src1\"\n\t\"example.com/threadmcp/src2\"\n)\n\nfunc A() int { return src1.One() + src2.Two() }\n",
		"b/b.go":       "package b\n\nimport (\n\t\"example.com/threadmcp/src1\"\n\t\"example.com/threadmcp/src2\"\n)\n\nfunc B() int { return src1.One() + src2.Two() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	if !strings.Contains(string(sites), "One not moved") {
		t.Errorf("skipped_sites = %s, want One reported", sites)
	}
	assertFileContains(t, dir, "shared/shared.go", "func Two() int")
	assertFileContains(t, dir, "src1/src1.go", "func One() int")
	goVets(t, dir, "")
}

func TestMoveByDependencies_SameNameFromTwoPackagesMovesOnlyTheFirst(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src1/src1.go": "package src1\n\nfunc Dup() int { return 1 }\n",
		"src2/src2.go": "package src2\n\nfunc Dup() int { return 2 }\n",
		"a/a.go":       "package a\n\nimport (\n\t\"example.com/threadmcp/src1\"\n\t\"example.com/threadmcp/src2\"\n)\n\nfunc A() int { return src1.Dup() + src2.Dup() }\n",
		"b/b.go":       "package b\n\nimport (\n\t\"example.com/threadmcp/src1\"\n\t\"example.com/threadmcp/src2\"\n)\n\nfunc B() int { return src1.Dup() + src2.Dup() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	if !strings.Contains(string(sites), "Dup not moved") {
		t.Errorf("skipped_sites = %s, want the second Dup reported", sites)
	}
	assertFileContains(t, dir, "src2/src2.go", "func Dup() int")
	goVets(t, dir, "")
}

func TestMoveByDependencies_SymbolAlreadyInTargetPackageIsNotACandidate(t *testing.T) {
	files := map[string]string{
		"shared/shared.go": "package shared\n\nfunc Old() int { return 1 }\n",
		"a/a.go":           "package a\n\nimport \"example.com/threadmcp/shared\"\n\nfunc A() int { return shared.Old() }\n",
		"b/b.go":           "package b\n\nimport \"example.com/threadmcp/shared\"\n\nfunc B() int { return shared.Old() }\n",
	}
	for _, analyzeOnly := range []bool{true, false} {
		dir := writeInlineWorkspace(t, maps.Clone(files))
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)

		out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared", "analyze_only": analyzeOnly})
		sess.Close()

		got, _ := json.Marshal(out)
		if strings.Contains(string(got), "Old") {
			t.Errorf("analyze_only=%v: output = %s, want no mention of Old", analyzeOnly, got)
		}
	}
}

const dotImportA = "package a\n\nimport . \"example.com/threadmcp/src\"\n\nfunc A() int { return Shared() }\n"

func suggestedSymbols(t *testing.T, sess *mcptest.Session) []string {
	t.Helper()
	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared", "analyze_only": true})
	impact, _ := out["impact"].(map[string]any)
	moves, _ := impact["suggested_moves"].([]any)
	var names []string
	for _, m := range moves {
		names = append(names, m.(map[string]any)["symbol"].(string))
	}
	return names
}

func TestMoveByDependencies_CandidatesDoNotDependOnEarlierTypeChecking(t *testing.T) {
	extra := map[string]string{"a/a.go": dotImportA}

	fresh := mcptest.Dial(context.Background(), t, mcpTransport(), mbdWorkspace(t, extra))
	defer fresh.Close()
	got := suggestedSymbols(t, fresh)

	warm := mcptest.Dial(context.Background(), t, mcpTransport(), mbdWorkspace(t, extra))
	defer warm.Close()
	callJSON(t, warm, "rename_symbol", map[string]any{"symbol": "A", "new_name": "AA", "dry_run": true})
	want := suggestedSymbols(t, warm)

	if !slices.Equal(got, want) || !slices.Equal(want, []string{"Shared"}) {
		t.Errorf("fresh suggestions = %v, after type-checking = %v, want both [Shared]", got, want)
	}
}

func TestMoveByDependencies_AnalyzeOnlyReportsSuggestedMovesAndChangesNothing(t *testing.T) {
	dir := mbdWorkspace(t, nil)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared", "analyze_only": true})

	if out["description"] != "dependency analysis" || out["change_count"] != float64(0) {
		t.Errorf("description = %v, change_count = %v, want dependency analysis and 0", out["description"], out["change_count"])
	}
	impact, _ := json.Marshal(out["impact"])
	for _, want := range []string{`"suggested_moves"`, `"symbol":"Shared"`, `example.com/threadmcp/a`, `example.com/threadmcp/b`} {
		if !strings.Contains(string(impact), want) {
			t.Errorf("impact = %s, want it to contain %s", impact, want)
		}
	}
	assertFileUnchanged(t, dir, "src/src.go", mbdSrc)
}
