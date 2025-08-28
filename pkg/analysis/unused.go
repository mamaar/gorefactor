package analysis

import (
	"fmt"
	"go/ast"

	"github.com/mamaar/gorefactor/pkg/types"
)

// UnusedSymbol represents an unused symbol in the codebase
type UnusedSymbol struct {
	Symbol   *types.Symbol
	SafeToDelete bool // True if unexported and safe to remove
	Reason   string   // Why it's considered unused
}

// UnusedAnalyzer finds unused symbols in the workspace
type UnusedAnalyzer struct {
	workspace *types.Workspace
	resolver  *SymbolResolver
}

// NewUnusedAnalyzer creates a new unused symbol analyzer
func NewUnusedAnalyzer(ws *types.Workspace) *UnusedAnalyzer {
	return &UnusedAnalyzer{
		workspace: ws,
		resolver:  NewSymbolResolver(ws),
	}
}

// FindUnusedSymbols finds all unused symbols in the workspace
func (ua *UnusedAnalyzer) FindUnusedSymbols() ([]*UnusedSymbol, error) {
	var unusedSymbols []*UnusedSymbol

	// Build symbol tables for all packages
	for _, pkg := range ua.workspace.Packages {
		if _, err := ua.resolver.BuildSymbolTable(pkg); err != nil {
			return nil, fmt.Errorf("failed to build symbol table for package %s: %v", pkg.Path, err)
		}
	}

	// Check each package for unused symbols
	for _, pkg := range ua.workspace.Packages {
		unused, err := ua.findUnusedInPackage(pkg)
		if err != nil {
			return nil, err
		}
		unusedSymbols = append(unusedSymbols, unused...)
	}

	return unusedSymbols, nil
}

// FindUnusedInPackage finds unused symbols in a specific package
func (ua *UnusedAnalyzer) findUnusedInPackage(pkg *types.Package) ([]*UnusedSymbol, error) {
	if pkg.Symbols == nil {
		return nil, nil
	}

	var unusedSymbols []*UnusedSymbol

	// Check functions
	for _, symbol := range pkg.Symbols.Functions {
		if unused := ua.checkSymbolUsage(symbol); unused != nil {
			unusedSymbols = append(unusedSymbols, unused)
		}
	}

	// Check types
	for _, symbol := range pkg.Symbols.Types {
		if unused := ua.checkSymbolUsage(symbol); unused != nil {
			unusedSymbols = append(unusedSymbols, unused)
		}
	}

	// Check variables
	for _, symbol := range pkg.Symbols.Variables {
		if unused := ua.checkSymbolUsage(symbol); unused != nil {
			unusedSymbols = append(unusedSymbols, unused)
		}
	}

	// Check constants
	for _, symbol := range pkg.Symbols.Constants {
		if unused := ua.checkSymbolUsage(symbol); unused != nil {
			unusedSymbols = append(unusedSymbols, unused)
		}
	}

	// Check methods
	for typeName, methods := range pkg.Symbols.Methods {
		for _, method := range methods {
			if unused := ua.checkMethodUsage(method, typeName); unused != nil {
				unusedSymbols = append(unusedSymbols, unused)
			}
		}
	}

	return unusedSymbols, nil
}

// checkSymbolUsage checks if a symbol is unused
func (ua *UnusedAnalyzer) checkSymbolUsage(symbol *types.Symbol) *UnusedSymbol {
	// Skip exported symbols for now (they might be used externally)
	if symbol.Exported {
		return nil
	}

	// Skip main functions and init functions
	if symbol.Name == "main" || symbol.Name == "init" {
		return nil
	}

	// Skip test functions
	if ua.isTestFunction(symbol) {
		return nil
	}

	// Find all references to this symbol
	references, err := ua.resolver.FindReferences(symbol)
	if err != nil {
		return nil // If we can't find references, assume it's used to be safe
	}

	// If no references found, it's unused
	if len(references) == 0 {
		return &UnusedSymbol{
			Symbol:       symbol,
			SafeToDelete: !symbol.Exported,
			Reason:       "No references found",
		}
	}

	// Check if all references are just declarations/definitions
	actualUses := ua.filterActualUses(references, symbol)
	if len(actualUses) == 0 {
		return &UnusedSymbol{
			Symbol:       symbol,
			SafeToDelete: !symbol.Exported,
			Reason:       "Only referenced in declarations",
		}
	}

	return nil
}

// checkMethodUsage checks if a method is unused with special handling for interface methods
func (ua *UnusedAnalyzer) checkMethodUsage(method *types.Symbol, typeName string) *UnusedSymbol {
	// Skip exported methods
	if method.Exported {
		return nil
	}

	// Skip methods that might implement interfaces
	if ua.mightImplementInterface(method, typeName) {
		return nil
	}

	return ua.checkSymbolUsage(method)
}

// isTestFunction checks if a function is a test function
func (ua *UnusedAnalyzer) isTestFunction(symbol *types.Symbol) bool {
	if symbol.Kind != types.FunctionSymbol {
		return false
	}

	// Check if it's in a test file
	if len(symbol.File) > 8 && symbol.File[len(symbol.File)-8:] == "_test.go" {
		return true
	}

	// Check common test function patterns
	testPrefixes := []string{"Test", "Benchmark", "Example", "Fuzz"}
	for _, prefix := range testPrefixes {
		if len(symbol.Name) >= len(prefix) && symbol.Name[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}

// filterActualUses filters out declaration/definition references to find actual usage
func (ua *UnusedAnalyzer) filterActualUses(references []*types.Reference, symbol *types.Symbol) []*types.Reference {
	var actualUses []*types.Reference

	for _, ref := range references {
		// Skip if this is the symbol definition itself
		if ref.Position == symbol.Position {
			continue
		}

		// Check if this reference is in a declaration context
		if !ua.isDeclarationContext(ref, symbol) {
			actualUses = append(actualUses, ref)
		}
	}

	return actualUses
}

// isDeclarationContext checks if a reference is in a declaration context
func (ua *UnusedAnalyzer) isDeclarationContext(ref *types.Reference, symbol *types.Symbol) bool {
	// Find the file containing the reference
	var file *types.File
	for _, pkg := range ua.workspace.Packages {
		if f, exists := pkg.Files[ref.File]; exists {
			file = f
			break
		}
	}

	if file == nil {
		return false
	}

	// Check the AST context around the reference position
	var isDeclaration bool
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		// Check if we're in the right position range
		if n.Pos() <= ref.Position && ref.Position <= n.End() {
			switch node := n.(type) {
			case *ast.FuncDecl:
				// Check if this is the function name in declaration
				if node.Name != nil && node.Name.Pos() == ref.Position {
					isDeclaration = true
					return false
				}
			case *ast.GenDecl:
				// Check if this is in a type/var/const declaration
				for _, spec := range node.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.Pos() == ref.Position {
							isDeclaration = true
							return false
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.Pos() == ref.Position {
								isDeclaration = true
								return false
							}
						}
					}
				}
			}
		}
		return true
	})

	return isDeclaration
}

// mightImplementInterface checks if a method might be implementing an interface
func (ua *UnusedAnalyzer) mightImplementInterface(method *types.Symbol, typeName string) bool {
	// For now, we'll be conservative and assume any unexported method with a common name
	// might be implementing an interface
	commonInterfaceMethods := []string{
		"String", "Error", "Read", "Write", "Close", "Len", "Less", "Swap",
		"MarshalJSON", "UnmarshalJSON", "MarshalBinary", "UnmarshalBinary",
		"ServeHTTP", "RoundTrip",
	}

	for _, common := range commonInterfaceMethods {
		if method.Name == common {
			return true
		}
	}

	// Check if there are any interfaces in the workspace that this might implement
	// This is a simplified check - a full implementation would do proper interface matching
	for _, pkg := range ua.workspace.Packages {
		if pkg.Symbols == nil {
			continue
		}
		for _, symbol := range pkg.Symbols.Types {
			if symbol.Kind == types.InterfaceSymbol {
				// Check if interface has a method with the same name
				ifaceMethods, err := ua.resolver.getInterfaceMethods(symbol)
				if err == nil {
					for _, ifaceMethod := range ifaceMethods {
						if ifaceMethod.Name == method.Name {
							return true
						}
					}
				}
			}
		}
	}

	return false
}

// GetUnusedUnexportedSymbols returns only unexported unused symbols that are safe to delete
func (ua *UnusedAnalyzer) GetUnusedUnexportedSymbols() ([]*UnusedSymbol, error) {
	allUnused, err := ua.FindUnusedSymbols()
	if err != nil {
		return nil, err
	}

	var safeToDelete []*UnusedSymbol
	for _, unused := range allUnused {
		if unused.SafeToDelete && !unused.Symbol.Exported {
			safeToDelete = append(safeToDelete, unused)
		}
	}

	return safeToDelete, nil
}

// FormatUnusedSymbol formats an unused symbol for display
func (ua *UnusedAnalyzer) FormatUnusedSymbol(unused *UnusedSymbol) string {
	symbol := unused.Symbol
	return fmt.Sprintf("%s %s (%s:%d:%d) - %s",
		symbol.Kind.String(),
		symbol.Name,
		symbol.File,
		symbol.Line,
		symbol.Column,
		unused.Reason)
}