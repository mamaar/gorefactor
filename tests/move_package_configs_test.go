package tests_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
	"github.com/mamaar/gorefactor/tests/mcptest"
)

const modLine = "module scratch\n\ngo 1.22\n"

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = modLine
	for rel, content := range files {
		mustWrite(t, filepath.Join(dir, rel), content)
	}
	return dir
}

func readRel(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func assertGone(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
		t.Errorf("%s should be gone, stat err=%v", rel, err)
	}
}

func movePackageWorkspace() map[string]string {
	return map[string]string{
		"p/p.go":               "package p\n\nfunc P() {}\n",
		"p/p_plan9.go":         "package p\n\nfunc Plan9() {}\n",
		"p/p_tagx.go":          "//go:build tagx\n\npackage p\n\nfunc Tagx() {}\n",
		"p/gen.go":             "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		"use/use.go":           "package use\n\nimport \"scratch/p\"\n\nfunc U() { p.P() }\n",
		"use/use_plan9.go":     "package use\n\nimport \"scratch/p\"\n\nfunc U9() { p.Plan9() }\n",
		"use/use_tagx_test.go": "//go:build tagx\n\npackage use\n\nimport \"scratch/p\"\n\nfunc init() { p.P() }\n",
	}
}

func TestMovePackageMovesAndRewritesFilesOfEveryConfiguration(t *testing.T) {
	dir := writeFiles(t, movePackageWorkspace())
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}
	if len(plan.Coverage.Configs) < 2 {
		t.Errorf("Coverage.Configs = %v, want every configuration", plan.Coverage.Configs)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	for _, f := range []string{"p.go", "p_plan9.go", "p_tagx.go", "gen.go"} {
		assertGone(t, dir, "p/"+f)
	}
	for _, f := range []string{"q/p.go", "q/p_plan9.go", "q/p_tagx.go"} {
		if got := readRel(t, dir, f); !strings.Contains(got, "package q") {
			t.Errorf("%s clause not renamed:\n%s", f, got)
		}
	}
	if got := readRel(t, dir, "q/gen.go"); !strings.Contains(got, "package main") {
		t.Errorf("ignore-tagged package main clause should be untouched:\n%s", got)
	}
	for _, f := range []string{"use/use.go", "use/use_plan9.go", "use/use_tagx_test.go"} {
		got := readRel(t, dir, f)
		if !strings.Contains(got, `"scratch/q"`) || strings.Contains(got, `"scratch/p"`) {
			t.Errorf("%s import not rewritten:\n%s", f, got)
		}
	}
}

func TestMovePackagesMovesFilesOfEveryConfiguration(t *testing.T) {
	dir := writeFiles(t, movePackageWorkspace())
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackages(ws, types.MovePackagesRequest{
		Packages: []types.PackageMapping{{SourcePackage: "p", TargetPackage: "q"}},
	})
	if err != nil {
		t.Fatalf("MovePackages: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertGone(t, dir, "p/p_plan9.go")
	if got := readRel(t, dir, "q/p_plan9.go"); !strings.Contains(got, "package q") {
		t.Errorf("q/p_plan9.go clause not renamed:\n%s", got)
	}
	if got := readRel(t, dir, "use/use_plan9.go"); !strings.Contains(got, `"scratch/q"`) {
		t.Errorf("use_plan9.go import not rewritten:\n%s", got)
	}
}

func TestMoveDirMovesPackageHoldingOnlyExcludedFiles(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/a.go":           "package a\n\nfunc A() {}\n",
		"a/sub/s_plan9.go": "package sub\n\nfunc S() {}\n",
		"use/use_plan9.go": "package use\n\nimport \"scratch/a/sub\"\n\nfunc U() { sub.S() }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MoveDir(ws, types.MoveDirRequest{SourceDir: "a", TargetDir: "b"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertGone(t, dir, "a/sub/s_plan9.go")
	if got := readRel(t, dir, "b/sub/s_plan9.go"); !strings.Contains(got, "package sub") {
		t.Errorf("moved file clause changed unexpectedly:\n%s", got)
	}
	if got := readRel(t, dir, "use/use_plan9.go"); !strings.Contains(got, `"scratch/b/sub"`) {
		t.Errorf("use_plan9.go import not rewritten:\n%s", got)
	}
}

func TestMovePackageRefusesUnparseableFileNeedingClauseRename(t *testing.T) {
	files := movePackageWorkspace()
	files["p/broken_plan9.go"] = "func broken(\n"
	dir := writeFiles(t, files)
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	_, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
	if err == nil || !strings.Contains(err.Error(), "broken_plan9.go") {
		t.Fatalf("err = %v, want a refusal naming broken_plan9.go", err)
	}
}

func TestBatchMovePackageRewritesImporterExcludedByDefaultConfig(t *testing.T) {
	dir := writeFiles(t, movePackageWorkspace())
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "batch_operations", batchArgs(false,
		`{"type":"move_package","source":"p","target":"q"}`))

	assertFileContains(t, dir, "use/use_plan9.go", `"scratch/q"`)
	assertFileContains(t, dir, "q/p_plan9.go", "package q")
	assertGone(t, dir, "p/p_plan9.go")
}

func TestMovePackagesMovesPackageHoldingOnlyExcludedFiles(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p/p_plan9.go":     "package p\n\nfunc Plan9() {}\n",
		"use/use_plan9.go": "package use\n\nimport \"scratch/p\"\n\nfunc U9() { p.Plan9() }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackages(ws, types.MovePackagesRequest{
		Packages: []types.PackageMapping{{SourcePackage: "p", TargetPackage: "q"}},
	})
	if err != nil {
		t.Fatalf("MovePackages: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertGone(t, dir, "p/p_plan9.go")
	if got := readRel(t, dir, "q/p_plan9.go"); !strings.Contains(got, "package q") {
		t.Errorf("q/p_plan9.go clause not renamed:\n%s", got)
	}
	if got := readRel(t, dir, "use/use_plan9.go"); !strings.Contains(got, `"scratch/q"`) {
		t.Errorf("use_plan9.go import not rewritten:\n%s", got)
	}
}

func TestMovePackageMovesPackageHoldingOnlyExcludedFiles(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p/p_plan9.go":     "package p\n\nfunc Plan9() {}\n",
		"use/use_plan9.go": "package use\n\nimport \"scratch/p\"\n\nfunc U9() { p.Plan9() }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)
	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertGone(t, dir, "p/p_plan9.go")
	if got := readRel(t, dir, "use/use_plan9.go"); !strings.Contains(got, `"scratch/q"`) {
		t.Errorf("use_plan9.go import not rewritten:\n%s", got)
	}
}

func TestMoveDirRenamesBaseDirClauseOfExcludedFileAndLeavesIgnoredMain(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/a.go":       "package a\n\nfunc A() {}\n",
		"a/a_plan9.go": "package a\n\nfunc Plan9() {}\n",
		"a/gen.go":     "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		"use/use.go":   "package use\n\nimport \"scratch/a\"\n\nfunc U() { a.A() }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MoveDir(ws, types.MoveDirRequest{SourceDir: "a", TargetDir: "b"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertFileContains(t, dir, "b/a_plan9.go", "package b\n")
	assertFileContains(t, dir, "b/gen.go", "package main\n")
	assertFileContains(t, dir, "use/use.go", "b.A()")
}

func TestMovePackageKeepsMainClause(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"cmd/a/main.go": "package main\n\nfunc main() {}\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "cmd/a", TargetPackage: "cmd/b"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertFileContains(t, dir, "cmd/b/main.go", "package main\n")
}

func TestMovePackagePrefersExactDirOverSuffixMatch(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/p/ap.go":    "package p\n",
		"p/p_plan9.go": "package p\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertGone(t, dir, "p/p_plan9.go")
	assertFileContains(t, dir, "q/p_plan9.go", "package q")
	assertFileContains(t, dir, "a/p/ap.go", "package p")
}

func TestMovePackageRefusesAmbiguousSuffix(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/p/ap.go": "package p\n",
		"b/p/bp.go": "package p\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	_, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), "a/p") || !strings.Contains(err.Error(), "b/p") {
		t.Fatalf("err = %v, want an ambiguity error listing both candidates", err)
	}
}

func TestMovePackageAndRenamePackageAcceptImportPathOfExcludedOnlyPackage(t *testing.T) {
	files := map[string]string{
		"p/p_plan9.go":     "package p\n\nfunc Plan9() {}\n",
		"use/use_plan9.go": "package use\n\nimport \"scratch/p\"\n\nfunc U9() { p.Plan9() }\n",
	}
	t.Run("move_package", func(t *testing.T) {
		dir := writeFiles(t, maps.Clone(files))
		eng := createEngine(t)
		ws := loadWorkspace(t, eng, dir)
		plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "scratch/p", TargetPackage: "q"})
		if err != nil {
			t.Fatalf("MovePackage: %v", err)
		}
		if err := eng.ExecutePlan(plan); err != nil {
			t.Fatalf("ExecutePlan: %v", err)
		}
		assertFileContains(t, dir, "q/p_plan9.go", "package q")
	})
	t.Run("rename_package", func(t *testing.T) {
		dir := writeFiles(t, maps.Clone(files))
		eng := createEngine(t)
		ws := loadWorkspace(t, eng, dir)
		plan, err := eng.RenamePackage(ws, types.RenamePackageRequest{PackagePath: "scratch/p", NewPackageName: "fresh", UpdateImports: true})
		if err != nil {
			t.Fatalf("RenamePackage: %v", err)
		}
		if err := eng.ExecutePlan(plan); err != nil {
			t.Fatalf("ExecutePlan: %v", err)
		}
		assertFileContains(t, dir, "p/p_plan9.go", "package fresh")
	})
}

func TestBatchRefusesPackageStepsNamingSamePackageByDifferentForms(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p/p_plan9.go": "package p\n\nfunc Plan9() {}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := batchError(t, sess, batchArgs(false,
		`{"type":"rename_package","package_path":"p","new_name":"fresh"}`,
		`{"type":"move_package","source":"scratch/p","target":"q"}`))
	if !strings.Contains(msg, "both refer to package") {
		t.Errorf("error = %s, want a same-package conflict", msg)
	}
}
