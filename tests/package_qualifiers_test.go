package tests_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

const oldPkgSource = "package old\n\nfunc Foo() string { return \"\" }\n"

// vetAs runs go vet in dir for goos.
func vetAs(t *testing.T, dir, goos string) {
	t.Helper()
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOOS="+goos, "GOARCH=amd64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("go vet (GOOS=%s) failed: %v\n%s", goos, err, out)
	}
}

func renameOldToFresh(t *testing.T, dir, pkgDir string) *types.RefactoringPlan {
	t.Helper()
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)
	plan, err := eng.RenamePackage(ws, types.RenamePackageRequest{
		PackagePath:    filepath.Join(dir, pkgDir),
		OldPackageName: "old",
		NewPackageName: "fresh",
		UpdateImports:  true,
	})
	if err != nil {
		t.Fatalf("RenamePackage: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	return plan
}

func moveOldToFresh(t *testing.T, dir string) *types.RefactoringPlan {
	t.Helper()
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)
	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "old", TargetPackage: "lib/fresh"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	return plan
}

func assertSkipped(t *testing.T, plan *types.RefactoringPlan, fileSuffix string) {
	t.Helper()
	for _, s := range plan.SkippedSites {
		if strings.HasSuffix(s.File, fileSuffix) {
			return
		}
	}
	t.Errorf("no skipped site for %s in %+v", fileSuffix, plan.SkippedSites)
}

func TestRenamePackageRewritesQualifiersNotText(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"use/use.go": `package use

import (
	"fmt"

	"scratch/old"
)

type c struct{ old struct{ x int } }

var xold = struct{ Bar int }{}

// old.Foo is documented here.
func U() string {
	var v c
	_ = v.old.x
	_ = xold.Bar
	fmt.Println("old.yaml")
	return old.Foo()
}
`,
	})
	renameOldToFresh(t, dir, "old")

	got := readRel(t, dir, "use/use.go")
	for _, want := range []string{"return fresh.Foo()", `"old.yaml"`, "xold.Bar", "v.old.x", "// old.Foo is documented"} {
		if !strings.Contains(got, want) {
			t.Errorf("use.go missing %q:\n%s", want, got)
		}
	}
	vetAs(t, dir, "linux")
}

func TestRenamePackageUsesClauseNameNotDirectoryName(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/v2/v2.go": oldPkgSource,
		"use/use.go":   "package use\n\nimport \"scratch/old/v2\"\n\nfunc U() string { return old.Foo() }\n",
	})
	renameOldToFresh(t, dir, "old/v2")

	assertFileContains(t, dir, "use/use.go", "fresh.Foo()")
	vetAs(t, dir, "linux")
}

func TestMovePackageRewritesImporterQualifiers(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go":      oldPkgSource,
		"use/use.go":      "package use\n\nimport \"scratch/old\"\n\nfunc U() string { return old.Foo() }\n",
		"use/use_test.go": "package use\n\nimport (\n\t\"testing\"\n\n\t\"scratch/old\"\n)\n\nfunc TestU(t *testing.T) { _ = old.Foo() }\n",
	})
	moveOldToFresh(t, dir)

	assertFileContains(t, dir, "use/use.go", "fresh.Foo()")
	assertFileContains(t, dir, "use/use_test.go", "fresh.Foo()")
	assertFileContains(t, dir, "use/use.go", `"scratch/lib/fresh"`)
	vetAs(t, dir, "linux")
}

func TestMovePackagesRewritesImporterQualifiers(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"use/use.go": "package use\n\nimport \"scratch/old\"\n\nfunc U() string { return old.Foo() }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)
	plan, err := eng.MovePackages(ws, types.MovePackagesRequest{
		Packages:  []types.PackageMapping{{SourcePackage: "old", TargetPackage: "lib/fresh"}},
		TargetDir: "lib",
	})
	if err != nil {
		t.Fatalf("MovePackages: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	assertFileContains(t, dir, "use/use.go", "fresh.Foo()")
	vetAs(t, dir, "linux")
}

func TestMovePackageOnlyRepathsAliasedAndDotImports(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"use/a.go":   "package use\n\nimport o \"scratch/old\"\n\nfunc A() string { return o.Foo() }\n",
		"use/b.go":   "package use\n\nimport . \"scratch/old\"\n\nfunc B() string { return Foo() }\n",
	})
	plan := moveOldToFresh(t, dir)

	assertFileContains(t, dir, "use/a.go", `o "scratch/lib/fresh"`)
	assertFileContains(t, dir, "use/a.go", "o.Foo()")
	assertFileContains(t, dir, "use/b.go", `. "scratch/lib/fresh"`)
	if len(plan.SkippedSites) != 0 {
		t.Errorf("unexpected skipped sites: %+v", plan.SkippedSites)
	}
	vetAs(t, dir, "linux")
}

func TestRenamePackageKeepsOldNameAsAliasWhenNewNameIsTaken(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		goos  []string
	}{
		{
			name: "local variable at the use site",
			files: map[string]string{
				"use/use.go": "package use\n\nimport \"scratch/old\"\n\nfunc U() string {\n\tfresh := 1\n\t_ = fresh\n\treturn old.Foo()\n}\n",
			},
			goos: []string{"linux"},
		},
		{
			name: "package-level name in the importer's package",
			files: map[string]string{
				"use/use.go":   "package use\n\nimport \"scratch/old\"\n\nfunc U() string { return old.Foo() }\n",
				"use/fresh.go": "package use\n\nvar fresh = 1\n",
			},
			goos: []string{"linux"},
		},
		{
			name: "package-level name only in a plan9 sibling",
			files: map[string]string{
				"use/use.go":       "package use\n\nimport \"scratch/old\"\n\nfunc U() string { return old.Foo() }\n",
				"use/use_plan9.go": "package use\n\nvar fresh = 1\n",
			},
			goos: []string{"linux", "plan9"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.files["old/old.go"] = oldPkgSource
			dir := writeFiles(t, tc.files)
			plan := renameOldToFresh(t, dir, "old")

			assertFileContains(t, dir, "use/use.go", `old "scratch/old"`)
			assertFileContains(t, dir, "use/use.go", "old.Foo()")
			assertSkipped(t, plan, "use/use.go")
			for _, goos := range tc.goos {
				vetAs(t, dir, goos)
			}
		})
	}
}

func TestMovePackageKeepsOldNameAsAliasForIgnoredImporter(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"gen/gen.go": "//go:build ignore\n\npackage main\n\nimport \"scratch/old\"\n\nfunc main() { _ = old.Foo() }\n",
	})
	plan := moveOldToFresh(t, dir)

	assertFileContains(t, dir, "gen/gen.go", `old "scratch/lib/fresh"`)
	assertFileContains(t, dir, "gen/gen.go", "old.Foo()")
	assertSkipped(t, plan, "gen/gen.go")
}

func TestExternalTestFileFollowsPackageRenameAndMove(t *testing.T) {
	files := func() map[string]string {
		return map[string]string{
			"old/old.go":      oldPkgSource,
			"old/old_test.go": "package old_test\n\nimport (\n\t\"testing\"\n\n\t\"scratch/old\"\n)\n\nfunc TestFoo(t *testing.T) { _ = old.Foo() }\n",
		}
	}

	t.Run("rename_package", func(t *testing.T) {
		dir := writeFiles(t, files())
		renameOldToFresh(t, dir, "old")
		assertFileContains(t, dir, "old/old_test.go", "package fresh_test")
		assertFileContains(t, dir, "old/old_test.go", "fresh.Foo()")
		vetAs(t, dir, "linux")
	})
	t.Run("move_package", func(t *testing.T) {
		dir := writeFiles(t, files())
		moveOldToFresh(t, dir)
		assertFileContains(t, dir, "lib/fresh/old_test.go", "package fresh_test")
		assertFileContains(t, dir, "lib/fresh/old_test.go", "fresh.Foo()")
		assertFileContains(t, dir, "lib/fresh/old_test.go", `"scratch/lib/fresh"`)
		vetAs(t, dir, "linux")
	})
}

func TestMoveDirRenamesQualifiersOfTwoPackagesInOneFile(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/p1/p1.go": "package one\n\nfunc F() string { return \"\" }\n",
		"a/p2/p2.go": "package two\n\nfunc G() string { return \"\" }\n",
		"use/use.go": "package use\n\nimport (\n\t\"scratch/a/p1\"\n\t\"scratch/a/p2\"\n)\n\nfunc U() string { return one.F() + two.G() + \"one.txt\" }\n",
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

	assertFileContains(t, dir, "use/use.go", `p1.F() + p2.G() + "one.txt"`)
	assertFileContains(t, dir, "b/p1/p1.go", "package p1")
	vetAs(t, dir, "linux")
}

func TestRenamePackageRefusesUnparseableImporterNeedingQualifierEdits(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"use/bad.go": "package use\n\nimport \"scratch/old\"\n\nfunc U() { old.Foo( }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	_, err := eng.RenamePackage(ws, types.RenamePackageRequest{
		PackagePath:    filepath.Join(dir, "old"),
		OldPackageName: "old",
		NewPackageName: "fresh",
		UpdateImports:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "bad.go") {
		t.Fatalf("want refusal naming bad.go, got %v", err)
	}
	assertFileContains(t, dir, "old/old.go", "package old")
}
