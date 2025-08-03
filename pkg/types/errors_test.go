package types

import (
	"errors"
	"strings"
	"testing"
)

func TestRefactorError(t *testing.T) {
	err := &RefactorError{
		Type:    ParseError,
		Message: "Failed to parse file",
		File:    "/test/file.go",
		Line:    15,
		Column:  10,
	}

	if err.Type != ParseError {
		t.Errorf("Expected Type to be ParseError, got %v", err.Type)
	}

	if err.Message != "Failed to parse file" {
		t.Errorf("Expected Message to be 'Failed to parse file', got '%s'", err.Message)
	}

	if err.File != "/test/file.go" {
		t.Errorf("Expected File to be '/test/file.go', got '%s'", err.File)
	}

	if err.Line != 15 {
		t.Errorf("Expected Line to be 15, got %d", err.Line)
	}

	if err.Column != 10 {
		t.Errorf("Expected Column to be 10, got %d", err.Column)
	}
}

func TestRefactorError_Error(t *testing.T) {
	testCases := []struct {
		name     string
		err      *RefactorError
		expected string
	}{
		{
			name: "With file location",
			err: &RefactorError{
				Type:    ParseError,
				Message: "Failed to parse",
				File:    "/test/file.go",
				Line:    15,
				Column:  10,
			},
			expected: "/test/file.go:15:10: Failed to parse",
		},
		{
			name: "Without file location",
			err: &RefactorError{
				Type:    SymbolNotFound,
				Message: "Symbol not found",
				File:    "",
				Line:    0,
				Column:  0,
			},
			expected: "Symbol not found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.err.Error()
			if result != tc.expected {
				t.Errorf("Expected error message '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestRefactorError_Unwrap(t *testing.T) {
	cause := errors.New("original error")
	err := &RefactorError{
		Type:    FileSystemError,
		Message: "File operation failed",
		Cause:   cause,
	}

	unwrapped := err.Unwrap()
	if unwrapped != cause {
		t.Errorf("Expected unwrapped error to be original error, got %v", unwrapped)
	}

	// Test with nil cause
	errNoCause := &RefactorError{
		Type:    ParseError,
		Message: "Parse failed",
		Cause:   nil,
	}

	unwrappedNil := errNoCause.Unwrap()
	if unwrappedNil != nil {
		t.Errorf("Expected unwrapped error to be nil, got %v", unwrappedNil)
	}
}

func TestErrorType(t *testing.T) {
	testCases := []struct {
		name     string
		errType  ErrorType
		expected ErrorType
	}{
		{"ParseError", ParseError, 0},
		{"SymbolNotFound", SymbolNotFound, 1},
		{"InvalidOperation", InvalidOperation, 2},
		{"CompilationError", CompilationError, 3},
		{"CyclicDependency", CyclicDependency, 4},
		{"VisibilityViolation", VisibilityViolation, 5},
		{"NameConflict", NameConflict, 6},
		{"FileSystemError", FileSystemError, 7},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.errType != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.errType)
			}
		})
	}
}

func TestValidationError(t *testing.T) {
	issues := []Issue{
		{
			Type:        IssueCompilationError,
			Description: "Compilation error 1",
			Severity:    Error,
		},
		{
			Type:        IssueNameConflict,
			Description: "Name conflict",
			Severity:    Warning,
		},
	}

	validationErr := &ValidationError{
		Issues: issues,
	}

	if len(validationErr.Issues) != 2 {
		t.Errorf("Expected 2 issues, got %d", len(validationErr.Issues))
	}

	if validationErr.Issues[0].Type != IssueCompilationError {
		t.Errorf("Expected first issue type to be IssueCompilationError, got %v", validationErr.Issues[0].Type)
	}

	if validationErr.Issues[1].Type != IssueNameConflict {
		t.Errorf("Expected second issue type to be IssueNameConflict, got %v", validationErr.Issues[1].Type)
	}
}

func TestValidationError_Error(t *testing.T) {
	testCases := []struct {
		name       string
		issueCount int
		expected   string
	}{
		{
			name:       "Single issue",
			issueCount: 1,
			expected:   "validation failed with 1 issues",
		},
		{
			name:       "Multiple issues",
			issueCount: 5,
			expected:   "validation failed with 5 issues",
		},
		{
			name:       "No issues",
			issueCount: 0,
			expected:   "validation failed with 0 issues",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			issues := make([]Issue, tc.issueCount)
			for i := range issues {
				issues[i] = Issue{
					Type:        IssueCompilationError,
					Description: "Test issue",
					Severity:    Error,
				}
			}

			validationErr := &ValidationError{Issues: issues}
			result := validationErr.Error()

			if result != tc.expected {
				t.Errorf("Expected error message '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestErrorChaining(t *testing.T) {
	// Test error wrapping and unwrapping
	originalErr := errors.New("original error")
	
	refactorErr := &RefactorError{
		Type:    FileSystemError,
		Message: "File operation failed",
		File:    "/test/file.go",
		Line:    10,
		Column:  5,
		Cause:   originalErr,
	}

	// Test that error implements error interface
	var err error = refactorErr
	errMsg := err.Error()
	expectedMsg := "/test/file.go:10:5: File operation failed"
	
	if errMsg != expectedMsg {
		t.Errorf("Expected error message '%s', got '%s'", expectedMsg, errMsg)
	}

	// Test unwrapping
	if unwrapped := errors.Unwrap(refactorErr); unwrapped != originalErr {
		t.Errorf("Expected unwrapped error to be original error, got %v", unwrapped)
	}

	// Test errors.Is
	if !errors.Is(refactorErr, originalErr) {
		t.Error("Expected errors.Is to return true for wrapped error")
	}
}

func TestErrorMessages(t *testing.T) {
	testCases := []struct {
		name        string
		errorType   ErrorType
		message     string
		shouldMatch string
	}{
		{
			name:        "Parse error",
			errorType:   ParseError,
			message:     "syntax error at line 10",
			shouldMatch: "syntax error",
		},
		{
			name:        "Symbol not found",
			errorType:   SymbolNotFound,
			message:     "function 'TestFunc' not found in package",
			shouldMatch: "not found",
		},
		{
			name:        "Invalid operation",
			errorType:   InvalidOperation,
			message:     "cannot move unexported symbol across packages",
			shouldMatch: "cannot move",
		},
		{
			name:        "Cyclic dependency",
			errorType:   CyclicDependency,
			message:     "import cycle detected: pkg1 -> pkg2 -> pkg1",
			shouldMatch: "cycle detected",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := &RefactorError{
				Type:    tc.errorType,
				Message: tc.message,
			}

			errorMsg := err.Error()
			if !strings.Contains(errorMsg, tc.shouldMatch) {
				t.Errorf("Expected error message to contain '%s', got '%s'", tc.shouldMatch, errorMsg)
			}

			if !strings.Contains(errorMsg, tc.message) {
				t.Errorf("Expected error message to contain original message '%s', got '%s'", tc.message, errorMsg)
			}
		})
	}
}