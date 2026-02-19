package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

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

// AnalyzeTypeError provides analysis for type-related errors
func (de *DiagnosticEngine) AnalyzeTypeError(symbol *types.Symbol, expectedKind types.SymbolKind) *ResolutionError {
	suggestions := []string{
		fmt.Sprintf("Expected %s, but '%s' is a %s", expectedKind.String(), symbol.Name, symbol.Kind.String()),
	}

	// Look for symbols with the same name but different kind
	for _, pkg := range de.workspace.Packages {
		if pkg.Symbols == nil {
			continue
		}

		// Check all symbol categories
		allSymbols := make(map[string]*types.Symbol)
		maps.Copy(allSymbols, pkg.Symbols.Functions)
		maps.Copy(allSymbols, pkg.Symbols.Types)
		maps.Copy(allSymbols, pkg.Symbols.Variables)
		maps.Copy(allSymbols, pkg.Symbols.Constants)

		if altSymbol, exists := allSymbols[symbol.Name]; exists && altSymbol != symbol {
			if altSymbol.Kind == expectedKind {
				if altSymbol.Package != symbol.Package {
					suggestions = append(suggestions, fmt.Sprintf("Did you mean '%s' from package '%s'?", altSymbol.Name, altSymbol.Package))
				} else {
					suggestions = append(suggestions, fmt.Sprintf("There is a %s named '%s' in the same package", expectedKind.String(), altSymbol.Name))
				}
			}
		}
	}

	return &ResolutionError{
		RefactorError: &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("symbol '%s' has wrong type", symbol.Name),
			File:    symbol.File,
		},
		Suggestions: suggestions,
		Context:     nil, // Type errors don't need scope context
	}
}

// AnalyzeVisibilityError provides analysis for visibility-related errors
func (de *DiagnosticEngine) AnalyzeVisibilityError(symbol *types.Symbol, accessingPackage string) *ResolutionError {
	suggestions := []string{
		fmt.Sprintf("Symbol '%s' is not exported from package '%s'", symbol.Name, symbol.Package),
	}

	if symbol.Package == accessingPackage {
		suggestions = append(suggestions, "This symbol should be accessible within the same package - this might be a scoping issue")
	} else {
		exportedName := cases.Title(language.English).String(symbol.Name)
		if exportedName != symbol.Name {
			suggestions = append(suggestions, fmt.Sprintf("To make it accessible, rename it to '%s' (capitalize first letter)", exportedName))
		}

		// Look for exported symbols with similar names
		if pkg := de.workspace.Packages[symbol.Package]; pkg != nil && pkg.Symbols != nil {
			for name, sym := range pkg.Symbols.Functions {
				if sym.Exported && de.isSimilarName(name, symbol.Name) {
					suggestions = append(suggestions, fmt.Sprintf("Did you mean the exported function '%s'?", name))
				}
			}
			for name, sym := range pkg.Symbols.Types {
				if sym.Exported && de.isSimilarName(name, symbol.Name) {
					suggestions = append(suggestions, fmt.Sprintf("Did you mean the exported type '%s'?", name))
				}
			}
		}
	}

	return &ResolutionError{
		RefactorError: &types.RefactorError{
			Type:    types.VisibilityViolation,
			Message: fmt.Sprintf("cannot access unexported symbol '%s'", symbol.Name),
			File:    symbol.File,
		},
		Suggestions: suggestions,
	}
}

// AnalyzeImportError provides analysis for import-related errors
func (de *DiagnosticEngine) AnalyzeImportError(packagePath string, file *types.File) *ResolutionError {
	suggestions := []string{
		fmt.Sprintf("Package '%s' not found", packagePath),
	}

	// Look for similar package names
	for existingPath := range de.workspace.Packages {
		if de.isSimilarName(existingPath, packagePath) {
			suggestions = append(suggestions, fmt.Sprintf("Did you mean '%s'?", existingPath))
		}
	}

	// Check if it's a standard library package that might be misspelled
	commonStdLib := []string{
		"fmt", "os", "io", "net", "http", "json", "time", "strings", "strconv",
		"context", "sync", "errors", "log", "path", "filepath", "bufio",
	}

	for _, stdPkg := range commonStdLib {
		if de.isSimilarName(stdPkg, packagePath) {
			suggestions = append(suggestions, fmt.Sprintf("Did you mean the standard library package '%s'?", stdPkg))
		}
	}

	return &ResolutionError{
		RefactorError: &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("package '%s' not found", packagePath),
			File:    file.Path,
		},
		Suggestions: suggestions,
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
		result.WriteString(fmt.Sprintf("  Scope: %v\n", re.Context.ScopeKind))
		if re.Context.ParentFunction != "" {
			result.WriteString(fmt.Sprintf("  In function: %s\n", re.Context.ParentFunction))
		}
	}

	// Add suggestions
	if len(re.Suggestions) > 0 {
		result.WriteString("  Suggestions:\n")
		for _, suggestion := range re.Suggestions {
			result.WriteString(fmt.Sprintf("    • %s\n", suggestion))
		}
	}

	// Add similar symbols
	if len(re.Similar) > 0 {
		result.WriteString("  Similar symbols found:\n")
		for _, symbol := range re.Similar {
			result.WriteString(fmt.Sprintf("    • %s (%s) in %s\n", symbol.Name, symbol.Kind.String(), symbol.Package))
		}
	}

	return result.String()
}

// Error implements the error interface
func (re *ResolutionError) Error() string {
	return re.FormatError()
}
