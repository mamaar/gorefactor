package refactor

import (
	"path/filepath"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func relocationFixture(t *testing.T) (*DefaultEngine, *types.Workspace, string) {
	t.Helper()
	eng, dir := loadConfigsEngine(t, map[string]string{
		"a/a.go":   "package a\n\nfunc A() int { return 1 }\n",
		"b/b.go":   "package b\n\nfunc B() int { return 2 }\n",
		"cmd/m.go": "package main\n\nimport (\n\t\"cfg/a\"\n\t\"cfg/b\"\n)\n\nfunc main() { _ = a.A() + b.B() }\n",
	})
	return eng, eng.WorkspaceSet().Primary().Workspace, dir
}

func requireRelocation(t *testing.T, plan *types.RefactoringPlan, want bool) {
	t.Helper()
	if len(plan.Changes) == 0 {
		t.Fatal("plan has no changes")
	}
	for _, c := range plan.Changes {
		if c.Relocation != want {
			t.Errorf("Relocation = %v, want %v for %q in %s", c.Relocation, want, c.Description, c.File)
		}
	}
}

func TestMovePackagePlanMarksEveryChangeAsRelocation(t *testing.T) {
	eng, ws, dir := relocationFixture(t)
	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "a", TargetPackage: filepath.Join(dir, "moved")})
	if err != nil {
		t.Fatal(err)
	}
	requireRelocation(t, plan, true)
}

func TestMoveDirPlanMarksEveryChangeAsRelocation(t *testing.T) {
	eng, ws, _ := relocationFixture(t)
	plan, err := eng.MoveDir(ws, types.MoveDirRequest{SourceDir: "a", TargetDir: "moved", UpdateImports: true})
	if err != nil {
		t.Fatal(err)
	}
	requireRelocation(t, plan, true)
}

func TestMovePackagesPlanMarksEveryChangeAsRelocation(t *testing.T) {
	eng, ws, dir := relocationFixture(t)
	plan, err := eng.MovePackages(ws, types.MovePackagesRequest{
		Packages: []types.PackageMapping{
			{SourcePackage: "a", TargetPackage: filepath.Join(dir, "ma")},
			{SourcePackage: "b", TargetPackage: filepath.Join(dir, "mb")},
		},
		CreateTargets: true,
		UpdateImports: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireRelocation(t, plan, true)
}

func TestRenamePackagePlanMarksEveryChangeAsRelocation(t *testing.T) {
	eng, ws, dir := relocationFixture(t)
	plan, err := eng.RenamePackage(ws, types.RenamePackageRequest{
		OldPackageName: "a",
		NewPackageName: "alpha",
		PackagePath:    filepath.Join(dir, "a"),
		UpdateImports:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireRelocation(t, plan, true)
}

func TestRenameSymbolPlanHasNoRelocationChange(t *testing.T) {
	eng, ws, dir := relocationFixture(t)
	plan, err := eng.RenameSymbol(ws, types.RenameSymbolRequest{SymbolName: "A", NewName: "Alpha", Package: filepath.Join(dir, "a")})
	if err != nil {
		t.Fatal(err)
	}
	requireRelocation(t, plan, false)
}

func TestMergeStepPlansPreservesRelocation(t *testing.T) {
	a := stepPlan([]string{"a.go"}, types.Change{File: "a.go", Start: 0, End: 1, NewText: "x", Relocation: true})
	b := stepPlan([]string{"b.go"}, types.Change{File: "b.go", Start: 0, End: 1, NewText: "y"})

	got, err := MergeStepPlans([]*types.RefactoringPlan{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changes[0].Relocation || got.Changes[1].Relocation {
		t.Errorf("Relocation flags = %v, %v; want true, false", got.Changes[0].Relocation, got.Changes[1].Relocation)
	}
}
