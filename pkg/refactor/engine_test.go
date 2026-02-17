package refactor

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestCreateEngine(t *testing.T) {
	engine := CreateEngine(slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	engine := CreateEngine(slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

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
	engine := CreateEngine(slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

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
	engine := CreateEngine(slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

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
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.InvalidOperation {
			t.Errorf("Expected InvalidOperation error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestDefaultEngine_ExecutePlan_Success(t *testing.T) {
	engine := CreateEngine(slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

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

func TestDefaultEngine_findOperationConflicts(t *testing.T) {
	engine := CreateEngine(slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	// Test with non-overlapping changes
	changes := []types.Change{
		{File: "test.go", Start: 0, End: 10},
		{File: "test.go", Start: 20, End: 30},
		{File: "other.go", Start: 0, End: 10},
	}

	conflicts := engine.findOperationConflicts(changes)
	if len(conflicts) != 0 {
		t.Errorf("Expected no conflicts, got %d", len(conflicts))
	}

	// Test with overlapping changes
	overlappingChanges := []types.Change{
		{File: "test.go", Start: 0, End: 15},
		{File: "test.go", Start: 10, End: 25},
	}

	conflicts = engine.findOperationConflicts(overlappingChanges)
	if len(conflicts) == 0 {
		t.Error("Expected conflicts with overlapping changes")
	}
}