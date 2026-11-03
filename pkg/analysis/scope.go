package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// ScopeAnalyzer handles lexical scope analysis and symbol resolution within scopes
type ScopeAnalyzer struct {
	resolver *SymbolResolver
}

func NewScopeAnalyzer(resolver *SymbolResolver) *ScopeAnalyzer {
	return &ScopeAnalyzer{resolver: resolver}
}

// Private helper methods

func (sa *ScopeAnalyzer) resolveBuiltinSymbol(name string) *types.Symbol {
	// Go built-in identifiers
	builtins := map[string]types.SymbolKind{
		// Types
		"bool": types.TypeSymbol, "byte": types.TypeSymbol, "complex64": types.TypeSymbol,
		"complex128": types.TypeSymbol, "error": types.TypeSymbol, "float32": types.TypeSymbol,
		"float64": types.TypeSymbol, "int": types.TypeSymbol, "int8": types.TypeSymbol,
		"int16": types.TypeSymbol, "int32": types.TypeSymbol, "int64": types.TypeSymbol,
		"rune": types.TypeSymbol, "string": types.TypeSymbol, "uint": types.TypeSymbol,
		"uint8": types.TypeSymbol, "uint16": types.TypeSymbol, "uint32": types.TypeSymbol,
		"uint64": types.TypeSymbol, "uintptr": types.TypeSymbol,

		// Functions
		"append": types.FunctionSymbol, "cap": types.FunctionSymbol, "close": types.FunctionSymbol,
		"complex": types.FunctionSymbol, "copy": types.FunctionSymbol, "delete": types.FunctionSymbol,
		"imag": types.FunctionSymbol, "len": types.FunctionSymbol, "make": types.FunctionSymbol,
		"new": types.FunctionSymbol, "panic": types.FunctionSymbol, "print": types.FunctionSymbol,
		"println": types.FunctionSymbol, "real": types.FunctionSymbol, "recover": types.FunctionSymbol,

		// Constants
		"true": types.ConstantSymbol, "false": types.ConstantSymbol, "iota": types.ConstantSymbol,
		"nil": types.ConstantSymbol,
	}

	kind, exists := builtins[name]
	if exists {
		return &types.Symbol{
			Name:     name,
			Kind:     kind,
			Package:  "", // Built-in symbols have no package
			File:     "", // Built-in symbols have no file
			Position: token.NoPos,
			End:      token.NoPos,
			Exported: true, // Built-in symbols are always available
		}
	}

	return nil
}

// NEW: Type Resolution Helpers for Method Call Matching

// GetIdentifierType resolves the type of an identifier at a given position in a file.
// This uses direct AST walking to avoid scope analysis position-matching issues.
func (sa *ScopeAnalyzer) GetIdentifierType(identName string, file *types.File, pos token.Pos) *types.Symbol {
	// Check cache first
	cacheKey := fmt.Sprintf("%s:%s:%d", file.Path, identName, pos)

	cached := sa.resolver.cache.GetIdentifierType(cacheKey)
	if cached != nil {
		return cached
	}

	if file.AST == nil {
		return nil
	}

	// Find the enclosing function for this position
	enclosingFunc := sa.enclosingFuncDecl(file, pos)

	if enclosingFunc == nil {
		// Not inside a function - check package-level variables
		typeSymbol := sa.findPackageLevelVarType(identName, file)
		if typeSymbol != nil {
			sa.resolver.cache.SetIdentifierType(cacheKey, typeSymbol)
		}

		return typeSymbol
	}

	// Search for variable declarations in the function BY NAME (not position)
	typeExpr := sa.localDeclarationType(enclosingFunc, identName)

	// If not found in function body, check function parameters and receiver
	typeExpr = sa.paramOrReceiverType(typeExpr, enclosingFunc, identName)

	if typeExpr == nil {
		return nil
	}

	// Resolve the type expression to a symbol (reuse existing helper)
	typeSymbol := sa.resolveTypeExpression(typeExpr, file)
	if typeSymbol != nil {
		sa.resolver.cache.SetIdentifierType(cacheKey, typeSymbol)
	}

	return typeSymbol
}

func (sa *ScopeAnalyzer) paramOrReceiverType(typeExpr ast.Expr, enclosingFunc *ast.FuncDecl, identName string) ast.Expr {
	if typeExpr == nil {
		if enclosingFunc.Recv != nil {
			for _, field := range enclosingFunc.Recv.List {
				for _, name := range field.Names {
					if name.Name == identName {
						typeExpr = field.Type
						break
					}
				}
			}
		}

		if typeExpr == nil && enclosingFunc.Type.Params != nil {
			for _, field := range enclosingFunc.Type.Params.List {
				for _, name := range field.Names {
					if name.Name == identName {
						typeExpr = field.Type
						break
					}
				}
			}
		}
	}

	return typeExpr
}

func (sa *ScopeAnalyzer) enclosingFuncDecl(file *types.File, pos token.Pos) *ast.FuncDecl {
	var enclosingFunc *ast.FuncDecl

	ast.Inspect(file.AST, func(n ast.Node) bool {
		funcDecl, ok := n.(*ast.FuncDecl)
		if ok {
			if funcDecl.Pos() <= pos && pos <= funcDecl.End() {
				enclosingFunc = funcDecl
				return false // Stop once we find it
			}
		}

		return true
	})

	return enclosingFunc
}

func (sa *ScopeAnalyzer) localDeclarationType(enclosingFunc *ast.FuncDecl, identName string) ast.Expr {
	var typeExpr ast.Expr

	ast.Inspect(enclosingFunc, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			// Handle := and = assignments
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != identName {
					continue
				}
				// Found declaration! Extract type from RHS
				if i < len(node.Rhs) {
					typeExpr = sa.extractTypeFromExpression(node.Rhs[i])
					return false
				}
			}
		case *ast.ValueSpec:
			// Handle var declarations
			for _, name := range node.Names {
				if name.Name != identName {
					continue
				}

				if node.Type != nil {
					typeExpr = node.Type // Explicit type
				} else if len(node.Values) > 0 {
					typeExpr = sa.extractTypeFromExpression(node.Values[0]) // Infer from RHS
				}

				return false
			}
		}

		return true
	})

	return typeExpr
}

// findPackageLevelVarType finds the type of a package-level variable by name.
// This handles variables declared outside of any function.
func (sa *ScopeAnalyzer) findPackageLevelVarType(identName string, file *types.File) *types.Symbol {
	var typeExpr ast.Expr

	ast.Inspect(file.AST, func(n ast.Node) bool {
		genDecl, ok := n.(*ast.GenDecl)
		if ok && genDecl.Tok == token.VAR {
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}

				for _, name := range valueSpec.Names {
					if name.Name != identName {
						continue
					}

					if valueSpec.Type != nil {
						typeExpr = valueSpec.Type
					} else if len(valueSpec.Values) > 0 {
						typeExpr = sa.extractTypeFromExpression(valueSpec.Values[0])
					}

					return false
				}
			}
		}

		return true
	})

	if typeExpr == nil {
		return nil
	}

	return sa.resolveTypeExpression(typeExpr, file)
}

// extractTypeFromExpression gets the type from an expression (for := assignments).
// Handles: &Type{}, Type{}, Type(value), x.(*Type)
func (sa *ScopeAnalyzer) extractTypeFromExpression(expr ast.Expr) ast.Expr {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND { // &Type{}
			return sa.extractTypeFromExpression(e.X)
		}
	case *ast.CompositeLit: // Type{} or &Type{}
		return e.Type
	case *ast.CallExpr:
		// Type conversion: Type(value) or pkg.Type(value)
		ident, ok := e.Fun.(*ast.Ident)
		if ok {
			return ident
		}

		sel, ok := e.Fun.(*ast.SelectorExpr)
		if ok {
			return sel
		}
	case *ast.TypeAssertExpr: // x.(*Type)
		return e.Type
	}

	return nil
}

// resolveTypeExpression converts an AST type expression to a type symbol.
// Handles: Ident, SelectorExpr (pkg.Type), StarExpr (*Type)
func (sa *ScopeAnalyzer) resolveTypeExpression(typeExpr ast.Expr, file *types.File) *types.Symbol {
	if typeExpr == nil {
		return nil
	}

	switch expr := typeExpr.(type) {
	case *ast.Ident:
		// Simple type like "User" or built-in like "string"
		// Check built-in first
		builtin := sa.resolveBuiltinSymbol(expr.Name)
		if builtin != nil {
			return builtin
		}

		// Try to resolve in same package
		if file.Package != nil {
			sym, _ := sa.resolver.ResolveSymbol(file.Package, expr.Name)
			if sym != nil {
				return sym
			}
		}

		return nil

	case *ast.SelectorExpr:
		// Qualified type like "pkg.Type"
		pkgIdent, ok := expr.X.(*ast.Ident)
		if ok {
			// Resolve package from imports
			for _, imp := range file.AST.Imports {
				var importAlias string
				if imp.Name != nil {
					importAlias = imp.Name.Name
				} else {
					parts := strings.Split(strings.Trim(imp.Path.Value, `"`), "/")
					importAlias = parts[len(parts)-1]
				}

				if importAlias != pkgIdent.Name {
					continue
				}

				importPath := strings.Trim(imp.Path.Value, `"`)
				// Try to find package by import path
				if sa.resolver.workspace == nil {
					continue
				}

				fsPath, ok := sa.resolver.workspace.ImportToPath[importPath]
				if !ok {
					continue
				}

				pkg := sa.resolver.workspace.Packages[fsPath]
				if pkg != nil {
					sym, _ := sa.resolver.ResolveSymbol(pkg, expr.Sel.Name)
					return sym
				}
			}
		}

		return nil

	case *ast.StarExpr:
		// Pointer type like *User - resolve the underlying type
		return sa.resolveTypeExpression(expr.X, file)

	default:
		return nil
	}
}
