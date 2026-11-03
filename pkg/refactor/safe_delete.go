package refactor

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"maps"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// SafeDeleteOperation implements safe deletion of symbols with usage verification
type SafeDeleteOperation struct {
	SymbolName string
	SourceFile string
	Scope      pkgtypes.RenameScope // PackageScope or WorkspaceScope
	Force      bool                 // If true, delete even if references exist

	// CachedIndex is used instead of building a reference index when set.
	CachedIndex *analysis.ReferenceIndex

	blocking     []pkgtypes.Reference
	blockingDone bool
}

// dropAnalysis forgets the reference index the operation planned from.
func (op *SafeDeleteOperation) dropAnalysis() { op.CachedIndex = nil }

// deletionTarget is one declaration removed by the operation: a symbol asked
// for or, for a type, one of its methods.
type deletionTarget struct {
	file   *pkgtypes.File
	symbol *pkgtypes.Symbol
	root   *pkgtypes.Symbol // the symbol asked for that brings this one along
	node   ast.Node         // declaration or spec to cut
	doc    *ast.CommentGroup
	field  *ast.Field // set when the target is a struct field; node is the field
}

func (op *SafeDeleteOperation) Type() pkgtypes.OperationType {
	return pkgtypes.InlineOperation // Reuse inline operation type for now
}

func (op *SafeDeleteOperation) Validate(ws *pkgtypes.Workspace) error {
	err := op.validateRequest()
	if err != nil {
		return err
	}

	// Confirm the source file and symbol can be resolved before checking references
	_, _, _, _, err = op.resolveSourceAndSymbol(ws)
	if err != nil {
		return err
	}

	// Check if symbol is safe to delete (no references unless forced)
	if !op.Force {
		actualReferences, err := op.BlockingReferences(ws)
		if err != nil {
			return err
		}

		if len(actualReferences) > 0 {
			return op.refusal(len(actualReferences))
		}
	}

	return nil
}

func (op *SafeDeleteOperation) validateRequest() error {
	if op.SymbolName == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "symbol name cannot be empty",
		}
	}

	if op.SourceFile == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}

	return nil
}

func (op *SafeDeleteOperation) refusal(references int) *pkgtypes.RefactorError {
	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf("cannot safely delete %s: found %d references (use --force to delete anyway)",
			op.SymbolName, references),
	}
}

// BlockingReferences returns the references that would block a safe delete of
// op's symbol, regardless of op.Force. It resolves the source file and
// symbol, finds all references, and applies the same scope and
// definition-exclusion filtering as Validate, but never deletes anything and
// never consults op.Force — it always reports what would block the delete.
// The returned slice is empty (not nil) when nothing blocks.
func (op *SafeDeleteOperation) BlockingReferences(ws *pkgtypes.Workspace) ([]pkgtypes.Reference, error) {
	if op.blockingDone {
		return op.blocking, nil
	}

	targets, err := op.targetsIn(ws, nil)
	if err != nil {
		return nil, err
	}

	blocking, err := op.blockingAmong(ws, targets)
	if err != nil {
		return nil, err
	}

	op.blocking, op.blockingDone = blocking, true

	return blocking, nil
}

// targetsIn returns the declarations to delete in ws.
func (op *SafeDeleteOperation) targetsIn(ws *pkgtypes.Workspace, linked pkgtypes.SymbolKeySet) ([]deletionTarget, error) {
	roots, notFound := op.rootsIn(ws, linked)
	if len(roots) == 0 {
		return nil, notFound
	}

	return op.targetsOf(ws, roots)
}

// rootsIn returns the symbols ws is asked to delete: the requested one and
// those the linked keys name. When there are none it also returns the
// requested symbol's lookup error.
func (op *SafeDeleteOperation) rootsIn(ws *pkgtypes.Workspace, linked pkgtypes.SymbolKeySet) ([]*pkgtypes.Symbol, error) {
	var roots []*pkgtypes.Symbol

	_, _, _, symbol, notFound := op.resolveSourceAndSymbol(ws)
	if notFound == nil {
		roots = append(roots, symbol)
	}

	return withLinkedSymbols(ws, roots, linked), notFound
}

// targetsOf returns the declarations of roots and of what goes with them,
// each once.
func (op *SafeDeleteOperation) targetsOf(ws *pkgtypes.Workspace, roots []*pkgtypes.Symbol) ([]deletionTarget, error) {
	var targets []deletionTarget
	seen := make(map[ast.Node]bool)

	for _, root := range roots {
		found, err := op.deletionTargets(resolveSymbolPackage(ws, root), root)
		if err != nil {
			return nil, fmt.Errorf("targets of: %w", err)
		}

		for _, t := range found {
			if !seen[t.node] {
				seen[t.node] = true
				targets = append(targets, t)
			}
		}
	}

	return targets, nil
}

// blockingAmong returns the references to targets from outside the
// declarations being deleted. The result is empty, not nil, when there are none.
func (op *SafeDeleteOperation) blockingAmong(ws *pkgtypes.Workspace, targets []deletionTarget) ([]pkgtypes.Reference, error) {
	resolver := quietResolver(ws)

	idx := op.CachedIndex
	if idx == nil {
		idx = resolver.BuildReferenceIndex()
	}

	blocking := make([]pkgtypes.Reference, 0)

	for _, t := range targets {
		references, err := resolver.FindReferencesIndexed(t.symbol, idx)
		if err != nil {
			return nil, &pkgtypes.RefactorError{
				Type:    pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("failed to find references: %v", err),
			}
		}

		scoped := op.filterReferencesByScope(references, resolveSymbolPackage(ws, t.root))
		for _, ref := range op.filterOutDefinition(scoped, t.symbol) {
			if !insideTargets(ws, targets, ref) {
				blocking = append(blocking, *ref)
			}
		}
	}

	return blocking, nil
}

// insideTargets reports whether ref lies within a declaration this operation
// deletes, so uses inside doomed code (a method's receiver) do not block.
func insideTargets(ws *pkgtypes.Workspace, targets []deletionTarget, ref *pkgtypes.Reference) bool {
	for _, t := range targets {
		if t.file.Path != ref.File {
			continue
		}

		start, end := nodeOffsets(ws, t.node)
		if ref.Offset >= start && ref.Offset < end {
			return true
		}
	}

	return false
}

func nodeOffsets(ws *pkgtypes.Workspace, n ast.Node) (int, int) {
	return ws.FileSet.Position(n.Pos()).Offset, ws.FileSet.Position(n.End()).Offset
}

// deletionTargets returns the declaration of symbol plus, when symbol is a
// type, the declarations of its methods.
func (op *SafeDeleteOperation) deletionTargets(pkg *pkgtypes.Package, symbol *pkgtypes.Symbol) ([]deletionTarget, error) {
	symbols := []*pkgtypes.Symbol{symbol}
	if symbol.Kind == pkgtypes.TypeSymbol && pkg != nil && pkg.Symbols != nil {
		for _, m := range pkg.Symbols.Methods[symbol.Name] {
			if m.Kind == pkgtypes.MethodSymbol {
				symbols = append(symbols, m)
			}
		}
	}

	targets := make([]deletionTarget, 0, len(symbols))
	for _, sym := range symbols {
		var file *pkgtypes.File
		if pkg != nil {
			file = findFileByPath(pkg, sym.File)
		}

		if file == nil {
			return nil, &pkgtypes.RefactorError{
				Type:    pkgtypes.FileSystemError,
				Message: fmt.Sprintf("file %s of %s not found in package", sym.File, sym.Name),
			}
		}
		var node ast.Node
		var doc *ast.CommentGroup

		var field *ast.Field
		if sym.Kind == pkgtypes.StructFieldSymbol {
			field = findFieldDeclaration(file, sym)
			if field != nil {
				node, doc = field, field.Doc
			}
		} else {
			node, doc = op.findDeclaration(file, sym)
		}

		if node == nil {
			return nil, &pkgtypes.RefactorError{
				Type:    pkgtypes.SymbolNotFound,
				Message: fmt.Sprintf("declaration for %s not found", sym.Name),
			}
		}

		targets = append(targets, deletionTarget{file: file, symbol: sym, root: symbol, node: node, doc: doc, field: field})
	}

	return targets, nil
}

func findFileByPath(pkg *pkgtypes.Package, path string) *pkgtypes.File {
	for _, group := range []map[string]*pkgtypes.File{pkg.Files, pkg.TestFiles} {
		for _, f := range group {
			if f.Path == path {
				return f
			}
		}
	}

	return nil
}

// resolveSourceAndSymbol locates the file and package named by op.SourceFile
// and resolves op.SymbolName within that package. It is the single place
// Validate, Execute, and BlockingReferences share for this lookup.
func (op *SafeDeleteOperation) resolveSourceAndSymbol(ws *pkgtypes.Workspace) (*pkgtypes.File, *pkgtypes.Package, *analysis.SymbolResolver, *pkgtypes.Symbol, error) {
	sourceFile, sourcePackage, err := ws.FindFile(op.SourceFile)

	resolver := quietResolver(ws)
	if errors.Is(err, pkgtypes.ErrFileNotFound) {
		return nil, nil, resolver, nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("resolve source and symbol: %w", err)
	}

	declared := declaredInFile(sourcePackage, sourceFile, op.SymbolName)

	sym, ok := declared[op.SymbolName]
	if ok {
		return sourceFile, sourcePackage, resolver, sym, nil
	}

	if len(declared) == 1 {
		return sourceFile, sourcePackage, resolver, slices.Collect(maps.Values(declared))[0], nil
	}

	if len(declared) > 1 {
		return nil, nil, resolver, nil, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: fmt.Sprintf("ambiguous symbol %q: %s declares %s; use Type.Method syntax to pick one",
				op.SymbolName, op.SourceFile, strings.Join(slices.Sorted(maps.Keys(declared)), ", ")),
		}
	}

	// Dotted names (Type.Method, Type.Field) are not in declaredInFile.
	symbol, err := resolver.ResolveSymbol(sourcePackage, op.SymbolName)
	if err != nil {
		return nil, nil, resolver, nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("symbol %s not found: %v", op.SymbolName, err),
		}
	}

	if symbol.File != sourceFile.Path {
		return nil, nil, resolver, nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("%s is not declared in %s", op.SymbolName, op.SourceFile),
		}
	}

	return sourceFile, sourcePackage, resolver, symbol, nil
}

// declaredInFile returns the package-level symbols and methods of pkg that file
// declares under the bare name, keyed by how to spell each (Type.Method for a
// method). A dotted name matches none.
func declaredInFile(pkg *pkgtypes.Package, file *pkgtypes.File, name string) map[string]*pkgtypes.Symbol {
	found := map[string]*pkgtypes.Symbol{}
	if pkg.Symbols == nil {
		return found
	}

	for _, byName := range []map[string]*pkgtypes.Symbol{pkg.Symbols.Functions, pkg.Symbols.Types, pkg.Symbols.Variables, pkg.Symbols.Constants} {
		sym := byName[name]
		if sym != nil && sym.File == file.Path {
			found[name] = sym
		}
	}

	for recv, methods := range pkg.Symbols.Methods {
		for _, m := range methods {
			if m.Name == name && m.File == file.Path {
				found[recv+"."+name] = m
			}
		}
	}

	return found
}

func (op *SafeDeleteOperation) Execute(ws *pkgtypes.Workspace) (*pkgtypes.RefactoringPlan, error) {
	targets, err := op.targetsIn(ws, nil)
	if err != nil {
		return nil, err
	}

	var blocking []pkgtypes.Reference
	if op.Force {
		blocking, err = op.BlockingReferences(ws)
		if err != nil {
			return nil, err
		}
	}

	return op.removalPlan(ws, targets, blocking), nil
}

// removalPlan cuts targets out of their files. A forced delete leaves the
// blocking call sites in place, since removing a use safely depends on its
// statement, and reports each as a skipped site.
func (op *SafeDeleteOperation) removalPlan(ws *pkgtypes.Workspace, targets []deletionTarget, blocking []pkgtypes.Reference) *pkgtypes.RefactoringPlan {
	plan := &pkgtypes.RefactoringPlan{
		Changes:       make([]pkgtypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}
	removals := map[*pkgtypes.File][]pkgtypes.Change{}

	for _, t := range targets {
		change := op.generateRemovalChange(ws, t)
		plan.Changes = append(plan.Changes, change)

		removals[t.file] = append(removals[t.file], change)
		if !slices.Contains(plan.AffectedFiles, t.file.Path) {
			plan.AffectedFiles = append(plan.AffectedFiles, t.file.Path)
		}
	}

	for _, t := range targets {
		changes, ok := removals[t.file]
		if !ok {
			continue
		}

		delete(removals, t.file)

		if deletion, ok := emptiedFileDeletion(t.file, changes); ok {
			plan.Changes = append(slices.DeleteFunc(plan.Changes, func(c pkgtypes.Change) bool { return c.File == t.file.Path }), deletion)
			continue
		}

		plan.Changes = append(plan.Changes, removeImportSpecs(ws, t.file, unusedImportsAfterRemoval(ws, t.file, changes))...)
	}

	if op.Force {
		for _, ref := range blocking {
			plan.SkippedSites = append(plan.SkippedSites, pkgtypes.SkippedSite{
				File:    ref.File,
				Line:    ref.Line,
				Reason:  fmt.Sprintf("%s was force-deleted; this call site still references the deleted symbol and must be updated manually", op.SymbolName),
				Snippet: ref.Context,
			})
		}
	}

	plan.Impact = &pkgtypes.ImpactAnalysis{
		AffectedFiles:    plan.AffectedFiles,
		AffectedPackages: op.getAffectedPackages(ws, plan.AffectedFiles),
		PotentialIssues:  op.analyzeImpact(targets[0].root, op.Force),
	}

	return plan
}

func (op *SafeDeleteOperation) Description() string {
	scopeStr := "package"
	if op.Scope == pkgtypes.WorkspaceScope {
		scopeStr = "workspace"
	}

	forceStr := ""
	if op.Force {
		forceStr = " (forced)"
	}

	return fmt.Sprintf("Safe delete %s from %s (scope: %s)%s", op.SymbolName, op.SourceFile, scopeStr, forceStr)
}

// Helper methods

func (op *SafeDeleteOperation) filterReferencesByScope(references []*pkgtypes.Reference, sourcePackage *pkgtypes.Package) []*pkgtypes.Reference {
	if op.Scope == pkgtypes.PackageScope {
		// Only include references from the same package
		var filtered []*pkgtypes.Reference

		for _, ref := range references {
			if findFileByPath(sourcePackage, ref.File) != nil {
				filtered = append(filtered, ref)
			}
		}

		return filtered
	}

	// WorkspaceScope - return all references
	return references
}

func (op *SafeDeleteOperation) filterOutDefinition(references []*pkgtypes.Reference, symbol *pkgtypes.Symbol) []*pkgtypes.Reference {
	var filtered []*pkgtypes.Reference

	for _, ref := range references {
		// Skip references that are at the same position as the symbol definition
		if ref.Position != symbol.Position {
			filtered = append(filtered, ref)
		}
	}

	return filtered
}

// findDeclaration returns the node to cut for symbol and its doc comment. A
// spec inside a grouped declaration is cut on its own; otherwise the whole
// declaration goes.
func (op *SafeDeleteOperation) findDeclaration(file *pkgtypes.File, symbol *pkgtypes.Symbol) (ast.Node, *ast.CommentGroup) {
	if file.AST == nil {
		return nil, nil
	}

	for _, d := range file.AST.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if decl.Name.Pos() == symbol.Position {
				return decl, decl.Doc
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				var doc *ast.CommentGroup
				match := false

				switch s := spec.(type) {
				case *ast.TypeSpec:
					match, doc = s.Name.Pos() == symbol.Position, s.Doc
				case *ast.ValueSpec:
					for _, name := range s.Names {
						match = match || name.Pos() == symbol.Position
					}

					doc = s.Doc
				}

				if !match {
					continue
				}

				if len(decl.Specs) == 1 {
					return decl, decl.Doc
				}

				return spec, doc
			}
		}
	}

	return nil, nil
}

// findFieldDeclaration returns the struct field of a top-level type in file
// whose name identifier is at symbol's position.
func findFieldDeclaration(file *pkgtypes.File, symbol *pkgtypes.Symbol) *ast.Field {
	if file.AST == nil {
		return nil
	}
	var found *ast.Field

	ast.Inspect(file.AST, func(n ast.Node) bool {
		f, ok := n.(*ast.Field)
		if ok {
			for _, name := range f.Names {
				if name.Pos() == symbol.Position {
					found = f
				}
			}
		}

		return found == nil
	})

	return found
}

func (op *SafeDeleteOperation) generateRemovalChange(ws *pkgtypes.Workspace, t deletionTarget) pkgtypes.Change {
	requested := t.root
	var start, end int

	var newText string
	if t.field != nil {
		start, end, newText = fieldRemoval(ws, t)
	} else {
		start, end = calculateRemovalRange(ws, t)
	}

	oldText := ""
	if start >= 0 && end <= len(t.file.OriginalContent) && start <= end {
		oldText = string(t.file.OriginalContent[start:end])
	}

	desc := fmt.Sprintf("Remove declaration of %s", t.symbol.Name)
	if t.symbol != requested {
		desc = fmt.Sprintf("Remove method %s.%s (deleted with type %s)", requested.Name, t.symbol.Name, requested.Name)
	}

	if t.field != nil {
		// "Replace" marks the change as a fragment for the syntax validator.
		desc = fmt.Sprintf("Replace struct text after removing field %s.%s", t.symbol.Parent.Name, t.symbol.Name)
	}

	return pkgtypes.Change{
		File:        t.file.Path,
		Start:       start,
		End:         end,
		OldText:     oldText,
		NewText:     newText,
		Description: desc,
	}
}

// fieldRemoval returns the replacement for deleting one field name: the whole
// line for a single-name field, only the name for `A, B T`. The file is then
// gofmt'ed so struct column alignment is recomputed, and the change is trimmed
// to the span that differs from the original.
func fieldRemoval(ws *pkgtypes.Workspace, t deletionTarget) (int, int, string) {
	content := t.file.OriginalContent
	cutStart, cutEnd := fieldCutRange(ws, t)
	edited := append(append([]byte{}, content[:cutStart]...), content[cutEnd:]...)

	formatted, err := format.Source(edited)
	if err != nil {
		formatted = edited
	}

	prefix := 0
	for prefix < len(content) && prefix < len(formatted) && content[prefix] == formatted[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < len(content)-prefix && suffix < len(formatted)-prefix &&
		content[len(content)-1-suffix] == formatted[len(formatted)-1-suffix] {
		suffix++
	}

	return prefix, len(content) - suffix, string(formatted[prefix : len(formatted)-suffix])
}

func fieldCutRange(ws *pkgtypes.Workspace, t deletionTarget) (int, int) {
	content := t.file.OriginalContent

	f := t.field
	if len(f.Names) > 1 {
		for i, name := range f.Names {
			if name.Pos() != t.symbol.Position {
				continue
			}

			if i+1 < len(f.Names) {
				return ws.FileSet.Position(name.Pos()).Offset, ws.FileSet.Position(f.Names[i+1].Pos()).Offset
			}

			return ws.FileSet.Position(f.Names[i-1].End()).Offset, ws.FileSet.Position(name.End()).Offset
		}
	}

	first := f.Pos()
	if f.Doc != nil {
		first = f.Doc.Pos()
	}

	last := f.End()
	if f.Comment != nil {
		last = f.Comment.End()
	}

	start, end := ws.FileSet.Position(first).Offset, ws.FileSet.Position(last).Offset
	if end < len(content) && content[end] == '\r' {
		end++
	}

	if end < len(content) && content[end] == '\n' {
		end++
	}

	for start > 0 && content[start-1] != '\n' {
		start--
	}

	return start, end
}

// pastTrailingLineComment returns the offset after a // comment that follows
// offset on the same line, or offset when there is none.
func pastTrailingLineComment(content []byte, offset int) int {
	i := offset
	for i < len(content) && (content[i] == ' ' || content[i] == '\t') {
		i++
	}

	if !bytes.HasPrefix(content[i:], []byte("//")) {
		return offset
	}

	for i < len(content) && content[i] != '\n' && content[i] != '\r' {
		i++
	}

	return i
}

// calculateRemovalRange returns the byte range to cut: from the start of the
// line holding the doc comment (or declaration) through trailing newlines.
func calculateRemovalRange(ws *pkgtypes.Workspace, t deletionTarget) (int, int) {
	first := t.node.Pos()
	if t.doc != nil {
		first = t.doc.Pos()
	}

	start := ws.FileSet.Position(first).Offset
	_, end := nodeOffsets(ws, t.node)

	content := t.file.OriginalContent

	end = pastTrailingLineComment(content, end)
	for end < len(content) && (content[end] == '\n' || content[end] == '\r') {
		end++
	}

	for start > 0 && content[start-1] != '\n' {
		start--
	}

	return start, end
}

func (op *SafeDeleteOperation) getAffectedPackages(ws *pkgtypes.Workspace, affectedFiles []string) []string {
	packageMap := make(map[string]bool)

	for _, filePath := range affectedFiles {
		pkg := findPackageForFile(ws, filePath)
		if pkg != nil {
			packageMap[pkg.Path] = true
		}
	}

	packages := make([]string, 0, len(packageMap))
	for pkg := range packageMap {
		packages = append(packages, pkg)
	}

	return packages
}

func (op *SafeDeleteOperation) analyzeImpact(symbol *pkgtypes.Symbol, forced bool) []pkgtypes.Issue {
	var issues []pkgtypes.Issue

	// Check if symbol is exported
	if symbol.Exported {
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueVisibilityError,
			Severity:    pkgtypes.Warning,
			Description: fmt.Sprintf("Deleting exported symbol %s may break external packages", symbol.Name),
		})
	}

	// Check symbol type for specific warnings
	switch symbol.Kind {
	case pkgtypes.TypeSymbol:
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueTypeMismatch,
			Severity:    pkgtypes.Warning,
			Description: "Deleting a type may cause compilation errors in dependent code",
		})
	case pkgtypes.InterfaceSymbol:
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueTypeMismatch,
			Severity:    pkgtypes.Warning,
			Description: "Deleting an interface may break implementations and type assertions",
		})
	}

	// Warn about forced deletion
	if forced {
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueCompilationError,
			Severity:    pkgtypes.Warning,
			Description: "Forced deletion will remove references, likely causing compilation errors",
		})
	}

	return issues
}
