package tests_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
	"github.com/mamaar/gorefactor/tests/mcptest"
)

func renamePackageWorkspace() map[string]string {
	return map[string]string{
		"old/p.go":             "package old\n\nfunc P() {}\n",
		"old/p_test.go":        "package old\n\nimport \"testing\"\n\nfunc TestP(t *testing.T) { P() }\n",
		"old/x_test.go":        "package old_test\n\nimport (\n\t\"testing\"\n\n\t\"scratch/old\"\n)\n\nfunc TestX(t *testing.T) { old.P() }\n",
		"old/p_plan9.go":       "package old\n\nfunc Plan9() {}\n",
		"old/gen.go":           "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		"use/use.go":           "package use\n\nimport \"scratch/old\"\n\nfunc U() { old.P() }\n",
		"use/use_plan9.go":     "package use\n\nimport \"scratch/old\"\n\nfunc U9() { old.Plan9() }\n",
		"use/use_test.go":      "package use\n\nimport \"scratch/old\"\n\nfunc init() { old.P() }\n",
		"use/use_tagx_test.go": "//go:build tagx\n\npackage use\n\nimport \"scratch/old\"\n\nfunc init() { old.P() }\n",
	}
}

func assertRenamedEverywhere(t *testing.T, dir string) {
	t.Helper()
	for rel, want := range map[string]string{
		"old/p.go":       "package fresh\n",
		"old/p_test.go":  "package fresh\n",
		"old/x_test.go":  "package fresh_test\n",
		"old/p_plan9.go": "package fresh\n",
		"old/gen.go":     "package main\n",
	} {
		assertFileContains(t, dir, rel, want)
	}
	assertFileContains(t, dir, "old/x_test.go", "fresh.P()")
	for _, rel := range []string{"use/use.go", "use/use_plan9.go", "use/use_test.go", "use/use_tagx_test.go"} {
		got := readRel(t, dir, rel)
		if !strings.Contains(got, "fresh.") || strings.Contains(got, "old.") {
			t.Errorf("%s qualified references not renamed:\n%s", rel, got)
		}
	}
}

func TestRenamePackageRewritesFilesOfEveryConfiguration(t *testing.T) {
	dir := writeFiles(t, renamePackageWorkspace())
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.RenamePackage(ws, types.RenamePackageRequest{
		PackagePath:    filepath.Join(dir, "old"),
		OldPackageName: "old",
		NewPackageName: "fresh",
		UpdateImports:  true,
	})
	if err != nil {
		t.Fatalf("RenamePackage: %v", err)
	}
	if len(plan.Coverage.Configs) < 2 {
		t.Errorf("Coverage.Configs = %v, want every configuration", plan.Coverage.Configs)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertRenamedEverywhere(t, dir)
}

func TestRenamePackageRefusesUnparseableFileNeedingClauseRename(t *testing.T) {
	files := renamePackageWorkspace()
	files["old/broken_plan9.go"] = "func broken(\n"
	dir := writeFiles(t, files)
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	_, err := eng.RenamePackage(ws, types.RenamePackageRequest{
		PackagePath:    filepath.Join(dir, "old"),
		OldPackageName: "old",
		NewPackageName: "fresh",
		UpdateImports:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "broken_plan9.go") {
		t.Fatalf("want refusal naming broken_plan9.go, got %v", err)
	}
}

func TestBatchRenamePackageRewritesFilesOfEveryConfiguration(t *testing.T) {
	dir := writeFiles(t, renamePackageWorkspace())
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "batch_operations", batchArgs(false,
		`{"type":"rename_package","package_path":"old","new_name":"fresh"}`))

	assertRenamedEverywhere(t, dir)
}
