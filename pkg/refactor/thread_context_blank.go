package refactor

import (
	"fmt"
	"go/ast"
	gotypes "go/types"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// blankCtxField returns the parameter field of fd that is a context.Context the
// body cannot reference, and for a blank one its `_` identifier. Both are nil
// when every context parameter is named; the identifier is nil when unnamed.
func blankCtxField(pkg *pkgtypes.Package, fd *ast.FuncDecl) (*ast.Field, *ast.Ident) {
	if fd == nil || fd.Type.Params == nil || pkg.TypesInfo == nil {
		return nil, nil
	}
	for _, field := range fd.Type.Params.List {
		if !analysis.IsContextType(pkg.TypesInfo.TypeOf(field.Type)) {
			continue
		}
		if len(field.Names) == 0 {
			return field, nil
		}
		for _, name := range field.Names {
			if name.Name == "_" {
				return field, name
			}
		}
	}
	return nil, nil
}

// ctxNameConflict returns where fd declares or uses a variable, constant or
// other object called ctx, or nil when the name is free. Fields named ctx do
// not conflict: they are reached through a selector or a literal key.
func ctxNameConflict(pkg *pkgtypes.Package, fd *ast.FuncDecl) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(fd, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || id.Name != ctxParamName || found != nil {
			return found == nil
		}
		obj := pkg.TypesInfo.ObjectOf(id)
		if v, isVar := obj.(*gotypes.Var); !isVar || !v.IsField() {
			found = id
		}
		return found == nil
	})
	return found
}

// renameBlankCtx records that caller's blank or unnamed context parameter is to
// be named ctx and used. It reports false when caller has no such parameter.
func (op *ThreadContextOperation) renameBlankCtx(plan *threadPlan, caller *gotypes.Func, site analysis.CallSite) (bool, error) {
	fd := op.declIndex[caller]
	if fd == nil || fd.Decl == nil {
		return false, nil
	}
	if field, _ := blankCtxField(fd.Pkg, fd.Decl); field == nil {
		return false, nil
	}
	if id := ctxNameConflict(fd.Pkg, fd.Decl); id != nil {
		return false, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: fmt.Sprintf("cannot thread context through %s: its context.Context parameter is blank or unnamed "+
				"and the name ctx is already used at %s; rename that identifier and re-run",
				funcLabel(caller), fmt.Sprintf("%s:%d", fd.File.Path, offsetLine(fd.File, id.Pos()))),
			File: site.File.Path,
			Line: op.siteLine(site),
		}
	}
	plan.Renamed[caller] = true
	plan.Decls[caller] = fd
	return true, nil
}

// renderBlankCtx names the blank or unnamed context parameter of fd ctx. In an
// unnamed list every other parameter becomes `_`, since Go forbids mixing.
func renderBlankCtx(ws *pkgtypes.Workspace, fd *funcDecl) []pkgtypes.Change {
	field, blank := blankCtxField(fd.Pkg, fd.Decl)
	if blank != nil {
		start := ws.FileSet.Position(blank.Pos()).Offset
		return []pkgtypes.Change{{File: fd.File.Path, Start: start, End: start + len(blank.Name), NewText: ctxParamName, Description: "Name blank context.Context parameter ctx"}}
	}
	changes := []pkgtypes.Change{ctxInsertion(ws, fd.File, field.Type.Pos(), ctxParamName+" ", "Name unnamed context.Context parameter ctx")}
	for _, other := range fd.Decl.Type.Params.List {
		if other != field {
			changes = append(changes, ctxInsertion(ws, fd.File, other.Type.Pos(), "_ ", "Name unused parameter"))
		}
	}
	return changes
}
