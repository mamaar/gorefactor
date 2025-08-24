package types

// Operation represents any refactoring operation
type Operation interface {
	Type() OperationType
	Validate(ws *Workspace) error
	Execute(ws *Workspace) (*RefactoringPlan, error)
	Description() string
}

type OperationType int

const (
	MoveOperation OperationType = iota
	RenameOperation
	RenamePackageOperation
	ExtractOperation
	InlineOperation
	BatchOperation
)

// MoveSymbolRequest represents moving a symbol between packages
type MoveSymbolRequest struct {
	SymbolName   string
	FromPackage  string
	ToPackage    string
	CreateTarget bool   // Create target package if it doesn't exist
	UpdateTests  bool   // Update test files as well
}

// RenameSymbolRequest represents renaming a symbol
type RenameSymbolRequest struct {
	SymbolName string
	NewName    string
	Package    string  // Empty means workspace-wide
	Scope      RenameScope
}

// RenamePackageRequest represents renaming a package
type RenamePackageRequest struct {
	OldPackageName string
	NewPackageName string
	PackagePath    string // Path to the package directory
	UpdateImports  bool   // Whether to update import statements in other packages
}

type RenameScope int

const (
	PackageScope RenameScope = iota
	WorkspaceScope
)

// ExtractMethodRequest represents extracting a method from code
type ExtractMethodRequest struct {
	SourceFile    string
	StartLine     int
	EndLine       int
	NewMethodName string
	TargetStruct  string
}

// ExtractFunctionRequest represents extracting a function from code
type ExtractFunctionRequest struct {
	SourceFile      string
	StartLine       int
	EndLine         int
	NewFunctionName string
}

// ExtractInterfaceRequest represents extracting an interface from a struct
type ExtractInterfaceRequest struct {
	SourceStruct  string
	InterfaceName string
	Methods       []string
	TargetPackage string
}

// ExtractVariableRequest represents extracting a variable from an expression
type ExtractVariableRequest struct {
	SourceFile   string
	StartLine    int
	EndLine      int
	VariableName string
	Expression   string
}

// InlineMethodRequest represents inlining a method call with its implementation
type InlineMethodRequest struct {
	MethodName   string
	SourceStruct string
	TargetFile   string
	CallSites    []CallSite // Specific call sites to inline, empty means all
}

// InlineVariableRequest represents inlining a variable with its value
type InlineVariableRequest struct {
	VariableName string
	SourceFile   string
	TargetFiles  []string // Files where to inline the variable
}

// InlineFunctionRequest represents inlining a function call with its implementation  
type InlineFunctionRequest struct {
	FunctionName string
	SourceFile   string
	TargetFiles  []string // Files where to inline the function
}

// CallSite represents a specific location where a method/function is called
type CallSite struct {
	File   string
	Line   int
	Column int
}

// RefactoringPlan represents a planned set of changes
type RefactoringPlan struct {
	Operations    []Operation
	Changes       []Change
	AffectedFiles []string
	Impact        *ImpactAnalysis
	Reversible    bool
}

// Change represents a specific change to be made
type Change struct {
	File        string
	Start       int
	End         int
	OldText     string
	NewText     string
	Description string
}

// ImpactAnalysis shows what will be affected by a refactoring
type ImpactAnalysis struct {
	AffectedPackages []string
	AffectedFiles    []string
	AffectedSymbols  []*Symbol
	PotentialIssues  []Issue
	ImportChanges    []ImportChange
}

type Issue struct {
	Type        IssueType
	Description string
	File        string
	Line        int
	Severity    IssueSeverity
}

type IssueType int

const (
	IssueCompilationError IssueType = iota
	IssueImportCycle
	IssueVisibilityError
	IssueNameConflict
	IssueTypeMismatch
)

type IssueSeverity int

const (
	Error IssueSeverity = iota
	Warning
	Info
)

// String returns the string representation of IssueSeverity
func (s IssueSeverity) String() string {
	switch s {
	case Error:
		return "Error"
	case Warning:
		return "Warning"
	case Info:
		return "Info"
	default:
		return "Unknown"
	}
}

type ImportChange struct {
	File      string
	OldImport string
	NewImport string
	Action    ImportAction
}

type ImportAction int

const (
	AddImport ImportAction = iota
	RemoveImport
	UpdateImport
)