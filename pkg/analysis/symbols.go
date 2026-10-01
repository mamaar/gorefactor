package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
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
// The workspaceIndex provides O(1) lookups by types.Object pointer identity when type
// information is available, using varint-compressed cursor indexes for ~10x memory
// reduction vs the previous objectIndex.
type ReferenceIndex struct {
	nameIndex    map[string][]indexEntry
	workspaceIdx *workspaceIndex

	// pending builds nameIndex on first use; nil once it has, and for an index
	// whose names were given.
	namesOnce sync.Once
	pending   *nameIndexBuilder
}

// names returns the name index, building it if that was deferred.
func (idx *ReferenceIndex) names() map[string][]indexEntry {
	idx.namesOnce.Do(idx.buildNames)
	return idx.nameIndex
}

func (idx *ReferenceIndex) buildNames() {
	if idx.pending != nil {
		idx.nameIndex = idx.pending.run()
		idx.pending = nil
	}
}

// SymbolResolver handles symbol resolution and reference finding
type SymbolResolver struct {
	workspace     *types.Workspace
	scopeAnalyzer *ScopeAnalyzer
	cache         *SymbolCache
	diagnostics   *DiagnosticEngine
	logger        *slog.Logger

	// inspectors is shared with the other views of the workspace set; nil for
	// a resolver made outside one.
	inspectors *inspectorCache
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
		Fields:    make(map[string][]*types.Symbol),
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

	// Struct fields need every package-level type retained first, so they get their own pass.
	for _, file := range pkg.Files {
		sr.extractStructFieldSymbols(file, symbolTable)
	}
	for _, file := range pkg.TestFiles {
		sr.extractStructFieldSymbols(file, symbolTable)
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

// FindReferences finds all references to a symbol in workspace. Where a
// package is type-checked an identifier counts when it uses the symbol's
// go/types object; other identifiers (untyped packages, test files outside the
// check, names the checker could not resolve) count by name and qualifier.
func (sr *SymbolResolver) FindReferences(symbol *types.Symbol) ([]*types.Reference, error) {
	var references []*types.Reference
	target := sr.resolveTypesObject(symbol)

	for _, pkg := range sr.workspace.Packages {
		for _, files := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
			for _, file := range files {
				refs, err := sr.findReferencesInFile(pkg, file, symbol, target)
				if err != nil {
					return nil, err
				}
				references = append(references, refs...)
			}
		}
	}

	return references, nil
}

// BuildReferenceIndex builds a reverse index mapping identifier names to their
// occurrences across all files in the workspace, and an index by go/types
// object for the packages that are type-checked.
func (sr *SymbolResolver) BuildReferenceIndex() *ReferenceIndex {
	idx := sr.BuildLazyReferenceIndex()
	sr.logger.Info("reference index built successfully",
		"name_entries", len(idx.names()),
		"workspace_packages", len(idx.workspaceIdx.packages))
	return idx
}

// BuildLazyReferenceIndex is BuildReferenceIndex with the name index built on
// first use, from the type information the packages hold at this call.
func (sr *SymbolResolver) BuildLazyReferenceIndex() *ReferenceIndex {
	sr.logger.Info("building reference index", "packages", len(sr.workspace.Packages))
	return &ReferenceIndex{pending: sr.newNameIndexBuilder(), workspaceIdx: sr.buildWorkspaceIndex()}
}

// buildWorkspaceIndex indexes the identifiers of every type-checked package by
// go/types object, one package per goroutine.
func (sr *SymbolResolver) buildWorkspaceIndex() *workspaceIndex {
	b := &objectIndexBuilder{resolver: sr}
	for _, pkg := range sr.workspace.Packages {
		if pkg.TypesInfo == nil || pkg.TypesPkg == nil {
			continue
		}
		if files := indexedFiles(pkg); len(files) > 0 {
			b.jobs = append(b.jobs, objectIndexJob{pkg: pkg, files: files})
		}
	}

	var wg sync.WaitGroup
	for i := range b.jobs {
		wg.Add(1)
		go b.build(i, &wg)
	}
	wg.Wait()

	wsIdx := newWorkspaceIndex(sr.workspace.FileSet)
	for _, job := range b.jobs {
		wsIdx.packages[job.pkg.TypesPkg] = job.index
		for _, f := range job.files {
			if tf := sr.workspace.FileSet.File(f.AST.Pos()); tf != nil {
				wsIdx.tokenToFile[tf] = f
			}
		}
	}
	return wsIdx
}

// indexedFiles returns pkg's parsed files, non-test before test and each group
// in name order, so that every view lists the same files the same way.
func indexedFiles(pkg *types.Package) []*types.File {
	var files []*types.File
	for _, group := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
		for _, name := range slices.Sorted(maps.Keys(group)) {
			if f := group[name]; f.AST != nil {
				files = append(files, f)
			}
		}
	}
	return files
}

type objectIndexJob struct {
	pkg   *types.Package
	files []*types.File
	index *packageIndex
}

// objectIndexBuilder builds one packageIndex per job; each goroutine writes
// only its own job.
type objectIndexBuilder struct {
	resolver *SymbolResolver
	jobs     []objectIndexJob
}

func (b *objectIndexBuilder) build(i int, wg *sync.WaitGroup) {
	defer wg.Done()
	job := &b.jobs[i]
	asts := make([]*ast.File, len(job.files))
	for j, f := range job.files {
		asts[j] = f.AST
	}
	ins := b.resolver.inspectors.get(job.pkg.Dir, asts)
	job.index = newPackageIndex(ins, job.pkg.TypesPkg, job.pkg.TypesInfo)
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

// nameIndexJob is one package as it was when the index was requested.
type nameIndexJob struct {
	info  *gotypes.Info // nil when the package was not type-checked
	files []*types.File
}

// nameIndexBuilder indexes every identifier of the workspace by name, one
// package per job on several goroutines; each worker writes only its own map
// of locals.
type nameIndexBuilder struct {
	resolver *SymbolResolver
	jobs     []nameIndexJob
	next     chan int
	locals   []map[string][]indexEntry
}

func (sr *SymbolResolver) newNameIndexBuilder() *nameIndexBuilder {
	b := &nameIndexBuilder{resolver: sr}
	for _, pkg := range sr.workspace.Packages {
		b.jobs = append(b.jobs, nameIndexJob{info: pkg.TypesInfo, files: indexedFiles(pkg)})
	}
	return b
}

func (b *nameIndexBuilder) run() map[string][]indexEntry {
	b.next = make(chan int, len(b.jobs))
	for i := range b.jobs {
		b.next <- i
	}
	close(b.next)

	b.locals = make([]map[string][]indexEntry, workerCount(len(b.jobs)))
	var wg sync.WaitGroup
	for w := range b.locals {
		b.locals[w] = make(map[string][]indexEntry)
		wg.Add(1)
		go b.work(w, &wg)
	}
	wg.Wait()

	merged := make(map[string][]indexEntry)
	for _, local := range b.locals {
		for name, entries := range local {
			merged[name] = append(merged[name], entries...)
		}
	}
	return merged
}

func (b *nameIndexBuilder) work(w int, wg *sync.WaitGroup) {
	defer wg.Done()
	local := b.locals[w]
	for i := range b.next {
		job := b.jobs[i]
		if job.info != nil {
			b.resolver.indexPackageTyped(job, local)
			continue
		}
		for _, f := range job.files {
			b.resolver.indexFileLocal(f, local)
		}
	}
}

// typedFile is one file of a type-checked package while its identifiers are indexed.
type typedFile struct {
	file *types.File
	// qualifiers maps the position of a selector's Sel to the name of its X,
	// for selectors whose X is a plain identifier.
	qualifiers map[token.Pos]string
}

func (f *typedFile) visit(n ast.Node) bool {
	if sel, ok := n.(*ast.SelectorExpr); ok {
		if x, ok := sel.X.(*ast.Ident); ok {
			f.qualifiers[sel.Sel.Pos()] = x.Name
		}
	}
	return true
}

// indexPackageTyped adds an entry for every identifier the package's type
// information defines or resolves, with its go/types object for
// pointer-equality matching.
func (sr *SymbolResolver) indexPackageTyped(job nameIndexJob, nameIndex map[string][]indexEntry) {
	fset := sr.workspace.FileSet
	files := make(map[*token.File]*typedFile, len(job.files))
	for _, f := range job.files {
		tf := &typedFile{file: f, qualifiers: make(map[token.Pos]string)}
		ast.Inspect(f.AST, tf.visit)
		files[fset.File(f.AST.Pos())] = tf
	}

	for ident, obj := range job.info.Defs {
		if obj == nil {
			continue
		}
		tf := files[fset.File(ident.Pos())]
		if tf == nil {
			continue
		}
		nameIndex[ident.Name] = append(nameIndex[ident.Name], indexEntry{
			File:          tf.file,
			Pos:           ident.Pos(),
			IsDeclaration: true,
			TypesObject:   obj,
		})
	}

	for ident, obj := range job.info.Uses {
		if obj == nil {
			continue
		}
		tf := files[fset.File(ident.Pos())]
		if tf == nil {
			continue
		}
		entry := indexEntry{File: tf.file, Pos: ident.Pos(), TypesObject: obj}
		if alias, ok := tf.qualifiers[ident.Pos()]; ok {
			entry.IsSelector = true
			entry.PkgAlias = alias
		}
		nameIndex[ident.Name] = append(nameIndex[ident.Name], entry)
	}
}

// ObjectEntries returns all entries for a given types.Object via the workspaceIndex.
// Returns nil if the workspace index is not populated or the object is not found.
func (idx *ReferenceIndex) ObjectEntries(obj gotypes.Object) []objectEntry {
	if idx.workspaceIdx == nil {
		return nil
	}
	return idx.workspaceIdx.findReferences(obj)
}

// NameEntries returns all name index entries for a given identifier name.
func (idx *ReferenceIndex) NameEntries(name string) ([]indexEntry, bool) {
	entries, ok := idx.names()[name]
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

	// Workspace-index fast-path: varint-compressed cursor index with ~10x memory savings.
	if targetObj != nil && idx.workspaceIdx != nil && len(idx.workspaceIdx.packages) > 0 {
		if objEntries := idx.workspaceIdx.findReferences(targetObj); len(objEntries) > 0 {
			return sr.objectEntriesToReferences(symbol, objEntries, allowedPackages), nil
		}
	}

	// Fall back to name-based path
	entries, ok := idx.names()[symbol.Name]
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
			if !sameObject(entry.TypesObject, targetObj) {
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

// objectEntriesToReferences converts workspace-index object entries into
// []*types.Reference. It skips the symbol's own declaration position and any
// declaration entries, and optionally filters to allowedPackages. This is the
// shared conversion used by both FindReferencesIndexedFiltered (the fast path)
// and FindFieldReferences (the fail-closed field path), so both populate
// Offset/File/Line/Column identically.
func (sr *SymbolResolver) objectEntriesToReferences(symbol *types.Symbol, objEntries []objectEntry, allowedPackages map[string]*types.Package) []*types.Reference {
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
	return references
}

// FindFieldReferences resolves symbol to its go/types object and returns every
// non-declaration reference via the workspace index. It never falls back to
// name-based matching: for a struct field that would rewrite unrelated
// identically-named identifiers.
//
// It fails closed. Any condition that would prevent go/types-backed resolution
// (missing package or type info, an unresolvable object, or an empty/absent
// workspace index) is returned as a *types.RefactorError rather than degrading
// to a name match or a silently-empty result. An empty (but non-nil-object)
// result set is legitimate — a field with zero uses — and returns (nil, nil).
func (sr *SymbolResolver) FindFieldReferences(symbol *types.Symbol, idx *ReferenceIndex) ([]*types.Reference, error) {
	// The workspace type index must be present and populated; without it there
	// is no object-identity path and name matching would corrupt source.
	if idx == nil || idx.workspaceIdx == nil || len(idx.workspaceIdx.packages) == 0 {
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf(
				"cannot safely find references to field %q: the workspace type index is unavailable; struct-field renames require a fully type-checked workspace",
				symbol.Name),
			File:   symbol.File,
			Line:   symbol.Line,
			Column: symbol.Column,
		}
	}

	// The symbol's package must be present and type-checked.
	pkg := sr.findPackageForSymbol(symbol)
	if pkg == nil || pkg.TypesInfo == nil {
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf(
				"cannot safely find references to field %q: type information for package %q is unavailable; struct-field renames require a fully type-checked workspace",
				symbol.Name, symbol.Package),
			File:   symbol.File,
			Line:   symbol.Line,
			Column: symbol.Column,
		}
	}

	// Resolve to the go/types object. A nil object here means we cannot
	// distinguish this field from same-named identifiers, so we must not fall
	// back to a name walk — fail closed instead.
	obj := sr.resolveTypesObject(symbol)
	if obj == nil {
		return nil, &types.RefactorError{
			Type: types.SymbolNotFound,
			Message: fmt.Sprintf(
				"cannot resolve field %q to a type-checked object; struct-field renames require full type information to avoid rewriting unrelated identifiers of the same name",
				symbol.Name),
			File:   symbol.File,
			Line:   symbol.Line,
			Column: symbol.Column,
		}
	}

	// Object resolved: an empty result is a legitimate "field is never used",
	// not an error.
	objEntries := idx.workspaceIdx.findReferences(obj)
	return sr.objectEntriesToReferences(symbol, objEntries, nil), nil
}

// findPackageForSymbol locates the package that declares symbol, trying the
// package identifier directly and then the import-path mapping. Mirrors the
// lookup performed by resolveTypesObject.
func (sr *SymbolResolver) findPackageForSymbol(symbol *types.Symbol) *types.Package {
	pkg := sr.workspace.Packages[symbol.Package]
	if pkg == nil {
		if fsPath, ok := sr.workspace.ImportToPath[symbol.Package]; ok {
			pkg = sr.workspace.Packages[fsPath]
		}
	}
	return pkg
}

// HasNonDeclarationReference checks if a symbol has at least one non-declaration reference
// in the index. Returns true as soon as one is found (early exit for unused detection).
func (sr *SymbolResolver) HasNonDeclarationReference(symbol *types.Symbol, idx *ReferenceIndex) bool {
	targetObj := sr.resolveTypesObject(symbol)

	// Workspace-index fast-path
	if targetObj != nil && idx.workspaceIdx != nil && len(idx.workspaceIdx.packages) > 0 {
		if idx.workspaceIdx.hasNonDeclarationReference(targetObj) {
			return true
		}
		// workspaceIndex covers all typed packages; if it found nothing, check
		// whether we even have entries for this object before falling through.
		if entries := idx.workspaceIdx.findReferences(targetObj); len(entries) > 0 {
			return false
		}
		// Object not found in workspaceIndex — fall through to name-based path
	}

	// Fall back to name-based path
	entries, ok := idx.names()[symbol.Name]
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
			if !sameObject(entry.TypesObject, targetObj) {
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

	// Dotted Type.Member syntax: explicit disambiguation for methods and struct
	// fields. Method first, then field — Go forbids a field and method sharing a
	// name on the same type, so at most one matches; ordering only affects error
	// text. Fields are reachable ONLY through this dotted form (see the bare-name
	// guard below), because bare field names are highly collision-prone.
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		if len(parts) == 2 {
			typeName, member := parts[0], parts[1]
			if methods, exists := pkg.Symbols.Methods[typeName]; exists {
				for _, method := range methods {
					if method.Name == member {
						sr.cache.SetResolvedRef(cacheKey, method)
						return method, nil
					}
				}
			}
			if fields, exists := pkg.Symbols.Fields[typeName]; exists {
				for _, field := range fields {
					if field.Name == member {
						sr.cache.SetResolvedRef(cacheKey, field)
						return field, nil
					}
				}
			}

			// Dotted form supplied but member not found. Report an actionable
			// error rather than falling through to the bare-name paths (which
			// would try to resolve the literal string "Type.Member" and fail
			// generically).
			_, typeIsKnown := pkg.Symbols.Types[typeName]
			methodsForType := pkg.Symbols.Methods[typeName]
			fieldsForType := pkg.Symbols.Fields[typeName]
			if typeIsKnown || len(methodsForType) > 0 || len(fieldsForType) > 0 {
				var availFields, availMethods []string
				for _, f := range fieldsForType {
					availFields = append(availFields, f.Name)
				}
				for _, m := range methodsForType {
					availMethods = append(availMethods, m.Name)
				}
				return nil, &types.RefactorError{
					Type: types.SymbolNotFound,
					Message: fmt.Sprintf(
						"type %q has no field or method %q.\nAvailable fields: %s\nAvailable methods: %s",
						typeName, member,
						formatMemberList(availFields),
						formatMemberList(availMethods)),
					File: pkg.Dir,
				}
			}
			return nil, &types.RefactorError{
				Type: types.SymbolNotFound,
				Message: fmt.Sprintf(
					"unknown type %q in %q: no such type in package %s",
					typeName, name, pkg.Path),
				File: pkg.Dir,
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

	// A bare (undotted) name that matches only struct field(s) must NOT resolve:
	// fields are far more collision-prone than methods, and a bare match could
	// silently target the wrong symbol. Suggest the Type.Field form instead
	// (mirrors the ambiguous-method block above).
	var fieldSuggestions []string
	for typeName, fields := range pkg.Symbols.Fields {
		for _, field := range fields {
			if field.Name == name {
				fieldSuggestions = append(fieldSuggestions, typeName+"."+name)
				break
			}
		}
	}
	if len(fieldSuggestions) > 0 {
		return nil, &types.RefactorError{
			Type: types.SymbolNotFound,
			Message: fmt.Sprintf(
				"%q is a struct field and cannot be resolved by bare name.\nUse Type.Field syntax to select it:\n  %s",
				name, strings.Join(fieldSuggestions, "\n  ")),
			File: pkg.Dir,
		}
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("symbol not found: %s", name),
		File:    pkg.Dir,
	}
}

// formatMemberList renders a list of member names for error messages, using a
// placeholder when the list is empty.
func formatMemberList(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// Helper methods

func (sr *SymbolResolver) extractSymbolsFromFile(file *types.File, symbolTable *types.SymbolTable) error {
	if file == nil || file.AST == nil {
		// Defensive: pkg.Files entries should always carry a parsed AST, but
		// a fabricated/placeholder File slipping through (see
		// pkg/watch/updater.go's recollectImports for the matching guard)
		// must not crash symbol-table rebuilds for the whole package.
		return nil
	}
	for _, decl := range file.AST.Decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			symbol := sr.extractFunctionSymbol(node, file)
			if node.Recv != nil {
				recvType := sr.extractReceiverType(node.Recv)
				symbolTable.Methods[recvType] = append(symbolTable.Methods[recvType], symbol)
			} else if !shadowsProduction(symbolTable.Functions[symbol.Name], symbol) {
				symbolTable.Functions[symbol.Name] = symbol
			}

		case *ast.GenDecl:
			sr.extractGenDeclSymbols(node, file, symbolTable)
		}
	}

	return nil
}

// shadowsProduction reports whether symbol, from a test file, would replace
// existing, a symbol from a non-test file.
func shadowsProduction(existing, symbol *types.Symbol) bool {
	return existing != nil &&
		!strings.HasSuffix(existing.File, "_test.go") &&
		strings.HasSuffix(symbol.File, "_test.go")
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

				table, kind := symbolTable.Variables, types.VariableSymbol
				if genDecl.Tok == token.CONST {
					table, kind = symbolTable.Constants, types.ConstantSymbol
				}
				symbol.Kind = kind
				if !shadowsProduction(table[symbol.Name], symbol) {
					table[symbol.Name] = symbol
				}

				if genDecl.Doc != nil {
					symbol.DocComment = genDecl.Doc.Text()
				}
			}

		case *ast.TypeSpec:
			symbol := sr.extractTypeSymbol(s, file)
			if !shadowsProduction(symbolTable.Types[symbol.Name], symbol) {
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

// extractStructFieldSymbols indexes the fields of a file's package-level struct
// types into symbolTable.Fields, keyed by the owning type's name.
func (sr *SymbolResolver) extractStructFieldSymbols(file *types.File, symbolTable *types.SymbolTable) {
	if file == nil || file.AST == nil {
		return
	}

	for _, decl := range file.AST.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok || structType.Fields == nil {
				continue
			}

			typeName := typeSpec.Name.Name

			// Only index fields for the TypeSpec that was actually retained as the
			// package's symbol for typeName. extractSymbolsFromFile/extractGenDeclSymbols
			// refuse to let a _test.go TypeSpec overwrite a non-test one, so a
			// test-file redeclaration of the same type name must not contribute
			// phantom fields to the kept type. Comparing positions identifies the
			// exact retained TypeSpec (equivalent to, but more robust than,
			// re-checking the _test.go suffix condition here).
			typeSymbol, exists := symbolTable.Types[typeName]
			if !exists || typeSymbol.Position != typeSpec.Name.Pos() {
				continue
			}

			for _, field := range structType.Fields.List {
				// Skip embedded/anonymous fields — they have no field-name ident.
				if len(field.Names) == 0 {
					continue
				}
				for _, name := range field.Names {
					pos := sr.workspace.FileSet.Position(name.Pos())
					fieldSymbol := &types.Symbol{
						Name:     name.Name,
						Kind:     types.StructFieldSymbol,
						Package:  getPackageIdentifier(file.Package),
						File:     file.Path,
						Position: name.Pos(),
						End:      name.End(),
						Line:     pos.Line,
						Column:   pos.Column,
						Exported: sr.isExported(name.Name),
						Parent:   typeSymbol,
					}
					if field.Doc != nil {
						fieldSymbol.DocComment = field.Doc.Text()
					}
					symbolTable.Fields[typeName] = append(symbolTable.Fields[typeName], fieldSymbol)
				}
			}
		}
	}
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

func (sr *SymbolResolver) findReferencesInFile(pkg *types.Package, file *types.File, symbol *types.Symbol, target gotypes.Object) ([]*types.Reference, error) {
	var references []*types.Reference

	if file.AST == nil {
		return references, nil
	}

	info := checkedInfo(pkg, file)
	var roles *identRoles
	ast.Inspect(file.AST, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok || ident.Name != symbol.Name || ident.Pos() == symbol.Position {
			return true
		}
		var refers bool
		if obj, resolved := identObject(info, ident); target != nil && resolved {
			refers = sameObject(obj, target)
		} else {
			if roles == nil {
				roles = newIdentRoles(file.AST)
			}
			refers = sr.nameRefersToSymbol(ident, file, roles, symbol)
		}
		if refers {
			references = append(references, sr.referenceAt(file, ident, symbol))
		}
		return true
	})

	return references, nil
}

func (sr *SymbolResolver) referenceAt(file *types.File, ident *ast.Ident, symbol *types.Symbol) *types.Reference {
	pos := sr.workspace.FileSet.Position(ident.Pos())
	return &types.Reference{
		Symbol:   symbol,
		Position: ident.Pos(),
		Offset:   pos.Offset,
		File:     file.Path,
		Line:     pos.Line,
		Column:   pos.Column,
		Context:  sr.extractContext(ident, file),
	}
}

// FindEmbeddedFieldReferences finds the identifiers that name the field a
// struct embeds a type as (keys of keyed literals and selectors). They are
// not references to the type, but the field is named after it, so renaming
// the type renames them; moving it does not.
func (sr *SymbolResolver) FindEmbeddedFieldReferences(symbol *types.Symbol) []*types.Reference {
	target := sr.resolveTypesObject(symbol)
	if target == nil {
		return nil
	}
	var references []*types.Reference
	for _, pkg := range sr.workspace.Packages {
		for _, files := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
			for _, file := range files {
				info := checkedInfo(pkg, file)
				if info == nil || file.AST == nil {
					continue
				}
				ast.Inspect(file.AST, func(n ast.Node) bool {
					ident, ok := n.(*ast.Ident)
					if !ok || ident.Name != symbol.Name {
						return true
					}
					if field, ok := info.Uses[ident].(*gotypes.Var); ok && field.IsField() && field.Embedded() && sameObject(embeddedTypeName(field), target) {
						references = append(references, sr.referenceAt(file, ident, symbol))
					}
					return true
				})
			}
		}
	}
	return references
}

// embeddedTypeName returns the declared name of the type an embedded field embeds.
func embeddedTypeName(field *gotypes.Var) gotypes.Object {
	t := field.Type()
	if p, ok := t.(*gotypes.Pointer); ok {
		t = p.Elem()
	}
	switch t := t.(type) {
	case *gotypes.Named:
		return t.Origin().Obj()
	case *gotypes.Alias:
		return t.Obj()
	}
	return nil
}

// checkedInfo returns pkg's type information when it covers the identifiers
// of file, and nil for a package that is not type-checked and for a test file
// the check left out or checked as a package other than pkg or its external
// test package.
func checkedInfo(pkg *types.Package, file *types.File) *gotypes.Info {
	if pkg == nil || pkg.TypesInfo == nil || pkg.TypesInfo.Uses == nil || pkg.TypesPkg == nil {
		return nil
	}
	for name, f := range pkg.TestFiles {
		if f != file {
			continue
		}
		pkgName := file.AST.Name.Name
		if _, excluded := pkg.ExcludedTestFiles[name]; excluded || (pkgName != pkg.TypesPkg.Name() && pkgName != pkg.TypesPkg.Name()+"_test") {
			return nil
		}
	}
	return pkg.TypesInfo
}

// identObject returns the object ident uses, or nil for one it defines, and
// whether info resolved ident at all.
func identObject(info *gotypes.Info, ident *ast.Ident) (obj gotypes.Object, resolved bool) {
	if info == nil {
		return nil, false
	}
	if obj, ok := info.Uses[ident]; ok {
		return obj, true
	}
	if _, ok := info.Defs[ident]; ok {
		return nil, true
	}
	return nil, false
}

// identRoles holds what the syntax alone says about the identifiers of a file.
type identRoles struct {
	// nonReference holds the identifiers that name no declaration of the
	// package scope: fields, parameters, method names, labels, import
	// aliases, the package clause and the keys of struct literals.
	nonReference map[*ast.Ident]bool
	// selector maps the selected name of each selector expression to it.
	selector map[*ast.Ident]*ast.SelectorExpr
}

func newIdentRoles(f *ast.File) *identRoles {
	r := &identRoles{nonReference: map[*ast.Ident]bool{f.Name: true}, selector: map[*ast.Ident]*ast.SelectorExpr{}}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Field:
			for _, name := range node.Names {
				r.nonReference[name] = true
			}
		case *ast.FuncDecl:
			if node.Recv != nil {
				r.nonReference[node.Name] = true
			}
		case *ast.LabeledStmt:
			r.nonReference[node.Label] = true
		case *ast.BranchStmt:
			if node.Label != nil {
				r.nonReference[node.Label] = true
			}
		case *ast.ImportSpec:
			if node.Name != nil {
				r.nonReference[node.Name] = true
			}
		case *ast.SelectorExpr:
			r.selector[node.Sel] = node
		case *ast.CompositeLit:
			switch node.Type.(type) {
			case *ast.MapType, *ast.ArrayType:
				// Keys are expressions.
			default:
				for _, elt := range node.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok {
							r.nonReference[key] = true
						}
					}
				}
			}
		}
		return true
	})
	return r
}

// nameRefersToSymbol decides by name and import qualifier whether ident refers
// to symbol: a selected name must be qualified by an import of the symbol's
// package, any other name must be in that package.
func (sr *SymbolResolver) nameRefersToSymbol(ident *ast.Ident, file *types.File, roles *identRoles, symbol *types.Symbol) bool {
	if roles.nonReference[ident] {
		return false
	}
	if sel, ok := roles.selector[ident]; ok {
		x, ok := sel.X.(*ast.Ident)
		return ok && sr.importAliasRefersToPackage(x.Name, file, symbol.Package)
	}
	return sr.isSamePackage(file.Package, symbol.Package)
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
