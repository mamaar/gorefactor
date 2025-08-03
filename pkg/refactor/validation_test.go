package refactor

import (
	"go/token"
	"testing"

	refactorTypes "github.com/mamaar/gorefactor/pkg/types"
)

func TestNewValidator(t *testing.T) {
	validator := NewValidator()
	if validator == nil {
		t.Fatal("Expected NewValidator to return a non-nil validator")
	}

	if validator.typeChecker == nil {
		t.Error("Expected validator to have a non-nil typeChecker")
	}
}

func TestValidator_ValidatePlan_NilPlan(t *testing.T) {
	validator := NewValidator()

	err := validator.ValidatePlan(nil)
	if err == nil {
		t.Error("Expected error with nil plan")
	}

	// Check that it's a RefactorError
	if refErr, ok := err.(*refactorTypes.RefactorError); ok {
		if refErr.Type != refactorTypes.InvalidOperation {
			t.Errorf("Expected InvalidOperation error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestValidator_ValidatePlan_EmptyPlan(t *testing.T) {
	validator := NewValidator()

	plan := &refactorTypes.RefactoringPlan{
		Operations:    make([]refactorTypes.Operation, 0),
		Changes:       make([]refactorTypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	err := validator.ValidatePlan(plan)
	if err != nil {
		t.Errorf("Expected no error with empty plan, got %v", err)
	}

	// Check that impact analysis was created
	if plan.Impact == nil {
		t.Error("Expected Impact to be created")
	}
}

func TestValidator_ValidatePlan_WithOperations(t *testing.T) {
	validator := NewValidator()

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

	err := validator.ValidatePlan(plan)
	if err != nil {
		t.Errorf("Expected no error with valid rename operation, got %v", err)
	}

	// Check that impact analysis was created (issues might be empty for valid operations)
	if plan.Impact == nil {
		t.Error("Expected Impact to be created")
	}
}

func TestValidator_ValidateMove(t *testing.T) {
	// Create a test workspace
	ws := &refactorTypes.Workspace{
		Packages: map[string]*refactorTypes.Package{
			"source/pkg": {
				Name: "source",
				Path: "source/pkg",
				Symbols: &refactorTypes.SymbolTable{
					Functions: map[string]*refactorTypes.Symbol{
						"TestFunc": {
							Name:     "TestFunc",
							Kind:     refactorTypes.FunctionSymbol,
							Exported: true,
							Package:  "source/pkg",
							File:     "source.go",
						},
					},
				},
			},
			"target/pkg": {
				Name:    "target",
				Path:    "target/pkg",
				Symbols: &refactorTypes.SymbolTable{
					Functions: make(map[string]*refactorTypes.Symbol),
				},
			},
		},
		FileSet: token.NewFileSet(),
	}

	validator := NewValidator()

	// Test valid move
	req := refactorTypes.MoveSymbolRequest{
		SymbolName:   "TestFunc",
		FromPackage:  "source/pkg",
		ToPackage:    "target/pkg",
		CreateTarget: false,
	}

	issues := validator.ValidateMove(ws, req)
	
	// Should have no critical issues for a valid move
	criticalIssues := 0
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			criticalIssues++
		}
	}

	if criticalIssues > 0 {
		t.Errorf("Expected no critical issues for valid move, got %d", criticalIssues)
	}
}

func TestValidator_ValidateMove_NonExistentSource(t *testing.T) {
	ws := &refactorTypes.Workspace{
		Packages: make(map[string]*refactorTypes.Package),
		FileSet:  token.NewFileSet(),
	}

	validator := NewValidator()

	req := refactorTypes.MoveSymbolRequest{
		SymbolName:  "TestFunc",
		FromPackage: "nonexistent/pkg",
		ToPackage:   "target/pkg",
	}

	issues := validator.ValidateMove(ws, req)
	
	// Should have at least one error
	hasError := false
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			hasError = true
			break
		}
	}

	if !hasError {
		t.Error("Expected error for non-existent source package")
	}
}

func TestValidator_ValidateRename(t *testing.T) {
	// Create a test workspace
	ws := &refactorTypes.Workspace{
		Packages: map[string]*refactorTypes.Package{
			"test/pkg": {
				Name: "test",
				Path: "test/pkg",
				Symbols: &refactorTypes.SymbolTable{
					Functions: map[string]*refactorTypes.Symbol{
						"TestFunc": {
							Name:     "TestFunc",
							Kind:     refactorTypes.FunctionSymbol,
							Exported: true,
							Package:  "test/pkg",
							File:     "test.go",
						},
					},
				},
			},
		},
		FileSet: token.NewFileSet(),
	}

	validator := NewValidator()

	// Test valid rename
	req := refactorTypes.RenameSymbolRequest{
		SymbolName: "TestFunc",
		NewName:    "RenamedFunc",
		Package:    "test/pkg",
	}

	issues := validator.ValidateRename(ws, req)

	// Should have no critical issues for a valid rename
	criticalIssues := 0
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			criticalIssues++
		}
	}

	if criticalIssues > 0 {
		t.Errorf("Expected no critical issues for valid rename, got %d", criticalIssues)
	}
}

func TestValidator_ValidateRename_InvalidIdentifier(t *testing.T) {
	ws := &refactorTypes.Workspace{
		Packages: make(map[string]*refactorTypes.Package),
		FileSet:  token.NewFileSet(),
	}

	validator := NewValidator()

	req := refactorTypes.RenameSymbolRequest{
		SymbolName: "TestFunc",
		NewName:    "123InvalidName", // Invalid Go identifier
		Package:    "test/pkg",
	}

	issues := validator.ValidateRename(ws, req)

	// Should have at least one error
	hasError := false
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			hasError = true
			break
		}
	}

	if !hasError {
		t.Error("Expected error for invalid identifier")
	}
}

func TestValidator_ValidateRename_GoKeyword(t *testing.T) {
	ws := &refactorTypes.Workspace{
		Packages: make(map[string]*refactorTypes.Package),
		FileSet:  token.NewFileSet(),
	}

	validator := NewValidator()

	req := refactorTypes.RenameSymbolRequest{
		SymbolName: "TestFunc",
		NewName:    "func", // Go keyword
		Package:    "test/pkg",
	}

	issues := validator.ValidateRename(ws, req)

	// Should have at least one error
	hasError := false
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			hasError = true
			break
		}
	}

	if !hasError {
		t.Error("Expected error for Go keyword as identifier")
	}
}

func TestValidator_validateChanges(t *testing.T) {
	validator := NewValidator()

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
	validator := NewValidator()

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
	validator := NewValidator()

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
	validator := NewValidator()

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

func TestValidator_isGoKeyword(t *testing.T) {
	validator := NewValidator()

	keywords := []string{"func", "var", "const", "type", "if", "else", "for", "range", "return"}
	nonKeywords := []string{"function", "variable", "constant", "myType", "ifElse", "forEach"}

	for _, keyword := range keywords {
		if !validator.isGoKeyword(keyword) {
			t.Errorf("Expected '%s' to be recognized as a Go keyword", keyword)
		}
	}

	for _, nonKeyword := range nonKeywords {
		if validator.isGoKeyword(nonKeyword) {
			t.Errorf("Expected '%s' to NOT be recognized as a Go keyword", nonKeyword)
		}
	}
}

func TestValidator_isSymbolMoveable(t *testing.T) {
	validator := NewValidator()

	testCases := []struct {
		name     string
		symbol   *refactorTypes.Symbol
		expected bool
	}{
		{
			name:     "Function symbol",
			symbol:   &refactorTypes.Symbol{Kind: refactorTypes.FunctionSymbol},
			expected: true,
		},
		{
			name:     "Type symbol",
			symbol:   &refactorTypes.Symbol{Kind: refactorTypes.TypeSymbol},
			expected: true,
		},
		{
			name:     "Constant symbol",
			symbol:   &refactorTypes.Symbol{Kind: refactorTypes.ConstantSymbol},
			expected: true,
		},
		{
			name:     "Variable symbol",
			symbol:   &refactorTypes.Symbol{Kind: refactorTypes.VariableSymbol},
			expected: true,
		},
		{
			name:     "Method symbol",
			symbol:   &refactorTypes.Symbol{Kind: refactorTypes.MethodSymbol},
			expected: false,
		},
		{
			name:     "Struct field symbol",
			symbol:   &refactorTypes.Symbol{Kind: refactorTypes.StructFieldSymbol},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := validator.isSymbolMoveable(tc.symbol)
			if result != tc.expected {
				t.Errorf("Expected isSymbolMoveable for %s to be %v, got %v", tc.name, tc.expected, result)
			}
		})
	}
}

func TestValidator_validateGoSyntax(t *testing.T) {
	validator := NewValidator()

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
	validator := NewValidator()

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
	validator := NewValidator()

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