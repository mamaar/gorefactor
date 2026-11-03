package analysis

import (
	"go/ast"
	"go/token"
	gotypes "go/types"

	"golang.org/x/tools/go/ast/edge"
	"golang.org/x/tools/go/ast/inspector"

	"github.com/mamaar/gorefactor/pkg/types"
)

// CallSite is a single call site of some callee, resolved to plain ast/types
// values; Cursor locates Call in its file for callers that need its parents.
type CallSite struct {
	File   *types.File
	Pkg    *types.Package
	Call   *ast.CallExpr
	Cursor inspector.Cursor

	// Fn is the nearest enclosing named function declaration, even when Call
	// sits inside a FuncLit nested in that FuncDecl (Lit != nil is how a caller
	// tells the two situations apart). Nil for a call at package/init level
	// that is not inside any FuncDecl (e.g. inside a package-level var init
	// that isn't itself a closure).
	Fn *ast.FuncDecl

	// Lit is the innermost enclosing FuncLit, or nil if Call is not inside a
	// closure at all.
	Lit *ast.FuncLit

	// InGo/InDefer report whether Lit (the innermost enclosing closure) is
	// itself the operand of a `go func(){...}()` / `defer func(){...}()`
	// expression. Both are false whenever Lit is nil.
	InGo    bool
	InDefer bool
}

// ValueUse is a use of a symbol that is not the callee of a call — a method
// value, a func value, or a symbol passed as an argument. These are exactly
// the references a signature change cannot rewrite by editing call sites.
type ValueUse struct {
	File   *types.File
	Pkg    *types.Package
	Pos    token.Pos
	Ident  *ast.Ident
	Cursor inspector.Cursor
}

// CallSitesOf returns every call site of obj across the workspace. obj is
// canonicalized first so that a generic instantiation (F[int]) collapses to
// its origin declaration and is found regardless of which instantiation was
// passed in.
func (idx *ReferenceIndex) CallSitesOf(obj gotypes.Object) []CallSite {
	if idx.workspaceIdx == nil {
		return nil
	}

	var sites []CallSite

	for _, pix := range idx.workspaceIdx.packages {
		for cur := range pix.Calls(obj) {
			call, ok := cur.Node().(*ast.CallExpr)
			if !ok {
				continue
			}

			file, pkg := idx.workspaceIdx.fileAndPkgFor(call.Pos())
			if file == nil {
				continue
			}

			site := CallSite{
				File:   file,
				Pkg:    pkg,
				Call:   call,
				Cursor: cur,
			}

			// Innermost enclosing FuncLit, regardless of whether a FuncDecl
			// also encloses it — Lit != nil is how callers detect a closure.
			var litCur inspector.Cursor
			for c := range cur.Enclosing((*ast.FuncLit)(nil)) {
				litCur = c
				site.Lit, _ = c.Node().(*ast.FuncLit)

				break
			}

			// Nearest enclosing FuncDecl. This is independent of Lit: a call
			// inside a closure nested in a FuncDecl still reports that
			// FuncDecl as Fn.
			for c := range cur.Enclosing((*ast.FuncDecl)(nil)) {
				site.Fn, _ = c.Node().(*ast.FuncDecl)
				break
			}

			if site.Lit != nil {
				site.InGo, site.InDefer = goOrDeferLit(litCur)
			}

			sites = append(sites, site)
		}
	}

	return sites
}

// goOrDeferLit reports whether litCur (a cursor over a *ast.FuncLit) is the
// Fun of a CallExpr that is itself the Call of a GoStmt/DeferStmt, i.e. the
// litCur represents the func literal in `go func(){...}()` or
// `defer func(){...}()`.
func goOrDeferLit(litCur inspector.Cursor) (isGo, isDefer bool) {
	if litCur.ParentEdgeKind() != edge.CallExpr_Fun {
		return false, false
	}

	callCur := litCur.Parent()
	if !callCur.Valid() {
		return false, false
	}

	switch callCur.ParentEdgeKind() {
	case edge.GoStmt_Call:
		return true, false
	case edge.DeferStmt_Call:
		return false, true
	}

	return false, false
}

// NonCallUsesOf returns every use of obj that is NOT the callee of a
// CallExpr — method values, func values, and a symbol passed as an argument
// are exactly the references a signature change cannot rewrite by editing
// call sites. callSiteFor is the single source of truth for the callee/value
// distinction, so a use is in exactly one of CallSitesOf or NonCallUsesOf
// (aside from the definition, which appears in neither).
func (idx *ReferenceIndex) NonCallUsesOf(obj gotypes.Object) []ValueUse {
	if idx.workspaceIdx == nil {
		return nil
	}

	var uses []ValueUse

	for _, pix := range idx.workspaceIdx.packages {
		for cur := range pix.Uses(obj) {
			_, ok := callSiteFor(cur)
			if ok {
				continue // it's a call site, reported by CallSitesOf instead
			}

			ident, ok := cur.Node().(*ast.Ident)
			if !ok {
				continue
			}

			file, pkg := idx.workspaceIdx.fileAndPkgFor(ident.Pos())
			if file == nil {
				continue
			}

			uses = append(uses, ValueUse{
				File:   file,
				Pkg:    pkg,
				Pos:    ident.Pos(),
				Ident:  ident,
				Cursor: cur,
			})
		}
	}

	return uses
}

// HasTypeInfo reports whether the index has object-identity data at all.
//
// LoadWorkspace does not type-check, and BuildReferenceIndex silently skips
// packages whose TypesPkg/TypesInfo is nil. An index built before
// type-checking therefore has an empty workspaceIdx.packages, so
// CallSitesOf/NonCallUsesOf return nothing — not because obj has no uses,
// but because there is no object-identity data to search. Without this
// check a caller could "succeed" having silently changed nothing; callers
// should treat HasTypeInfo() == false as a hard error, not an empty result.
func (idx *ReferenceIndex) HasTypeInfo() bool {
	return idx.workspaceIdx != nil && len(idx.workspaceIdx.packages) > 0
}

// fileAndPkgFor resolves a token.Pos to its *types.File and, via the file's
// back-reference, its *types.Package. There is no direct *gotypes.Package ->
// *types.Package map on workspaceIndex, but every *types.File populated by
// BuildReferenceIndex already carries its owning *types.Package, so routing
// through tokenToFile covers both lookups in one step.
func (wi *workspaceIndex) fileAndPkgFor(pos token.Pos) (*types.File, *types.Package) {
	tf := wi.fileSet.File(pos)
	if tf == nil {
		return nil, nil
	}

	file := wi.tokenToFile[tf]
	if file == nil {
		return nil, nil
	}

	return file, file.Package
}
