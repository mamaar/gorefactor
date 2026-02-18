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
	RenameInterfaceMethodOperation
	RenameMethodOperation
	ExtractOperation
	InlineOperation
	BatchOperation
	MovePackageOperation
	MoveDirOperation
	MovePackagesOperation
	CreateFacadeOperation
	GenerateFacadesOperation
	UpdateFacadesOperation
	CleanAliasesOperation
	StandardizeImportsOperation
	ResolveAliasConflictsOperation
	ConvertAliasesOperation
	MoveByDependenciesOperation
	OrganizeByLayersOperation
	FixCyclesOperation
	AnalyzeDependenciesOperation
	BatchOperations
	PlanOperation
	ExecuteOperation
	RollbackOperation
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

// RenameInterfaceMethodRequest represents renaming a method on an interface
type RenameInterfaceMethodRequest struct {
	InterfaceName     string  // Name of the interface
	MethodName        string  // Current method name
	NewMethodName     string  // New method name
	PackagePath       string  // Path to the package containing the interface (optional, "" means workspace-wide)
	UpdateImplementations bool // Whether to update all implementations of the interface
}

// RenameMethodRequest represents renaming a method on a specific type (struct or interface)
type RenameMethodRequest struct {
	TypeName          string  // Name of the type (struct or interface) that owns the method
	MethodName        string  // Current method name
	NewMethodName     string  // New method name
	PackagePath       string  // Path to the package containing the type (optional, "" means workspace-wide)
	UpdateImplementations bool // For interfaces: whether to update all implementations
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

// SuggestedMove represents a symbol that would benefit from being moved
type SuggestedMove struct {
	Symbol              string   `json:"symbol"`
	FromPackage         string   `json:"from_package"`
	ToPackage           string   `json:"to_package"`
	Reason              string   `json:"reason"`
	ReferencingPackages []string `json:"referencing_packages"`
}

// PackageCouplingInfo holds coupling metrics for a single package
type PackageCouplingInfo struct {
	IncomingDeps int `json:"incoming_deps"`
	OutgoingDeps int `json:"outgoing_deps"`
	SymbolCount  int `json:"symbol_count"`
}

// ImpactAnalysis shows what will be affected by a refactoring
type ImpactAnalysis struct {
	AffectedPackages []string
	AffectedFiles    []string
	AffectedSymbols  []*Symbol
	PotentialIssues  []Issue
	ImportChanges    []ImportChange
	SuggestedMoves   []SuggestedMove                `json:"suggested_moves,omitempty"`
	PackageCoupling  map[string]PackageCouplingInfo  `json:"package_coupling,omitempty"`
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

// SafeDeleteRequest represents safely deleting a symbol
type SafeDeleteRequest struct {
	Symbol     string
	SourceFile string
	Force      bool
}

// MovePackageRequest represents moving an entire package
type MovePackageRequest struct {
	SourcePackage string
	TargetPackage string
	CreateTarget  bool
	UpdateImports bool
}

// MoveDirRequest represents moving a directory structure
type MoveDirRequest struct {
	SourceDir     string
	TargetDir     string
	PreserveStructure bool
	UpdateImports bool
}

// MovePackagesRequest represents moving multiple packages atomically
type MovePackagesRequest struct {
	Packages      []PackageMapping
	TargetDir     string
	CreateTargets bool
	UpdateImports bool
}

type PackageMapping struct {
	SourcePackage string
	TargetPackage string
}

// CreateFacadeRequest represents creating a facade package
type CreateFacadeRequest struct {
	TargetPackage string
	Exports       []ExportSpec
}

type ExportSpec struct {
	SourcePackage string
	SymbolName    string
	Alias         string // optional alias for the export
}

// GenerateFacadesRequest represents auto-generating facades for modules
type GenerateFacadesRequest struct {
	ModulesDir string
	TargetDir  string
	ExportTypes []string // e.g., "commands", "models", "events"
}

// UpdateFacadesRequest represents updating existing facades
type UpdateFacadesRequest struct {
	FacadePackages []string
	AutoDetect     bool
}

// CleanAliasesRequest represents removing import aliases
type CleanAliasesRequest struct {
	Workspace      string
	PreserveConflicts bool // keep aliases only where needed to resolve conflicts
}

// StandardizeImportsRequest represents standardizing import aliases
type StandardizeImportsRequest struct {
	Workspace string
	Rules     []AliasRule
}

type AliasRule struct {
	PackagePattern string // e.g., "github.com/user/repo/pkg/events"
	Alias          string // e.g., "events"
}

// ResolveAliasConflictsRequest represents resolving import alias conflicts
type ResolveAliasConflictsRequest struct {
	Workspace string
	Strategy  ConflictStrategy
}

type ConflictStrategy int

const (
	UseFullNames ConflictStrategy = iota
	UseShortestUnique
	UseCustomAlias
)

// ConvertAliasesRequest represents converting between aliased and non-aliased imports
type ConvertAliasesRequest struct {
	Workspace    string
	ToFullNames  bool
	FromFullNames bool
}

// MoveByDependenciesRequest represents moving symbols based on dependency analysis
type MoveByDependenciesRequest struct {
	Workspace      string
	MoveSharedTo   string // e.g., "pkg/"
	KeepInternal   []string // e.g., ["internal/app", "internal/handlers"]
	AnalyzeOnly    bool // If true, only analyze and suggest moves
}

// OrganizeByLayersRequest represents organizing imports/packages by architectural layers
type OrganizeByLayersRequest struct {
	Workspace      string
	DomainLayer    string // e.g., "modules/"
	InfrastructureLayer string // e.g., "pkg/"
	ApplicationLayer string // e.g., "internal/"
	ReorderImports bool // Whether to reorder imports according to layers
}

// FixCyclesRequest represents detecting and fixing circular dependencies
type FixCyclesRequest struct {
	Workspace    string
	AutoFix      bool // If true, attempt automatic fixes
	OutputReport string // Optional: file to write cycle analysis report
}

// AnalyzeDependenciesRequest represents analyzing dependency flow
type AnalyzeDependenciesRequest struct {
	Workspace           string
	DetectBackwardsDeps bool
	SuggestMoves       bool
	OutputFile         string // File to write analysis results
}

// BatchOperationRequest represents executing multiple operations atomically
type BatchOperationRequest struct {
	Operations       []string // Command strings to execute
	RollbackOnFailure bool
	DryRun           bool
}

// PlanOperationRequest represents creating a refactoring plan
type PlanOperationRequest struct {
	Operations []PlanStep
	OutputFile string
	DryRun     bool
}

type PlanStep struct {
	Type string            `json:"type"`
	Args map[string]string `json:"args"`
}

// ExecuteOperationRequest represents executing a previously created plan
type ExecuteOperationRequest struct {
	PlanFile string
}

// RollbackOperationRequest represents rolling back operations
type RollbackOperationRequest struct {
	LastBatch bool
	ToStep    int // Rollback to specific step number
}