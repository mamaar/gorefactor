package types

import (
	"os"
	"strings"
)

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
	RenameMethodOperation
	ExtractOperation
	InlineOperation
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
	AnalyzeDependenciesOperation
	BatchOperations
	RenameFieldOperationType
	ThreadContextOperationType
)

// QualifiedSymbol names a symbol by its declaring package and name.
type QualifiedSymbol struct {
	Package string
	Name    string
}

// MoveSymbolRequest represents moving symbols between packages
type MoveSymbolRequest struct {
	SymbolName   string
	FromPackage  string
	ToPackage    string
	CreateTarget bool // Create target package if it doesn't exist
	UpdateTests  bool // Update test files as well

	// ToFile, when set, is the file of ToPackage the symbols land in, as an
	// absolute path or one relative to the workspace root; a missing file is
	// created. ToPackage may then equal the source package.
	ToFile string

	// Symbols, when set, replaces SymbolName and FromPackage: all of them move
	// to ToPackage in one plan.
	Symbols []QualifiedSymbol
}

// Refs returns the symbols the request moves, each package and name once.
func (r MoveSymbolRequest) Refs() []QualifiedSymbol {
	if len(r.Symbols) > 0 {
		seen := make(map[QualifiedSymbol]bool, len(r.Symbols))
		refs := make([]QualifiedSymbol, 0, len(r.Symbols))
		for _, ref := range r.Symbols {
			if !seen[ref] {
				seen[ref] = true
				refs = append(refs, ref)
			}
		}
		return refs
	}
	return []QualifiedSymbol{{Package: r.FromPackage, Name: r.SymbolName}}
}

// RenameSymbolRequest represents renaming a symbol
type RenameSymbolRequest struct {
	SymbolName string
	NewName    string
	Package    string // Empty means workspace-wide
	Scope      RenameScope
}

// RenamePackageRequest represents renaming a package
type RenamePackageRequest struct {
	OldPackageName string
	NewPackageName string
	PackagePath    string // Path to the package directory
	UpdateImports  bool   // Whether to update import statements in other packages
}

// RenameMethodRequest represents renaming a method on a specific type (struct or interface)
type RenameMethodRequest struct {
	TypeName      string // Name of the type (struct or interface) that owns the method
	MethodName    string // Current method name
	NewMethodName string // New method name
	PackagePath   string // Path to the package containing the type (optional, "" means workspace-wide)
}

// RenameFieldRequest represents renaming a struct field on a named type
type RenameFieldRequest struct {
	TypeName     string // Named struct type that owns the field
	FieldName    string // Current field name
	NewFieldName string // New field name
	PackagePath  string // Path to the package containing the type ("" means search the workspace)
}

// ThreadContextRequest represents threading context.Context transitively
// through every call chain in the workspace that reaches FunctionName. Unlike
// ChangeSignatureRequest, this carries no CachedIndex: the engine call always
// type-checks the whole workspace and hands the operation its own parser
// (see DefaultEngine.ThreadContext), so the operation builds a fresh,
// post-type-check reference index itself rather than reusing one that might
// predate type-checking.
type ThreadContextRequest struct {
	FunctionName string   // "Process" or "Type.Method"
	SourceFile   string   // File containing the function declaration
	StopAt       []string // Functions the climb stops at: "Name", "Type.Method" or "path/file.go:Type.Method"
	EntryPackage string   // A package main: the climb stops at callers its program does not reach
	// CallSitesOf, with EntryPackage, replaces FunctionName: the seeds are the declarations enclosing the
	// reached call sites of these targets (pkg.Func, pkg.Type.Method or a package path).
	CallSitesOf []string
	// WithinFunction (pkg.Func or pkg.Type.Method; needs CallSitesOf) limits the seeds to the code this
	// function reaches, and the climb stops at callers the entry program reaches outside it.
	WithinFunction string
}

// AddContextParameterRequest adds `ctx context.Context` as the first parameter
// of FunctionName and passes DefaultValue (context.TODO() when empty) at every
// call site. Propagate also rewrites interface declarations and the other
// implementations of the method.
type AddContextParameterRequest struct {
	FunctionName string // "Process" or "Type.Method"
	SourceFile   string // File containing the function declaration
	DefaultValue string
	Propagate    bool
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

// SkippedSite records a call site that could not be automatically rewritten,
// or a declaration an operation left in place.
type SkippedSite struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Reason  string `json:"reason"`
	Snippet string `json:"snippet"`
}

// Blocker is a use of a symbol that stands in the way of an operation which
// needs the symbol unused.
type Blocker struct {
	Reference
	// Configs names the build configurations that see the use, or that include
	// the file of an unverified one. It is empty for a resolved reference every
	// configuration of the run sees.
	Configs []string
	// Unverified says why the use is known by name only: no configuration
	// type-checks the file, or the identifier sits in an erroring expression.
	// It is empty for a resolved reference.
	Unverified string
}

// Note returns what sets the blocker apart from a reference every
// configuration resolves, as a suffix to its file:line, or "".
func (b Blocker) Note() string {
	note := ""
	if len(b.Configs) > 0 {
		note = " [" + strings.Join(b.Configs, ", ") + "]"
	}
	if b.Unverified != "" {
		note += " (unverified: " + b.Unverified + ")"
	}
	return note
}

// RefactoringPlan represents a planned set of changes
type RefactoringPlan struct {
	Operations    []Operation
	Changes       []Change
	AffectedFiles []string
	Impact        *ImpactAnalysis
	Reversible    bool
	// SkippedSites holds call sites that were too complex to rewrite automatically,
	// or declarations left unmoved. The caller should inspect these; call sites need manual updates.
	SkippedSites []SkippedSite
	// Coverage says which build configurations the plan was computed under.
	// It is empty for a plan made in a single view.
	Coverage Coverage
}

// Coverage describes how much of the workspace's build configurations a plan
// accounts for.
type Coverage struct {
	Configs []string // display names, primary first
	// Uncovered lists, by path, the files in the packages the plan reached
	// that no configuration type-checks. Edits they need are not in the plan.
	Uncovered []UncoveredFile
	// TypeErrors lists the packages with type errors in non-test files that
	// the plan reached or whose erroring files name a changed symbol. Uses
	// inside the erroring code may be missing from the plan.
	TypeErrors []PackageTypeError
}

// PackageTypeError is the first type error in the non-test files of a package
// under one build configuration.
type PackageTypeError struct {
	Package string // import path
	Config  string
	Error   string
}

// UncoveredFile is a file a plan could not account for.
type UncoveredFile struct {
	Path    string
	Configs []string // the configurations that include the file; none when no configuration can
	Reason  string
}

// Change represents a specific change to be made
type Change struct {
	File        string
	Start       int
	End         int
	OldText     string
	NewText     string
	Description string
	// Delete, when true, marks this change as a whole-file deletion: the
	// serializer removes File from disk (and prunes now-empty parent
	// directories) instead of writing NewText. Start/End/OldText/NewText are
	// ignored for a delete. A file targeted by a Delete change must not also
	// have other (edit) changes in the same plan.
	Delete bool
	// Relocation marks a change that belongs to relocating a whole file or
	// package (a move or a package rename).
	Relocation bool
	// Mode is the permission bits a newly created file gets; zero means 0644.
	// A file that already exists keeps its own mode.
	Mode os.FileMode
}

// Carried reports whether the change creates a file relocated verbatim, which
// a preview summarises instead of showing its content.
func (c Change) Carried() bool { return c.Relocation && !c.Delete && c.Mode != 0 }

// SuggestedMove represents a symbol that would benefit from being moved
type SuggestedMove struct {
	Symbol              string   `json:"symbol"`
	FromPackage         string   `json:"from_package"`
	ToPackage           string   `json:"to_package"`
	Reason              string   `json:"reason"`
	ReferencingPackages []string `json:"referencing_packages"`
}

// BackwardDependency is an import from a more inner layer to a more outer one.
type BackwardDependency struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// PackageCouplingInfo holds coupling metrics for a single package
type PackageCouplingInfo struct {
	IncomingDeps int `json:"incoming_deps"`
	OutgoingDeps int `json:"outgoing_deps"`
	SymbolCount  int `json:"symbol_count"`
}

// ImpactAnalysis shows what will be affected by a refactoring
type ImpactAnalysis struct {
	AffectedPackages      []string
	AffectedFiles         []string
	AffectedSymbols       []*Symbol
	PotentialIssues       []Issue
	ImportChanges         []ImportChange
	SuggestedMoves        []SuggestedMove                `json:"suggested_moves,omitempty"`
	PackageCoupling       map[string]PackageCouplingInfo `json:"package_coupling,omitempty"`
	BackwardsDependencies []BackwardDependency           `json:"backwards_dependencies,omitempty"`
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
	UpdateImports bool
}

// MovePackagesRequest represents moving multiple packages atomically
type MovePackagesRequest struct {
	Packages      []PackageMapping
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
	ModulesDir  string
	TargetDir   string
	ExportTypes []string // e.g., "commands", "models", "events"
}

// UpdateFacadesRequest represents updating existing facades
type UpdateFacadesRequest struct {
	FacadePackages []string
	AutoDetect     bool
}

// CleanAliasesRequest represents removing import aliases
type CleanAliasesRequest struct {
	Workspace         string
	PreserveConflicts bool // keep aliases only where needed to resolve conflicts
}

// StandardizeImportsRequest represents standardizing import aliases
type StandardizeImportsRequest struct {
	Workspace string
	Rules     []AliasRule
	Files     []string // absolute paths; when set, only these files are standardized
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
	Workspace     string
	ToFullNames   bool
	FromFullNames bool
}

// MoveByDependenciesRequest represents moving symbols based on dependency analysis
type MoveByDependenciesRequest struct {
	Workspace    string
	MoveSharedTo string   // e.g., "pkg/"
	KeepInternal []string // e.g., ["internal/app", "internal/handlers"]
	AnalyzeOnly  bool     // If true, only analyze and suggest moves
}

// OrganizeByLayersRequest represents organizing imports/packages by architectural layers
type OrganizeByLayersRequest struct {
	Workspace           string
	DomainLayer         string // e.g., "modules/"
	InfrastructureLayer string // e.g., "pkg/"
	ApplicationLayer    string // e.g., "internal/"
	ReorderImports      bool   // Whether to reorder imports according to layers
}

// AnalyzeDependenciesRequest represents analyzing dependency flow
type AnalyzeDependenciesRequest struct {
	Workspace           string
	DetectBackwardsDeps bool
	SuggestMoves        bool
	OutputFile          string // File to write analysis results
}

// BatchOperationRequest represents executing multiple operations atomically
type BatchOperationRequest struct {
	Operations []string // Command strings to execute
}

// ExtractConstantRequest represents extracting a literal into a package-level constant
type ExtractConstantRequest struct {
	SourceFile   string
	Line         int // 1-based, any position inside the literal
	Column       int // 1-based byte column
	ConstantName string
}
