package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Scope represents a lexical scope in Go code
type Scope struct {
	Kind     ScopeKind
	Node     ast.Node               // AST node that creates this scope
	Parent   *Scope                 // Parent scope
	Children []*Scope               // Child scopes
	Symbols  map[string]*types.Symbol // Symbols defined in this scope
	Start    token.Pos              // Start position of scope
	End      token.Pos              // End position of scope
}

type ScopeKind int

const (
	UniverseScope ScopeKind = iota // Built-in identifiers
	PackageScope                   // Package-level declarations
	FileScope                      // File-level imports and declarations
	FunctionScope                  // Function parameters and body
	BlockScope                     // Block statements
	TypeScope                      // Type parameter scope (for generics)
)

// String returns the string representation of a ScopeKind
func (sk ScopeKind) String() string {
	switch sk {
	case UniverseScope:
		return "Universe"
	case PackageScope:
		return "Package"
	case FileScope:
		return "File"
	case FunctionScope:
		return "Function"
	case BlockScope:
		return "Block"
	case TypeScope:
		return "Type"
	default:
		return "Unknown"
	}
}

// ScopeAnalyzer handles lexical scope analysis and symbol resolution within scopes
type ScopeAnalyzer struct {
	resolver  *SymbolResolver
	workspace *types.Workspace
	// Cache for scope chains to avoid recomputation
	scopeCache map[string]*Scope // file path -> root scope
}

func NewScopeAnalyzer(resolver *SymbolResolver) *ScopeAnalyzer {
	return &ScopeAnalyzer{
		resolver:   resolver,
		workspace:  resolver.workspace,
		scopeCache: make(map[string]*Scope),
	}
}

// BuildScopeTree builds the complete scope tree for a file
func (sa *ScopeAnalyzer) BuildScopeTree(file *types.File) (*Scope, error) {
	// Check cache first
	if cached, exists := sa.scopeCache[file.Path]; exists {
		return cached, nil
	}

	// Create package scope as root
	packageScope := &Scope{
		Kind:    PackageScope,
		Node:    file.AST,
		Symbols: make(map[string]*types.Symbol),
		Start:   file.AST.Pos(),
		End:     file.AST.End(),
	}

	// Add package-level symbols
	if err := sa.addPackageLevelSymbols(packageScope, file); err != nil {
		return nil, err
	}

	// Create file scope for imports
	fileScope := &Scope{
		Kind:    FileScope,
		Node:    file.AST,
		Parent:  packageScope,
		Symbols: make(map[string]*types.Symbol),
		Start:   file.AST.Pos(),
		End:     file.AST.End(),
	}
	packageScope.Children = append(packageScope.Children, fileScope)

	// Add import symbols
	if err := sa.addImportSymbols(fileScope, file); err != nil {
		return nil, err
	}

	// Build scopes for function bodies and other blocks
	if err := sa.buildNestedScopes(fileScope, file.AST); err != nil {
		return nil, err
	}

	// Cache the result
	sa.scopeCache[file.Path] = packageScope

	return packageScope, nil
}

// ResolveInScope resolves an identifier within its lexical scope
func (sa *ScopeAnalyzer) ResolveInScope(ident *ast.Ident, file *types.File, pos token.Pos) (*types.Symbol, error) {
	// Build scope tree if not cached
	rootScope, err := sa.BuildScopeTree(file)
	if err != nil {
		return nil, err
	}

	// Find the scope containing the identifier
	scope := sa.findEnclosingScope(rootScope, pos)
	if scope == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "could not determine scope for identifier",
			File:    file.Path,
		}
	}

	// Search up the scope chain
	for currentScope := scope; currentScope != nil; currentScope = currentScope.Parent {
		if symbol, exists := currentScope.Symbols[ident.Name]; exists {
			return symbol, nil
		}
	}

	// Check for qualified identifiers (pkg.Symbol)
	if qualified := sa.resolveQualifiedIdentifier(ident, file, scope); qualified != nil {
		return qualified, nil
	}

	// Try universe scope (built-in functions and types)
	if builtinSymbol := sa.resolveBuiltinSymbol(ident.Name); builtinSymbol != nil {
		return builtinSymbol, nil
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: "identifier not found in scope: " + ident.Name,
		File:    file.Path,
	}
}

// FindInLocalScope searches for a symbol in the immediate local scope
func (sa *ScopeAnalyzer) FindInLocalScope(name string, scope *Scope) (*types.Symbol, error) {
	if symbol, exists := scope.Symbols[name]; exists {
		return symbol, nil
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: "symbol not found in local scope: " + name,
	}
}

// GetScopeAt returns the scope at a specific position
func (sa *ScopeAnalyzer) GetScopeAt(file *types.File, pos token.Pos) (*Scope, error) {
	rootScope, err := sa.BuildScopeTree(file)
	if err != nil {
		return nil, err
	}

	scope := sa.findEnclosingScope(rootScope, pos)
	if scope == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "no scope found at position",
			File:    file.Path,
		}
	}

	return scope, nil
}

// Private helper methods

func (sa *ScopeAnalyzer) addPackageLevelSymbols(scope *Scope, file *types.File) error {
	if file.Package.Symbols == nil {
		return nil
	}

	// Add functions
	for name, symbol := range file.Package.Symbols.Functions {
		if symbol.File == file.Path {
			scope.Symbols[name] = symbol
		}
	}

	// Add types
	for name, symbol := range file.Package.Symbols.Types {
		if symbol.File == file.Path {
			scope.Symbols[name] = symbol
		}
	}

	// Add variables
	for name, symbol := range file.Package.Symbols.Variables {
		if symbol.File == file.Path {
			scope.Symbols[name] = symbol
		}
	}

	// Add constants
	for name, symbol := range file.Package.Symbols.Constants {
		if symbol.File == file.Path {
			scope.Symbols[name] = symbol
		}
	}

	return nil
}

func (sa *ScopeAnalyzer) addImportSymbols(scope *Scope, file *types.File) error {
	for _, imp := range file.AST.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		
		var symbolName string
		if imp.Name != nil {
			// Named import (alias or dot import)
			symbolName = imp.Name.Name
		} else {
			// Default import - use last part of path
			parts := strings.Split(importPath, "/")
			symbolName = parts[len(parts)-1]
		}

		// Create import symbol
		pos := sa.workspace.FileSet.Position(imp.Pos())
		symbol := &types.Symbol{
			Name:     symbolName,
			Kind:     types.PackageSymbol,
			Package:  importPath,
			File:     file.Path,
			Position: imp.Pos(),
			End:      imp.End(),
			Line:     pos.Line,
			Column:   pos.Column,
			Exported: true, // Import symbols are always "visible"
		}

		scope.Symbols[symbolName] = symbol
	}

	return nil
}

func (sa *ScopeAnalyzer) buildNestedScopes(parent *Scope, node ast.Node) error {
	// Only process direct children, don't recurse into complex structures
	switch n := node.(type) {
	case *ast.FuncDecl:
		if n.Body != nil {
			funcScope, err := sa.createFunctionScope(parent, n)
			if err == nil {
				parent.Children = append(parent.Children, funcScope)
			}
		}
		
	case *ast.BlockStmt:
		// Process each statement in the block
		for _, stmt := range n.List {
			_ = sa.buildNestedScopes(parent, stmt)
		}
		
	case *ast.IfStmt:
		if n.Body != nil {
			blockScope, err := sa.createImplicitBlockScope(parent, n)
			if err == nil {
				parent.Children = append(parent.Children, blockScope)
			}
		}
		
	case *ast.ForStmt, *ast.RangeStmt:
		blockScope, err := sa.createImplicitBlockScope(parent, n)
		if err == nil {
			parent.Children = append(parent.Children, blockScope)
		}
		
	case *ast.SwitchStmt, *ast.TypeSwitchStmt:
		blockScope, err := sa.createImplicitBlockScope(parent, n)
		if err == nil {
			parent.Children = append(parent.Children, blockScope)
		}
	}

	return nil
}

func (sa *ScopeAnalyzer) createFunctionScope(parent *Scope, funcDecl *ast.FuncDecl) (*Scope, error) {
	scope := &Scope{
		Kind:    FunctionScope,
		Node:    funcDecl,
		Parent:  parent,
		Symbols: make(map[string]*types.Symbol),
		Start:   funcDecl.Type.Pos(),
		End:     funcDecl.End(),
	}

	// Add receiver if present
	if funcDecl.Recv != nil {
		for _, field := range funcDecl.Recv.List {
			for _, name := range field.Names {
				pos := sa.workspace.FileSet.Position(name.Pos())
				symbol := &types.Symbol{
					Name:     name.Name,
					Kind:     types.VariableSymbol,
					Position: name.Pos(),
					End:      name.End(),
					Line:     pos.Line,
					Column:   pos.Column,
					Exported: false, // Receiver is always local
				}
				scope.Symbols[name.Name] = symbol
			}
		}
	}

	// Add parameters
	if funcDecl.Type.Params != nil {
		for _, field := range funcDecl.Type.Params.List {
			for _, name := range field.Names {
				pos := sa.workspace.FileSet.Position(name.Pos())
				symbol := &types.Symbol{
					Name:     name.Name,
					Kind:     types.VariableSymbol,
					Position: name.Pos(),
					End:      name.End(),
					Line:     pos.Line,
					Column:   pos.Column,
					Exported: false, // Parameters are always local
				}
				scope.Symbols[name.Name] = symbol
			}
		}
	}

	// Add result parameters (named returns)
	if funcDecl.Type.Results != nil {
		for _, field := range funcDecl.Type.Results.List {
			for _, name := range field.Names {
				pos := sa.workspace.FileSet.Position(name.Pos())
				symbol := &types.Symbol{
					Name:     name.Name,
					Kind:     types.VariableSymbol,
					Position: name.Pos(),
					End:      name.End(),
					Line:     pos.Line,
					Column:   pos.Column,
					Exported: false, // Return parameters are local
				}
				scope.Symbols[name.Name] = symbol
			}
		}
	}

	// Function body scopes will be built separately

	return scope, nil
}

func (sa *ScopeAnalyzer) createImplicitBlockScope(parent *Scope, node ast.Node) (*Scope, error) {
	scope := &Scope{
		Kind:    BlockScope,
		Node:    node,
		Parent:  parent,
		Symbols: make(map[string]*types.Symbol),
		Start:   node.Pos(),
		End:     node.End(),
	}

	// Add symbols specific to statement type
	switch stmt := node.(type) {
	case *ast.RangeStmt:
		// Add range variables
		if stmt.Key != nil {
			if ident, ok := stmt.Key.(*ast.Ident); ok && ident.Name != "_" {
				pos := sa.workspace.FileSet.Position(ident.Pos())
				symbol := &types.Symbol{
					Name:     ident.Name,
					Kind:     types.VariableSymbol,
					Position: ident.Pos(),
					End:      ident.End(),
					Line:     pos.Line,
					Column:   pos.Column,
					Exported: false,
				}
				scope.Symbols[ident.Name] = symbol
			}
		}
		if stmt.Value != nil {
			if ident, ok := stmt.Value.(*ast.Ident); ok && ident.Name != "_" {
				pos := sa.workspace.FileSet.Position(ident.Pos())
				symbol := &types.Symbol{
					Name:     ident.Name,
					Kind:     types.VariableSymbol,
					Position: ident.Pos(),
					End:      ident.End(),
					Line:     pos.Line,
					Column:   pos.Column,
					Exported: false,
				}
				scope.Symbols[ident.Name] = symbol
			}
		}

	case *ast.TypeSwitchStmt:
		// Add type switch variable
		if stmt.Assign != nil {
			if assign, ok := stmt.Assign.(*ast.AssignStmt); ok && len(assign.Lhs) > 0 {
				if ident, ok := assign.Lhs[0].(*ast.Ident); ok && ident.Name != "_" {
					pos := sa.workspace.FileSet.Position(ident.Pos())
					symbol := &types.Symbol{
						Name:     ident.Name,
						Kind:     types.VariableSymbol,
						Position: ident.Pos(),
						End:      ident.End(),
						Line:     pos.Line,
						Column:   pos.Column,
						Exported: false,
					}
					scope.Symbols[ident.Name] = symbol
				}
			}
		}
	}

	// Nested scopes will be built separately

	return scope, nil
}

func (sa *ScopeAnalyzer) findEnclosingScope(root *Scope, pos token.Pos) *Scope {
	// Check if position is within this scope
	if pos < root.Start || pos >= root.End {
		return nil
	}

	// Check children first (inner scopes take precedence)
	for _, child := range root.Children {
		if childScope := sa.findEnclosingScope(child, pos); childScope != nil {
			return childScope
		}
	}

	// Position is in this scope but not in any child scope
	return root
}

func (sa *ScopeAnalyzer) resolveQualifiedIdentifier(ident *ast.Ident, file *types.File, scope *Scope) *types.Symbol {
	// Look for selector expressions in the AST that contain this identifier
	// This happens when we have expressions like pkg.Symbol
	
	if file.AST == nil {
		return nil
	}
	
	var result *types.Symbol
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if selector, ok := n.(*ast.SelectorExpr); ok {
			// Check if this selector contains our identifier
			if selector.Sel == ident {
				// The identifier is the selected name (e.g., Symbol in pkg.Symbol)
				if pkgIdent, ok := selector.X.(*ast.Ident); ok {
					// Find the package this refers to
					packageSymbol := sa.resolvePackageIdentifier(pkgIdent, file)
					if packageSymbol != nil {
						// Now look for the symbol in that package
						result = sa.resolveSymbolInPackage(ident.Name, packageSymbol.Package)
						return false // Stop searching
					}
				}
			}
		}
		return true
	})
	
	return result
}

func (sa *ScopeAnalyzer) resolvePackageIdentifier(pkgIdent *ast.Ident, file *types.File) *types.Symbol {
	// Check if this identifier refers to an imported package
	if file.AST != nil {
		for _, imp := range file.AST.Imports {
			importPath := strings.Trim(imp.Path.Value, "\"")
			
			// Check if there's an explicit alias
			if imp.Name != nil {
				if imp.Name.Name == pkgIdent.Name {
					return &types.Symbol{
						Name:    pkgIdent.Name,
						Kind:    types.PackageSymbol,
						Package: importPath,
					}
				}
			} else {
				// Use the last component of the import path as the package name
				lastComponent := filepath.Base(importPath)
				if lastComponent == pkgIdent.Name {
					return &types.Symbol{
						Name:    pkgIdent.Name,
						Kind:    types.PackageSymbol,
						Package: importPath,
					}
				}
			}
		}
	}
	
	return nil
}

func (sa *ScopeAnalyzer) resolveSymbolInPackage(symbolName, packagePath string) *types.Symbol {
	// Find the package in the workspace
	if sa.resolver == nil || sa.resolver.workspace == nil {
		return nil
	}

	ws := sa.resolver.workspace

	// Try direct lookup by filesystem path
	pkg := ws.Packages[packagePath]
	if pkg == nil {
		// Try via import path mapping
		if ws.ImportToPath != nil {
			if fsPath, ok := ws.ImportToPath[packagePath]; ok {
				pkg = ws.Packages[fsPath]
			}
		}
	}

	if pkg == nil {
		return nil
	}

	// Try to resolve the symbol in that package
	symbol, err := sa.resolver.ResolveSymbol(pkg, symbolName)
	if err != nil {
		return nil
	}

	return symbol
}

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

	if kind, exists := builtins[name]; exists {
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

// InvalidateCache clears the scope cache for a file
func (sa *ScopeAnalyzer) InvalidateCache(filePath string) {
	delete(sa.scopeCache, filePath)
}

// ClearCache clears all cached scopes
func (sa *ScopeAnalyzer) ClearCache() {
	sa.scopeCache = make(map[string]*Scope)
}

// NEW: Type Resolution Helpers for Method Call Matching

// GetIdentifierType resolves the type of an identifier at a given position in a file.
// This uses direct AST walking to avoid scope analysis position-matching issues.
func (sa *ScopeAnalyzer) GetIdentifierType(identName string, file *types.File, pos token.Pos) *types.Symbol {
	// Check cache first
	cacheKey := fmt.Sprintf("%s:%s:%d", file.Path, identName, pos)
	if cached := sa.resolver.cache.GetIdentifierType(cacheKey); cached != nil {
		return cached
	}

	if file.AST == nil {
		return nil
	}

	// Find the enclosing function for this position
	var enclosingFunc *ast.FuncDecl
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			if funcDecl.Pos() <= pos && pos <= funcDecl.End() {
				enclosingFunc = funcDecl
				return false // Stop once we find it
			}
		}
		return true
	})

	if enclosingFunc == nil {
		// Not inside a function - check package-level variables
		typeSymbol := sa.findPackageLevelVarType(identName, file)
		if typeSymbol != nil {
			sa.resolver.cache.SetIdentifierType(cacheKey, typeSymbol)
		}
		return typeSymbol
	}

	// Search for variable declarations in the function BY NAME (not position)
	var typeExpr ast.Expr
	ast.Inspect(enclosingFunc, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			// Handle := and = assignments
			for i, lhs := range node.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name == identName {
					// Found declaration! Extract type from RHS
					if i < len(node.Rhs) {
						typeExpr = sa.extractTypeFromExpression(node.Rhs[i])
						return false
					}
				}
			}
		case *ast.ValueSpec:
			// Handle var declarations
			for _, name := range node.Names {
				if name.Name == identName {
					if node.Type != nil {
						typeExpr = node.Type // Explicit type
					} else if len(node.Values) > 0 {
						typeExpr = sa.extractTypeFromExpression(node.Values[0]) // Infer from RHS
					}
					return false
				}
			}
		}
		return true
	})

	// If not found in function body, check function parameters and receiver
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

// findPackageLevelVarType finds the type of a package-level variable by name.
// This handles variables declared outside of any function.
func (sa *ScopeAnalyzer) findPackageLevelVarType(identName string, file *types.File) *types.Symbol {
	var typeExpr ast.Expr
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if genDecl, ok := n.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
			for _, spec := range genDecl.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					for _, name := range valueSpec.Names {
						if name.Name == identName {
							if valueSpec.Type != nil {
								typeExpr = valueSpec.Type
							} else if len(valueSpec.Values) > 0 {
								typeExpr = sa.extractTypeFromExpression(valueSpec.Values[0])
							}
							return false
						}
					}
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
		if ident, ok := e.Fun.(*ast.Ident); ok {
			return ident
		}
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
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
		if builtin := sa.resolveBuiltinSymbol(expr.Name); builtin != nil {
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
		if pkgIdent, ok := expr.X.(*ast.Ident); ok {
			// Resolve package from imports
			for _, imp := range file.AST.Imports {
				var importAlias string
				if imp.Name != nil {
					importAlias = imp.Name.Name
				} else {
					parts := strings.Split(strings.Trim(imp.Path.Value, `"`), "/")
					importAlias = parts[len(parts)-1]
				}

				if importAlias == pkgIdent.Name {
					importPath := strings.Trim(imp.Path.Value, `"`)
					// Try to find package by import path
					if sa.resolver.workspace != nil {
						if fsPath, ok := sa.resolver.workspace.ImportToPath[importPath]; ok {
							if pkg := sa.resolver.workspace.Packages[fsPath]; pkg != nil {
								sym, _ := sa.resolver.ResolveSymbol(pkg, expr.Sel.Name)
								return sym
							}
						}
					}
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