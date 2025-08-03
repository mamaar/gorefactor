package types

import "fmt"

// RefactorError represents errors in refactoring operations
type RefactorError struct {
	Type    ErrorType
	Message string
	File    string
	Line    int
	Column  int
	Cause   error
}

func (e *RefactorError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Column, e.Message)
	}
	return e.Message
}

func (e *RefactorError) Unwrap() error {
	return e.Cause
}

type ErrorType int

const (
	ParseError ErrorType = iota
	SymbolNotFound
	InvalidOperation
	CompilationError
	CyclicDependency
	VisibilityViolation
	NameConflict
	FileSystemError
)

// ValidationError represents validation failures
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed with %d issues", len(e.Issues))
}
