package refactor

import (
	"fmt"
	"go/ast"
	"io"
	"log/slog"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// SafeDeleteOperation implements safe deletion of symbols with usage verification
type SafeDeleteOperation struct {
	SymbolName string
	SourceFile string
	Scope      pkgtypes.RenameScope // PackageScope or WorkspaceScope
	Force      bool                 // If true, delete even if references exist
}

func (op *SafeDeleteOperation) Type() pkgtypes.OperationType {
	return pkgtypes.InlineOperation // Reuse inline operation type for now
}

func (op *SafeDeleteOperation) Validate(ws *pkgtypes.Workspace) error {
	if op.SymbolName == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "symbol name cannot be empty",
		}
	}
	if op.SourceFile == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}

	// Check if source file exists
	var sourceFile *pkgtypes.File
	var sourcePackage *pkgtypes.Package
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
		// Also try to match by comparing file paths (for absolute paths from MCP)
		for _, file := range pkg.Files {
			if file.Path == op.SourceFile {
				sourceFile = file
				sourcePackage = pkg
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}
	if sourceFile == nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Find the symbol to delete
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	symbol, err := resolver.ResolveSymbol(sourcePackage, op.SymbolName)
	if err != nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("symbol %s not found: %v", op.SymbolName, err),
		}
	}

	// Check if symbol is safe to delete (no references unless forced)
	if !op.Force {
		references, err := resolver.FindReferences(symbol)
		if err != nil {
			return &pkgtypes.RefactorError{
				Type:    pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("failed to find references: %v", err),
			}
		}

		// Filter references based on scope
		scopedReferences := op.filterReferencesByScope(references, sourcePackage, ws)
		
		// Exclude the definition itself from references
		actualReferences := op.filterOutDefinition(scopedReferences, symbol)
		
		if len(actualReferences) > 0 {
			return &pkgtypes.RefactorError{
				Type:    pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("cannot safely delete %s: found %d references (use --force to delete anyway)", 
					op.SymbolName, len(actualReferences)),
			}
		}
	}

	return nil
}

func (op *SafeDeleteOperation) Execute(ws *pkgtypes.Workspace) (*pkgtypes.RefactoringPlan, error) {
	// Find the source file and symbol
	var sourceFile *pkgtypes.File
	var sourcePackage *pkgtypes.Package
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
		// Also try to match by comparing file paths (for absolute paths from MCP)
		for _, file := range pkg.Files {
			if file.Path == op.SourceFile {
				sourceFile = file
				sourcePackage = pkg
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}

	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	symbol, err := resolver.ResolveSymbol(sourcePackage, op.SymbolName)
	if err != nil {
		return nil, err
	}

	plan := &pkgtypes.RefactoringPlan{
		Changes:       make([]pkgtypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Find the declaration to delete
	declaration := op.findDeclaration(sourceFile, symbol)
	if declaration == nil {
		return nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("declaration for %s not found", op.SymbolName),
		}
	}

	// Generate change to remove the declaration
	removeChange := op.generateRemovalChange(sourceFile, declaration, symbol)
	plan.Changes = append(plan.Changes, removeChange)
	plan.AffectedFiles = append(plan.AffectedFiles, sourceFile.Path)

	// If forced deletion, also remove all references
	if op.Force {
		references, err := resolver.FindReferences(symbol)
		if err != nil {
			return nil, err
		}

		scopedReferences := op.filterReferencesByScope(references, sourcePackage, ws)
		actualReferences := op.filterOutDefinition(scopedReferences, symbol)

		for _, ref := range actualReferences {
			refChanges := op.generateReferenceRemovalChanges(ref, ws)
			plan.Changes = append(plan.Changes, refChanges...)
			
			if !contains(plan.AffectedFiles, ref.File) {
				plan.AffectedFiles = append(plan.AffectedFiles, ref.File)
			}
		}
	}

	// Perform impact analysis
	plan.Impact = &pkgtypes.ImpactAnalysis{
		AffectedFiles:    plan.AffectedFiles,
		AffectedPackages: op.getAffectedPackages(ws, plan.AffectedFiles),
		PotentialIssues:  op.analyzeImpact(ws, symbol, op.Force),
	}

	return plan, nil
}

func (op *SafeDeleteOperation) Description() string {
	scopeStr := "package"
	if op.Scope == pkgtypes.WorkspaceScope {
		scopeStr = "workspace"
	}
	forceStr := ""
	if op.Force {
		forceStr = " (forced)"
	}
	return fmt.Sprintf("Safe delete %s from %s (scope: %s)%s", op.SymbolName, op.SourceFile, scopeStr, forceStr)
}

// Helper methods

func (op *SafeDeleteOperation) filterReferencesByScope(references []*pkgtypes.Reference, sourcePackage *pkgtypes.Package, ws *pkgtypes.Workspace) []*pkgtypes.Reference {
	if op.Scope == pkgtypes.PackageScope {
		// Only include references from the same package
		var filtered []*pkgtypes.Reference
		for _, ref := range references {
			if _, exists := sourcePackage.Files[ref.File]; exists {
				filtered = append(filtered, ref)
			}
		}
		return filtered
	}
	
	// WorkspaceScope - return all references
	return references
}

func (op *SafeDeleteOperation) filterOutDefinition(references []*pkgtypes.Reference, symbol *pkgtypes.Symbol) []*pkgtypes.Reference {
	var filtered []*pkgtypes.Reference
	for _, ref := range references {
		// Skip references that are at the same position as the symbol definition
		if ref.Position != symbol.Position {
			filtered = append(filtered, ref)
		}
	}
	return filtered
}

func (op *SafeDeleteOperation) findDeclaration(file *pkgtypes.File, symbol *pkgtypes.Symbol) ast.Node {
	if file.AST == nil {
		return nil
	}

	var found ast.Node
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		switch decl := n.(type) {
		case *ast.FuncDecl:
			if decl.Name != nil && decl.Name.Name == symbol.Name && decl.Name.Pos() == symbol.Position {
				found = decl
				return false
			}
		case *ast.GenDecl:
			// Check for type, var, or const declarations
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == symbol.Name && s.Name.Pos() == symbol.Position {
						found = decl
						return false
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.Name == symbol.Name && name.Pos() == symbol.Position {
							found = decl
							return false
						}
					}
				}
			}
		}
		return true
	})

	return found
}

func (op *SafeDeleteOperation) generateRemovalChange(file *pkgtypes.File, declaration ast.Node, symbol *pkgtypes.Symbol) pkgtypes.Change {
	// Calculate the range to remove (including comments and whitespace)
	start, end := op.calculateRemovalRange(file, declaration)
	
	// Extract the text to be removed
	oldText := ""
	if start < len(file.OriginalContent) && end <= len(file.OriginalContent) {
		oldText = string(file.OriginalContent[start:end])
	}

	return pkgtypes.Change{
		File:        file.Path,
		Start:       start,
		End:         end,
		OldText:     oldText,
		NewText:     "",
		Description: fmt.Sprintf("Remove declaration of %s", symbol.Name),
	}
}

func (op *SafeDeleteOperation) calculateRemovalRange(file *pkgtypes.File, declaration ast.Node) (int, int) {
	start := int(declaration.Pos())
	end := int(declaration.End())

	// Extend to include leading comments and whitespace
	if file.AST != nil {
		for _, commentGroup := range file.AST.Comments {
			// Check if this comment is associated with our declaration
			if commentGroup.End() < declaration.Pos() && 
			   int(declaration.Pos()) - int(commentGroup.End()) < 100 { // Within reasonable distance
				start = int(commentGroup.Pos())
				break
			}
		}
	}

	// Extend to include trailing newline
	content := file.OriginalContent
	for end < len(content) && (content[end] == '\n' || content[end] == '\r') {
		end++
	}

	// If we're at the beginning of a line, include the entire line
	if start > 0 && content[start-1] == '\n' {
		// We're already at the start of a line, good
	} else {
		// Find the beginning of the line
		for start > 0 && content[start-1] != '\n' {
			start--
		}
	}

	return start, end
}

func (op *SafeDeleteOperation) generateReferenceRemovalChanges(ref *pkgtypes.Reference, ws *pkgtypes.Workspace) []pkgtypes.Change {
	// Find the file containing the reference
	var refFile *pkgtypes.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[ref.File]; exists {
			refFile = file
			break
		}
	}
	if refFile == nil || refFile.AST == nil {
		return nil
	}

	var changes []pkgtypes.Change

	// This is a simplified implementation
	// In practice, we would need more sophisticated analysis to determine
	// how to safely remove each reference (remove entire statement, expression, etc.)
	ast.Inspect(refFile.AST, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		// Check if this node contains our reference
		if n.Pos() <= ref.Position && ref.Position <= n.End() {
			// Try to remove the entire statement containing the reference
			if stmt := op.findContainingStatement(n, refFile.AST); stmt != nil {
				oldText := op.extractNodeText(stmt, refFile)
				changes = append(changes, pkgtypes.Change{
					File:        ref.File,
					Start:       int(stmt.Pos()),
					End:         int(stmt.End()),
					OldText:     oldText,
					NewText:     "",
					Description: fmt.Sprintf("Remove reference to deleted symbol %s", op.SymbolName),
				})
				return false
			}
		}
		return true
	})

	return changes
}

func (op *SafeDeleteOperation) findContainingStatement(node ast.Node, fileAST *ast.File) ast.Stmt {
	// Find the statement that contains this node
	var containingStmt ast.Stmt
	
	ast.Inspect(fileAST, func(n ast.Node) bool {
		if stmt, ok := n.(ast.Stmt); ok {
			if stmt.Pos() <= node.Pos() && node.End() <= stmt.End() {
				containingStmt = stmt
			}
		}
		return true
	})
	
	return containingStmt
}

func (op *SafeDeleteOperation) extractNodeText(node ast.Node, file *pkgtypes.File) string {
	start := int(node.Pos())
	end := int(node.End())
	
	if start < 0 || end > len(file.OriginalContent) || start >= end {
		return ""
	}
	
	return string(file.OriginalContent[start:end])
}

func (op *SafeDeleteOperation) getAffectedPackages(ws *pkgtypes.Workspace, affectedFiles []string) []string {
	packageMap := make(map[string]bool)
	for _, filePath := range affectedFiles {
		for _, pkg := range ws.Packages {
			if _, exists := pkg.Files[filePath]; exists {
				packageMap[pkg.Path] = true
				break
			}
		}
	}

	packages := make([]string, 0, len(packageMap))
	for pkg := range packageMap {
		packages = append(packages, pkg)
	}
	return packages
}

func (op *SafeDeleteOperation) analyzeImpact(ws *pkgtypes.Workspace, symbol *pkgtypes.Symbol, forced bool) []pkgtypes.Issue {
	var issues []pkgtypes.Issue

	// Check if symbol is exported
	if symbol.Exported {
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueVisibilityError,
			Severity:    pkgtypes.Warning,
			Description: fmt.Sprintf("Deleting exported symbol %s may break external packages", symbol.Name),
		})
	}

	// Check symbol type for specific warnings
	switch symbol.Kind {
	case pkgtypes.TypeSymbol:
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueTypeMismatch,
			Severity:    pkgtypes.Warning,
			Description: "Deleting a type may cause compilation errors in dependent code",
		})
	case pkgtypes.InterfaceSymbol:
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueTypeMismatch,
			Severity:    pkgtypes.Warning,
			Description: "Deleting an interface may break implementations and type assertions",
		})
	}

	// Warn about forced deletion
	if forced {
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueCompilationError,
			Severity:    pkgtypes.Error,
			Description: "Forced deletion will remove references, likely causing compilation errors",
		})
	}

	return issues
}