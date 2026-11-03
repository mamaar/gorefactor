package refactor

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestCreateEngine(t *testing.T) {
	engine := CreateEngineWithConfig(DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if engine == nil {
		t.Fatal("Expected CreateEngine to return a non-nil engine")
	}

	// Test that it implements the Engine interface
	_, ok := engine.(*DefaultEngine)
	if !ok {
		t.Error("Expected CreateEngine to return a DefaultEngine")
	}
}

func TestDefaultEngine_ValidateRefactoring(t *testing.T) {
	engine := CreateEngineWithConfig(DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	// Test with nil plan
	err := engine.ValidateRefactoring(nil)
	if err == nil {
		t.Error("Expected error with nil plan")
	}

	// Test with valid plan
	plan := &types.RefactoringPlan{
		Operations:    make([]types.Operation, 0),
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Impact:        &types.ImpactAnalysis{},
		Reversible:    true,
	}

	err = engine.ValidateRefactoring(plan)
	if err != nil {
		t.Errorf("Expected no error with valid plan, got %v", err)
	}
}

func TestDefaultEngine_PreviewPlan(t *testing.T) {
	engine := CreateEngineWithConfig(DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	plan := &types.RefactoringPlan{
		Changes: []types.Change{
			{
				File:        "test.go",
				Start:       10,
				End:         20,
				OldText:     "old",
				NewText:     "new",
				Description: "test change",
			},
		},
	}

	preview, err := engine.PreviewPlan(plan)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// Check that preview contains expected information from the new detailed format
	if !strings.Contains(preview, "Preview of 1 changes") {
		t.Error("Expected preview to mention 1 change")
	}

	if !strings.Contains(preview, "test.go") {
		t.Error("Expected preview to mention test.go")
	}

	if !strings.Contains(preview, "test change") {
		t.Error("Expected preview to mention change description")
	}
}

func TestDefaultEngine_ExecutePlan_WithErrors(t *testing.T) {
	engine := CreateEngineWithConfig(DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	// Create a plan with critical errors
	plan := &types.RefactoringPlan{
		Changes: make([]types.Change, 0),
		Impact: &types.ImpactAnalysis{
			PotentialIssues: []types.Issue{
				{
					Type:        types.IssueCompilationError,
					Description: "Critical error",
					Severity:    types.Error,
				},
			},
		},
	}

	err := engine.ExecutePlan(plan)
	if err == nil {
		t.Error("Expected error when executing plan with critical issues")
	}

	// Check that it's a RefactorError
	refErr, ok := errors.AsType[*types.RefactorError](err)
	if ok {
		if refErr.Type != types.InvalidOperation {
			t.Errorf("Expected InvalidOperation error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestDefaultEngine_ExecutePlan_Success(t *testing.T) {
	engine := CreateEngineWithConfig(DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	// Create a plan with only warnings
	plan := &types.RefactoringPlan{
		Changes: make([]types.Change, 0),
		Impact: &types.ImpactAnalysis{
			PotentialIssues: []types.Issue{
				{
					Type:        types.IssueNameConflict,
					Description: "Warning issue",
					Severity:    types.Warning,
				},
			},
		},
	}

	err := engine.ExecutePlan(plan)
	if err != nil {
		t.Errorf("Expected no error with warning-only plan, got %v", err)
	}
}

// TestDefaultEngine_EnsureTypeCheckedPackage_ScopedToTarget verifies that
// EnsureTypeCheckedPackage only type-checks the requested package and the
// dependencies it transitively pulls in through the importer, leaving
// unrelated packages untouched. This is the whole point of the scoped
// method: on a workspace with many packages, callers that only care about
// one package should not pay to type-check the rest.
func TestDefaultEngine_EnsureTypeCheckedPackage_ScopedToTarget(t *testing.T) {
	engine, ws := writeRenameFieldWorkspace(t, map[string]string{
		"dep/dep.go": `package dep

func Helper() int {
	return 42
}
`,
		"user/user.go": `package user

import "example.com/m/dep"

func Use() int {
	return dep.Helper()
}
`,
		"unrelated/unrelated.go": `package unrelated

func Standalone() int {
	return 1
}
`,
	})

	de, ok := engine.(*DefaultEngine)
	if !ok {
		t.Fatalf("engine is not *DefaultEngine")
	}

	de.EnsureTypeCheckedPackage(ws, "example.com/m/user")

	userPkg := findPackageByName(t, ws, "user")
	depPkg := findPackageByName(t, ws, "dep")
	unrelatedPkg := findPackageByName(t, ws, "unrelated")

	if !userPkg.TypesChecked {
		t.Error("expected target package user to be type-checked")
	}

	if !depPkg.TypesChecked {
		t.Error("expected transitive dependency dep to be type-checked (importer pulls it in lazily)")
	}

	if unrelatedPkg.TypesChecked {
		t.Error("expected unrelated package to NOT be type-checked when only user is requested")
	}
}

func TestDefaultEngine_ExecutePlan_RefusesGeneratedFileDespiteAllowBreaking(t *testing.T) {
	dir := t.TempDir()
	src := "// Code generated by stringer; DO NOT EDIT.\n\npackage p\n"
	gen := filepath.Join(dir, "gen.go")

	err := os.WriteFile(gen, []byte(src), 0644)
	if err != nil {
		t.Fatal(err)
	}

	engine := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true, AllowBreaking: true},
		slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	err = engine.ExecutePlan(&types.RefactoringPlan{
		Changes: []types.Change{{File: gen, NewText: "// edited\n", Description: "Replace text"}},
	})
	if err == nil {
		t.Fatal("expected the plan to be refused")
	}

	got, _ := os.ReadFile(gen)
	if string(got) != src {
		t.Errorf("generated file was modified:\n%s", got)
	}
}
