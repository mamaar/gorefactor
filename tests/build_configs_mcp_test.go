package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func buildConfigTags(t *testing.T, out map[string]any, i int) []any {
	t.Helper()
	configs, _ := out["build_configs"].([]any)
	if len(configs) <= i {
		t.Fatalf("build_configs has %d entries, want index %d: %v", len(configs), i, out)
	}
	tags, _ := configs[i].(map[string]any)["tags"].([]any)
	return tags
}

func assertBuildConfigReport(t *testing.T, tool string, out map[string]any) {
	t.Helper()
	configs, _ := out["build_configs"].([]any)
	if len(configs) != 2 {
		t.Fatalf("%s build_configs = %v, want 2 entries", tool, configs)
	}
	if !slices.Contains(buildConfigTags(t, out, 1), any("neverset_a")) {
		t.Errorf("%s second config tags = %v, want neverset_a", tool, buildConfigTags(t, out, 1))
	}
	uncovered, _ := out["uncovered_files"].([]any)
	if len(uncovered) != 1 {
		t.Fatalf("%s uncovered_files = %v, want 1 entry", tool, uncovered)
	}
	file := uncovered[0].(map[string]any)["file"]
	if file != "gen.go" {
		t.Errorf("%s uncovered file = %v, want gen.go", tool, file)
	}
}

func TestBuildConfigsReportedAndRecomputedAfterRefresh(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a.go":      "package app\n",
		"tagged.go": "//go:build neverset_a\n\npackage app\n",
		"gen.go":    "//go:build ignore\n\npackage main\n",
	})
	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	assertBuildConfigReport(t, "load_workspace", callJSON(t, sess, "load_workspace", map[string]any{"path": dir}))
	assertBuildConfigReport(t, "workspace_status", callJSON(t, sess, "workspace_status", map[string]any{}))

	extra := "//go:build neverset_b\n\npackage app\n"
	err := os.WriteFile(filepath.Join(dir, "tagged_b.go"), []byte(extra), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	status := callJSON(t, sess, "workspace_status", map[string]any{})
	if !slices.Contains(buildConfigTags(t, status, 1), any("neverset_b")) {
		t.Errorf("after refresh, second config tags = %v, want neverset_b", buildConfigTags(t, status, 1))
	}
}
