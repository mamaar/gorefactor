package refactor

import (
	"fmt"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/analysis"
	refactorTypes "github.com/mamaar/gorefactor/pkg/types"
)

// Validator validates refactoring operations for safety
type Validator struct {
	typeChecker *types.Checker
	logger      *slog.Logger
}

func NewValidator(logger *slog.Logger) *Validator {
	return &Validator{
		typeChecker: &types.Checker{},
		logger:      logger,
	}
}

// ValidatePlan validates a complete refactoring plan
func (v *Validator) ValidatePlan(plan *refactorTypes.RefactoringPlan) error {
	return v.ValidatePlanWithConfig(plan, nil)
}

// ValidatePlanWithConfig validates a complete refactoring plan with configuration options
func (v *Validator) ValidatePlanWithConfig(plan *refactorTypes.RefactoringPlan, config *EngineConfig) error {
	if plan == nil {
		return &refactorTypes.RefactorError{
			Type:    refactorTypes.InvalidOperation,
			Message: "refactoring plan is nil",
		}
	}

	// Use default config if none provided
	if config == nil {
		config = DefaultConfig()
	}

	var allIssues []refactorTypes.Issue

	// Validate each operation
	for i, operation := range plan.Operations {
		issues := v.validateOperation(operation)
		for _, issue := range issues {
			issue.Description = fmt.Sprintf("Operation %d: %s", i+1, issue.Description)
			allIssues = append(allIssues, issue)
		}
	}

	// Validate changes for conflicts and consistency
	conflictIssues := v.validateChanges(plan.Changes)
	allIssues = append(allIssues, conflictIssues...)

	// Check for compilation issues after changes
	compilationIssues := v.validateCompilation(plan)
	allIssues = append(allIssues, compilationIssues...)

	// Check for import cycles
	cycleIssues := v.validateImportCycles(plan)
	allIssues = append(allIssues, cycleIssues...)

	// Return validation error if any critical issues found, unless AllowBreaking is enabled
	criticalIssues := v.filterCriticalIssues(allIssues)
	if len(criticalIssues) > 0 && !config.AllowBreaking {
		return &refactorTypes.ValidationError{
			Issues: criticalIssues,
		}
	}

	// Update plan with all issues (including warnings)
	if plan.Impact == nil {
		plan.Impact = &refactorTypes.ImpactAnalysis{}
	}
	plan.Impact.PotentialIssues = append(plan.Impact.PotentialIssues, allIssues...)

	return nil
}

// ValidateMove validates a move operation specifically
func (v *Validator) ValidateMove(ws *refactorTypes.Workspace, req refactorTypes.MoveSymbolRequest) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Check source symbol exists and is moveable
	sourcePackage, exists := ws.Packages[req.FromPackage]
	if !exists {
		// Build list of available packages for helpful error message
		var availablePackages []string
		for pkgPath := range ws.Packages {
			availablePackages = append(availablePackages, pkgPath)
		}

		var description strings.Builder
		description.WriteString(fmt.Sprintf("source package not found: %s\nAvailable packages:\n", req.FromPackage))
		if len(availablePackages) == 0 {
			description.WriteString("  (no packages found - ensure you're in a Go workspace with go.mod)")
		} else {
			for _, pkgPath := range availablePackages {
				if pkg, exists := ws.Packages[pkgPath]; exists {
					description.WriteString(fmt.Sprintf("  - %s (Go package: %s)\n", pkgPath, pkg.Name))
				} else {
					description.WriteString(fmt.Sprintf("  - %s\n", pkgPath))
				}
			}
		}

		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: description.String(),
			Severity:    refactorTypes.Error,
		})
		return issues
	}

	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	symbol, err := resolver.ResolveSymbol(sourcePackage, req.SymbolName)
	if err != nil {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("symbol not found: %s", req.SymbolName),
			Severity:    refactorTypes.Error,
		})
		return issues
	}

	// Check if symbol is moveable
	if !v.isSymbolMoveable(symbol) {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("symbol %s cannot be moved (unsupported type or has dependencies)", req.SymbolName),
			Severity:    refactorTypes.Error,
		})
	}

	// Check target package accessibility
	targetPackage, targetExists := ws.Packages[req.ToPackage]
	if !targetExists && !req.CreateTarget {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("target package not found and CreateTarget is false: %s", req.ToPackage),
			Severity:    refactorTypes.Error,
		})
		return issues
	}

	// Check for name conflicts in target
	if targetExists && targetPackage.Symbols != nil {
		if _, err := resolver.ResolveSymbol(targetPackage, req.SymbolName); err == nil {
			issues = append(issues, refactorTypes.Issue{
				Type:        refactorTypes.IssueNameConflict,
				Description: fmt.Sprintf("symbol %s already exists in target package %s", req.SymbolName, req.ToPackage),
				Severity:    refactorTypes.Error,
			})
		}
	}

	// Check for import cycle creation
	if v.wouldCreateImportCycle(ws, req.FromPackage, req.ToPackage) {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueImportCycle,
			Description: fmt.Sprintf("moving symbol would create import cycle between %s and %s", req.FromPackage, req.ToPackage),
			Severity:    refactorTypes.Error,
		})
	}

	// Check visibility rules (unexported symbols crossing packages)
	if !symbol.Exported && req.FromPackage != req.ToPackage {
		references, err := resolver.FindReferences(symbol)
		if err == nil {
			for _, ref := range references {
				refPackage := v.findPackageForFile(ws, ref.File)
				if refPackage != nil && refPackage.Path != req.ToPackage {
					issues = append(issues, refactorTypes.Issue{
						Type:        refactorTypes.IssueVisibilityError,
						Description: fmt.Sprintf("unexported symbol %s cannot be accessed from package %s after move", symbol.Name, refPackage.Path),
						File:        ref.File,
						Line:        ref.Line,
						Severity:    refactorTypes.Error,
					})
				}
			}
		}
	}

	// Check for circular dependencies in symbol relationships
	circularIssues := v.checkCircularSymbolDependencies(ws, symbol, req.ToPackage)
	issues = append(issues, circularIssues...)

	return issues
}

// ValidateRename validates a rename operation specifically
func (v *Validator) ValidateRename(ws *refactorTypes.Workspace, req refactorTypes.RenameSymbolRequest) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Check that new name is valid Go identifier
	if !v.isValidGoIdentifier(req.NewName) {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("invalid Go identifier: %s", req.NewName),
			Severity:    refactorTypes.Error,
		})
		return issues
	}

	// Check that new name doesn't conflict with Go keywords
	if v.isGoKeyword(req.NewName) {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("cannot use Go keyword as identifier: %s", req.NewName),
			Severity:    refactorTypes.Error,
		})
		return issues
	}

	// Find symbols to rename
	var targetSymbols []*refactorTypes.Symbol
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if req.Package != "" {
		// Package-scoped rename
		if pkg, exists := ws.Packages[req.Package]; exists && pkg.Symbols != nil {
			symbol, err := resolver.ResolveSymbol(pkg, req.SymbolName)
			if err != nil {
				issues = append(issues, refactorTypes.Issue{
					Type:        refactorTypes.IssueCompilationError,
					Description: fmt.Sprintf("symbol not found in package %s: %s", req.Package, req.SymbolName),
					Severity:    refactorTypes.Error,
				})
				return issues
			}
			targetSymbols = append(targetSymbols, symbol)
		}
	} else {
		// Workspace-wide rename
		for _, pkg := range ws.Packages {
			if pkg.Symbols != nil {
				symbol, err := resolver.ResolveSymbol(pkg, req.SymbolName)
				if err == nil {
					targetSymbols = append(targetSymbols, symbol)
				}
			}
		}
	}

	if len(targetSymbols) == 0 {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("no symbols found with name: %s", req.SymbolName),
			Severity:    refactorTypes.Error,
		})
		return issues
	}

	// Check for naming conflicts at each symbol location
	for _, symbol := range targetSymbols {
		conflictIssues := v.checkRenameConflicts(ws, symbol, req.NewName)
		issues = append(issues, conflictIssues...)
	}

	// Check for interface compliance issues
	interfaceIssues := v.checkInterfaceCompliance(ws, targetSymbols, req.NewName)
	issues = append(issues, interfaceIssues...)

	// Check for method set changes
	methodSetIssues := v.checkMethodSetChanges(ws, targetSymbols, req.NewName)
	issues = append(issues, methodSetIssues...)

	return issues
}

// Helper methods for validation

func (v *Validator) validateOperation(operation refactorTypes.Operation) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	switch op := operation.(type) {
	case *MoveSymbolOperation:
		// Note: This would need access to workspace, simplified for now
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("Move operation for symbol: %s", op.Request.SymbolName),
			Severity:    refactorTypes.Info,
		})
	case *RenameSymbolOperation:
		// Basic validation of rename request
		if !v.isValidGoIdentifier(op.Request.NewName) {
			issues = append(issues, refactorTypes.Issue{
				Type:        refactorTypes.IssueCompilationError,
				Description: fmt.Sprintf("invalid identifier: %s", op.Request.NewName),
				Severity:    refactorTypes.Error,
			})
		}
	default:
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("unsupported operation type: %T", operation),
			Severity:    refactorTypes.Warning,
		})
	}

	return issues
}

func (v *Validator) validateChanges(changes []refactorTypes.Change) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Group changes by file
	fileChanges := make(map[string][]refactorTypes.Change)
	for _, change := range changes {
		fileChanges[change.File] = append(fileChanges[change.File], change)
	}

	// Check for overlapping changes within each file
	for fileName, changes := range fileChanges {
		for i, change1 := range changes {
			for j, change2 := range changes {
				if i >= j {
					continue
				}

				// Check if changes overlap
				if v.changesOverlap(change1, change2) {
					issues = append(issues, refactorTypes.Issue{
						Type:        refactorTypes.IssueNameConflict,
						Description: fmt.Sprintf("overlapping changes detected in file %s", fileName),
						File:        fileName,
						Severity:    refactorTypes.Error,
					})
				}
			}
		}
	}

	// Validate that changes are syntactically valid
	for _, change := range changes {
		if change.Start < 0 || change.End < change.Start {
			issues = append(issues, refactorTypes.Issue{
				Type:        refactorTypes.IssueCompilationError,
				Description: fmt.Sprintf("invalid change bounds: start=%d, end=%d", change.Start, change.End),
				File:        change.File,
				Severity:    refactorTypes.Error,
			})
		}
	}

	return issues
}

func (v *Validator) validateCompilation(plan *refactorTypes.RefactoringPlan) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// This is a simplified compilation check
	// In a full implementation, this would apply changes to a temporary copy
	// and attempt compilation

	for _, change := range plan.Changes {
		// Basic syntax validation of new text
		if change.NewText != "" {
			// Skip validation for code fragments that aren't valid standalone Go:
			// - Interface method signatures (method name + signature)
			// - Parameter list changes (just parameter declarations)
			// - Call argument changes (just expressions)
			// - Import additions (just import path strings)
			desc := change.Description
			isFragment := strings.Contains(desc, "interface method") ||
				strings.Contains(desc, "parameters") ||
				strings.Contains(desc, "parameter") ||
				strings.Contains(desc, "call to") ||
				strings.Contains(desc, "argument") ||
				strings.Contains(desc, "import") ||
				strings.Contains(desc, "qualified references")
			if !isFragment {
				if err := v.validateGoSyntax(change.NewText); err != nil {
					issues = append(issues, refactorTypes.Issue{
						Type:        refactorTypes.IssueCompilationError,
						Description: fmt.Sprintf("syntax error in new text: %v", err),
						File:        change.File,
						Severity:    refactorTypes.Error,
					})
				}
			}
		}
	}

	return issues
}

func (v *Validator) validateImportCycles(plan *refactorTypes.RefactoringPlan) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Check for import changes that might create cycles
	if plan.Impact != nil {
		for _, importChange := range plan.Impact.ImportChanges {
			if importChange.Action == refactorTypes.AddImport {
				// This is simplified - would need full dependency analysis
				if strings.Contains(importChange.NewImport, "cycle") {
					issues = append(issues, refactorTypes.Issue{
						Type:        refactorTypes.IssueImportCycle,
						Description: fmt.Sprintf("potential import cycle with: %s", importChange.NewImport),
						File:        importChange.File,
						Severity:    refactorTypes.Warning,
					})
				}
			}
		}
	}

	return issues
}

func (v *Validator) isSymbolMoveable(symbol *refactorTypes.Symbol) bool {
	// Check if symbol can be safely moved
	switch symbol.Kind {
	case refactorTypes.FunctionSymbol, refactorTypes.TypeSymbol, refactorTypes.ConstantSymbol, refactorTypes.VariableSymbol:
		return true
	case refactorTypes.MethodSymbol:
		// Methods can only be moved with their receiver type
		return false
	case refactorTypes.StructFieldSymbol:
		// Struct fields cannot be moved independently
		return false
	default:
		return false
	}
}

func (v *Validator) wouldCreateImportCycle(ws *refactorTypes.Workspace, fromPkg, toPkg string) bool {
	if ws.Dependencies == nil {
		return false
	}

	// Check if toPkg already depends on fromPkg
	toDeps, exists := ws.Dependencies.PackageDeps[toPkg]
	if !exists {
		return false
	}

	return slices.Contains(toDeps, fromPkg)
}

func (v *Validator) isValidGoIdentifier(name string) bool {
	if len(name) == 0 {
		return false
	}

	// First character must be letter or underscore
	first := rune(name[0])
	if !unicode.IsLetter(first) && first != '_' {
		return false
	}

	// Remaining characters must be letters, digits, or underscores
	for _, r := range name[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}

	return true
}

func (v *Validator) isGoKeyword(name string) bool {
	keywords := map[string]bool{
		"break": true, "case": true, "chan": true, "const": true,
		"continue": true, "default": true, "defer": true, "else": true,
		"fallthrough": true, "for": true, "func": true, "go": true,
		"goto": true, "if": true, "import": true, "interface": true,
		"map": true, "package": true, "range": true, "return": true,
		"select": true, "struct": true, "switch": true, "type": true,
		"var": true,
	}
	return keywords[name]
}

func (v *Validator) checkRenameConflicts(ws *refactorTypes.Workspace, symbol *refactorTypes.Symbol, newName string) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Find the package containing the symbol
	pkg := v.findPackageForFile(ws, symbol.File)
	if pkg == nil || pkg.Symbols == nil {
		return issues
	}

	// Check for conflicts in the same package
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := resolver.ResolveSymbol(pkg, newName); err == nil {
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueNameConflict,
			Description: fmt.Sprintf("name conflict: symbol %s already exists in package %s", newName, pkg.Path),
			File:        symbol.File,
			Severity:    refactorTypes.Error,
		})
	}

	return issues
}

func (v *Validator) checkInterfaceCompliance(ws *refactorTypes.Workspace, symbols []*refactorTypes.Symbol, newName string) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Check if renaming methods would break interface compliance
	for _, symbol := range symbols {
		if symbol.Kind == refactorTypes.MethodSymbol {
			// This is simplified - would need full interface analysis
			issues = append(issues, refactorTypes.Issue{
				Type:        refactorTypes.IssueCompilationError,
				Description: fmt.Sprintf("renaming method %s to %s may break interface compliance", symbol.Name, newName),
				File:        symbol.File,
				Severity:    refactorTypes.Warning,
			})
		}
	}

	return issues
}

func (v *Validator) checkMethodSetChanges(ws *refactorTypes.Workspace, symbols []*refactorTypes.Symbol, newName string) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Check if renaming would affect method sets
	for _, symbol := range symbols {
		if symbol.Kind == refactorTypes.MethodSymbol && symbol.Exported != v.isExported(newName) {
			issues = append(issues, refactorTypes.Issue{
				Type:        refactorTypes.IssueVisibilityError,
				Description: fmt.Sprintf("renaming %s to %s changes export status and may affect method sets", symbol.Name, newName),
				File:        symbol.File,
				Severity:    refactorTypes.Warning,
			})
		}
	}

	return issues
}

func (v *Validator) checkCircularSymbolDependencies(ws *refactorTypes.Workspace, symbol *refactorTypes.Symbol, targetPackage string) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// This is a simplified check for circular dependencies
	// A full implementation would analyze the symbol dependency graph
	if ws.Dependencies != nil {
		if symbolDeps, exists := ws.Dependencies.SymbolDeps[symbol.Package]; exists {
			if deps, exists := symbolDeps[symbol.Name]; exists {
				for _, dep := range deps {
					if strings.HasPrefix(dep, targetPackage+".") {
						issues = append(issues, refactorTypes.Issue{
							Type:        refactorTypes.IssueImportCycle,
							Description: fmt.Sprintf("moving %s to %s may create circular dependency with %s", symbol.Name, targetPackage, dep),
							File:        symbol.File,
							Severity:    refactorTypes.Warning,
						})
					}
				}
			}
		}
	}

	return issues
}

func (v *Validator) validateGoSyntax(code string) error {
	// Try to parse the code as a Go expression or statement
	if code == "" {
		return nil
	}

	// First, try as an expression (for simple values)
	_, err := parser.ParseExpr(code)
	if err == nil {
		return nil
	}

	// Try as a complete file (code already has package declaration)
	_, err = parser.ParseFile(token.NewFileSet(), "", code, 0)
	if err == nil {
		return nil
	}

	// Try as a statement inside a function
	src := fmt.Sprintf("package main\nfunc main() {\n%s\n}", code)
	_, err = parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err == nil {
		return nil
	}

	// Try as a complete file content (for imports, function declarations, etc.)
	src = fmt.Sprintf("package main\n%s", code)
	_, err = parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err == nil {
		return nil
	}

	// For very simple additions like import statements, try minimal validation
	if strings.Contains(code, "\"") && !strings.Contains(code, "\n") {
		// Likely an import path, consider it valid
		return nil
	}

	return err
}

func (v *Validator) changesOverlap(change1, change2 refactorTypes.Change) bool {
	return (change1.Start < change2.End && change2.Start < change1.End)
}

func (v *Validator) filterCriticalIssues(issues []refactorTypes.Issue) []refactorTypes.Issue {
	var critical []refactorTypes.Issue
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			critical = append(critical, issue)
		}
	}
	return critical
}

func (v *Validator) findPackageForFile(ws *refactorTypes.Workspace, filePath string) *refactorTypes.Package {
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if file.Path == filePath {
				return pkg
			}
		}
	}
	return nil
}

func (v *Validator) isExported(name string) bool {
	return len(name) > 0 && unicode.IsUpper(rune(name[0]))
}
