package refactor

import (
	"fmt"
	"go/ast"
	"go/token"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// ExtractConstantOperation implements extracting a literal value into a constant
type ExtractConstantOperation struct {
	SourceFile   string
	Position     token.Pos // Position of the literal to extract
	ConstantName string
	Scope        pkgtypes.RenameScope // PackageScope or WorkspaceScope
	TargetFile   string                // Optional: specific file to place the constant
}

func (op *ExtractConstantOperation) Type() pkgtypes.OperationType {
	return pkgtypes.ExtractOperation
}

func (op *ExtractConstantOperation) Validate(ws *pkgtypes.Workspace) error {
	if op.SourceFile == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}
	if op.ConstantName == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "constant name cannot be empty",
		}
	}
	if op.Position == token.NoPos {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "position must be specified",
		}
	}

	// Validate constant name is a valid Go identifier
	if !isValidGoIdentifierExtract(op.ConstantName) {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.ConstantName),
		}
	}

	// Check if source file exists
	var sourceFile *pkgtypes.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			break
		}
	}
	if sourceFile == nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Find the literal at the specified position
	literal := op.findLiteralAtPosition(sourceFile, op.Position)
	if literal == nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "no literal found at specified position",
		}
	}

	// Check if it's a valid literal type for extraction
	if !op.isExtractableLiteral(literal) {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "only basic literals (string, int, float, bool) can be extracted as constants",
		}
	}

	return nil
}

func (op *ExtractConstantOperation) Execute(ws *pkgtypes.Workspace) (*pkgtypes.RefactoringPlan, error) {
	// Find the source file
	var sourceFile *pkgtypes.File
	var sourcePackage *pkgtypes.Package
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
	}

	// Find the literal to extract
	literal := op.findLiteralAtPosition(sourceFile, op.Position)
	if literal == nil {
		return nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "literal not found at position",
		}
	}

	// Get literal value and type
	literalValue, literalType := op.getLiteralValueAndType(literal)

	// Find all occurrences of this literal in the scope
	occurrences := op.findLiteralOccurrences(ws, literal, op.Scope, sourcePackage)

	plan := &pkgtypes.RefactoringPlan{
		Changes:       make([]pkgtypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Determine where to place the constant
	targetFile := sourceFile
	if op.TargetFile != "" {
		// Find the specified target file
		for _, pkg := range ws.Packages {
			if file, exists := pkg.Files[op.TargetFile]; exists {
				targetFile = file
				break
			}
		}
	}

	// Generate constant declaration
	constDecl := op.generateConstantDeclaration(op.ConstantName, literalValue, literalType)

	// Find position to insert constant (after package declaration, before first non-import declaration)
	insertPos := op.findConstantInsertPosition(targetFile)

	// Add constant declaration change
	plan.Changes = append(plan.Changes, pkgtypes.Change{
		File:        targetFile.Path,
		Start:       insertPos,
		End:         insertPos,
		OldText:     "",
		NewText:     constDecl + "\n",
		Description: fmt.Sprintf("Add constant declaration %s", op.ConstantName),
	})
	plan.AffectedFiles = append(plan.AffectedFiles, targetFile.Path)

	// Replace all occurrences with the constant name
	for _, occurrence := range occurrences {
		if occurrence.File != targetFile.Path || occurrence.Pos != op.Position {
			plan.Changes = append(plan.Changes, pkgtypes.Change{
				File:        occurrence.File,
				Start:       int(occurrence.Pos),
				End:         int(occurrence.End),
				OldText:     literalValue,
				NewText:     op.ConstantName,
				Description: fmt.Sprintf("Replace literal with constant %s", op.ConstantName),
			})
			
			// Add to affected files if not already present
			if !contains(plan.AffectedFiles, occurrence.File) {
				plan.AffectedFiles = append(plan.AffectedFiles, occurrence.File)
			}
		}
	}

	// Perform impact analysis
	plan.Impact = &pkgtypes.ImpactAnalysis{
		AffectedFiles:    plan.AffectedFiles,
		AffectedPackages: op.getAffectedPackages(ws, plan.AffectedFiles),
		PotentialIssues:  op.analyzeImpact(ws, occurrences),
	}

	return plan, nil
}

func (op *ExtractConstantOperation) Description() string {
	scopeStr := "package"
	if op.Scope == pkgtypes.WorkspaceScope {
		scopeStr = "workspace"
	}
	return fmt.Sprintf("Extract constant %s from %s (scope: %s)", op.ConstantName, op.SourceFile, scopeStr)
}

// Helper methods

func (op *ExtractConstantOperation) findLiteralAtPosition(file *pkgtypes.File, pos token.Pos) ast.Expr {
	if file.AST == nil {
		return nil
	}

	var found ast.Expr
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		
		// Check if this is a literal at our position
		switch lit := n.(type) {
		case *ast.BasicLit:
			if lit.Pos() <= pos && pos <= lit.End() {
				found = lit
				return false
			}
		case *ast.Ident:
			// Check for true/false boolean literals
			if lit.Pos() <= pos && pos <= lit.End() {
				if lit.Name == "true" || lit.Name == "false" {
					found = lit
					return false
				}
			}
		}
		return true
	})

	return found
}

func (op *ExtractConstantOperation) isExtractableLiteral(expr ast.Expr) bool {
	switch lit := expr.(type) {
	case *ast.BasicLit:
		// String, int, float literals
		return lit.Kind == token.STRING || lit.Kind == token.INT || lit.Kind == token.FLOAT
	case *ast.Ident:
		// Boolean literals
		return lit.Name == "true" || lit.Name == "false"
	default:
		return false
	}
}

func (op *ExtractConstantOperation) getLiteralValueAndType(expr ast.Expr) (string, string) {
	switch lit := expr.(type) {
	case *ast.BasicLit:
		switch lit.Kind {
		case token.STRING:
			return lit.Value, "string"
		case token.INT:
			return lit.Value, ""  // Type will be inferred
		case token.FLOAT:
			return lit.Value, "float64"
		}
	case *ast.Ident:
		if lit.Name == "true" || lit.Name == "false" {
			return lit.Name, "bool"
		}
	}
	return "", ""
}

func (op *ExtractConstantOperation) findLiteralOccurrences(ws *pkgtypes.Workspace, literal ast.Expr, scope pkgtypes.RenameScope, sourcePackage *pkgtypes.Package) []LiteralOccurrence {
	var occurrences []LiteralOccurrence
	targetValue, _ := op.getLiteralValueAndType(literal)

	// Determine which packages to search based on scope
	packagesToSearch := make(map[string]*pkgtypes.Package)
	if scope == pkgtypes.PackageScope {
		packagesToSearch[sourcePackage.Path] = sourcePackage
	} else {
		// WorkspaceScope - search all packages
		packagesToSearch = ws.Packages
	}

	// Search for occurrences in the determined packages
	for _, pkg := range packagesToSearch {
		for _, file := range pkg.Files {
			if file.AST == nil {
				continue
			}

			ast.Inspect(file.AST, func(n ast.Node) bool {
				switch lit := n.(type) {
				case *ast.BasicLit:
					if lit.Value == targetValue {
						occurrences = append(occurrences, LiteralOccurrence{
							File: file.Path,
							Pos:  lit.Pos(),
							End:  lit.End(),
						})
					}
				case *ast.Ident:
					if (lit.Name == "true" || lit.Name == "false") && lit.Name == targetValue {
						occurrences = append(occurrences, LiteralOccurrence{
							File: file.Path,
							Pos:  lit.Pos(),
							End:  lit.End(),
						})
					}
				}
				return true
			})
		}
	}

	return occurrences
}

func (op *ExtractConstantOperation) generateConstantDeclaration(name, value, typeName string) string {
	// For untyped constants (like integers), omit the type
	if typeName == "" {
		return fmt.Sprintf("const %s = %s", name, value)
	}
	return fmt.Sprintf("const %s %s = %s", name, typeName, value)
}

func (op *ExtractConstantOperation) findConstantInsertPosition(file *pkgtypes.File) int {
	if file.AST == nil {
		return 0
	}

	// Find the position after package declaration and imports
	lastImportEnd := 0
	packageEnd := 0

	// Find package declaration end
	if file.AST.Name != nil {
		packageEnd = int(file.AST.Name.End())
		// Skip to end of line
		if packageEnd < len(file.OriginalContent) {
			for i := packageEnd; i < len(file.OriginalContent); i++ {
				if file.OriginalContent[i] == '\n' {
					packageEnd = i + 1
					break
				}
			}
		}
	}

	// Find last import end
	if len(file.AST.Imports) > 0 {
		lastImport := file.AST.Imports[len(file.AST.Imports)-1]
		lastImportEnd = int(lastImport.End())
		// Skip to end of line
		if lastImportEnd < len(file.OriginalContent) {
			for i := lastImportEnd; i < len(file.OriginalContent); i++ {
				if file.OriginalContent[i] == '\n' {
					lastImportEnd = i + 1
					break
				}
			}
		}
	}

	// If there are imports, insert after them; otherwise after package
	if lastImportEnd > 0 {
		return lastImportEnd + 1 // Add extra newline
	}
	return packageEnd + 1
}

func (op *ExtractConstantOperation) getAffectedPackages(ws *pkgtypes.Workspace, affectedFiles []string) []string {
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

func (op *ExtractConstantOperation) analyzeImpact(ws *pkgtypes.Workspace, occurrences []LiteralOccurrence) []pkgtypes.Issue {
	var issues []pkgtypes.Issue

	// Check for naming conflicts
	for _, pkg := range ws.Packages {
		if pkg.Symbols != nil {
			// Check if constant name already exists
			if _, exists := pkg.Symbols.Constants[op.ConstantName]; exists {
				issues = append(issues, pkgtypes.Issue{
					Type:        pkgtypes.IssueNameConflict,
					Severity:    pkgtypes.Error,
					Description: fmt.Sprintf("Constant %s already exists in package %s", op.ConstantName, pkg.Path),
					File:        pkg.Dir,
					Line:        0,
				})
			}
		}
	}

	// Warn about large number of replacements
	if len(occurrences) > 10 {
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueCompilationError,
			Severity:    pkgtypes.Info,
			Description: fmt.Sprintf("Found %d occurrences of the literal value", len(occurrences)),
		})
	}

	return issues
}

// LiteralOccurrence represents a location where a literal value appears
type LiteralOccurrence struct {
	File string
	Pos  token.Pos
	End  token.Pos
}