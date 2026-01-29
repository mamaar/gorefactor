package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/types"
)

// SymbolResolver handles symbol resolution and reference finding
type SymbolResolver struct {
	workspace     *types.Workspace
	scopeAnalyzer *ScopeAnalyzer
	cache         *SymbolCache
	diagnostics   *DiagnosticEngine
}

func NewSymbolResolver(ws *types.Workspace) *SymbolResolver {
	sr := &SymbolResolver{
		workspace: ws,
		cache:     NewSymbolCache(),
	}
	sr.scopeAnalyzer = NewScopeAnalyzer(sr)
	sr.diagnostics = NewDiagnosticEngine(sr)
	return sr
}

// BuildSymbolTable builds complete symbol table for a package
func (sr *SymbolResolver) BuildSymbolTable(pkg *types.Package) (*types.SymbolTable, error) {
	symbolTable := &types.SymbolTable{
		Package:   pkg,
		Functions: make(map[string]*types.Symbol),
		Types:     make(map[string]*types.Symbol),
		Variables: make(map[string]*types.Symbol),
		Constants: make(map[string]*types.Symbol),
		Methods:   make(map[string][]*types.Symbol),
	}

	// Process all files in the package
	for _, file := range pkg.Files {
		err := sr.extractSymbolsFromFile(file, symbolTable)
		if err != nil {
			return nil, err
		}
	}

	// Process test files
	for _, file := range pkg.TestFiles {
		err := sr.extractSymbolsFromFile(file, symbolTable)
		if err != nil {
			return nil, err
		}
	}

	pkg.Symbols = symbolTable
	return symbolTable, nil
}

// FindReferences finds all references to a symbol in workspace
func (sr *SymbolResolver) FindReferences(symbol *types.Symbol) ([]*types.Reference, error) {
	var references []*types.Reference

	for _, pkg := range sr.workspace.Packages {
		for _, file := range pkg.Files {
			refs, err := sr.findReferencesInFile(file, symbol)
			if err != nil {
				return nil, err
			}
			references = append(references, refs...)
		}
		for _, file := range pkg.TestFiles {
			refs, err := sr.findReferencesInFile(file, symbol)
			if err != nil {
				return nil, err
			}
			references = append(references, refs...)
		}
	}

	return references, nil
}

// FindDefinition finds the definition of symbol at given position
func (sr *SymbolResolver) FindDefinition(file string, pos token.Pos) (*types.Symbol, error) {
	// Find the file and AST node at position
	var targetFile *types.File
	for _, pkg := range sr.workspace.Packages {
		for _, f := range pkg.Files {
			if f.Path == file {
				targetFile = f
				break
			}
		}
		if targetFile == nil {
			for _, f := range pkg.TestFiles {
				if f.Path == file {
					targetFile = f
					break
				}
			}
		}
		if targetFile != nil {
			break
		}
	}

	if targetFile == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("file not found: %s", file),
			File:    file,
		}
	}

	// Find identifier at position
	var ident *ast.Ident
	ast.Inspect(targetFile.AST, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		if id, ok := n.(*ast.Ident); ok {
			if id.Pos() <= pos && pos <= id.End() {
				ident = id
				return false
			}
		}
		return true
	})

	if ident == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "no identifier found at position",
			File:    file,
		}
	}

	// Resolve the identifier
	return sr.resolveIdentifier(ident, targetFile)
}

// ResolveSymbol resolves a symbol name within a package context
func (sr *SymbolResolver) ResolveSymbol(pkg *types.Package, name string) (*types.Symbol, error) {
	// Try cache first
	cacheKey := fmt.Sprintf("%s:%s", pkg.Path, name)
	if cached := sr.cache.GetResolvedRef(cacheKey); cached != nil {
		return cached, nil
	}

	if pkg.Symbols == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "package symbol table not built",
			File:    pkg.Dir,
		}
	}

	var symbol *types.Symbol
	
	// Check different symbol types
	if sym, exists := pkg.Symbols.Functions[name]; exists {
		symbol = sym
	} else if sym, exists := pkg.Symbols.Types[name]; exists {
		symbol = sym
	} else if sym, exists := pkg.Symbols.Variables[name]; exists {
		symbol = sym
	} else if sym, exists := pkg.Symbols.Constants[name]; exists {
		symbol = sym
	} else {
		// Check for method symbols by searching through all methods
		for _, methods := range pkg.Symbols.Methods {
			for _, method := range methods {
				if method.Name == name {
					symbol = method
					break
				}
			}
			if symbol != nil {
				break
			}
		}
	}
	
	if symbol != nil {
		// Cache the result
		sr.cache.SetResolvedRef(cacheKey, symbol)
		return symbol, nil
	}

	// Check for method symbols (Type.Method syntax)
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		if len(parts) == 2 {
			typeName, methodName := parts[0], parts[1]
			if methods, exists := pkg.Symbols.Methods[typeName]; exists {
				for _, method := range methods {
					if method.Name == methodName {
						return method, nil
					}
				}
			}
		}
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("symbol not found: %s", name),
		File:    pkg.Dir,
	}
}

// Helper methods

func (sr *SymbolResolver) extractSymbolsFromFile(file *types.File, symbolTable *types.SymbolTable) error {
	ast.Inspect(file.AST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			symbol := sr.extractFunctionSymbol(node, file)
			if node.Recv != nil {
				// Method
				recvType := sr.extractReceiverType(node.Recv)
				if symbolTable.Methods[recvType] == nil {
					symbolTable.Methods[recvType] = make([]*types.Symbol, 0)
				}
				symbolTable.Methods[recvType] = append(symbolTable.Methods[recvType], symbol)
			} else {
				// Function
				symbolTable.Functions[symbol.Name] = symbol
			}

		case *ast.GenDecl:
			sr.extractGenDeclSymbols(node, file, symbolTable)

		case *ast.TypeSpec:
			symbol := sr.extractTypeSymbol(node, file)
			symbolTable.Types[symbol.Name] = symbol
		}
		return true
	})

	return nil
}

func (sr *SymbolResolver) extractFunctionSymbol(funcDecl *ast.FuncDecl, file *types.File) *types.Symbol {
	pos := sr.workspace.FileSet.Position(funcDecl.Name.Pos())
	symbol := &types.Symbol{
		Name:     funcDecl.Name.Name,
		Package:  file.Package.Path,
		File:     file.Path,
		Position: funcDecl.Name.Pos(), // Position of the function name, not the whole declaration
		End:      funcDecl.End(),
		Line:     pos.Line,
		Column:   pos.Column,
		Exported: sr.isExported(funcDecl.Name.Name),
	}

	if funcDecl.Recv != nil {
		symbol.Kind = types.MethodSymbol
	} else {
		symbol.Kind = types.FunctionSymbol
	}

	// Extract signature
	symbol.Signature = sr.extractFunctionSignature(funcDecl)

	// Extract doc comment
	if funcDecl.Doc != nil {
		symbol.DocComment = funcDecl.Doc.Text()
	}

	return symbol
}

func (sr *SymbolResolver) extractGenDeclSymbols(genDecl *ast.GenDecl, file *types.File, symbolTable *types.SymbolTable) {
	for _, spec := range genDecl.Specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			// Variables or constants
			for _, name := range s.Names {
				pos := sr.workspace.FileSet.Position(name.Pos())
				symbol := &types.Symbol{
					Name:     name.Name,
					Package:  file.Package.Path,
					File:     file.Path,
					Position: name.Pos(),
					End:      name.End(),
					Line:     pos.Line,
					Column:   pos.Column,
					Exported: sr.isExported(name.Name),
				}

				if genDecl.Tok == token.CONST {
					symbol.Kind = types.ConstantSymbol
					symbolTable.Constants[symbol.Name] = symbol
				} else {
					symbol.Kind = types.VariableSymbol
					symbolTable.Variables[symbol.Name] = symbol
				}

				if genDecl.Doc != nil {
					symbol.DocComment = genDecl.Doc.Text()
				}
			}

		case *ast.TypeSpec:
			symbol := sr.extractTypeSymbol(s, file)
			symbolTable.Types[symbol.Name] = symbol
		}
	}
}

func (sr *SymbolResolver) extractTypeSymbol(typeSpec *ast.TypeSpec, file *types.File) *types.Symbol {
	pos := sr.workspace.FileSet.Position(typeSpec.Name.Pos())
	symbol := &types.Symbol{
		Name:     typeSpec.Name.Name,
		Kind:     types.TypeSymbol,
		Package:  file.Package.Path,
		File:     file.Path,
		Position: typeSpec.Name.Pos(), // Position of the type name, not the whole declaration
		End:      typeSpec.End(),
		Line:     pos.Line,
		Column:   pos.Column,
		Exported: sr.isExported(typeSpec.Name.Name),
	}

	// Check if it's an interface
	if _, ok := typeSpec.Type.(*ast.InterfaceType); ok {
		symbol.Kind = types.InterfaceSymbol
	}

	return symbol
}

func (sr *SymbolResolver) findReferencesInFile(file *types.File, symbol *types.Symbol) ([]*types.Reference, error) {
	var references []*types.Reference

	ast.Inspect(file.AST, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok {
			if ident.Name == symbol.Name {
				// Check if this identifier refers to our symbol
				if sr.identifierRefersToSymbol(ident, file, symbol) {
					pos := sr.workspace.FileSet.Position(ident.Pos())
					ref := &types.Reference{
						Symbol:   symbol,
						Position: ident.Pos(),
						File:     file.Path,
						Line:     pos.Line,
						Column:   pos.Column,
						Context:  sr.extractContext(ident, file),
					}
					references = append(references, ref)
				}
			}
		}
		return true
	})

	return references, nil
}

func (sr *SymbolResolver) resolveIdentifier(ident *ast.Ident, file *types.File) (*types.Symbol, error) {
	// Use scope-aware resolution with caching
	cacheKey := fmt.Sprintf("%s:%s:%d", file.Path, ident.Name, ident.Pos())
	if cached := sr.cache.GetResolvedRef(cacheKey); cached != nil {
		return cached, nil
	}

	// Try scope-based resolution first (most accurate)
	if symbol, err := sr.scopeAnalyzer.ResolveInScope(ident, file, ident.Pos()); err == nil {
		sr.cache.SetResolvedRef(cacheKey, symbol)
		return symbol, nil
	}

	// Try qualified identifier resolution (pkg.Symbol)
	if qualified := sr.resolveQualifiedIdentifier(ident, file); qualified != nil {
		sr.cache.SetResolvedRef(cacheKey, qualified)
		return qualified, nil
	}

	// Try to find in current package (package-level symbols)
	if file.Package.Symbols != nil {
		if symbol, err := sr.ResolveSymbol(file.Package, ident.Name); err == nil {
			sr.cache.SetResolvedRef(cacheKey, symbol)
			return symbol, nil
		}
	}

	// Try other packages (for exported symbols)
	for _, pkg := range sr.workspace.Packages {
		if symbol, err := sr.ResolveSymbol(pkg, ident.Name); err == nil {
			if symbol.Exported {
				sr.cache.SetResolvedRef(cacheKey, symbol)
				return symbol, nil
			}
		}
	}

	// Use diagnostic engine for enhanced error reporting
	basicError := &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("could not resolve identifier: %s", ident.Name),
		File:    file.Path,
	}
	
	return nil, sr.diagnostics.AnalyzeResolutionFailure(ident, file, basicError)
}

func (sr *SymbolResolver) identifierRefersToSymbol(ident *ast.Ident, file *types.File, symbol *types.Symbol) bool {
	// Don't include the name match if it's the symbol definition itself
	if ident.Pos() == symbol.Position {
		return false
	}

	// Check if this identifier is part of a function declaration for the same symbol
	var isDefinition bool
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			if funcDecl.Name != nil && funcDecl.Name.Pos() == ident.Pos() && funcDecl.Name.Name == symbol.Name {
				isDefinition = true
				return false
			}
		}
		return true
	})

	if isDefinition {
		return false
	}

	// Name must match
	if ident.Name != symbol.Name {
		return false
	}

	// Check if this is a qualified reference (pkg.Symbol)
	if pkgAlias := sr.getQualifyingPackage(ident, file); pkgAlias != "" {
		return sr.importAliasRefersToPackage(pkgAlias, file, symbol.Package)
	}

	// Unqualified reference - must be in the same package
	return sr.isSamePackage(file.Package, symbol.Package)
}

// getQualifyingPackage checks if ident is the selector in pkg.ident and returns the package alias
func (sr *SymbolResolver) getQualifyingPackage(ident *ast.Ident, file *types.File) string {
	var pkgAlias string
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if selector, ok := n.(*ast.SelectorExpr); ok {
			if selector.Sel == ident {
				// This identifier is the selected name (e.g., Symbol in pkg.Symbol)
				if pkgIdent, ok := selector.X.(*ast.Ident); ok {
					pkgAlias = pkgIdent.Name
					return false
				}
			}
		}
		return true
	})
	return pkgAlias
}

// importAliasRefersToPackage checks if import alias refers to target package
func (sr *SymbolResolver) importAliasRefersToPackage(alias string, file *types.File, targetPkg string) bool {
	for _, imp := range file.AST.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)

		var importAlias string
		if imp.Name != nil {
			importAlias = imp.Name.Name
		} else {
			// Default import - use last part of path
			parts := strings.Split(importPath, "/")
			importAlias = parts[len(parts)-1]
		}

		if importAlias == alias {
			// Check if this import path matches the target package
			// Try direct match with import path
			if importPath == targetPkg {
				return true
			}
			// Try looking up the filesystem path via ImportToPath
			if sr.workspace != nil && sr.workspace.ImportToPath != nil {
				if fsPath, ok := sr.workspace.ImportToPath[importPath]; ok {
					if fsPath == targetPkg {
						return true
					}
				}
			}
			// Check if target package's import path matches
			if pkg := sr.workspace.Packages[targetPkg]; pkg != nil && pkg.ImportPath == importPath {
				return true
			}
		}
	}
	return false
}

// isSamePackage checks if the file's package matches the target package path
func (sr *SymbolResolver) isSamePackage(filePkg *types.Package, targetPkg string) bool {
	if filePkg == nil {
		return false
	}
	// Check filesystem path
	if filePkg.Path == targetPkg {
		return true
	}
	// Check import path
	if filePkg.ImportPath != "" && filePkg.ImportPath == targetPkg {
		return true
	}
	return false
}

func (sr *SymbolResolver) extractContext(ident *ast.Ident, file *types.File) string {
	// Extract surrounding context for the identifier
	pos := sr.workspace.FileSet.Position(ident.Pos())
	lines := strings.Split(string(file.OriginalContent), "\n")
	
	if pos.Line > 0 && pos.Line <= len(lines) {
		return strings.TrimSpace(lines[pos.Line-1])
	}
	
	return ""
}

func (sr *SymbolResolver) extractReceiverType(recv *ast.FieldList) string {
	if len(recv.List) == 0 {
		return ""
	}

	switch t := recv.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	}

	return ""
}

func (sr *SymbolResolver) extractFunctionSignature(funcDecl *ast.FuncDecl) string {
	// Extract function signature with function name and parameter names only
	signature := funcDecl.Name.Name + "("
	
	if funcDecl.Type.Params != nil {
		for i, param := range funcDecl.Type.Params.List {
			if i > 0 {
				signature += ", "
			}
			// Add only parameter names (not types) to match test expectations
			if len(param.Names) > 0 {
				signature += param.Names[0].Name
			}
		}
	}
	
	signature += ")"
	
	return signature
}

func (sr *SymbolResolver) isExported(name string) bool {
	return len(name) > 0 && unicode.IsUpper(rune(name[0]))
}

// Advanced resolution methods

// ResolveMethodSet returns all methods available on a type (including promoted methods)
func (sr *SymbolResolver) ResolveMethodSet(symbol *types.Symbol) ([]*types.Symbol, error) {
	if symbol.Kind != types.TypeSymbol {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "can only resolve method set for types",
			File:    symbol.File,
		}
	}

	cacheKey := fmt.Sprintf("methodset:%s:%s", symbol.Package, symbol.Name)
	if cached := sr.cache.GetMethodSet(cacheKey); cached != nil {
		return cached, nil
	}

	var methods []*types.Symbol

	// Find package containing the type
	pkg := sr.workspace.Packages[symbol.Package]
	if pkg == nil || pkg.Symbols == nil {
		return methods, nil
	}

	// Add direct methods
	if directMethods, exists := pkg.Symbols.Methods[symbol.Name]; exists {
		methods = append(methods, directMethods...)
	}

	// Add promoted methods from embedded fields
	promoted, err := sr.findPromotedMethods(symbol)
	if err == nil {
		methods = append(methods, promoted...)
	}

	sr.cache.SetMethodSet(cacheKey, methods)
	return methods, nil
}

// FindInterfaceImplementations finds all types that implement an interface
func (sr *SymbolResolver) FindInterfaceImplementations(iface *types.Symbol) ([]*types.Symbol, error) {
	if iface.Kind != types.InterfaceSymbol {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "can only find implementations for interfaces",
			File:    iface.File,
		}
	}

	var implementations []*types.Symbol

	// Get interface methods
	ifaceMethods, err := sr.getInterfaceMethods(iface)
	if err != nil {
		return nil, err
	}

	// Check all types in workspace
	for _, pkg := range sr.workspace.Packages {
		if pkg.Symbols == nil {
			continue
		}

		for _, typeSymbol := range pkg.Symbols.Types {
			if sr.implementsInterface(typeSymbol, ifaceMethods) {
				implementations = append(implementations, typeSymbol)
			}
		}
	}

	return implementations, nil
}

// CheckInterfaceCompliance checks if a type implements an interface
func (sr *SymbolResolver) CheckInterfaceCompliance(typ, iface *types.Symbol) (bool, []string) {
	if iface.Kind != types.InterfaceSymbol {
		return false, []string{"target is not an interface"}
	}

	ifaceMethods, err := sr.getInterfaceMethods(iface)
	if err != nil {
		return false, []string{"could not get interface methods"}
	}

	typeMethods, err := sr.ResolveMethodSet(typ)
	if err != nil {
		return false, []string{"could not get type methods"}
	}

	var missing []string
	for _, ifaceMethod := range ifaceMethods {
		found := false
		for _, typeMethod := range typeMethods {
			if sr.methodsMatch(ifaceMethod, typeMethod) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, ifaceMethod.Name)
		}
	}

	return len(missing) == 0, missing
}

// ResolveEmbeddedFields finds all embedded fields in a struct type
func (sr *SymbolResolver) ResolveEmbeddedFields(symbol *types.Symbol) ([]*types.Symbol, error) {
	if symbol.Kind != types.TypeSymbol {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "can only resolve embedded fields for types",
			File:    symbol.File,
		}
	}

	// Find the struct definition in AST
	file := sr.findFileContainingSymbol(symbol)
	if file == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "could not find file containing symbol",
			File:    symbol.File,
		}
	}

	var embeddedFields []*types.Symbol

	// Find the type declaration
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok && typeSpec.Name.Name == symbol.Name {
			if structType, ok := typeSpec.Type.(*ast.StructType); ok {
				for _, field := range structType.Fields.List {
					// Embedded field has no name
					if len(field.Names) == 0 {
						embeddedSymbol := sr.resolveFieldType(field.Type, file)
						if embeddedSymbol != nil {
							embeddedFields = append(embeddedFields, embeddedSymbol)
						}
					}
				}
			}
			return false
		}
		return true
	})

	return embeddedFields, nil
}

// FindPromotedMethods finds methods promoted from embedded fields
func (sr *SymbolResolver) FindPromotedMethods(symbol *types.Symbol) ([]*types.Symbol, error) {
	return sr.findPromotedMethods(symbol)
}

// UpdateSymbolTable incrementally updates a package's symbol table
func (sr *SymbolResolver) UpdateSymbolTable(pkg *types.Package, changedFiles []string) error {
	// Invalidate cache for changed files
	for _, filePath := range changedFiles {
		sr.InvalidateCacheForFile(filePath)
	}

	// Rebuild symbol table for the package
	_, err := sr.BuildSymbolTable(pkg)
	return err
}

// InvalidateCacheForPackage clears cache entries for a package
func (sr *SymbolResolver) InvalidateCacheForPackage(pkgPath string) {
	sr.cache.InvalidatePackage(pkgPath)
}

// InvalidateCacheForFile clears cache entries for a file
func (sr *SymbolResolver) InvalidateCacheForFile(filePath string) {
	sr.cache.InvalidateFile(filePath)
	sr.scopeAnalyzer.InvalidateCache(filePath)
}

// Helper methods for advanced resolution

func (sr *SymbolResolver) resolveQualifiedIdentifier(ident *ast.Ident, file *types.File) *types.Symbol {
	// This would need to analyze the context to see if ident is part of a selector
	// For now, return nil - this requires more sophisticated AST analysis
	return nil
}

func (sr *SymbolResolver) getInterfaceMethods(iface *types.Symbol) ([]*types.Symbol, error) {
	// Find the interface definition and extract method signatures
	file := sr.findFileContainingSymbol(iface)
	if file == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "could not find file containing interface",
			File:    iface.File,
		}
	}

	var methods []*types.Symbol

	// Find the interface declaration
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok && typeSpec.Name.Name == iface.Name {
			if interfaceType, ok := typeSpec.Type.(*ast.InterfaceType); ok {
				for _, method := range interfaceType.Methods.List {
					if len(method.Names) > 0 {
						// Named method
						for _, name := range method.Names {
							pos := sr.workspace.FileSet.Position(name.Pos())
							methodSymbol := &types.Symbol{
								Name:     name.Name,
								Kind:     types.MethodSymbol,
								Package:  iface.Package,
								File:     iface.File,
								Position: name.Pos(),
								End:      name.End(),
								Line:     pos.Line,
								Column:   pos.Column,
								Exported: sr.isExported(name.Name),
								Parent:   iface,
							}
							methods = append(methods, methodSymbol)
						}
					}
				}
			}
			return false
		}
		return true
	})

	return methods, nil
}

func (sr *SymbolResolver) implementsInterface(typ *types.Symbol, ifaceMethods []*types.Symbol) bool {
	typeMethods, err := sr.ResolveMethodSet(typ)
	if err != nil {
		return false
	}

	for _, ifaceMethod := range ifaceMethods {
		found := false
		for _, typeMethod := range typeMethods {
			if sr.methodsMatch(ifaceMethod, typeMethod) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

func (sr *SymbolResolver) methodsMatch(method1, method2 *types.Symbol) bool {
	// Simple name matching for now
	// A complete implementation would compare full signatures
	return method1.Name == method2.Name
}

func (sr *SymbolResolver) findPromotedMethods(symbol *types.Symbol) ([]*types.Symbol, error) {
	embeddedFields, err := sr.ResolveEmbeddedFields(symbol)
	if err != nil {
		return nil, err
	}

	var promotedMethods []*types.Symbol

	for _, embedded := range embeddedFields {
		// Get methods from embedded type
		embeddedMethods, err := sr.ResolveMethodSet(embedded)
		if err != nil {
			continue
		}

		// Promoted methods are those that don't conflict with direct methods
		for _, method := range embeddedMethods {
			if method.Exported || method.Package == symbol.Package {
				promotedMethods = append(promotedMethods, method)
			}
		}
	}

	return promotedMethods, nil
}

func (sr *SymbolResolver) findFileContainingSymbol(symbol *types.Symbol) *types.File {
	pkg := sr.workspace.Packages[symbol.Package]
	if pkg == nil {
		return nil
	}

	if file := pkg.Files[symbol.File]; file != nil {
		return file
	}
	return pkg.TestFiles[symbol.File]
}

func (sr *SymbolResolver) resolveFieldType(expr ast.Expr, file *types.File) *types.Symbol {
	// Resolve the type of an embedded field
	switch t := expr.(type) {
	case *ast.Ident:
		// Simple type name
		if symbol, err := sr.ResolveSymbol(file.Package, t.Name); err == nil {
			return symbol
		}
	case *ast.SelectorExpr:
		// Qualified type name (pkg.Type)
		if pkgIdent, ok := t.X.(*ast.Ident); ok {
			// Find the package
			for _, pkg := range sr.workspace.Packages {
				if strings.HasSuffix(pkg.Path, pkgIdent.Name) {
					if symbol, err := sr.ResolveSymbol(pkg, t.Sel.Name); err == nil {
						return symbol
					}
				}
			}
		}
	case *ast.StarExpr:
		// Pointer type
		return sr.resolveFieldType(t.X, file)
	}

	return nil
}