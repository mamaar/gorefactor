package refactor

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// MoveSymbolOperation implements moving symbols between packages
type MoveSymbolOperation struct {
	Request types.MoveSymbolRequest

	// moved and references hold what Execute moved and the references it found.
	moved      []*types.Symbol
	references []*types.Reference

	// target is the target package's path, and targetImports and importers
	// the packages the move makes it import and the packages that come to
	// import it.
	target                   string
	targetImports, importers []string
}

func (op *MoveSymbolOperation) Type() types.OperationType {
	return types.MoveOperation
}

func (op *MoveSymbolOperation) Validate(ws *types.Workspace) error {
	refs := op.Request.Refs()
	for i, ref := range refs {
		if err := op.validateSymbol(ws, ref); err != nil {
			if len(refs) > 1 {
				return &groupMemberError{index: i, err: err}
			}
			return err
		}
	}
	return validateDistinctNames(refs)
}

// groupMemberError blames one symbol of a multi-symbol move, by its index in
// the request, for making the whole move invalid.
type groupMemberError struct {
	index int
	err   error
}

func (e *groupMemberError) Error() string { return e.err.Error() }
func (e *groupMemberError) Unwrap() error { return e.err }

func validateDistinctNames(refs []types.QualifiedSymbol) error {
	seen := make(map[string]string, len(refs))
	for i, ref := range refs {
		if from, ok := seen[ref.Name]; ok {
			return &groupMemberError{index: i, err: &types.RefactorError{
				Type:    types.NameConflict,
				Message: fmt.Sprintf("symbol %s is also moved from %s, so the two would collide in the target package", ref.Name, from),
			}}
		}
		seen[ref.Name] = ref.Package
	}
	return nil
}

func (op *MoveSymbolOperation) validateSymbol(ws *types.Workspace, ref types.QualifiedSymbol) error {
	// Check that source symbol exists
	sourcePackage, exists := ws.Packages[ref.Package]
	if !exists {
		// Build list of available packages for helpful error message
		var availablePackages []string
		for pkgPath := range ws.Packages {
			availablePackages = append(availablePackages, pkgPath)
		}

		var message strings.Builder
		message.WriteString(fmt.Sprintf("source package not found: %s\nAvailable packages:\n", ref.Package))
		if len(availablePackages) == 0 {
			message.WriteString("  (no packages found - ensure you're in a Go workspace with go.mod)")
		} else {
			for _, pkgPath := range availablePackages {
				if pkg, exists := ws.Packages[pkgPath]; exists {
					message.WriteString(fmt.Sprintf("  - %s (Go package: %s)\n", pkgPath, pkg.Name))
				} else {
					message.WriteString(fmt.Sprintf("  - %s\n", pkgPath))
				}
			}
		}

		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: message.String(),
		}
	}

	if sourcePackage.Symbols == nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "source package symbol table not built",
		}
	}

	// References are told from identically named identifiers by go/types object.
	if sourcePackage.TypesInfo == nil {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("source package %s is not type-checked, so references to %s cannot be told from other identifiers of that name", ref.Package, ref.Name),
		}
	}

	if ref.Name == "_" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "cannot move _: a blank name moves with the declaration that holds it, so name the other names of that declaration",
		}
	}

	// Find the symbol to move
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	symbol, err := resolver.ResolveSymbol(sourcePackage, ref.Name)
	if err != nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("symbol not found: %s", ref.Name),
			Cause:   err,
		}
	}
	if err := memberRefusal(sourcePackage, symbol); err != nil {
		return err
	}
	if err := embedRefusal(sourcePackage, symbol); err != nil {
		return err
	}
	// Check that target package exists or can be created
	if !op.Request.CreateTarget {
		if _, exists := ws.Packages[op.Request.ToPackage]; !exists {
			// Build list of available packages for helpful error message
			var availablePackages []string
			for pkgPath := range ws.Packages {
				availablePackages = append(availablePackages, pkgPath)
			}

			var message strings.Builder
			message.WriteString(fmt.Sprintf("target package not found: %s (CreateTarget=false)\nAvailable packages:\n", op.Request.ToPackage))
			for _, pkg := range availablePackages {
				message.WriteString(fmt.Sprintf("  - %s\n", pkg))
			}
			message.WriteString("\nTip: Use CreateTarget=true to create the target package automatically")

			return &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: message.String(),
			}
		}
	}

	if op.existsInTarget(ws, ref.Name) {
		return &types.RefactorError{
			Type:    types.NameConflict,
			Message: fmt.Sprintf("symbol %s already exists in target package %s", ref.Name, op.Request.ToPackage),
		}
	}

	return nil
}

// existsInTarget reports whether the target package declares a package-level
// name; a method of the same name does not count.
func (op *MoveSymbolOperation) existsInTarget(ws *types.Workspace, name string) bool {
	target, exists := ws.Packages[op.Request.ToPackage]
	if !exists || target.Symbols == nil {
		return false
	}
	for _, group := range []map[string]*types.Symbol{target.Symbols.Functions, target.Symbols.Types, target.Symbols.Variables, target.Symbols.Constants} {
		if group[name] != nil {
			return true
		}
	}
	return false
}

// memberRefusal refuses a method or struct field, which move only with their type.
func memberRefusal(pkg *types.Package, symbol *types.Symbol) error {
	var members map[string][]*types.Symbol
	var what string
	switch symbol.Kind {
	case types.MethodSymbol:
		members, what = pkg.Symbols.Methods, "method"
	case types.StructFieldSymbol:
		members, what = pkg.Symbols.Fields, "field"
	default:
		return nil
	}
	owner := ""
	if symbol.Parent != nil {
		owner = symbol.Parent.Name
	}
	for typeName, syms := range members {
		if slices.Contains(syms, symbol) {
			owner = typeName
		}
	}
	return &types.RefactorError{
		Type:    types.InvalidOperation,
		Message: fmt.Sprintf("cannot move %s: it is a %s of %s and moves with its type; move %s instead", symbol.Name, what, owner, owner),
	}
}

// embedRefusal refuses a constant or variable carrying a //go:embed directive:
// its files stay in the source directory and its blank "embed" import is not
// carried over.
func embedRefusal(pkg *types.Package, symbol *types.Symbol) error {
	if symbol.Kind != types.ConstantSymbol && symbol.Kind != types.VariableSymbol {
		return nil
	}
	file := findFileContainingSymbol(pkg, symbol)
	if file == nil {
		return nil
	}
	unit := findValueUnit(file, symbol.Position)
	if unit == nil {
		return nil
	}
	docs := []*ast.CommentGroup{unit.spec.Doc}
	if unit.whole {
		docs = nil
		for _, spec := range unit.decl.Specs {
			docs = append(docs, spec.(*ast.ValueSpec).Doc)
		}
	}
	if unit.whole || !unit.decl.Lparen.IsValid() {
		docs = append(docs, unit.decl.Doc)
	}
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		for _, c := range doc.List {
			if strings.HasPrefix(c.Text, "//go:embed") {
				return &types.RefactorError{
					Type:    types.InvalidOperation,
					Message: fmt.Sprintf("cannot move %s: it carries a //go:embed directive, whose files are relative to package %s; move it by hand", symbol.Name, pkg.Name),
				}
			}
		}
	}
	return nil
}

// movingSymbol is a declaration the operation moves, the package it leaves and
// the index of the requested symbol that brought it along. A method of a moving
// type is an entry of its own, with typ the entry of that type. Names declared
// in one unit (see valueUnit) are entries of their own too; all but the first
// have sharedWith the first, which alone removes and copies the unit.
type movingSymbol struct {
	sym        *types.Symbol
	pkg        *types.Package
	file       *types.File
	owner      int
	typ        *movingSymbol
	sharedWith *movingSymbol
	removal    []types.Change
}

// name is what error messages call the entry: its type for a method.
func (m *movingSymbol) name() string {
	if m.typ != nil {
		return m.typ.sym.Name
	}
	return m.sym.Name
}

func (op *MoveSymbolOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	moving, err := op.resolveMoving(ws, resolver)
	if err != nil {
		return nil, err
	}
	if err := op.checkGeneratedDeclarations(ws, moving); err != nil {
		return nil, err
	}
	// Find all references to every symbol being moved together. A method is
	// reached through its type, so only the requested symbols are searched.
	var moveSet []*types.Symbol
	var allReferences []*types.Reference
	refSource := make(map[*types.Reference]*types.Package)
	for _, m := range moving {
		if m.typ != nil {
			continue
		}
		moveSet = append(moveSet, m.sym)
		refs, err := resolver.FindReferences(m.sym)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			refSource[ref] = m.pkg
		}
		allReferences = append(allReferences, refs...)
	}

	op.moved, op.references = moveSet, allReferences

	// Remove each moved symbol from its source file.
	removals := make(map[string][]types.Change)
	var sourceFiles []*types.File
	for _, m := range moving {
		if m.file == nil {
			continue
		}
		c, err := op.generateSymbolRemovalChanges(m)
		if err != nil {
			return nil, err
		}
		m.removal = c
	}
	widenGroupRemovals(moving)
	for _, m := range moving {
		if m.file == nil {
			continue
		}
		if _, ok := removals[m.file.Path]; !ok {
			sourceFiles = append(sourceFiles, m.file)
		}
		removals[m.file.Path] = append(removals[m.file.Path], m.removal...)
	}
	if err := op.checkForStrandedReferences(ws, moving); err != nil {
		return nil, err
	}
	unusedImports := make(map[string][]*ast.ImportSpec)
	for _, f := range sourceFiles {
		plan.Changes = append(plan.Changes, removals[f.Path]...)
		plan.AffectedFiles = append(plan.AffectedFiles, f.Path)
		unusedImports[f.Path] = unusedImportsAfterRemoval(ws, f, removals[f.Path])
	}

	// Generate changes to add the moved symbols (and any imports they need)
	// to the target file
	targetPackage, targetFile, created, err := op.getOrCreateTargetFile(ws, op.Request.ToPackage)
	if err != nil {
		return nil, err
	}

	uses := scanMovedUses(ws, moving)
	addChanges, err := op.generateSymbolAdditionChanges(ws, targetFile, moving, uses, targetPackage)
	if err != nil {
		return nil, err
	}
	if created {
		addChanges = []types.Change{{
			File:        targetFile.Path,
			NewText:     string(applyChangesInMemory(targetFile.OriginalContent, addChanges)),
			Description: fmt.Sprintf("Create %s with %s", targetFile.Path, symbolNames(moving)),
		}}
	}
	plan.Changes = append(plan.Changes, addChanges...)
	if !contains(plan.AffectedFiles, targetFile.Path) {
		plan.AffectedFiles = append(plan.AffectedFiles, targetFile.Path)
	}

	// References inside a removed declaration go with it; only the surviving
	// ones are rewritten and need an import of the target package.
	var survivingReferences []*types.Reference
	for _, ref := range allReferences {
		owner := movingAt(moving, ref.File, ref.Offset)
		if owner == nil {
			survivingReferences = append(survivingReferences, ref)
			continue
		}
		if refSource[ref].Path != owner.pkg.Path {
			// The copied declaration would keep a qualifier for a package
			// whose symbol has moved.
			return nil, &groupMemberError{index: owner.owner, err: &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: fmt.Sprintf("%s refers to %s, which moves in the same call from another package", owner.name(), ref.Symbol.Name),
			}}
		}
	}

	op.target = targetPackage.Path
	if err := op.checkImportCycles(ws, moving, uses, survivingReferences, targetPackage.Path); err != nil {
		return nil, err
	}

	// Rewrite each surviving reference, file by file, using the qualifier the
	// file can actually use for the target package (see move_references.go).
	// targetPackage.Path is the workspace-absolute filesystem path resolved
	// (and possibly created) by getOrCreateTargetFile above, unlike
	// op.Request.ToPackage, which may still be the caller's raw input.
	targetImportPath := packagePathToImportPath(ws, targetPackage.Path)
	for _, group := range groupReferencesByFile(survivingReferences) {
		if refPackage := findPackageForFile(ws, group.file); refPackage != nil && refPackage.Path == targetPackage.Path {
			changes, err := op.planTargetPackageReferences(ws, group, refSource, uses, targetFile.Path)
			if err != nil {
				return nil, err
			}
			plan.Changes = append(plan.Changes, changes...)
			if !contains(plan.AffectedFiles, group.file) {
				plan.AffectedFiles = append(plan.AffectedFiles, group.file)
			}
			continue
		}
		var bySource []referenceSource
		for _, ref := range group.refs {
			importPath := packagePathToImportPath(ws, refSource[ref].Path)
			i := slices.IndexFunc(bySource, func(s referenceSource) bool { return s.importPath == importPath })
			if i < 0 {
				i = len(bySource)
				bySource = append(bySource, referenceSource{importPath: importPath, name: refSource[ref].Name})
			}
			bySource[i].refs = append(bySource[i].refs, ref)
		}
		edit := planReferenceFile(ws, group.file, bySource, unusedImports[group.file], targetImportPath, targetPackage.Name)
		plan.Changes = append(plan.Changes, edit.importChanges...)
		delete(unusedImports, group.file)
		for _, ref := range group.refs {
			updateChange, err := op.generateReferenceUpdateChange(ref, edit.qualifier)
			if err != nil {
				return nil, err
			}
			plan.Changes = append(plan.Changes, *updateChange)
		}
		if !contains(plan.AffectedFiles, group.file) {
			plan.AffectedFiles = append(plan.AffectedFiles, group.file)
		}
	}

	for _, f := range sourceFiles {
		for _, spec := range unusedImports[f.Path] {
			plan.Changes = append(plan.Changes, removeImportSpec(ws, f, spec))
		}
	}

	if err := op.checkGeneratedEdits(ws, moving, allReferences, plan.Changes); err != nil {
		return nil, err
	}
	return plan, nil
}

// planTargetPackageReferences drops the source-package qualifier from the
// references to moved symbols in a file of the target package, and the source
// import they leave unused unless the moved code added to targetFilePath needs it.
func (op *MoveSymbolOperation) planTargetPackageReferences(ws *types.Workspace, group fileReferences, refSource map[*types.Reference]*types.Package, uses map[*movingSymbol]*movedUses, targetFilePath string) ([]types.Change, error) {
	var changes []types.Change
	if file := findParsedFile(ws, group.file); file != nil && file.AST != nil {
		needed := make(map[string]bool)
		if group.file == targetFilePath {
			for m, u := range uses {
				maps.Copy(needed, u.imports)
				if len(u.leftBehind) > 0 {
					needed[packagePathToImportPath(ws, m.pkg.Path)] = true
				}
			}
		}
		seen := make(map[string]bool)
		for _, ref := range group.refs {
			importPath := packagePathToImportPath(ws, refSource[ref].Path)
			if seen[importPath] || needed[importPath] {
				continue
			}
			seen[importPath] = true
			var refs []*types.Reference
			for _, r := range group.refs {
				if packagePathToImportPath(ws, refSource[r].Path) == importPath {
					refs = append(refs, r)
				}
			}
			if spec := unusedSourceImport(file, refs, importPath, refSource[ref].Name); spec != nil {
				changes = append(changes, removeImportSpec(ws, file, spec))
			}
		}
	}
	for _, ref := range group.refs {
		change, err := op.generateReferenceUpdateChange(ref, "")
		if err != nil {
			return nil, err
		}
		changes = append(changes, *change)
	}
	return changes, nil
}

// resolveMoving resolves the requested symbols and, for a struct type, the
// constructors that must move with it (see move_companions.go; Execute
// refuses the move if anything else would be stranded).
func (op *MoveSymbolOperation) resolveMoving(ws *types.Workspace, resolver *analysis.SymbolResolver) ([]*movingSymbol, error) {
	var moving []*movingSymbol
	seen := make(map[*types.Symbol]bool)
	names := make(map[string]bool)
	for i, ref := range op.Request.Refs() {
		pkg := ws.Packages[ref.Package]
		symbol, err := resolver.ResolveSymbol(pkg, ref.Name)
		if err != nil {
			return nil, err
		}
		file := findFileContainingSymbol(pkg, symbol)
		companions, err := op.resolveCompanionSymbols(resolver, pkg, file, symbol)
		if err != nil {
			return nil, err
		}
		for _, s := range append([]*types.Symbol{symbol}, companions...) {
			if seen[s] {
				continue
			}
			if s != symbol && op.existsInTarget(ws, s.Name) {
				return nil, &groupMemberError{index: i, err: &types.RefactorError{
					Type:    types.NameConflict,
					Message: fmt.Sprintf("cannot move %s: its constructor %s already exists in package %s", symbol.Name, s.Name, ws.Packages[op.Request.ToPackage].Name),
				}}
			}
			if names[s.Name] {
				return nil, &groupMemberError{index: i, err: &types.RefactorError{
					Type:    types.NameConflict,
					Message: fmt.Sprintf("%s would collide with another symbol of the same name moved in the same call", s.Name),
				}}
			}
			seen[s], names[s.Name] = true, true
			entry := &movingSymbol{sym: s, pkg: pkg, file: findFileContainingSymbol(pkg, s), owner: i}
			moving = append(moving, entry)
			if s.Kind != types.TypeSymbol {
				continue
			}
			methods, err := methodEntries(ws, entry)
			if err != nil {
				return nil, &groupMemberError{index: i, err: err}
			}
			moving = append(moving, methods...)
		}
	}
	if err := groupValueUnits(moving); err != nil {
		return nil, err
	}
	return moving, nil
}

// filesByPath returns first, if any, followed by the other files by path.
func filesByPath(files map[string]*types.File, first *types.File) []*types.File {
	var rest []*types.File
	for _, f := range files {
		if f != first {
			rest = append(rest, f)
		}
	}
	slices.SortFunc(rest, func(a, b *types.File) int { return strings.Compare(a.Path, b.Path) })
	if first != nil {
		return append([]*types.File{first}, rest...)
	}
	return rest
}

// methodEntries returns an entry for each method of the type moving, from the
// type's own file first and then the package's other files by path. A method
// in an in-package test file cannot move and is refused.
func methodEntries(ws *types.Workspace, typ *movingSymbol) ([]*movingSymbol, error) {
	typeName := typ.sym.Name
	for _, file := range filesByPath(typ.pkg.TestFiles, nil) {
		if file.AST == nil || file.AST.Name.Name != typ.pkg.Name {
			continue
		}
		for _, decl := range file.AST.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && receiverTypeName(fd) == typeName {
				rel, err := filepath.Rel(ws.RootPath, file.Path)
				if err != nil {
					rel = file.Path
				}
				line := ws.FileSet.Position(fd.Name.Pos()).Line
				return nil, &types.RefactorError{
					Type:    types.InvalidOperation,
					Message: fmt.Sprintf("cannot move %s: its method %s is declared in the test file %s:%d; move or remove it first", typeName, fd.Name.Name, filepath.ToSlash(rel), line),
					File:    file.Path,
					Line:    line,
				}
			}
		}
	}

	files := filesByPath(typ.pkg.Files, typ.file)
	var methods []*movingSymbol
	for _, file := range files {
		if file == nil || file.AST == nil {
			continue
		}
		for _, decl := range file.AST.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || receiverTypeName(fd) != typeName {
				continue
			}
			sym := &types.Symbol{Name: fd.Name.Name, Kind: types.MethodSymbol, File: file.Path, Position: fd.Name.Pos()}
			methods = append(methods, &movingSymbol{sym: sym, pkg: typ.pkg, file: file, owner: typ.owner, typ: typ})
		}
	}
	return methods, nil
}

// movingAt returns the moved symbol whose removal covers offset in file.
func movingAt(moving []*movingSymbol, file string, offset int) *movingSymbol {
	for _, m := range moving {
		for _, c := range m.removal {
			if c.File == file && offset >= c.Start && offset < c.End {
				return m
			}
		}
	}
	return nil
}

func (op *MoveSymbolOperation) Description() string {
	refs := op.Request.Refs()
	if len(refs) > 1 {
		return fmt.Sprintf("Move %s to %s", refNames(refs), op.Request.ToPackage)
	}
	return fmt.Sprintf("Move %s from %s to %s", refs[0].Name, refs[0].Package, op.Request.ToPackage)
}

// RenameSymbolOperation implements symbol renaming
type RenameSymbolOperation struct {
	Request types.RenameSymbolRequest
}

func (op *RenameSymbolOperation) Type() types.OperationType {
	return types.RenameOperation
}

func (op *RenameSymbolOperation) Validate(ws *types.Workspace) error {
	// Check that symbol exists
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targetSymbols, err := op.findTargetSymbols(ws, resolver)
	if err != nil {
		return err
	}
	return op.validateTargets(ws, targetSymbols)
}

// validateTargets checks that the new name is valid and free for each symbol.
func (op *RenameSymbolOperation) validateTargets(ws *types.Workspace, targetSymbols []*types.Symbol) error {
	// Check that new name is valid Go identifier
	if !isValidGoIdentifier(op.Request.NewName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.Request.NewName),
		}
	}

	// Check for name conflicts
	for _, symbol := range targetSymbols {
		if err := op.checkNameConflict(ws, symbol, op.Request.NewName); err != nil {
			return err
		}
	}

	return nil
}

// buildAvailableSymbolsList creates a list of all symbols in a package for error messages
func (op *RenameSymbolOperation) buildAvailableSymbolsList(pkg *types.Package) []string {
	availableSymbols := make([]string, 0)
	if pkg.Symbols == nil {
		return availableSymbols
	}

	for name := range pkg.Symbols.Functions {
		availableSymbols = append(availableSymbols, fmt.Sprintf("func %s", name))
	}
	for name := range pkg.Symbols.Types {
		availableSymbols = append(availableSymbols, fmt.Sprintf("type %s", name))
	}
	for name := range pkg.Symbols.Variables {
		availableSymbols = append(availableSymbols, fmt.Sprintf("var %s", name))
	}
	for name := range pkg.Symbols.Constants {
		availableSymbols = append(availableSymbols, fmt.Sprintf("const %s", name))
	}

	return availableSymbols
}

// formatSymbolNotFoundError creates a detailed error message with available symbols
func (op *RenameSymbolOperation) formatSymbolNotFoundError(symbolName, packageName string, availableSymbols []string, originalErr error) error {
	var message strings.Builder
	message.WriteString(fmt.Sprintf("symbol not found: %s in package %s\nAvailable symbols (%d):",
		symbolName, packageName, len(availableSymbols)))

	if len(availableSymbols) == 0 {
		message.WriteString("\n  (no symbols found - package may not be parsed correctly)")
	} else {
		for i, sym := range availableSymbols {
			if i < 20 { // Limit output
				message.WriteString(fmt.Sprintf("\n  - %s", sym))
			} else {
				message.WriteString(fmt.Sprintf("\n  ... and %d more", len(availableSymbols)-20))
				break
			}
		}
	}

	return &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: message.String(),
		Cause:   originalErr,
	}
}

// findTargetSymbols locates all symbols matching the request across packages
func (op *RenameSymbolOperation) findTargetSymbols(ws *types.Workspace, resolver *analysis.SymbolResolver) ([]*types.Symbol, error) {
	var targetSymbols []*types.Symbol

	if op.Request.Package != "" {
		// Package-scoped rename
		pkg, exists := ws.Packages[op.Request.Package]
		if !exists {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package not found: %s", op.Request.Package),
			}
		}

		if pkg.Symbols == nil {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package %s has no symbol table built", op.Request.Package),
			}
		}

		symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
		if err != nil {
			// Build detailed error message
			availableSymbols := op.buildAvailableSymbolsList(pkg)
			return nil, op.formatSymbolNotFoundError(op.Request.SymbolName, op.Request.Package, availableSymbols, err)
		}
		targetSymbols = append(targetSymbols, symbol)
	} else {
		// Workspace-wide rename
		for _, pkg := range ws.Packages {
			if pkg.Symbols != nil {
				symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
				if err == nil {
					targetSymbols = append(targetSymbols, symbol)
				}
			}
		}
	}

	if len(targetSymbols) == 0 {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("no symbols found with name: %s", op.Request.SymbolName),
		}
	}

	return targetSymbols, nil
}

func (op *RenameSymbolOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find all symbols to rename
	var targetSymbols []*types.Symbol
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if op.Request.Package != "" {
		// Package-scoped rename
		if pkg, exists := ws.Packages[op.Request.Package]; exists && pkg.Symbols != nil {
			symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
			if err == nil {
				targetSymbols = append(targetSymbols, symbol)
			}
		}
	} else {
		// Workspace-wide rename
		for _, pkg := range ws.Packages {
			if pkg.Symbols != nil {
				symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
				if err == nil {
					targetSymbols = append(targetSymbols, symbol)
				}
			}
		}
	}

	return op.planSymbols(ws, resolver, targetSymbols)
}

// planSymbols plans renaming each symbol's definition and references.
func (op *RenameSymbolOperation) planSymbols(ws *types.Workspace, resolver *analysis.SymbolResolver, targetSymbols []*types.Symbol) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Process each symbol
	for _, symbol := range targetSymbols {
		// Find all references to this symbol
		references, err := resolver.FindReferences(symbol)
		if err != nil {
			return nil, err
		}

		if err := checkShadowedAtReferences(ws, symbol, references, op.Request.NewName); err != nil {
			return nil, err
		}

		// Update symbol definition
		defChange := op.generateDefinitionRenameChange(symbol, op.Request.NewName)
		plan.Changes = append(plan.Changes, defChange)
		if !contains(plan.AffectedFiles, symbol.File) {
			plan.AffectedFiles = append(plan.AffectedFiles, symbol.File)
		}

		// Update all references; a type's embedded fields are named after it
		references = append(references, resolver.FindEmbeddedFieldReferences(symbol)...)
		for _, ref := range references {
			refChange := op.generateReferenceRenameChange(ref, op.Request.NewName)
			plan.Changes = append(plan.Changes, refChange)
			if !contains(plan.AffectedFiles, ref.File) {
				plan.AffectedFiles = append(plan.AffectedFiles, ref.File)
			}
		}
	}

	return plan, nil
}

func (op *RenameSymbolOperation) Description() string {
	return fmt.Sprintf("Rename %s to %s", op.Request.SymbolName, op.Request.NewName)
}

// RenamePackageOperation implements package renaming
type RenamePackageOperation struct {
	Request types.RenamePackageRequest
}

func (op *RenamePackageOperation) Type() types.OperationType {
	return types.RenamePackageOperation
}

func (op *RenamePackageOperation) Validate(ws *types.Workspace) error {
	// Check that source package exists
	targetPackage, exists := ws.Packages[op.Request.PackagePath]
	if !exists {
		var availablePackages []string
		for pkgPath := range ws.Packages {
			availablePackages = append(availablePackages, pkgPath)
		}

		var message strings.Builder
		message.WriteString(fmt.Sprintf("package not found: %s\nAvailable packages:\n", op.Request.PackagePath))
		if len(availablePackages) == 0 {
			message.WriteString("  (no packages found - ensure you're in a Go workspace with go.mod)")
		} else {
			for _, pkgPath := range availablePackages {
				if pkg, exists := ws.Packages[pkgPath]; exists {
					message.WriteString(fmt.Sprintf("  - %s (Go package: %s)\n", pkgPath, pkg.Name))
				}
			}
		}

		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: message.String(),
		}
	}

	// Check that the current package name matches what the user expects
	if targetPackage.Name != op.Request.OldPackageName {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("package name mismatch: expected %s, found %s", op.Request.OldPackageName, targetPackage.Name),
		}
	}

	// Check that new package name is valid
	if !isValidGoIdentifier(op.Request.NewPackageName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go package name: %s", op.Request.NewPackageName),
		}
	}

	return nil
}

func (op *RenamePackageOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Find the package to rename
	targetPackage := ws.Packages[op.Request.PackagePath]

	// Step 1: Update package declaration in all files within the package
	for _, file := range targetPackage.Files {
		change, err := op.generatePackageDeclarationChange(file, op.Request.OldPackageName, op.Request.NewPackageName)
		if err != nil {
			return nil, fmt.Errorf("failed to generate package declaration change for %s: %v", file.Path, err)
		}
		if change != nil {
			plan.Changes = append(plan.Changes, *change)
			plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
		}
	}

	// Step 2: Update import statements in other packages (if requested)
	if op.Request.UpdateImports {
		importChanges, affectedFiles, err := op.generateImportUpdates(ws, op.Request.PackagePath, op.Request.NewPackageName)
		if err != nil {
			return nil, fmt.Errorf("failed to generate import updates: %v", err)
		}
		plan.Changes = append(plan.Changes, importChanges...)
		for _, file := range affectedFiles {
			if !contains(plan.AffectedFiles, file) {
				plan.AffectedFiles = append(plan.AffectedFiles, file)
			}
		}
	}

	return markRelocation(plan), nil
}

func (op *RenamePackageOperation) Description() string {
	return fmt.Sprintf("Rename package %s to %s in %s", op.Request.OldPackageName, op.Request.NewPackageName, op.Request.PackagePath)
}

// Helper methods for MoveSymbolOperation

func (op *MoveSymbolOperation) generateSymbolRemovalChanges(m *movingSymbol) ([]types.Change, error) {
	file, symbol := m.file, m.sym
	if m.sharedWith != nil {
		return nil, nil
	}
	if file.AST == nil {
		return nil, fmt.Errorf("AST not loaded for file %s", file.Path)
	}
	start, end, _, ok := declLines(file, symbol.Position)
	if !ok {
		return nil, fmt.Errorf("symbol %s not found in AST of file %s", symbol.Name, file.Path)
	}
	description := fmt.Sprintf("Remove function %s", symbol.Name)
	switch {
	case m.typ != nil:
		description = fmt.Sprintf("Remove method %s.%s", m.typ.sym.Name, symbol.Name)
	case symbol.Kind == types.TypeSymbol:
		description = fmt.Sprintf("Remove type %s", symbol.Name)
	case symbol.Kind == types.ConstantSymbol:
		description = fmt.Sprintf("Remove constant %s", symbol.Name)
	case symbol.Kind == types.VariableSymbol:
		description = fmt.Sprintf("Remove variable %s", symbol.Name)
	}
	return []types.Change{lineRemoval(file, start, end, description)}, nil
}

// widenGroupRemovals makes the removals of a parenthesised declaration whose
// specs all move cover the whole group, so no empty shell or stray comment
// stays behind. Each moving symbol keeps one removal; together they tile the
// group from its first line through the closing parenthesis.
func widenGroupRemovals(moving []*movingSymbol) {
	byName := make(map[token.Pos]*movingSymbol)
	for _, m := range moving {
		carrier := cmp.Or(m.sharedWith, m)
		if m.file != nil && m.typ == nil && len(carrier.removal) == 1 {
			byName[m.sym.Position] = carrier
		}
	}
	done := make(map[*types.File]bool)
	for _, m := range moving {
		if m.file == nil || m.file.AST == nil || done[m.file] {
			continue
		}
		done[m.file] = true
		for _, decl := range m.file.AST.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || !d.Lparen.IsValid() || len(d.Specs) < 2 || d.Tok == token.IMPORT || d.Tok != token.TYPE && !selfContained(d) {
				continue
			}
			members := make([]*movingSymbol, 0, len(d.Specs))
			for _, spec := range d.Specs {
				var mover *movingSymbol
				for _, id := range specNames(spec) {
					if byName[id.Pos()] != nil && byName[id.Pos()].file == m.file {
						mover = byName[id.Pos()]
					}
				}
				if mover == nil {
					break
				}
				members = append(members, mover)
			}
			if len(members) != len(d.Specs) {
				continue
			}
			first := d.Pos()
			if d.Doc != nil {
				first = d.Doc.Pos()
			}
			bounds := []int{sourceLine(m.file, first)}
			for _, spec := range d.Specs[1:] {
				bounds = append(bounds, sourceLine(m.file, specFirst(spec)))
			}
			bounds = append(bounds, sourceLine(m.file, d.End()-1)+1)
			ok = true
			for i, spec := range d.Specs {
				if sourceLine(m.file, spec.End()-1)+1 > bounds[i+1] {
					ok = false
				}
			}
			if !ok {
				continue
			}
			for i, mem := range members {
				mem.removal = []types.Change{lineRemoval(m.file, bounds[i], bounds[i+1], mem.removal[0].Description)}
			}
		}
	}
}

// specFirst is the first position of spec, including its doc comment.
func specFirst(spec ast.Spec) token.Pos {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Doc != nil {
			return s.Doc.Pos()
		}
	case *ast.ValueSpec:
		if s.Doc != nil {
			return s.Doc.Pos()
		}
	}
	return spec.Pos()
}

// specNames returns the identifiers spec declares.
func specNames(spec ast.Spec) []*ast.Ident {
	if vs, ok := spec.(*ast.ValueSpec); ok {
		return vs.Names
	}
	if ts, ok := spec.(*ast.TypeSpec); ok {
		return []*ast.Ident{ts.Name}
	}
	return nil
}

// lineRemoval is the change deleting the 0-based lines [start, end) of file.
// A range ending the file without a trailing newline has no newline to remove.
func lineRemoval(file *types.File, start, end int, description string) types.Change {
	lines := strings.Split(string(file.OriginalContent), "\n")
	startByte := 0
	for _, line := range lines[:start] {
		startByte += len(line) + 1
	}
	endByte := startByte
	for _, line := range lines[start:end] {
		endByte += len(line) + 1
	}
	change := types.Change{
		File:        file.Path,
		Start:       startByte,
		End:         endByte,
		OldText:     strings.Join(lines[start:end], "\n") + "\n",
		Description: description,
	}
	if limit := len(file.OriginalContent); change.End > limit {
		change.End = limit
		change.OldText = strings.TrimSuffix(change.OldText, "\n")
	}
	return change
}

// sourceLine is the 0-based line of pos in file.
func sourceLine(file *types.File, pos token.Pos) int {
	return bytes.Count(file.OriginalContent[:int(pos-file.AST.FileStart)], []byte("\n"))
}

// declLines is the 0-based line range [start, end) of the declaration whose
// name is at pos: its doc comment through its last line. A spec in a group
// is returned alone, with keyword the declaration keyword to put before it.
func declLines(file *types.File, pos token.Pos) (start, end int, keyword string, ok bool) {
	span := func(doc *ast.CommentGroup, node ast.Node) (int, int) {
		first := node.Pos()
		if doc != nil {
			first = doc.Pos()
		}
		return sourceLine(file, first), sourceLine(file, node.End()-1) + 1
	}
	for _, decl := range file.AST.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.Pos() == pos {
				start, end = span(d.Doc, d)
				return start, end, "", true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if !slices.ContainsFunc(specNames(spec), func(id *ast.Ident) bool { return id.Pos() == pos }) {
					continue
				}
				if d.Lparen.IsValid() && len(d.Specs) > 1 && (d.Tok == token.TYPE || selfContained(d)) {
					start, end = span(specDoc(spec), spec)
					return start, end, d.Tok.String(), true
				}
				start, end = span(d.Doc, d)
				return start, end, "", true
			}
		}
	}
	return 0, 0, "", false
}

// specDoc is the doc comment of spec, or nil.
func specDoc(spec ast.Spec) *ast.CommentGroup {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return s.Doc
	case *ast.ValueSpec:
		return s.Doc
	}
	return nil
}

// generateSymbolAdditionChanges builds the change(s) that add every moving
// symbol (the requested ones plus any companion functions — see
// move_companions.go) to targetFile, along with whatever imports that code
// needs (see move_imports.go). Names the code uses of exported symbols left
// behind in a source package are qualified with it and that package is
// imported.
func (op *MoveSymbolOperation) generateSymbolAdditionChanges(ws *types.Workspace, targetFile *types.File, moving []*movingSymbol, uses map[*movingSymbol]*movedUses, targetPackage *types.Package) ([]types.Change, error) {
	if len(moving) == 0 {
		return nil, fmt.Errorf("no symbols to add to %s", targetPackage.Path)
	}
	for _, m := range moving {
		if m.file == nil {
			return nil, fmt.Errorf("could not find source file for symbol %s", m.sym.Name)
		}
		if len(m.file.OriginalContent) == 0 {
			return nil, fmt.Errorf("source file %s has no content loaded", m.file.Path)
		}
		if path := uses[m].dotImport; path != "" {
			return nil, &groupMemberError{index: m.owner, err: &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: fmt.Sprintf("cannot move %s: it uses names from the dot import %q in %s; qualify them first", m.name(), path, filepath.Base(m.file.Path)),
			}}
		}
	}

	targetImportPath := packagePathToImportPath(ws, targetPackage.Path)
	names, needed := chooseImportNames(ws, targetFile, moving, uses, targetImportPath)

	var blocks []string
	for _, m := range moving {
		if m.sharedWith != nil {
			continue
		}
		symbolCode, err := op.extractSymbolSource(m.file, m.sym, qualifierEdits(ws, m, uses[m], names, targetImportPath))
		if err != nil {
			return nil, fmt.Errorf("failed to extract symbol source: %w", err)
		}
		blocks = append(blocks, symbolCode)
	}

	combinedCode := "\n" + strings.Join(blocks, "\n\n") + "\n"

	// Insert at end of file (simplified - real implementation would be smarter about placement)
	changes := []types.Change{{
		File:        targetFile.Path,
		Start:       len(targetFile.OriginalContent),
		End:         len(targetFile.OriginalContent),
		OldText:     "",
		NewText:     combinedCode,
		Description: fmt.Sprintf("Add %s to %s", symbolNames(moving), targetPackage.Path),
	}}

	if importChange := generateNeededImportsChange(ws, targetFile, needed); importChange != nil {
		changes = append(changes, *importChange)
	}

	return changes, nil
}

func symbolNames(moving []*movingSymbol) string {
	var names []string
	for _, m := range moving {
		if m.typ == nil {
			names = append(names, m.sym.Name)
		}
	}
	return strings.Join(names, ", ")
}

// generateReferenceUpdateChange rewrites one reference to a moved symbol so it
// is spelled qualifier.Name, replacing any existing package qualifier.
func (op *MoveSymbolOperation) generateReferenceUpdateChange(ref *types.Reference, qualifier string) (*types.Change, error) {
	// Read the file content to detect if this is a qualified reference
	content, err := os.ReadFile(ref.File)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", ref.File, err)
	}

	oldRef := ref.Symbol.Name
	newRef := ref.Symbol.Name
	if qualifier != "" {
		newRef = qualifier + "." + newRef
	}
	startPos := ref.Offset
	endPos := startPos + len(oldRef)

	// Check if this is a qualified reference (e.g., pkg.Symbol)
	// Look backwards for a dot and package name
	if startPos > 0 && content[startPos-1] == '.' {
		// Find the start of the package name
		pkgStart := startPos - 2 // Position before the dot
		for pkgStart >= 0 && (isIdentChar(content[pkgStart]) || content[pkgStart] == '_') {
			pkgStart--
		}
		pkgStart++ // Move to first char of package name

		// Extract the old package name
		oldPkg := string(content[pkgStart : startPos-1])
		if oldPkg != "" {
			// This is a qualified reference - replace the whole thing
			oldRef = oldPkg + "." + ref.Symbol.Name
			startPos = pkgStart
		}
	}

	change := &types.Change{
		File:        ref.File,
		Start:       startPos,
		End:         endPos,
		OldText:     oldRef,
		NewText:     newRef,
		Description: fmt.Sprintf("Update reference to %s at line %d", ref.Symbol.Name, ref.Line),
	}

	return change, nil
}

// isIdentChar returns true if the byte is a valid Go identifier character
func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// getOrCreateTargetFile returns the target package and the file the moved
// code is added to. A package with no file yet gets one that exists only in
// memory (created reports it); the plan then carries its creation, and
// planning changes neither the disk nor the workspace.
func (op *MoveSymbolOperation) getOrCreateTargetFile(ws *types.Workspace, targetPackagePath string) (targetPackage *types.Package, targetFile *types.File, created bool, err error) {
	targetPackage, exists := ws.Packages[targetPackagePath]
	if !exists {
		if !op.Request.CreateTarget {
			return nil, nil, false, &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: fmt.Sprintf("target package does not exist: %s", targetPackagePath),
			}
		}
		targetPackage = &types.Package{
			Path:  targetPackagePath,
			Name:  lastPathComponent(targetPackagePath),
			Files: make(map[string]*types.File),
		}
	}

	for _, name := range slices.Sorted(maps.Keys(targetPackage.Files)) {
		f := targetPackage.Files[name]
		if f.AST != nil && !f.Generated && analysis.IncludedInEveryConfig(targetPackage.Dir, filepath.Base(f.Path), f.AST) {
			return targetPackage, f, false, nil
		}
	}

	// A package that does not exist yet was passed through ResolvePackagePath
	// untouched. It is resolved as a path so the file lands inside the
	// project, and the module import path form ("mod/pkg/thing") does not
	// name a directory called after the module.
	absPackageDir := targetPackage.Dir
	if exists {
		if absPackageDir == "" {
			absPackageDir = targetPackage.Path
		}
	} else if absPackageDir, err = types.ResolveNewPackagePath(ws, targetPackagePath); err != nil {
		return nil, nil, false, err
	}
	filename := targetPackage.Name + ".go"
	fullPath := filepath.Join(absPackageDir, filename)
	if _, err := os.Stat(fullPath); err == nil {
		return nil, nil, false, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("target file %s exists but is not part of package %s under every build configuration", fullPath, targetPackagePath),
		}
	}

	initialContent := fmt.Sprintf("package %s\n", targetPackage.Name)
	fset := ws.FileSet
	if fset == nil {
		fset = token.NewFileSet()
	}
	astFile, err := parser.ParseFile(fset, fullPath, initialContent, parser.ParseComments)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to parse initial file content: %v", err)
	}
	if !exists {
		targetPackage = &types.Package{
			Path:  absPackageDir,
			Dir:   absPackageDir,
			Name:  targetPackage.Name,
			Files: make(map[string]*types.File),
		}
	}
	targetFile = &types.File{
		Path:            fullPath,
		Package:         targetPackage,
		AST:             astFile,
		OriginalContent: []byte(initialContent),
	}
	return targetPackage, targetFile, true, nil
}

// Helper methods for RenameSymbolOperation

func (op *RenameSymbolOperation) checkNameConflict(ws *types.Workspace, symbol *types.Symbol, newName string) error {
	pkg := findPackageForFile(ws, symbol.File)
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}

	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := resolver.ResolveSymbol(pkg, newName); err == nil {
		return &types.RefactorError{
			Type:    types.NameConflict,
			Message: fmt.Sprintf("name conflict: symbol %s already exists in package %s", newName, pkg.Path),
			File:    symbol.File,
		}
	}

	return nil
}

func (op *RenameSymbolOperation) generateDefinitionRenameChange(symbol *types.Symbol, newName string) types.Change {
	return definitionRenameChange(symbol, newName)
}

// definitionRenameChange builds the byte-offset splice that rewrites a symbol's
// own declaration ident from symbol.Name to newName. It resolves the offset from
// the symbol's line/column via calculateByteOffset so callers never duplicate
// that logic. Shared by RenameSymbolOperation and RenameFieldOperation.
func definitionRenameChange(symbol *types.Symbol, newName string) types.Change {
	start := calculateByteOffset(symbol.File, symbol.Line, symbol.Column)
	return types.Change{
		File:        symbol.File,
		Start:       start,
		End:         start + len(symbol.Name),
		OldText:     symbol.Name,
		NewText:     newName,
		Description: fmt.Sprintf("Rename definition of %s to %s", symbol.Name, newName),
	}
}

func (op *RenameSymbolOperation) generateReferenceRenameChange(ref *types.Reference, newName string) types.Change {
	start := calculateByteOffset(ref.File, ref.Line, ref.Column)
	return types.Change{
		File:        ref.File,
		Start:       start,
		End:         start + len(ref.Symbol.Name),
		OldText:     ref.Symbol.Name,
		NewText:     newName,
		Description: fmt.Sprintf("Rename reference to %s", newName),
	}
}

// Utility functions

func findFileContainingSymbol(pkg *types.Package, symbol *types.Symbol) *types.File {
	for _, file := range pkg.Files {
		if file.Path == symbol.File {
			return file
		}
	}
	return nil
}

// findPackageForFile locates the package containing filePath, searching both
// Files and TestFiles.
//
// Package keeps test files in a separate map (see findParsedFile above), so
// scanning only Files silently misses every _test.go. That miss cascades
// into generateImportChanges treating a _test.go reference site as if it
// belonged to no package at all, which skips adding the target package's
// import there — references get rewritten to a qualified name but the
// import that qualifier depends on is never added, leaving the file
// non-compiling.
func findPackageForFile(ws *types.Workspace, filePath string) *types.Package {
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if file.Path == filePath {
				return pkg
			}
		}
		for _, file := range pkg.TestFiles {
			if file.Path == filePath {
				return pkg
			}
		}
	}
	return nil
}

func isValidGoIdentifier(name string) bool {
	if len(name) == 0 {
		return false
	}

	// First character must be letter or underscore
	first := rune(name[0])
	if !unicode.IsLetter(first) && first != '_' {
		return false
	}

	// Remaining characters must be letters, digits, or underscores
	for _, r := range name[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}

	return true
}

func lastPathComponent(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

func contains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}

// findParsedFile locates a parsed file by absolute path, searching both Files
// and TestFiles.
//
// Package keeps test files in a separate map, so scanning only Files silently
// skips every _test.go. For import insertion that omission is invisible to
// `go build`, which never compiles test files: the result is a _test.go
// referencing a package that was never imported, caught only by `go vet` or
// `go test`.
func findParsedFile(ws *types.Workspace, filePath string) *types.File {
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if file.Path == filePath && file.AST != nil {
				return file
			}
		}
		for _, file := range pkg.TestFiles {
			if file.Path == filePath && file.AST != nil {
				return file
			}
		}
	}
	return nil
}

// generateAddImportChange generates a change to add a new import (under alias
// when non-empty), handling both single-line and multi-line import formats
func generateAddImportChange(ws *types.Workspace, filePath, alias, importPath string) *types.Change {
	file := findParsedFile(ws, filePath)
	if file == nil {
		return nil
	}

	// Check if we have imports and if they're in single-line or multi-line format
	if len(file.AST.Imports) > 0 {
		// Read file content to check import format
		content, err := os.ReadFile(filePath)
		if err != nil {
			return nil
		}

		// Check if imports are in grouped format (with parentheses) or single-line format
		isSingleLine := isSingleLineImport(file.AST, content)

		if isSingleLine {
			// Convert single-line import to multi-line format
			return convertSingleLineImportToMultiLine(file.AST, content, filePath, alias, importPath)
		}

		// Add to existing multi-line import block
		lastImport := file.AST.Imports[len(file.AST.Imports)-1]
		if ws.FileSet != nil {
			pos := ws.FileSet.Position(lastImport.End())
			byteOffset := calculateByteOffset(filePath, pos.Line, pos.Column)

			// Find the next newline after the import
			for i := byteOffset; i < len(content); i++ {
				if content[i] == '\n' {
					return &types.Change{
						File:        filePath,
						Start:       i + 1,
						End:         i + 1,
						OldText:     "",
						NewText:     "\t" + importSpecText(alias, importPath) + "\n",
						Description: fmt.Sprintf("Add import for %s", importPath),
					}
				}
			}
		}
		return nil
	}

	if file.AST.Name != nil && ws.FileSet != nil {
		// No imports exist, add new import block after package declaration
		pos := ws.FileSet.Position(file.AST.Name.End())
		byteOffset := calculateByteOffset(filePath, pos.Line, pos.Column)

		content, err := os.ReadFile(filePath)
		if err != nil {
			return nil
		}

		// Find the next newline after package declaration
		for i := byteOffset; i < len(content); i++ {
			if content[i] == '\n' {
				return &types.Change{
					File:        filePath,
					Start:       i + 1,
					End:         i + 1,
					OldText:     "",
					NewText:     "\nimport " + importSpecText(alias, importPath) + "\n",
					Description: fmt.Sprintf("Add import for %s", importPath),
				}
			}
		}
	}
	return nil
}

// isSingleLineImport checks if imports are in single-line format (no parentheses)
func isSingleLineImport(astFile *ast.File, content []byte) bool {
	if len(astFile.Imports) == 0 {
		return false
	}

	// Find the first import in the content
	lines := strings.SplitSeq(string(content), "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") {
			// Check if it has an opening parenthesis (multi-line format)
			return !strings.Contains(trimmed, "(")
		}
	}
	return false
}

// convertSingleLineImportToMultiLine converts a single-line import to multi-line format
// and adds the new import
func convertSingleLineImportToMultiLine(astFile *ast.File, content []byte, filePath, newAlias, newImportPath string) *types.Change {
	lines := strings.Split(string(content), "\n")

	// Find the single-line import statement
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") && !strings.Contains(trimmed, "(") {
			// Extract the import path from the line
			// Format: import "path" or import alias "path"
			parts := strings.Fields(trimmed)
			if len(parts) < 2 {
				continue
			}

			// Calculate byte position of the import line
			startByte := 0
			for j := range i {
				startByte += len(lines[j]) + 1 // +1 for newline
			}
			endByte := startByte + len(lines[i]) + 1 // +1 for newline

			// Get the import path (last part, with quotes)
			oldImportPath := parts[len(parts)-1]

			// Build new multi-line import block
			var newImport string
			if len(parts) == 3 {
				// Has alias: import alias "path"
				alias := parts[1]
				newImport = fmt.Sprintf("import (\n\t%s %s\n\t%s\n)", alias, oldImportPath, importSpecText(newAlias, newImportPath))
			} else {
				// No alias: import "path"
				newImport = fmt.Sprintf("import (\n\t%s\n\t%s\n)", oldImportPath, importSpecText(newAlias, newImportPath))
			}

			return &types.Change{
				File:        filePath,
				Start:       startByte,
				End:         endByte,
				OldText:     lines[i] + "\n",
				NewText:     newImport + "\n",
				Description: fmt.Sprintf("Convert single-line import to multi-line and add %s", newImportPath),
			}
		}
	}

	return nil
}

// extractSymbolSource extracts the source code of a symbol from a file, with
// edits (in the file's offsets) applied.
func (op *MoveSymbolOperation) extractSymbolSource(file *types.File, symbol *types.Symbol, edits []textEdit) (string, error) {
	if file.AST == nil {
		return "", fmt.Errorf("AST not loaded for file %s", file.Path)
	}
	start, end, keyword, ok := declLines(file, symbol.Position)
	if !ok {
		return "", fmt.Errorf("symbol %s not found in file %s", symbol.Name, file.Path)
	}
	sourceCode := copyLines(file.OriginalContent, start, end, edits)
	if keyword != "" {
		// The keyword goes on the spec's own line, below any doc comment.
		lines := strings.Split(sourceCode, "\n")
		spec := sourceLine(file, symbol.Position) - start
		lines[spec] = keyword + " " + strings.TrimLeft(lines[spec], " \t")
		sourceCode = strings.Join(lines, "\n")
	}
	return sourceCode, nil
}

// copyLines returns the 0-based lines [start, end) of content without the final
// newline, with edits applied as in copyWithEdits.
func copyLines(content []byte, start, end int, edits []textEdit) string {
	lineOffset := func(line int) int {
		offset := 0
		for range line {
			i := bytes.IndexByte(content[offset:], '\n')
			if i < 0 {
				return len(content) + 1
			}
			offset += i + 1
		}
		return offset
	}
	return copyWithEdits(content, lineOffset(start), min(lineOffset(end)-1, len(content)), edits)
}

// textEdit replaces content[start:end] with text; start == end inserts.
type textEdit struct {
	start, end int
	text       string
}

// copyWithEdits returns content[start:end] with the edits that lie within it
// applied.
func copyWithEdits(content []byte, start, end int, edits []textEdit) string {
	var b strings.Builder
	last := start
	for _, e := range slices.SortedFunc(slices.Values(edits), func(a, b textEdit) int { return a.start - b.start }) {
		if e.start < last || e.end > end {
			continue
		}
		b.Write(content[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.Write(content[last:end])
	return b.String()
}

// packagePathToImportPath converts an absolute package path to a Go import path
func packagePathToImportPath(ws *types.Workspace, packagePath string) string {
	// If we have module information, create module-relative import path
	if ws.Module != nil && ws.Module.Path != "" {
		// Remove the workspace root prefix to get relative path
		relPath, err := filepath.Rel(ws.RootPath, packagePath)
		if err != nil {
			// Rel fails when packagePath is not an absolute path under
			// ws.RootPath — i.e. when the caller passed something that is not a
			// workspace filesystem path at all. Returning the input verbatim
			// here used to make that mistake invisible: hand it a relative
			// "pkg/wms" and it answers "pkg/wms", hand it the import path
			// "mod/pkg/wms" and it answers with a correct-looking import path
			// derived from nothing. Callers guard on "" and can report the
			// failure instead of writing a fabricated import path into source.
			return ""
		}

		// If this is the root package, return just the module path
		if relPath == "." {
			return ws.Module.Path
		}

		// Combine module path with relative path (convert to forward slashes for Go imports)
		importPath := ws.Module.Path + "/" + filepath.ToSlash(relPath)
		return importPath
	}

	// Fallback: use the package path as-is (not ideal, but works for simple cases)
	return packagePath
}

// calculateByteOffset calculates the byte offset in a file from line and column numbers
func calculateByteOffset(filePath string, line, column int) int {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return 0 // Return 0 if we can't read the file
	}

	if line <= 0 || column <= 0 {
		return 0
	}

	lines := strings.Split(string(content), "\n")
	if line > len(lines) {
		return len(content) // Return end of file if line is beyond file
	}

	// Calculate offset by summing up previous lines plus current column
	offset := 0
	for i := 0; i < line-1; i++ {
		offset += len(lines[i]) + 1 // +1 for the newline character
	}

	// Add column offset (subtract 1 since columns are 1-based)
	if column-1 < len(lines[line-1]) {
		offset += column - 1
	} else {
		offset += len(lines[line-1]) // If column is beyond line, go to end of line
	}

	return offset
}

// Helper methods for RenamePackageOperation

func (op *RenamePackageOperation) generatePackageDeclarationChange(file *types.File, oldName, newName string) (*types.Change, error) {
	content := string(file.OriginalContent)
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 && parts[1] == oldName {
				// Calculate byte position of the package name
				startByte := 0
				for j := range i {
					startByte += len(lines[j]) + 1 // +1 for newline
				}

				// Find the start of the package name within the line
				packageKeywordPos := strings.Index(line, "package")
				if packageKeywordPos == -1 {
					continue
				}

				nameStartInLine := packageKeywordPos + len("package")
				// Skip whitespace to find the actual name
				for nameStartInLine < len(line) && (line[nameStartInLine] == ' ' || line[nameStartInLine] == '\t') {
					nameStartInLine++
				}

				startByte += nameStartInLine
				endByte := startByte + len(oldName)

				return &types.Change{
					File:        file.Path,
					Start:       startByte,
					End:         endByte,
					OldText:     oldName,
					NewText:     newName,
					Description: fmt.Sprintf("Update package declaration from %s to %s", oldName, newName),
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("package declaration not found in file %s", file.Path)
}

func (op *RenamePackageOperation) generateImportUpdates(ws *types.Workspace, packagePath, newPackageName string) ([]types.Change, []string, error) {
	var changes []types.Change
	var affectedFiles []string

	// Get the import path for this package
	importPath := packagePathToImportPath(ws, packagePath)

	// Find all files that import this package
	for _, pkg := range ws.Packages {
		if pkg.Path == packagePath {
			continue // Skip the package itself
		}

		for _, file := range pkg.Files {
			hasImport, fileChanges, err := op.generateFileImportUpdate(file, importPath, newPackageName)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to update imports in %s: %v", file.Path, err)
			}

			if hasImport && len(fileChanges) > 0 {
				changes = append(changes, fileChanges...)
				affectedFiles = append(affectedFiles, file.Path)
			}
		}
	}

	return changes, affectedFiles, nil
}

func (op *RenamePackageOperation) generateFileImportUpdate(file *types.File, importPath, newPackageName string) (bool, []types.Change, error) {
	content := string(file.OriginalContent)

	// Check if this file imports the target package
	if !strings.Contains(content, `"`+importPath+`"`) {
		return false, nil, nil
	}

	// For files that use the old package name, we need to update qualified references
	// This is a simplified implementation - a full implementation would parse the AST
	lines := strings.Split(content, "\n")

	// Find all qualified references to the old package name and update them
	var changes []types.Change
	oldPackageName := lastPathComponent(importPath) // Get default package name from import path

	if oldPackageName == newPackageName {
		// If the package name doesn't actually change (just the internal declaration), no updates needed
		return true, nil, nil
	}

	for i, line := range lines {
		if strings.Contains(line, oldPackageName+".") {
			// Replace qualified references
			newLine := strings.ReplaceAll(line, oldPackageName+".", newPackageName+".")
			if newLine != line {
				// Calculate byte positions
				startByte := 0
				for j := range i {
					startByte += len(lines[j]) + 1
				}
				endByte := startByte + len(line)

				change := types.Change{
					File:        file.Path,
					Start:       startByte,
					End:         endByte,
					OldText:     line,
					NewText:     newLine,
					Description: fmt.Sprintf("Update qualified references from %s to %s", oldPackageName, newPackageName),
				}
				changes = append(changes, change)
			}
		}
	}

	if len(changes) > 0 {
		return true, changes, nil
	}

	return true, nil, nil
}

// RenameMethodOperation implements renaming methods on specific types (structs or interfaces)
type RenameMethodOperation struct {
	Request types.RenameMethodRequest
}

func (op *RenameMethodOperation) Type() types.OperationType {
	return types.RenameMethodOperation
}

func (op *RenameMethodOperation) Validate(ws *types.Workspace) error {
	typeSymbol, _, err := op.target(ws)
	if err != nil {
		return err
	}
	return op.validateNames(ws, typeSymbol)
}

// target finds the type (struct or interface) and the method on it.
func (op *RenameMethodOperation) target(ws *types.Workspace) (typeSymbol, methodSymbol *types.Symbol, err error) {
	typeSymbol, err = op.findType(ws)
	if err != nil {
		return nil, nil, err
	}
	methodSymbol, err = op.findMethodOnType(ws, typeSymbol)
	if err != nil {
		return nil, nil, err
	}
	return typeSymbol, methodSymbol, nil
}

// validateNames checks that the new method name is valid and free on the type.
func (op *RenameMethodOperation) validateNames(ws *types.Workspace, typeSymbol *types.Symbol) error {
	// Check that new method name is valid
	if !isValidGoIdentifier(op.Request.NewMethodName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.Request.NewMethodName),
		}
	}

	// Check for name conflicts
	if op.Request.MethodName == op.Request.NewMethodName {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "new method name cannot be the same as the current name",
		}
	}

	// Check if new method name already exists on the type
	return op.checkMethodNameConflict(ws, typeSymbol)
}

func (op *RenameMethodOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	var changes []types.Change
	var affectedFiles []string

	// Find the type symbol
	typeSymbol, err := op.findType(ws)
	if err != nil {
		return nil, err
	}

	// Find the method symbol
	methodSymbol, err := op.findMethodOnType(ws, typeSymbol)
	if err != nil {
		return nil, err
	}

	// Generate change for method definition
	definitionChange, err := op.generateMethodDefinitionChange(ws, typeSymbol, methodSymbol)
	if err != nil {
		return nil, err
	}
	if definitionChange != nil {
		changes = append(changes, *definitionChange)
		if !contains(affectedFiles, definitionChange.File) {
			affectedFiles = append(affectedFiles, definitionChange.File)
		}
	}

	// Generate changes for all method calls/references
	referenceChanges, err := op.generateMethodReferenceChanges(ws, typeSymbol, methodSymbol)
	if err != nil {
		return nil, err
	}
	changes = append(changes, referenceChanges...)
	for _, change := range referenceChanges {
		if !contains(affectedFiles, change.File) {
			affectedFiles = append(affectedFiles, change.File)
		}
	}

	// If this is an interface and UpdateImplementations is true,
	// also rename the method on all implementations
	if typeSymbol.Kind == types.InterfaceSymbol && op.Request.UpdateImplementations {
		implChanges, err := op.generateImplementationChanges(ws)
		if err != nil {
			return nil, err
		}
		changes = append(changes, implChanges...)
		for _, change := range implChanges {
			if !contains(affectedFiles, change.File) {
				affectedFiles = append(affectedFiles, change.File)
			}
		}
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: affectedFiles,
		Reversible:    true,
	}, nil
}

func (op *RenameMethodOperation) Description() string {
	if op.Request.UpdateImplementations {
		return fmt.Sprintf("Rename method %s.%s to %s (including implementations)", op.Request.TypeName, op.Request.MethodName, op.Request.NewMethodName)
	}
	return fmt.Sprintf("Rename method %s.%s to %s", op.Request.TypeName, op.Request.MethodName, op.Request.NewMethodName)
}

// Helper methods for RenameMethodOperation

func (op *RenameMethodOperation) findType(ws *types.Workspace) (*types.Symbol, error) {
	// Search for the type in the specified package or workspace-wide
	var targetPackages []*types.Package

	if op.Request.PackagePath != "" {
		if pkg, exists := ws.Packages[op.Request.PackagePath]; exists {
			targetPackages = []*types.Package{pkg}
		} else {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package %s not found", op.Request.PackagePath),
			}
		}
	} else {
		// Search all packages
		for _, pkg := range ws.Packages {
			targetPackages = append(targetPackages, pkg)
		}
	}

	for _, pkg := range targetPackages {
		if pkg.Symbols == nil {
			continue
		}

		// Look for the type in the package
		if symbol, exists := pkg.Symbols.Types[op.Request.TypeName]; exists {
			return symbol, nil
		}
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("type %s not found", op.Request.TypeName),
	}
}

// resolveSymbolPackage looks up a symbol's package, handling the mismatch
// between import paths (stored in Symbol.Package) and filesystem paths
// (used as keys in ws.Packages).
func resolveSymbolPackage(ws *types.Workspace, sym *types.Symbol) *types.Package {
	if pkg := ws.Packages[sym.Package]; pkg != nil {
		return pkg
	}
	if fsPath := ws.ImportToPath[sym.Package]; fsPath != "" {
		return ws.Packages[fsPath]
	}
	return nil
}

func (op *RenameMethodOperation) findMethodOnType(ws *types.Workspace, typeSymbol *types.Symbol) (*types.Symbol, error) {
	pkg := resolveSymbolPackage(ws, typeSymbol)
	if pkg == nil || pkg.Symbols == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("package %s not found or has no symbols", typeSymbol.Package),
		}
	}

	// Look for methods on this type
	if methods, exists := pkg.Symbols.Methods[typeSymbol.Name]; exists {
		for _, method := range methods {
			if method.Name == op.Request.MethodName {
				return method, nil
			}
		}
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("method %s not found on type %s", op.Request.MethodName, op.Request.TypeName),
	}
}

func (op *RenameMethodOperation) checkMethodNameConflict(ws *types.Workspace, typeSymbol *types.Symbol) error {
	pkg := resolveSymbolPackage(ws, typeSymbol)
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}

	// Check if new method name already exists on this type
	if methods, exists := pkg.Symbols.Methods[typeSymbol.Name]; exists {
		for _, method := range methods {
			if method.Name == op.Request.NewMethodName {
				return &types.RefactorError{
					Type:    types.NameConflict,
					Message: fmt.Sprintf("method %s already exists on type %s", op.Request.NewMethodName, typeSymbol.Name),
				}
			}
		}
	}

	return nil
}

func (op *RenameMethodOperation) generateMethodDefinitionChange(ws *types.Workspace, ownerSymbol *types.Symbol, methodSymbol *types.Symbol) (*types.Change, error) {
	pkg := resolveSymbolPackage(ws, ownerSymbol)
	if pkg == nil {
		return nil, fmt.Errorf("package %s not found", ownerSymbol.Package)
	}

	var methodFile *types.File
	for _, file := range allFilesOf(pkg) {
		if file.Path == methodSymbol.File {
			methodFile = file
			break
		}
	}

	if methodFile == nil {
		return nil, fmt.Errorf("file %s not found", methodSymbol.File)
	}

	// Calculate the byte position for the method name change
	startByte := ws.FileSet.Position(methodSymbol.Position).Offset
	endByte := startByte + len(op.Request.MethodName)

	return &types.Change{
		File:        methodFile.Path,
		Start:       startByte,
		End:         endByte,
		OldText:     op.Request.MethodName,
		NewText:     op.Request.NewMethodName,
		Description: fmt.Sprintf("Rename method definition %s to %s", op.Request.MethodName, op.Request.NewMethodName),
	}, nil
}

func (op *RenameMethodOperation) generateMethodReferenceChanges(ws *types.Workspace, typeSymbol *types.Symbol, methodSymbol *types.Symbol) ([]types.Change, error) {
	var changes []types.Change

	// Find all method calls and references across the workspace
	for _, pkg := range ws.Packages {
		for _, file := range allFilesOf(pkg) {
			if file.AST == nil {
				continue
			}

			// Look for method calls on this type
			ast.Inspect(file.AST, func(n ast.Node) bool {
				if callExpr, ok := n.(*ast.CallExpr); ok {
					if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
						if selExpr.Sel.Name == op.Request.MethodName {
							// This is a potential method call - we need to verify it's on our type
							// For now, we'll create the change (a more sophisticated implementation
							// would verify the receiver type)
							pos := selExpr.Sel.Pos()
							startByte := ws.FileSet.Position(pos).Offset
							endByte := startByte + len(op.Request.MethodName)

							change := types.Change{
								File:        file.Path,
								Start:       startByte,
								End:         endByte,
								OldText:     op.Request.MethodName,
								NewText:     op.Request.NewMethodName,
								Description: fmt.Sprintf("Rename method call %s to %s", op.Request.MethodName, op.Request.NewMethodName),
							}
							changes = append(changes, change)
						}
					}
				}
				return true
			})
		}
	}

	return changes, nil
}

func (op *RenameMethodOperation) generateImplementationChanges(ws *types.Workspace) ([]types.Change, error) {
	var changes []types.Change
	for _, method := range op.implementationMethods(ws) {
		change, err := op.generateMethodDefinitionChange(ws, method, method)
		if err != nil {
			return nil, err
		}
		if change != nil {
			changes = append(changes, *change)
		}
	}
	return changes, nil
}

// implementationMethods returns the methods named like the request's on the
// workspace's struct types.
func (op *RenameMethodOperation) implementationMethods(ws *types.Workspace) []*types.Symbol {
	var methods []*types.Symbol
	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}
		for typeName, typeSymbol := range pkg.Symbols.Types {
			if typeSymbol.Kind != types.TypeSymbol {
				continue
			}
			for _, method := range pkg.Symbols.Methods[typeName] {
				if method.Name == op.Request.MethodName {
					methods = append(methods, method)
				}
			}
		}
	}
	return methods
}
