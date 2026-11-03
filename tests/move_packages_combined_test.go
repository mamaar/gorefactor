package tests_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	pkgASource = "package a\n\nfunc Foo() string { return \"\" }\n"
	pkgBSource = "package b\n\nfunc Bar() string { return \"\" }\n"
)

func movePackages(t *testing.T, dir string, mappings ...types.PackageMapping) (*types.RefactoringPlan, error) {
	t.Helper()
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackages(ws, types.MovePackagesRequest{Packages: mappings})
	if err != nil {
		return nil, fmt.Errorf("move packages: %w", err)
	}

	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	return plan, nil
}

func TestMovePackagesRewritesAFileImportingTwoMovedPackages(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/a.go":     pkgASource,
		"b/b.go":     pkgBSource,
		"use/use.go": "package use\n\nimport (\n\t\"scratch/a\"\n\t\"scratch/b\"\n)\n\nfunc U() string { return a.Foo() + b.Bar() }\n",
	})

	_, err := movePackages(t, dir,
		types.PackageMapping{SourcePackage: "a", TargetPackage: "lib/alpha"},
		types.PackageMapping{SourcePackage: "b", TargetPackage: "lib/beta"})
	if err != nil {
		t.Fatalf("MovePackages: %v", err)
	}

	assertFileContains(t, dir, "use/use.go", `"scratch/lib/alpha"`)
	assertFileContains(t, dir, "use/use.go", `"scratch/lib/beta"`)
	assertFileContains(t, dir, "use/use.go", "alpha.Foo() + beta.Bar()")
	vetAs(t, dir, "linux")
}

func TestMovePackagesMovedPackageSeesAnotherMovedPackagesNewPath(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/a.go": "package a\n\nimport \"scratch/b\"\n\nfunc Foo() string { return b.Bar() }\n",
		"b/b.go": pkgBSource,
	})

	_, err := movePackages(t, dir,
		types.PackageMapping{SourcePackage: "a", TargetPackage: "lib/alpha"},
		types.PackageMapping{SourcePackage: "b", TargetPackage: "lib/beta"})
	if err != nil {
		t.Fatalf("MovePackages: %v", err)
	}

	assertFileContains(t, dir, "lib/alpha/a.go", `"scratch/lib/beta"`)
	assertFileContains(t, dir, "lib/alpha/a.go", "beta.Bar()")
	vetAs(t, dir, "linux")
}

func TestMovePackagesRefusesConflictingMappingsAndWritesNothing(t *testing.T) {
	tests := []struct {
		name     string
		mappings []types.PackageMapping
		reason   string
	}{
		{"duplicate source", []types.PackageMapping{
			{SourcePackage: "a", TargetPackage: "lib/one"},
			{SourcePackage: "a", TargetPackage: "lib/two"},
		}, "share the source directory"},
		{"duplicate target", []types.PackageMapping{
			{SourcePackage: "a", TargetPackage: "lib/one"},
			{SourcePackage: "b", TargetPackage: "lib/one"},
		}, "share the target directory"},
		{"target holding only a windows file", []types.PackageMapping{
			{SourcePackage: "a", TargetPackage: "lib/win"},
		}, "already exists"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{
				"a/a.go":                 pkgASource,
				"b/b.go":                 pkgBSource,
				"lib/win/win_windows.go": "package win\n",
			})

			_, err := movePackages(t, dir, tc.mappings...)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("expected a refusal containing %q, got: %v", tc.reason, err)
			}

			for _, rel := range []string{"a/a.go", "b/b.go", "lib/win/win_windows.go"} {
				_, err := os.Stat(filepath.Join(dir, rel))
				if err != nil {
					t.Errorf("%s should be untouched: %v", rel, err)
				}
			}

			_, err = os.Stat(filepath.Join(dir, "lib", "one"))
			if !os.IsNotExist(err) {
				t.Errorf("lib/one should not exist, stat err=%v", err)
			}
		})
	}
}

func TestMovePackageToNonIdentifierDirKeepsPackageClause(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"use/use.go": "package use\n\nimport \"scratch/old\"\n\nfunc U() string { return old.Foo() }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "old", TargetPackage: "lib/my-pkg"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}

	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	assertFileContains(t, dir, "lib/my-pkg/old.go", "package old\n")
	assertFileContains(t, dir, "use/use.go", `"scratch/lib/my-pkg"`)
	vetAs(t, dir, "linux")
}

func TestMovePackagesViaMCPWithoutTargetDir(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"use/use.go": "package use\n\nimport \"scratch/old\"\n\nfunc U() string { return old.Foo() }\n",
	})
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_packages",
		Arguments: map[string]any{
			"packages": []map[string]any{{"source": "old", "target": "lib/fresh"}},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if result.IsError {
		t.Fatalf("move_packages returned an error result: %+v", result.Content)
	}

	assertFileContains(t, dir, "use/use.go", `"scratch/lib/fresh"`)
	assertFileContains(t, dir, "use/use.go", "fresh.Foo()")
}

func TestMovePackageOfMainKeepsQualifierInExternalTest(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"cmd/a/main.go":      "package main\n\nfunc Foo() string { return \"\" }\n\nfunc main() { _ = Foo() }\n",
		"cmd/a/main_test.go": "package main_test\n\nimport (\n\t\"testing\"\n\n\t\"scratch/cmd/a\"\n)\n\nfunc TestFoo(t *testing.T) { _ = main.Foo() }\n",
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

	assertFileContains(t, dir, "cmd/b/main_test.go", "main.Foo()")
	vetAs(t, dir, "linux")
}

func TestMovePackageRepathsImporterThatAlreadyDidNotParse(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"old/old.go": oldPkgSource,
		"use/use.go": "package use\n\nimport \"scratch/old\"\n\nfunc U() string { return old.Foo( }\n",
	})
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)

	plan, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "old", TargetPackage: "lib/old"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}

	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	assertFileContains(t, dir, "use/use.go", `"scratch/lib/old"`)
}
