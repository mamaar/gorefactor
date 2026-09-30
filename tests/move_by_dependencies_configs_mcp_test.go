package tests_test

import (
	"context"
	"encoding/json"
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

func TestMoveByDependencies_SecondSymbolIntoSameTargetIsSkippedAsConflict(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc One() int { return 1 }\n\nfunc Two() int { return 2 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.One() + src.Two() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.One() + src.Two() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	assertFileContains(t, dir, "shared/shared.go", "func One() int")
	assertFileContains(t, dir, "src/src.go", "func Two() int")
	sites, _ := json.Marshal(out["skipped_sites"])
	for _, want := range []string{"Two not moved", "conflict"} {
		if !strings.Contains(string(sites), want) {
			t.Errorf("skipped_sites = %s, want it to mention %q", sites, want)
		}
	}
	goBuilds(t, dir, "")
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
