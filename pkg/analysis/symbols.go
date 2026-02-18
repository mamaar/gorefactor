package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/types"
	"golang.org/x/tools/go/ast/inspector"
)

// indexEntry represents a single identifier occurrence found during index building.
type indexEntry struct {
	File          *types.File
	Pos           token.Pos
	IsDeclaration bool   // true if this ident is in a declaration context (func name, type name, var/const name)
	IsSelector    bool   // true if this ident is the Sel part of a SelectorExpr (e.g., pkg.Ident)
	PkgAlias      string // the "pkg" part if IsSelector is true

	// Method call detection fields
	IsMethodCall bool      // true if part of receiver.Method() pattern
	ReceiverName string    // receiver identifier name (e.g., "repo")
	ReceiverPos  token.Pos // position of receiver identifier

	// Type-checked object identity (nil when type info is unavailable)
	TypesObject gotypes.Object
}

// objectEntry is a lightweight index entry keyed by types.Object pointer identity.
// Only 3 fields are needed since the map key already identifies the symbol —
// no selector/alias/receiver fields required.
type objectEntry struct {
	File          *types.File
	Pos           token.Pos
	IsDeclaration bool
}

// ReferenceIndex maps identifier names to all their occurrences across the workspace.
// The objectIndex provides O(1) lookups by types.Object pointer identity when type
// information is available, bypassing all string-based matching.
type ReferenceIndex struct {
	nameIndex   map[string][]indexEntry
	objectIndex map[gotypes.Object][]objectEntry
}

// SymbolResolver handles symbol resolution and reference finding
type SymbolResolver struct {
	workspace     *types.Workspace
	scopeAnalyzer *ScopeAnalyzer
	cache         *SymbolCache
	diagnostics   *DiagnosticEngine
	logger        *slog.Logger
}

func NewSymbolResolver(ws *types.Workspace, logger *slog.Logger) *SymbolResolver {
	sr := &SymbolResolver{
		workspace: ws,
		cache:     NewSymbolCache(),
		logger:    logger,
	}
	sr.scopeAnalyzer = NewScopeAnalyzer(sr)
	sr.diagnostics = NewDiagnosticEngine(sr)
	return sr
}

// getPackageIdentifier returns the best available package identifier for a package.
// Prefers ImportPath (e.g., "github.com/foo/bar"), falls back to Path if ImportPath is empty.
func getPackageIdentifier(pkg *types.Package) string {
	if pkg.ImportPath != "" {
		return pkg.ImportPath
	}
	// Fallback to Path for packages without ImportPath (e.g., when module is not loaded)
	return pkg.Path
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

	// Second pass: Fix up method Parent pointers that couldn't be resolved during extraction
	// This handles cases where methods appear before their receiver types in source order
	for recvTypeName, methods := range symbolTable.Methods {
		for _, method := range methods {
			if method.Parent == nil {
				// Try to resolve the receiver type now that all types are extracted
				if typeSymbol, exists := symbolTable.Types[recvTypeName]; exists {
					method.Parent = typeSymbol
				}
			}
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

// BuildReferenceIndex builds a reverse index mapping identifier names to their
// occurrences across all files in the workspace. This performs one AST walk per file
// and pre-computes declaration and selector information, eliminating the need for
// repeated nested AST walks in identifierRefersToSymbol/getQualifyingPackage/isDeclarationContext.
func (sr *SymbolResolver) BuildReferenceIndex() *ReferenceIndex {
	sr.logger.Info("building reference index", "packages", len(sr.workspace.Packages))

	// Collect all files into a flat slice
	var files []*types.File
	for _, pkg := range sr.workspace.Packages {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
		for _, f := range pkg.TestFiles {
			files = append(files, f)
		}
	}

	sr.logger.Debug("indexing files", "file_count", len(files))

	workers := runtime.NumCPU()
	if workers > len(files) {
		workers = len(files)
	}
	if workers == 0 {
		return &ReferenceIndex{
			nameIndex:   make(map[string][]indexEntry),
			objectIndex: make(map[gotypes.Object][]objectEntry),
		}
	}

	// Each worker builds local indexes to avoid lock contention
	type localResult struct {
		nameIdx map[string][]indexEntry
		objIdx  map[gotypes.Object][]objectEntry
	}
	localResults := make([]localResult, workers)
	var wg sync.WaitGroup
	ch := make(chan int, len(files))
	for i := range files {
		ch <- i
	}
	close(ch)

	for w := 0; w < workers; w++ {
		localResults[w] = localResult{
			nameIdx: make(map[string][]indexEntry),
			objIdx:  make(map[gotypes.Object][]objectEntry),
		}
		wg.Add(1)
		go func(local localResult) {
			defer wg.Done()
			for i := range ch {
				f := files[i]
				if f.Package != nil && f.Package.TypesInfo != nil {
					sr.indexFileTyped(f, local.nameIdx, f.Package.TypesInfo)
					sr.indexFileObject(f, local.objIdx, f.Package.TypesInfo)
				} else {
					sr.indexFileLocal(f, local.nameIdx)
				}
			}
		}(localResults[w])
	}
	wg.Wait()

	// Merge local indexes into the final index
	idx := &ReferenceIndex{
		nameIndex:   make(map[string][]indexEntry),
		objectIndex: make(map[gotypes.Object][]objectEntry),
	}
	for _, local := range localResults {
		for name, entries := range local.nameIdx {
			idx.nameIndex[name] = append(idx.nameIndex[name], entries...)
		}
		for obj, entries := range local.objIdx {
			idx.objectIndex[obj] = append(idx.objectIndex[obj], entries...)
		}
	}

	sr.logger.Info("reference index built successfully",
		"name_entries", len(idx.nameIndex),
		"object_entries", len(idx.objectIndex))
	return idx
}

// indexFileLocal performs a single AST walk over a file using the cursor-based
// inspector API, collecting declarations, selectors, method calls, and identifiers
// in one pass. The cursor's Parent() method replaces the need for a pre-built
// methodCallMap, enabling a true single-pass traversal.
func (sr *SymbolResolver) indexFileLocal(file *types.File, nameIndex map[string][]indexEntry) {
	if file.AST == nil {
		return
	}

	declPositions := make(map[token.Pos]bool)
	selectorMap := make(map[token.Pos]string)

	ins := inspector.New([]*ast.File{file.AST})
	ins.Root().Inspect(
		[]ast.Node{(*ast.FuncDecl)(nil), (*ast.GenDecl)(nil), (*ast.SelectorExpr)(nil), (*ast.Ident)(nil)},
		func(cur inspector.Cursor) bool {
			switch node := cur.Node().(type) {
			case *ast.FuncDecl:
				if node.Name != nil {
					declPositions[node.Name.Pos()] = true
				}
			case *ast.GenDecl:
				for _, spec := range node.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						declPositions[s.Name.Pos()] = true
					case *ast.ValueSpec:
						for _, name := range s.Names {
							declPositions[name.Pos()] = true
						}
					}
				}
			case *ast.SelectorExpr:
				if pkgIdent, ok := node.X.(*ast.Ident); ok {
					selectorMap[node.Sel.Pos()] = pkgIdent.Name
				}
			case *ast.Ident:
				entry := indexEntry{
					File:          file,
					Pos:           node.Pos(),
					IsDeclaration: declPositions[node.Pos()],
				}
				if alias, found := selectorMap[node.Pos()]; found {
					entry.IsSelector = true
					entry.PkgAlias = alias
				}
				// Use cursor parent chain to detect method calls without a pre-pass.
				// Pattern: CallExpr -> SelectorExpr -> Ident (the .Sel)
				parentCur := cur.Parent()
				if parentCur.Node() != nil {
					if selExpr, ok := parentCur.Node().(*ast.SelectorExpr); ok && selExpr.Sel == node {
						// This ident is the Sel of a SelectorExpr — check if grandparent is a CallExpr
						grandparent := parentCur.Parent()
						if grandparent.Node() != nil {
							if _, isCall := grandparent.Node().(*ast.CallExpr); isCall {
								if receiverIdent, ok := selExpr.X.(*ast.Ident); ok {
									entry.IsMethodCall = true
									entry.ReceiverName = receiverIdent.Name
									entry.ReceiverPos = receiverIdent.Pos()
								}
							}
						}
					}
				}
				nameIndex[node.Name] = append(nameIndex[node.Name], entry)
			}
			return true
		},
	)
}

// indexFileTyped builds index entries using go/types information directly.
// Instead of walking the AST, it iterates the TypesInfo.Defs and Uses maps,
// which provide *ast.Ident → types.Object pairs. This eliminates AST walking
// for type-checked packages and populates TypesObject for pointer-equality matching.
func (sr *SymbolResolver) indexFileTyped(file *types.File, nameIndex map[string][]indexEntry, info *gotypes.Info) {
	if file.AST == nil || info == nil {
		return
	}

	// Process definitions (Defs maps defining identifiers to their objects)
	for ident, obj := range info.Defs {
		if obj == nil {
			continue
		}
		// Only include idents that belong to this file
		if sr.workspace.FileSet.File(ident.Pos()) != sr.workspace.FileSet.File(file.AST.Pos()) {
			continue
		}
		entry := indexEntry{
			File:          file,
			Pos:           ident.Pos(),
			IsDeclaration: true,
			TypesObject:   obj,
		}
		nameIndex[ident.Name] = append(nameIndex[ident.Name], entry)
	}

	// Process uses (Uses maps using identifiers to their objects)
	for ident, obj := range info.Uses {
		if obj == nil {
			continue
		}
		if sr.workspace.FileSet.File(ident.Pos()) != sr.workspace.FileSet.File(file.AST.Pos()) {
			continue
		}
		entry := indexEntry{
			File:        file,
			Pos:         ident.Pos(),
			TypesObject: obj,
		}
		// Determine if this is a selector (qualified reference)
		// Walk file AST to find if this ident is the Sel of a SelectorExpr
		ast.Inspect(file.AST, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Pos() == ident.Pos() {
				if pkgIdent, ok := sel.X.(*ast.Ident); ok {
					entry.IsSelector = true
					entry.PkgAlias = pkgIdent.Name
					// Check for method call pattern
					// We'd need parent context, but for typed index we rely on TypesObject
				}
				return false
			}
			return true
		})

		nameIndex[ident.Name] = append(nameIndex[ident.Name], entry)
	}
}

// indexFileObject populates the object index by iterating TypesInfo.Defs and Uses.
// Each entry is keyed by types.Object pointer identity, enabling O(1) lookups
// with zero false positives — no string matching needed.
func (sr *SymbolResolver) indexFileObject(file *types.File, objIndex map[gotypes.Object][]objectEntry, info *gotypes.Info) {
	if file.AST == nil || info == nil {
		return
	}

	tokenFile := sr.workspace.FileSet.File(file.AST.Pos())
	if tokenFile == nil {
		return
	}

	for ident, obj := range info.Defs {
		if obj == nil {
			continue
		}
		if sr.workspace.FileSet.File(ident.Pos()) != tokenFile {
			continue
		}
		objIndex[obj] = append(objIndex[obj], objectEntry{
			File:          file,
			Pos:           ident.Pos(),
			IsDeclaration: true,
		})
	}

	for ident, obj := range info.Uses {
		if obj == nil {
			continue
		}
		if sr.workspace.FileSet.File(ident.Pos()) != tokenFile {
			continue
		}
		objIndex[obj] = append(objIndex[obj], objectEntry{
			File:          file,
			Pos:           ident.Pos(),
			IsDeclaration: false,
		})
	}
}

// ObjectEntries returns all object index entries for a given types.Object.
// Returns nil if the object index is not populated or the object is not found.
func (idx *ReferenceIndex) ObjectEntries(obj gotypes.Object) []objectEntry {
	if idx.objectIndex == nil {
		return nil
	}
	return idx.objectIndex[obj]
}

// NameEntries returns all name index entries for a given identifier name.
func (idx *ReferenceIndex) NameEntries(name string) ([]indexEntry, bool) {
	entries, ok := idx.nameIndex[name]
	return entries, ok
}

// FindReferencesIndexed finds references to a symbol using the pre-built index.
// This is O(1) lookup + O(R) filtering where R is the number of occurrences of the name,
// compared to the O(P×F×A) of FindReferences.
func (sr *SymbolResolver) FindReferencesIndexed(symbol *types.Symbol, idx *ReferenceIndex) ([]*types.Reference, error) {
	return sr.FindReferencesIndexedFiltered(symbol, idx, nil)
}

// FindReferencesIndexedFiltered finds all references to a symbol using the index,
// optionally filtering to only specific packages for performance.
func (sr *SymbolResolver) FindReferencesIndexedFiltered(symbol *types.Symbol, idx *ReferenceIndex, allowedPackages map[string]*types.Package) ([]*types.Reference, error) {
	// Resolve the target symbol's types.Object for pointer-equality matching.
	targetObj := sr.resolveTypesObject(symbol)

	// Object-fast-path: when both the target resolves and the object index is available,
	// look up by types.Object pointer identity — O(1) with zero false positives.
	if targetObj != nil && idx.objectIndex != nil {
		if objEntries := idx.objectIndex[targetObj]; len(objEntries) > 0 {
			var references []*types.Reference
			for i := range objEntries {
				oe := &objEntries[i]
				if oe.Pos == symbol.Position {
					continue
				}
				if oe.IsDeclaration {
					continue
				}
				if allowedPackages != nil && oe.File != nil {
					inAllowed := false
					for _, pkg := range allowedPackages {
						if _, exists := pkg.Files[oe.File.Path]; exists {
							inAllowed = true
							break
						}
						if oe.File.Package == pkg {
							inAllowed = true
							break
						}
					}
					if !inAllowed {
						continue
					}
				}
				pos := sr.workspace.FileSet.Position(oe.Pos)
				references = append(references, &types.Reference{
					Symbol:   symbol,
					Position: oe.Pos,
					Offset:   pos.Offset,
					File:     oe.File.Path,
					Line:     pos.Line,
					Column:   pos.Column,
					Context:  sr.extractContext2(oe.File, pos.Line),
				})
			}
			return references, nil
		}
	}

	// Fall back to name-based path
	entries, ok := idx.nameIndex[symbol.Name]
	if !ok {
		return nil, nil
	}

	var references []*types.Reference
	skippedReasons := make(map[string]int) // DEBUG: track why entries are skipped
	totalEntries := len(entries)

	for i := range entries {
		entry := &entries[i]

		// Skip the symbol's own definition position
		if entry.Pos == symbol.Position {
			skippedReasons["same_position"]++
			continue
		}

		// Skip declarations (these are definition sites, not usages)
		if entry.IsDeclaration {
			skippedReasons["is_declaration"]++
			continue
		}

		// Early package filter: skip entries from files not in allowed packages
		// This happens BEFORE expensive type resolution
		if allowedPackages != nil && entry.File != nil {
			inAllowedPackage := false
			for _, pkg := range allowedPackages {
				// Check both absolute and relative paths
				if _, exists := pkg.Files[entry.File.Path]; exists {
					inAllowedPackage = true
					break
				}
				// Also check if the file's package matches (more reliable than path matching)
				if entry.File.Package == pkg {
					inAllowedPackage = true
					break
				}
			}
			if !inAllowedPackage {
				skippedReasons["not_in_allowed_packages"]++
				continue
			}
		}

		// Fast path: when both entry and target have types.Object, use pointer equality.
		// This eliminates false positives from name shadowing across packages.
		if targetObj != nil && entry.TypesObject != nil {
			if entry.TypesObject != targetObj {
				skippedReasons["types_object_mismatch"]++
				continue
			}
			// Object identity confirmed — skip string-based checks
		} else if entry.IsSelector {
			// Fallback: string-based matching
			// Method call path (receiver.Method) — check if method belongs to receiver's type
			if entry.IsMethodCall && symbol.Kind == types.MethodSymbol {
				if !sr.isMethodCallMatch(entry, symbol) {
					skippedReasons["method_call_no_match"]++
					continue
				}
			} else {
				// Qualified reference (pkg.Symbol) — check if the alias refers to the symbol's package
				if !sr.importAliasRefersToPackage(entry.PkgAlias, entry.File, symbol.Package) {
					skippedReasons["import_alias_mismatch"]++
					continue
				}
			}
		} else {
			// Unqualified reference — must be in the same package
			if !sr.isSamePackage(entry.File.Package, symbol.Package) {
				skippedReasons["package_mismatch"]++
				continue
			}
		}

		pos := sr.workspace.FileSet.Position(entry.Pos)
		ref := &types.Reference{
			Symbol:   symbol,
			Position: entry.Pos,
			Offset:   pos.Offset,
			File:     entry.File.Path,
			Line:     pos.Line,
			Column:   pos.Column,
			Context:  sr.extractContext2(entry.File, pos.Line),
		}
		references = append(references, ref)
	}

	// DEBUG: Log why entries were skipped (only if we found fewer references than total entries)
	if len(references) < totalEntries && len(skippedReasons) > 0 {
		sr.logger.Debug("symbol references skipped",
			"symbol", symbol.Package+"."+symbol.Name,
			"found", len(references),
			"total", totalEntries,
			"skipped_reasons", skippedReasons)
	}

	return references, nil
}

// HasNonDeclarationReference checks if a symbol has at least one non-declaration reference
// in the index. Returns true as soon as one is found (early exit for unused detection).
func (sr *SymbolResolver) HasNonDeclarationReference(symbol *types.Symbol, idx *ReferenceIndex) bool {
	targetObj := sr.resolveTypesObject(symbol)

	// Object-fast-path: O(1) lookup by types.Object pointer identity
	if targetObj != nil && idx.objectIndex != nil {
		if objEntries := idx.objectIndex[targetObj]; len(objEntries) > 0 {
			for i := range objEntries {
				oe := &objEntries[i]
				if oe.Pos == symbol.Position {
					continue
				}
				if oe.IsDeclaration {
					continue
				}
				return true
			}
			return false
		}
	}

	// Fall back to name-based path
	entries, ok := idx.nameIndex[symbol.Name]
	if !ok {
		return false
	}

	for i := range entries {
		entry := &entries[i]

		if entry.Pos == symbol.Position {
			continue
		}
		if entry.IsDeclaration {
			continue
		}

		// Fast path: type-identity comparison
		if targetObj != nil && entry.TypesObject != nil {
			if entry.TypesObject != targetObj {
				continue
			}
			return true
		}

		if entry.IsSelector {
			if entry.IsMethodCall && symbol.Kind == types.MethodSymbol {
				if !sr.isMethodCallMatch(entry, symbol) {
					continue
				}
			} else {
				if !sr.importAliasRefersToPackage(entry.PkgAlias, entry.File, symbol.Package) {
					continue
				}
			}
		} else {
			if !sr.isSamePackage(entry.File.Package, symbol.Package) {
				continue
			}
		}

		return true
	}

	return false
}

// extractContext2 extracts surrounding context using pre-computed line number.
func (sr *SymbolResolver) extractContext2(file *types.File, line int) string {
	lines := strings.Split(string(file.OriginalContent), "\n")
	if line > 0 && line <= len(lines) {
		return strings.TrimSpace(lines[line-1])
	}
	return ""
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

	// Check for method symbols (Type.Method syntax) first for explicit disambiguation
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		if len(parts) == 2 {
			typeName, methodName := parts[0], parts[1]
			if methods, exists := pkg.Symbols.Methods[typeName]; exists {
				for _, method := range methods {
					if method.Name == methodName {
						sr.cache.SetResolvedRef(cacheKey, method)
						return method, nil
					}
				}
			}
		}
	}

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
		// Check for method symbols by searching through all receivers.
		// Detect ambiguity when multiple receiver types define the same method.
		var matchingReceivers []string
		for recvType, methods := range pkg.Symbols.Methods {
			for _, method := range methods {
				if method.Name == name {
					symbol = method
					matchingReceivers = append(matchingReceivers, recvType)
					break
				}
			}
		}
		if len(matchingReceivers) > 1 {
			// Ambiguous: multiple receiver types have this method
			var suggestions []string
			for _, recv := range matchingReceivers {
				suggestions = append(suggestions, recv+"."+name)
			}
			return nil, &types.RefactorError{
				Type: types.SymbolNotFound,
				Message: fmt.Sprintf(
					"ambiguous method name %q: found on %d receiver types.\nUse Type.Method syntax to disambiguate:\n  %s",
					name, len(matchingReceivers), strings.Join(suggestions, "\n  ")),
				File: pkg.Dir,
			}
		}
	}

	if symbol != nil {
		// Cache the result
		sr.cache.SetResolvedRef(cacheKey, symbol)
		return symbol, nil
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
			// Don't overwrite non-test symbols with test symbols
			if existing, exists := symbolTable.Types[symbol.Name]; exists {
				// If existing symbol is from non-test file and new symbol is from test file, keep existing
				if !strings.HasSuffix(existing.File, "_test.go") && strings.HasSuffix(symbol.File, "_test.go") {
					break // Skip adding the test file symbol
				}
			}
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
		Package:  getPackageIdentifier(file.Package),
		File:     file.Path,
		Position: funcDecl.Name.Pos(), // Position of the function name, not the whole declaration
		End:      funcDecl.End(),
		Line:     pos.Line,
		Column:   pos.Column,
		Exported: sr.isExported(funcDecl.Name.Name),
	}

	if funcDecl.Recv != nil {
		symbol.Kind = types.MethodSymbol
		// Set Parent to receiver type for method call matching
		if recvTypeName := sr.extractReceiverTypeName(funcDecl.Recv); recvTypeName != "" {
			if typeSymbol, err := sr.ResolveSymbol(file.Package, recvTypeName); err == nil {
				symbol.Parent = typeSymbol
			}
		}
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
					Package:  getPackageIdentifier(file.Package),
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
			// Don't overwrite non-test symbols with test symbols
			shouldAdd := true
			if existing, exists := symbolTable.Types[symbol.Name]; exists {
				// If existing symbol is from non-test file and new symbol is from test file, keep existing
				if !strings.HasSuffix(existing.File, "_test.go") && strings.HasSuffix(symbol.File, "_test.go") {
					shouldAdd = false
				}
			}
			if shouldAdd {
				symbolTable.Types[symbol.Name] = symbol
				// If this is an interface, also extract its method signatures
				// so they appear in the Methods map keyed by interface name.
				if symbol.Kind == types.InterfaceSymbol {
					sr.extractInterfaceMethodSymbols(s, file, symbolTable)
				}
			}
		}
	}
}

func (sr *SymbolResolver) extractTypeSymbol(typeSpec *ast.TypeSpec, file *types.File) *types.Symbol {
	pos := sr.workspace.FileSet.Position(typeSpec.Name.Pos())
	symbol := &types.Symbol{
		Name:     typeSpec.Name.Name,
		Kind:     types.TypeSymbol,
		Package:  getPackageIdentifier(file.Package),
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

// extractInterfaceMethodSymbols extracts method symbols from an interface type
// and adds them to the symbol table's Methods map keyed by the interface name.
func (sr *SymbolResolver) extractInterfaceMethodSymbols(typeSpec *ast.TypeSpec, file *types.File, symbolTable *types.SymbolTable) {
	interfaceType, ok := typeSpec.Type.(*ast.InterfaceType)
	if !ok || interfaceType.Methods == nil {
		return
	}

	ifaceName := typeSpec.Name.Name
	// Get the interface symbol for setting Parent on methods
	ifaceSym := symbolTable.Types[ifaceName]

	// DEBUG: Log if ifaceSym is nil
	if ifaceSym == nil {
		sr.logger.Debug("extractInterfaceMethodSymbols: ifaceSym is nil",
			"interface", ifaceName)
	}

	for _, field := range interfaceType.Methods.List {
		if len(field.Names) == 0 {
			continue // embedded interface, skip
		}
		for _, name := range field.Names {
			pos := sr.workspace.FileSet.Position(name.Pos())
			methodSymbol := &types.Symbol{
				Name:     name.Name,
				Kind:     types.MethodSymbol,
				Package:  getPackageIdentifier(file.Package),
				File:     file.Path,
				Position: name.Pos(),
				End:      name.End(),
				Line:     pos.Line,
				Column:   pos.Column,
				Exported: sr.isExported(name.Name),
				Parent:   ifaceSym,
			}
			if symbolTable.Methods[ifaceName] == nil {
				symbolTable.Methods[ifaceName] = make([]*types.Symbol, 0)
			}
			symbolTable.Methods[ifaceName] = append(symbolTable.Methods[ifaceName], methodSymbol)
		}
	}
}

func (sr *SymbolResolver) findReferencesInFile(file *types.File, symbol *types.Symbol) ([]*types.Reference, error) {
	var references []*types.Reference

	if file.AST == nil {
		return references, nil
	}

	ast.Inspect(file.AST, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok {
			if ident.Name == symbol.Name {
				// Check if this identifier refers to our symbol
				if sr.identifierRefersToSymbol(ident, file, symbol) {
					pos := sr.workspace.FileSet.Position(ident.Pos())
					ref := &types.Reference{
						Symbol:   symbol,
						Position: ident.Pos(),
						Offset:   pos.Offset,
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
	pkgAlias := sr.getQualifyingPackage(ident, file)
	if pkgAlias != "" {
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
			// Compare by position since we might have different node instances
			if selector.Sel.Pos() == ident.Pos() && selector.Sel.Name == ident.Name {
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
	// Try converting module-relative import path to absolute path for comparison
	if sr.workspace.Module != nil && strings.HasPrefix(targetPkg, sr.workspace.Module.Path+"/") {
		relativePath := strings.TrimPrefix(targetPkg, sr.workspace.Module.Path+"/")
		absPath := sr.workspace.RootPath + "/" + relativePath
		if filePkg.Path == absPath {
			return true
		}
	}
	// Try converting absolute path to module-relative for comparison
	if sr.workspace.Module != nil && strings.HasPrefix(filePkg.Path, sr.workspace.RootPath+"/") {
		relativePath := strings.TrimPrefix(filePkg.Path, sr.workspace.RootPath+"/")
		moduleRelative := sr.workspace.Module.Path + "/" + relativePath
		if moduleRelative == targetPkg {
			return true
		}
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

// extractReceiverTypeName extracts the type name from a receiver field list
func (sr *SymbolResolver) extractReceiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	field := recv.List[0]
	switch typ := field.Type.(type) {
	case *ast.Ident:
		return typ.Name
	case *ast.StarExpr:
		if ident, ok := typ.X.(*ast.Ident); ok {
			return ident.Name
		}
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
	var signature strings.Builder
	signature.WriteString(funcDecl.Name.Name + "(")

	if funcDecl.Type.Params != nil {
		for i, param := range funcDecl.Type.Params.List {
			if i > 0 {
				signature.WriteString(", ")
			}
			// Add only parameter names (not types) to match test expectations
			if len(param.Names) > 0 {
				signature.WriteString(param.Names[0].Name)
			}
		}
	}

	signature.WriteString(")")

	return signature.String()
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
	if pkg == nil {
		// Try to convert module-relative import path to absolute path
		if sr.workspace.Module != nil && strings.HasPrefix(symbol.Package, sr.workspace.Module.Path+"/") {
			// Strip module prefix to get relative path
			relativePath := strings.TrimPrefix(symbol.Package, sr.workspace.Module.Path+"/")
			// Construct absolute path
			absPath := sr.workspace.RootPath + "/" + relativePath
			if p, exists := sr.workspace.Packages[absPath]; exists {
				pkg = p
			}
		}
	}

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

	sr.logger.Debug("FindInterfaceImplementations: interface methods found",
		"interface", iface.Name,
		"method_count", len(ifaceMethods))

	// Check all types in workspace
	checkedTypes := 0
	for _, pkg := range sr.workspace.Packages {
		if pkg.Symbols == nil {
			continue
		}

		for _, typeSymbol := range pkg.Symbols.Types {
			checkedTypes++
			if sr.implementsInterface(typeSymbol, ifaceMethods) {
				sr.logger.Debug("FindInterfaceImplementations: found implementation",
					"type", typeSymbol.Name,
					"package", typeSymbol.Package)
				implementations = append(implementations, typeSymbol)
			}
		}
	}

	sr.logger.Debug("FindInterfaceImplementations: complete",
		"interface", iface.Name,
		"checked_types", checkedTypes,
		"implementations_found", len(implementations))

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
	// DEBUG: Early logging
	sr.logger.Debug("getInterfaceMethods called",
		"interface", iface.Name,
		"package", iface.Package,
		"file", iface.File)

	// Find the interface definition and extract method signatures
	file := sr.findFileContainingSymbol(iface)
	if file == nil {
		// DEBUG: File not found
		sr.logger.Debug("findFileContainingSymbol returned nil",
			"interface", iface.Name)

		// If this is a test file that can't be found, return empty methods instead of error
		// This allows change-signature operations to proceed when there are duplicate interfaces in test files
		if strings.HasSuffix(iface.File, "_test.go") {
			return []*types.Symbol{}, nil
		}
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "could not find file containing interface",
			File:    iface.File,
		}
	}

	var methods []*types.Symbol

	sr.logger.Debug("getInterfaceMethods called for interface",
		"interface", iface.Name,
		"package", iface.Package)

	// Find the interface declaration
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok && typeSpec.Name.Name == iface.Name {
			sr.logger.Debug("Found TypeSpec", "name", iface.Name)
			if interfaceType, ok := typeSpec.Type.(*ast.InterfaceType); ok {
				sr.logger.Debug("Found InterfaceType", "method_count", len(interfaceType.Methods.List))
				for i, method := range interfaceType.Methods.List {
					sr.logger.Debug("Processing method/field",
						"index", i,
						"names", method.Names,
						"type", fmt.Sprintf("%T", method.Type))
					if len(method.Names) > 0 {
						// Named method
						sr.logger.Debug("Named method", "name_count", len(method.Names))
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
							sr.logger.Debug("Added method", "name", name.Name)
						}
					} else {
						// Embedded interface - recursively get its methods
						sr.logger.Debug("Embedded field (no names)")
						if ident, ok := method.Type.(*ast.Ident); ok {
							sr.logger.Debug("Type is *ast.Ident", "name", ident.Name)
							// Look up the embedded interface in the same package
							pkg := sr.workspace.Packages[iface.Package]
							if pkg == nil {
								// Try to convert module-relative import path to absolute path
								if sr.workspace.Module != nil && strings.HasPrefix(iface.Package, sr.workspace.Module.Path+"/") {
									relativePath := strings.TrimPrefix(iface.Package, sr.workspace.Module.Path+"/")
									absPath := sr.workspace.RootPath + "/" + relativePath
									if p, exists := sr.workspace.Packages[absPath]; exists {
										pkg = p
										sr.logger.Debug("Found package by converting module path to absolute",
											"iface.Package", iface.Package,
											"absPath", absPath)
									}
								}
							}
							if pkg == nil {
								sr.logger.Debug("Package not found for embedded interface",
									"iface.Package", iface.Package,
									"embedded", ident.Name)
							}
							if pkg != nil && pkg.Symbols != nil {
								sr.logger.Debug("Looking up in package symbols",
									"name", ident.Name,
									"package", pkg.Path,
									"types_count", len(pkg.Symbols.Types))

								// Debug: list all available types
								var typeNames []string
								for typeName := range pkg.Symbols.Types {
									typeNames = append(typeNames, typeName)
								}
								sr.logger.Debug("Available types in package", "types", typeNames)

								// Check if it's a type (interface)
								if embeddedIface, exists := pkg.Symbols.Types[ident.Name]; exists {
									sr.logger.Debug("Found type", "name", ident.Name, "kind", embeddedIface.Kind)
									if embeddedIface.Kind == types.InterfaceSymbol {
										// Recursively get methods from embedded interface
										sr.logger.Debug("Recursively getting methods", "interface", ident.Name)
										embeddedMethods, err := sr.getInterfaceMethods(embeddedIface)
										if err == nil {
											sr.logger.Debug("Got methods from interface",
												"count", len(embeddedMethods),
												"interface", ident.Name)
											methods = append(methods, embeddedMethods...)
										} else {
											sr.logger.Debug("Error getting methods", "error", err)
										}
									}
								} else {
									sr.logger.Debug("Type not found in package symbols", "name", ident.Name)
								}
							} else {
								sr.logger.Debug("Package or symbols is nil")
							}
						} else {
							sr.logger.Debug("Type is NOT *ast.Ident", "type", fmt.Sprintf("%T", method.Type))
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
		if strings.Contains(typ.Name, "felt") || strings.Contains(typ.Name, "tile") || strings.Contains(typ.Name, "Cloud") {
			sr.logger.Debug("implementsInterface: ResolveMethodSet failed",
				"type", typ.Name,
				"package", typ.Package,
				"error", err)
		}
		return false
	}

	// Debug logging for potential implementation candidates
	if strings.Contains(typ.Name, "felt") || strings.Contains(typ.Name, "tile") || strings.Contains(typ.Name, "Cloud") {
		sr.logger.Debug("implementsInterface: checking type",
			"type", typ.Name,
			"package", typ.Package,
			"type_methods_count", len(typeMethods),
			"iface_methods_count", len(ifaceMethods))
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
		// Try import path → filesystem path mapping
		if fsPath, ok := sr.workspace.ImportToPath[symbol.Package]; ok {
			pkg = sr.workspace.Packages[fsPath]
		}
		if pkg == nil {
			return nil
		}
	}

	// Try direct lookup first (files are keyed by basename)
	baseName := filepath.Base(symbol.File)
	if file := pkg.Files[baseName]; file != nil {
		return file
	}
	if file := pkg.TestFiles[baseName]; file != nil {
		return file
	}

	// Fallback: match by comparing full file paths
	for _, file := range pkg.Files {
		if file.Path == symbol.File {
			return file
		}
	}
	for _, file := range pkg.TestFiles {
		if file.Path == symbol.File {
			return file
		}
	}

	return nil
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

// NEW: Method Call Matching Logic

// isMethodCallMatch checks if an index entry's method call matches a target method symbol.
// This is used to find references to interface methods via method calls like repo.Save().
func (sr *SymbolResolver) isMethodCallMatch(entry *indexEntry, methodSymbol *types.Symbol) bool {
	// Target must be a method
	if methodSymbol.Kind != types.MethodSymbol {
		return false
	}

	// Not a method call - can't match
	if !entry.IsMethodCall {
		return false
	}

	// Resolve the receiver's type
	receiverType := sr.resolveReceiverType(entry)
	if receiverType == nil {
		return false
	}

	// Check if the method belongs to the receiver's type
	return sr.methodBelongsToType(methodSymbol, receiverType)
}

// resolveReceiverType resolves the type of a receiver variable from an index entry.
// Returns the type symbol, or nil if type cannot be resolved.
func (sr *SymbolResolver) resolveReceiverType(entry *indexEntry) *types.Symbol {
	if entry == nil || entry.File == nil {
		return nil
	}

	// Use scope analyzer to get the type of the receiver identifier
	receiverType := sr.scopeAnalyzer.GetIdentifierType(entry.ReceiverName, entry.File, entry.ReceiverPos)
	return receiverType
}

// methodBelongsToType checks if a method symbol belongs to a given type.
// Handles: direct methods, interface methods, and implementations.
func (sr *SymbolResolver) methodBelongsToType(methodSym *types.Symbol, typeSym *types.Symbol) bool {
	if methodSym == nil || typeSym == nil {
		return false
	}

	// Get the type that the method is defined on (its Parent)
	methodReceiverType := methodSym.Parent
	if methodReceiverType == nil {
		// DEBUG: Log when Parent is nil
		sr.logger.Debug("methodBelongsToType: method has nil Parent",
			"method", methodSym.Package+"."+methodSym.Name)
		return false
	}

	// Direct match: same type
	if methodReceiverType.Name == typeSym.Name && methodReceiverType.Package == typeSym.Package {
		return true
	}

	// Strip pointer if needed for comparison
	var strippedTypeName string
	if strings.HasPrefix(typeSym.Name, "*") {
		strippedTypeName = typeSym.Name[1:]
	} else {
		strippedTypeName = typeSym.Name
	}

	var strippedMethodReceiverName string
	if strings.HasPrefix(methodReceiverType.Name, "*") {
		strippedMethodReceiverName = methodReceiverType.Name[1:]
	} else {
		strippedMethodReceiverName = methodReceiverType.Name
	}

	// Stripped match
	if strippedMethodReceiverName == strippedTypeName && methodReceiverType.Package == typeSym.Package {
		return true
	}

	// Interface match: check if typeSym implements the interface that methodSym is part of
	if methodReceiverType.Kind == types.InterfaceSymbol {
		var methods []*types.Symbol
		var err error

		sr.logger.Debug("Interface matching",
			"method_receiver", methodReceiverType.Name,
			"method_receiver_kind", methodReceiverType.Kind,
			"type", typeSym.Name,
			"type_kind", typeSym.Kind)

		// Use appropriate method to get methods based on type kind
		if typeSym.Kind == types.InterfaceSymbol {
			// Both are interfaces - check if typeSym embeds methodReceiverType
			sr.logger.Debug("Both are interfaces, calling getInterfaceMethods", "type", typeSym.Name)
			methods, err = sr.getInterfaceMethods(typeSym)
			sr.logger.Debug("getInterfaceMethods returned",
				"method_count", len(methods),
				"error", err)
			for i, m := range methods {
				sr.logger.Debug("Method",
					"index", i,
					"name", m.Name,
					"package", m.Package)
			}
		} else {
			// typeSym is a concrete type - get its method set
			methods, err = sr.ResolveMethodSet(typeSym)
		}

		if err == nil {
			sr.logger.Debug("Checking if method is in the list",
				"method", methodSym.Name,
				"package", methodSym.Package)
			for _, m := range methods {
				sr.logger.Debug("Comparing methods",
					"m_name", m.Name,
					"method_name", methodSym.Name,
					"m_package", m.Package,
					"method_package", methodSym.Package)
				if m.Name == methodSym.Name && m.Package == methodSym.Package {
					// Also check signature match (rough check: same name)
					sr.logger.Debug("MATCH FOUND")
					return true
				}
			}
			sr.logger.Debug("No match found in method list")
		} else {
			sr.logger.Debug("Error getting methods", "error", err)
		}
	}

	// DEBUG: Log why matching failed
	sr.logger.Debug("methodBelongsToType FAILED",
		"method", methodSym.Name,
		"receiver", methodReceiverType.Name,
		"receiver_kind", methodReceiverType.Kind,
		"type", typeSym.Name,
		"type_kind", typeSym.Kind)

	return false
}

// resolveTypesObject resolves a Symbol to its go/types.Object by looking up
// the identifier at the symbol's position in the TypesInfo of its package.
// Returns nil if type info is unavailable.
func (sr *SymbolResolver) resolveTypesObject(symbol *types.Symbol) gotypes.Object {
	// Find the package containing the symbol
	pkg := sr.workspace.Packages[symbol.Package]
	if pkg == nil {
		if fsPath, ok := sr.workspace.ImportToPath[symbol.Package]; ok {
			pkg = sr.workspace.Packages[fsPath]
		}
	}
	if pkg == nil || pkg.TypesInfo == nil {
		return nil
	}

	// Search Defs for declarations at the symbol's position
	for ident, obj := range pkg.TypesInfo.Defs {
		if ident.Pos() == symbol.Position && obj != nil {
			return obj
		}
	}

	return nil
}
