package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// DiagnosticEngine provides enhanced error reporting with suggestions
type DiagnosticEngine struct {
	resolver  *SymbolResolver
	workspace *types.Workspace
}

// ResolutionError represents an enhanced error with suggestions and context
type ResolutionError struct {
	*types.RefactorError
	Suggestions []string
	Context     *ResolutionContext
	Similar     []*types.Symbol // Similar symbols that might have been intended
}

// ResolutionContext provides additional context about where resolution failed
type ResolutionContext struct {
	ScopeKind        ScopeKind
	AvailableSymbols []string // Symbols available in current scope
	NearbySymbols    []string // Symbols in nearby scopes
	ImportedPackages []string // Available imported packages
	ParentFunction   string   // Name of containing function if applicable
	Line             int      // Line number where resolution failed
	Column           int      // Column number where resolution failed
}

func NewDiagnosticEngine(resolver *SymbolResolver) *DiagnosticEngine {
	return &DiagnosticEngine{
		resolver:  resolver,
		workspace: resolver.workspace,
	}
}

// AnalyzeResolutionFailure provides detailed analysis when symbol resolution fails
func (de *DiagnosticEngine) AnalyzeResolutionFailure(ident *ast.Ident, file *types.File, originalError error) *ResolutionError {
	pos := de.workspace.FileSet.Position(ident.Pos())

	context := de.buildResolutionContext(ident, file, ident.Pos())
	suggestions := de.generateSuggestions(ident.Name, context, file)
	similar := de.findSimilarSymbols(ident.Name, file)

	return &ResolutionError{
		RefactorError: &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("symbol '%s' not found", ident.Name),
			File:    file.Path,
			Line:    pos.Line,
			Column:  pos.Column,
			Cause:   originalError,
		},
		Suggestions: suggestions,
		Context:     context,
		Similar:     similar,
	}
}

// Private helper methods

func (de *DiagnosticEngine) buildResolutionContext(ident *ast.Ident, file *types.File, pos token.Pos) *ResolutionContext {
	context := &ResolutionContext{
		ImportedPackages: make([]string, 0),
		AvailableSymbols: make([]string, 0),
		NearbySymbols:    make([]string, 0),
	}

	// Get position info
	position := de.workspace.FileSet.Position(pos)
	context.Line = position.Line
	context.Column = position.Column

	// Get scope information
	if scope, err := de.resolver.scopeAnalyzer.GetScopeAt(file, pos); err == nil {
		context.ScopeKind = scope.Kind

		// Collect available symbols in current scope chain
		for currentScope := scope; currentScope != nil; currentScope = currentScope.Parent {
			for name := range currentScope.Symbols {
				context.AvailableSymbols = append(context.AvailableSymbols, name)
			}
		}

		// Find parent function if in function scope
		context.ParentFunction = de.findParentFunction(file.AST, pos)
	}

	// Get imported packages
	for _, imp := range file.AST.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		context.ImportedPackages = append(context.ImportedPackages, importPath)
	}

	// Get symbols from package scope
	if file.Package.Symbols != nil {
		for name := range file.Package.Symbols.Functions {
			context.NearbySymbols = append(context.NearbySymbols, name)
		}
		for name := range file.Package.Symbols.Types {
			context.NearbySymbols = append(context.NearbySymbols, name)
		}
		for name := range file.Package.Symbols.Variables {
			context.NearbySymbols = append(context.NearbySymbols, name)
		}
		for name := range file.Package.Symbols.Constants {
			context.NearbySymbols = append(context.NearbySymbols, name)
		}
	}

	return context
}

func (de *DiagnosticEngine) generateSuggestions(symbolName string, context *ResolutionContext, file *types.File) []string {
	var suggestions []string

	// Check for typos in available symbols
	allAvailable := append([]string{}, context.AvailableSymbols...)
	allAvailable = append(allAvailable, context.NearbySymbols...)
	for _, available := range allAvailable {
		if de.isSimilarName(available, symbolName) {
			suggestions = append(suggestions, fmt.Sprintf("Did you mean '%s'?", available))
		}
	}

	// Check if it might be a qualified identifier that's missing import
	if strings.Contains(symbolName, ".") {
		parts := strings.Split(symbolName, ".")
		if len(parts) == 2 {
			pkgName, symName := parts[0], parts[1]
			suggestions = append(suggestions, fmt.Sprintf("If '%s' is a package, make sure it's imported", pkgName))

			// Look for the symbol in other packages
			for _, pkg := range de.workspace.Packages {
				if pkg.Symbols == nil {
					continue
				}

				if _, exists := pkg.Symbols.Functions[symName]; exists {
					suggestions = append(suggestions, fmt.Sprintf("Function '%s' exists in package '%s'", symName, pkg.Path))
				}
				if _, exists := pkg.Symbols.Types[symName]; exists {
					suggestions = append(suggestions, fmt.Sprintf("Type '%s' exists in package '%s'", symName, pkg.Path))
				}
			}
		}
	}

	// Scope-specific suggestions
	switch context.ScopeKind {
	case FunctionScope:
		suggestions = append(suggestions, "Check function parameters and local variables")
		if context.ParentFunction != "" {
			suggestions = append(suggestions, fmt.Sprintf("This is inside function '%s'", context.ParentFunction))
		}
	case PackageScope:
		suggestions = append(suggestions, "Check package-level declarations and imports")
	case BlockScope:
		suggestions = append(suggestions, "Check local variable declarations in this block")
	}

	// Import suggestions
	if len(context.ImportedPackages) > 0 {
		suggestions = append(suggestions, "Available packages: "+strings.Join(context.ImportedPackages, ", "))
	}

	// Remove duplicates and sort
	suggestions = de.removeDuplicates(suggestions)
	sort.Strings(suggestions)

	return suggestions
}

func (de *DiagnosticEngine) findSimilarSymbols(symbolName string, file *types.File) []*types.Symbol {
	var similar []*types.Symbol

	// Search in current package
	if file.Package.Symbols != nil {
		for name, symbol := range file.Package.Symbols.Functions {
			if de.isSimilarName(name, symbolName) {
				similar = append(similar, symbol)
			}
		}
		for name, symbol := range file.Package.Symbols.Types {
			if de.isSimilarName(name, symbolName) {
				similar = append(similar, symbol)
			}
		}
		for name, symbol := range file.Package.Symbols.Variables {
			if de.isSimilarName(name, symbolName) {
				similar = append(similar, symbol)
			}
		}
		for name, symbol := range file.Package.Symbols.Constants {
			if de.isSimilarName(name, symbolName) {
				similar = append(similar, symbol)
			}
		}
	}

	// Limit results to avoid overwhelming output
	if len(similar) > 5 {
		similar = similar[:5]
	}

	return similar
}

func (de *DiagnosticEngine) findParentFunction(file *ast.File, pos token.Pos) string {
	var parentFunc string

	ast.Inspect(file, func(n ast.Node) bool {
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			if funcDecl.Pos() <= pos && pos <= funcDecl.End() {
				parentFunc = funcDecl.Name.Name
				return false // Found the function, no need to continue
			}
		}
		return true
	})

	return parentFunc
}

func (de *DiagnosticEngine) isSimilarName(name1, name2 string) bool {
	// Simple similarity check based on edit distance
	return de.editDistance(strings.ToLower(name1), strings.ToLower(name2)) <= 2
}

func (de *DiagnosticEngine) editDistance(s1, s2 string) int {
	// Simple Levenshtein distance implementation
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
		matrix[i][0] = i
	}
	for j := 0; j <= len(s2); j++ {
		matrix[0][j] = j
	}

	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			cost := 0
			if s1[i-1] != s2[j-1] {
				cost = 1
			}
			matrix[i][j] = min(
				matrix[i-1][j]+1,      // deletion
				matrix[i][j-1]+1,      // insertion
				matrix[i-1][j-1]+cost, // substitution
			)
		}
	}

	return matrix[len(s1)][len(s2)]
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func (de *DiagnosticEngine) removeDuplicates(suggestions []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, suggestion := range suggestions {
		if !seen[suggestion] {
			seen[suggestion] = true
			result = append(result, suggestion)
		}
	}

	return result
}

// Error formatting methods

// FormatError returns a nicely formatted error message with suggestions
func (re *ResolutionError) FormatError() string {
	var result strings.Builder

	// Basic error message
	result.WriteString(re.RefactorError.Error())
	result.WriteString("\n")

	// Add context information
	if re.Context != nil {
		fmt.Fprintf(&result, "  Scope: %v\n", re.Context.ScopeKind)
		if re.Context.ParentFunction != "" {
			fmt.Fprintf(&result, "  In function: %s\n", re.Context.ParentFunction)
		}
	}

	// Add suggestions
	if len(re.Suggestions) > 0 {
		result.WriteString("  Suggestions:\n")
		for _, suggestion := range re.Suggestions {
			fmt.Fprintf(&result, "    • %s\n", suggestion)
		}
	}

	// Add similar symbols
	if len(re.Similar) > 0 {
		result.WriteString("  Similar symbols found:\n")
		for _, symbol := range re.Similar {
			fmt.Fprintf(&result, "    • %s (%s) in %s\n", symbol.Name, symbol.Kind.String(), symbol.Package)
		}
	}

	return result.String()
}

// Error implements the error interface
func (re *ResolutionError) Error() string {
	return re.FormatError()
}
