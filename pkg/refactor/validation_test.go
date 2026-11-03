package refactor

import (
	"io"
	"log/slog"
	"testing"

	refactorTypes "github.com/mamaar/gorefactor/pkg/types"
)

func TestNewValidator(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if validator == nil {
		t.Fatal("Expected NewValidator to return a non-nil validator")
	}

	if validator.typeChecker == nil {
		t.Error("Expected validator to have a non-nil typeChecker")
	}
}

func TestValidator_ValidatePlan_NilPlan(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := validator.ValidatePlanWithConfig(nil, nil)
	if err == nil {
		t.Error("Expected error with nil plan")
	}

	// Check that it's a RefactorError
	refErr, ok := err.(*refactorTypes.RefactorError)
	if ok {
		if refErr.Type != refactorTypes.InvalidOperation {
			t.Errorf("Expected InvalidOperation error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestValidator_ValidatePlan_EmptyPlan(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	plan := &refactorTypes.RefactoringPlan{
		Operations:    make([]refactorTypes.Operation, 0),
		Changes:       make([]refactorTypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	err := validator.ValidatePlanWithConfig(plan, nil)
	if err != nil {
		t.Errorf("Expected no error with empty plan, got %v", err)
	}

	// Check that impact analysis was created
	if plan.Impact == nil {
		t.Error("Expected Impact to be created")
	}
}

func TestValidator_ValidatePlan_WithOperations(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a rename operation
	renameOp := &RenameSymbolOperation{
		Request: refactorTypes.RenameSymbolRequest{
			SymbolName: "TestFunc",
			NewName:    "RenamedFunc",
			Package:    "test/package",
		},
	}

	plan := &refactorTypes.RefactoringPlan{
		Operations:    []refactorTypes.Operation{renameOp},
		Changes:       make([]refactorTypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	err := validator.ValidatePlanWithConfig(plan, nil)
	if err != nil {
		t.Errorf("Expected no error with valid rename operation, got %v", err)
	}

	// Check that impact analysis was created (issues might be empty for valid operations)
	if plan.Impact == nil {
		t.Error("Expected Impact to be created")
	}
}

func TestValidator_validateChanges(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Test non-overlapping changes
	changes := []refactorTypes.Change{
		{File: "test.go", Start: 0, End: 10, NewText: "new1"},
		{File: "test.go", Start: 20, End: 30, NewText: "new2"},
		{File: "other.go", Start: 0, End: 10, NewText: "new3"},
	}

	issues := validator.validateChanges(changes)

	// Should have no critical issues
	criticalIssues := 0
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			criticalIssues++
		}
	}

	if criticalIssues > 0 {
		t.Errorf("Expected no critical issues for non-overlapping changes, got %d", criticalIssues)
	}
}

func TestValidator_validateChanges_Overlapping(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Test overlapping changes
	changes := []refactorTypes.Change{
		{File: "test.go", Start: 0, End: 15, NewText: "new1"},
		{File: "test.go", Start: 10, End: 25, NewText: "new2"}, // Overlaps with first
	}

	issues := validator.validateChanges(changes)

	// Should have at least one error
	hasError := false
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			hasError = true
			break
		}
	}

	if !hasError {
		t.Error("Expected error for overlapping changes")
	}
}

func TestValidator_validateChanges_InvalidBounds(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Test invalid change bounds
	changes := []refactorTypes.Change{
		{File: "test.go", Start: -1, End: 10, NewText: "new1"}, // Invalid start
		{File: "test.go", Start: 20, End: 15, NewText: "new2"}, // End before start
	}

	issues := validator.validateChanges(changes)

	// Should have at least two errors
	errorCount := 0
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			errorCount++
		}
	}

	if errorCount < 2 {
		t.Errorf("Expected at least 2 errors for invalid bounds, got %d", errorCount)
	}
}

func TestValidator_isValidGoIdentifier(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	testCases := []struct {
		name     string
		expected bool
	}{
		{"ValidName", true},
		{"validName", true},
		{"_validName", true},
		{"Valid123", true},
		{"123Invalid", false},
		{"invalid-name", false},
		{"invalid.name", false},
		{"", false},
		{"validName_123", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := validator.isValidGoIdentifier(tc.name)
			if result != tc.expected {
				t.Errorf("Expected isValidGoIdentifier('%s') to be %v, got %v", tc.name, tc.expected, result)
			}
		})
	}
}

func TestValidator_validateGoSyntax(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	testCases := []struct {
		name        string
		code        string
		expectError bool
	}{
		{
			name:        "Valid expression",
			code:        "a + b",
			expectError: false,
		},
		{
			name:        "Valid function call",
			code:        "fmt.Println(\"hello\")",
			expectError: false,
		},
		{
			name:        "Invalid syntax",
			code:        "func invalid( {",
			expectError: true,
		},
		{
			name:        "Empty code",
			code:        "",
			expectError: false,
		},
		{
			name:        "Valid statement",
			code:        "x := 42",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validator.validateGoSyntax(tc.code)
			hasError := err != nil

			if hasError != tc.expectError {
				t.Errorf("Expected validateGoSyntax('%s') error status to be %v, got %v (error: %v)",
					tc.code, tc.expectError, hasError, err)
			}
		})
	}
}

func TestValidator_changesOverlap(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	testCases := []struct {
		name     string
		change1  refactorTypes.Change
		change2  refactorTypes.Change
		expected bool
	}{
		{
			name:     "Non-overlapping (adjacent)",
			change1:  refactorTypes.Change{Start: 0, End: 10},
			change2:  refactorTypes.Change{Start: 10, End: 20},
			expected: false,
		},
		{
			name:     "Non-overlapping (separate)",
			change1:  refactorTypes.Change{Start: 0, End: 10},
			change2:  refactorTypes.Change{Start: 20, End: 30},
			expected: false,
		},
		{
			name:     "Overlapping",
			change1:  refactorTypes.Change{Start: 0, End: 15},
			change2:  refactorTypes.Change{Start: 10, End: 25},
			expected: true,
		},
		{
			name:     "Contained",
			change1:  refactorTypes.Change{Start: 0, End: 30},
			change2:  refactorTypes.Change{Start: 10, End: 20},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := validator.changesOverlap(tc.change1, tc.change2)
			if result != tc.expected {
				t.Errorf("Expected changesOverlap to be %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestValidator_filterCriticalIssues(t *testing.T) {
	validator := NewValidator(slog.New(slog.NewTextHandler(io.Discard, nil)))

	allIssues := []refactorTypes.Issue{
		{Severity: refactorTypes.Error, Description: "Critical error 1"},
		{Severity: refactorTypes.Warning, Description: "Warning 1"},
		{Severity: refactorTypes.Error, Description: "Critical error 2"},
		{Severity: refactorTypes.Info, Description: "Info 1"},
	}

	critical := validator.filterCriticalIssues(allIssues)

	if len(critical) != 2 {
		t.Errorf("Expected 2 critical issues, got %d", len(critical))
	}

	for _, issue := range critical {
		if issue.Severity != refactorTypes.Error {
			t.Error("Expected all filtered issues to be errors")
		}
	}
}
