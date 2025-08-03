package errors

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

// RefactorError was moved from /home/mamaar/Development/gorefactor/pkg/types
type RefactorError struct {
	Type    ErrorType
	Message string
	File    string
	Line    int
	Column  int
	Cause   error
}
