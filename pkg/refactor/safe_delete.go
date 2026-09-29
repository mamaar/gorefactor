package refactor

import (
	"fmt"
	"go/ast"
	"io"
	"log/slog"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// SafeDeleteOperation implements safe deletion of symbols with usage verification
type SafeDeleteOperation struct {
	SymbolName string
	SourceFile string
	Scope      pkgtypes.RenameScope // PackageScope or WorkspaceScope
	Force      bool                 // If true, delete even if references exist

	blocking     []pkgtypes.Reference
	blockingDone bool
}

// deletionTarget is one declaration removed by the operation: the requested
// symbol itself or, for a type, one of its methods.
type deletionTarget struct {
	file   *pkgtypes.File
	symbol *pkgtypes.Symbol
	node   ast.Node // declaration or spec to cut
	doc    *ast.CommentGroup
}

func (op *SafeDeleteOperation) Type() pkgtypes.OperationType {
	return pkgtypes.InlineOperation // Reuse inline operation type for now
}

func (op *SafeDeleteOperation) Validate(ws *pkgtypes.Workspace) error {
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

	// Confirm the source file and symbol can be resolved before checking references
	if _, _, _, _, err := op.resolveSourceAndSymbol(ws); err != nil {
		return err
	}

	// Check if symbol is safe to delete (no references unless forced)
	if !op.Force {
		actualReferences, err := op.BlockingReferences(ws)
		if err != nil {
			return err
		}

		if len(actualReferences) > 0 {
			return &pkgtypes.RefactorError{
				Type: pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("cannot safely delete %s: found %d references (use --force to delete anyway)",
					op.SymbolName, len(actualReferences)),
			}
		}
	}

	return nil
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
	_, sourcePackage, resolver, symbol, err := op.resolveSourceAndSymbol(ws)
	if err != nil {
		return nil, err
	}
	targets, err := op.deletionTargets(ws, sourcePackage, symbol)
	if err != nil {
		return nil, err
	}

	idx := resolver.BuildReferenceIndex()
	blocking := make([]pkgtypes.Reference, 0)
	for _, t := range targets {
		references, err := resolver.FindReferencesIndexed(t.symbol, idx)
		if err != nil {
			return nil, &pkgtypes.RefactorError{
				Type:    pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("failed to find references: %v", err),
			}
		}
		scoped := op.filterReferencesByScope(references, sourcePackage, ws)
		for _, ref := range op.filterOutDefinition(scoped, t.symbol) {
			if !insideTargets(ws, targets, ref) {
				blocking = append(blocking, *ref)
			}
		}
	}

	op.blocking, op.blockingDone = blocking, true
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
func (op *SafeDeleteOperation) deletionTargets(ws *pkgtypes.Workspace, pkg *pkgtypes.Package, symbol *pkgtypes.Symbol) ([]deletionTarget, error) {
	symbols := []*pkgtypes.Symbol{symbol}
	if symbol.Kind == pkgtypes.TypeSymbol && pkg.Symbols != nil {
		for _, m := range pkg.Symbols.Methods[symbol.Name] {
			if m.Kind == pkgtypes.MethodSymbol {
				symbols = append(symbols, m)
			}
		}
	}

	targets := make([]deletionTarget, 0, len(symbols))
	for _, sym := range symbols {
		file := findFileByPath(pkg, sym.File)
		if file == nil {
			return nil, &pkgtypes.RefactorError{
				Type:    pkgtypes.FileSystemError,
				Message: fmt.Sprintf("file %s of %s not found in package", sym.File, sym.Name),
			}
		}
		node, doc := op.findDeclaration(file, sym)
		if node == nil {
			return nil, &pkgtypes.RefactorError{
				Type:    pkgtypes.SymbolNotFound,
				Message: fmt.Sprintf("declaration for %s not found", sym.Name),
			}
		}
		targets = append(targets, deletionTarget{file: file, symbol: sym, node: node, doc: doc})
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
	// Check if source file exists
	var sourceFile *pkgtypes.File
	var sourcePackage *pkgtypes.Package
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
		// Also try to match by comparing file paths (for absolute paths from MCP)
		for _, file := range pkg.Files {
			if file.Path == op.SourceFile {
				sourceFile = file
				sourcePackage = pkg
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}

	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if sourceFile == nil {
		return nil, nil, resolver, nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Find the symbol to delete
	symbol, err := resolver.ResolveSymbol(sourcePackage, op.SymbolName)
	if err != nil {
		return nil, nil, resolver, nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("symbol %s not found: %v", op.SymbolName, err),
		}
	}

	return sourceFile, sourcePackage, resolver, symbol, nil
}

func (op *SafeDeleteOperation) Execute(ws *pkgtypes.Workspace) (*pkgtypes.RefactoringPlan, error) {
	_, sourcePackage, _, symbol, err := op.resolveSourceAndSymbol(ws)
	if err != nil {
		return nil, err
	}

	plan := &pkgtypes.RefactoringPlan{
		Changes:       make([]pkgtypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	targets, err := op.deletionTargets(ws, sourcePackage, symbol)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		change := op.generateRemovalChange(ws, t, symbol)
		plan.Changes = append(plan.Changes, change)
		if !slices.Contains(plan.AffectedFiles, t.file.Path) {
			plan.AffectedFiles = append(plan.AffectedFiles, t.file.Path)
		}
	}

	// If forced deletion, the declaration is removed but call sites are left
	// in place: rewriting them safely would require distinguishing "remove
	// the whole statement" from cases like `x := Foo()`, which is beyond
	// what this operation attempts (see the removed generateReferenceRemovalChanges).
	// Instead, every surviving reference is reported as a SkippedSite so the
	// caller can fix it up manually.
	if op.Force {
		actualReferences, err := op.BlockingReferences(ws)
		if err != nil {
			return nil, err
		}

		for _, ref := range actualReferences {
			plan.SkippedSites = append(plan.SkippedSites, pkgtypes.SkippedSite{
				File:    ref.File,
				Line:    ref.Line,
				Reason:  fmt.Sprintf("%s was force-deleted; this call site still references the deleted symbol and must be updated manually", op.SymbolName),
				Snippet: ref.Context,
			})
		}
	}

	// Perform impact analysis
	plan.Impact = &pkgtypes.ImpactAnalysis{
		AffectedFiles:    plan.AffectedFiles,
		AffectedPackages: op.getAffectedPackages(ws, plan.AffectedFiles),
		PotentialIssues:  op.analyzeImpact(ws, symbol, op.Force),
	}

	return plan, nil
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

func (op *SafeDeleteOperation) filterReferencesByScope(references []*pkgtypes.Reference, sourcePackage *pkgtypes.Package, ws *pkgtypes.Workspace) []*pkgtypes.Reference {
	if op.Scope == pkgtypes.PackageScope {
		// Only include references from the same package
		var filtered []*pkgtypes.Reference
		for _, ref := range references {
			if _, exists := sourcePackage.Files[ref.File]; exists {
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

func (op *SafeDeleteOperation) generateRemovalChange(ws *pkgtypes.Workspace, t deletionTarget, requested *pkgtypes.Symbol) pkgtypes.Change {
	start, end := calculateRemovalRange(ws, t)

	oldText := ""
	if start >= 0 && end <= len(t.file.OriginalContent) && start <= end {
		oldText = string(t.file.OriginalContent[start:end])
	}

	desc := fmt.Sprintf("Remove declaration of %s", t.symbol.Name)
	if t.symbol != requested {
		desc = fmt.Sprintf("Remove method %s.%s (deleted with type %s)", requested.Name, t.symbol.Name, requested.Name)
	}
	return pkgtypes.Change{
		File:        t.file.Path,
		Start:       start,
		End:         end,
		OldText:     oldText,
		NewText:     "",
		Description: desc,
	}
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
		for _, pkg := range ws.Packages {
			if _, exists := pkg.Files[filePath]; exists {
				packageMap[pkg.Path] = true
				break
			}
		}
	}

	packages := make([]string, 0, len(packageMap))
	for pkg := range packageMap {
		packages = append(packages, pkg)
	}
	return packages
}

func (op *SafeDeleteOperation) analyzeImpact(ws *pkgtypes.Workspace, symbol *pkgtypes.Symbol, forced bool) []pkgtypes.Issue {
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
			Severity:    pkgtypes.Error,
			Description: "Forced deletion will remove references, likely causing compilation errors",
		})
	}

	return issues
}
