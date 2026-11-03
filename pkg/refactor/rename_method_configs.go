package refactor

import (
	"fmt"
	"go/token"
	gotypes "go/types"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// renameMethodPlanner plans rename_method in one view.
type renameMethodPlanner struct {
	viewIndexes
	req types.RenameMethodRequest
}

func (p *renameMethodPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	return planRenameView(&p.viewIndexes, p, v, linked)
}

func (p *renameMethodPlanner) oldName() string { return p.req.MethodName }

// refuseIn refuses the rename when an identifier named like the method is left
// unresolved in a file of v with a type error and could use a renamed method.
func (p *renameMethodPlanner) refuseIn(v *analysis.View, renamed types.SymbolKeySet) error {
	return refuseUnresolvedMembers(v, renamed, "method", p.req.TypeName, p.req.MethodName)
}

func (p *renameMethodPlanner) label() string { return "rename method" }

func (p *renameMethodPlanner) planWorkspace(ws *types.Workspace, idx *analysis.ReferenceIndex, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error) {
	return planMemberRenames(ws, idx, p, linked)
}

func (p *renameMethodPlanner) requestedOp(idx *analysis.ReferenceIndex) memberRename {
	return &RenameMethodOperation{Request: p.req, CachedIndex: idx}
}

// linkedOp returns the operation renaming the method key names, or nil when
// key is not a method of the requested name or ws has no such package.
func (p *renameMethodPlanner) linkedOp(ws *types.Workspace, idx *analysis.ReferenceIndex, key types.SymbolKey) memberRename {
	pkg := resolveSymbolPackage(ws, &types.Symbol{Package: key.PkgPath})
	if pkg == nil || key.Recv == "" || key.Name != p.req.MethodName {
		return nil
	}

	return &RenameMethodOperation{Request: types.RenameMethodRequest{
		TypeName:      key.Recv,
		MethodName:    key.Name,
		NewMethodName: p.req.NewMethodName,
		PackagePath:   pkg.Path,
	}, CachedIndex: idx}
}

// validateIn validates the rename and returns the keys of the methods it
// renames: the requested one, the workspace interface methods it satisfies and
// the implementations of those.
func (op *RenameMethodOperation) validateIn(ws *types.Workspace) (types.SymbolKeySet, error) {
	typeSymbol, _, err := op.target(ws)
	if err != nil {
		return nil, err
	}

	err = op.validateNames(ws, typeSymbol)
	if err != nil {
		return nil, err
	}

	members, err := op.renamedMembers(ws)
	if err != nil {
		return nil, err
	}

	keys := make(types.SymbolKeySet)

	for _, member := range members {
		err := op.checkSiblingConflict(ws, member)
		if err != nil {
			return nil, err
		}

		addSymbolKey(keys, member)
	}

	return keys, nil
}

// checkSiblingConflict fails when the owner of member already has a method
// named like the new name.
func (op *RenameMethodOperation) checkSiblingConflict(ws *types.Workspace, member *types.Symbol) error {
	key, ok := symbolKeyOf(member)
	if !ok {
		return nil
	}

	key.Name = op.Request.NewMethodName
	if symbolForKey(ws, key) == nil {
		return nil
	}

	return &types.RefactorError{
		Type:    types.NameConflict,
		Message: fmt.Sprintf("method %s already exists on type %s", key.Name, key.Recv),
	}
}

// renamedMembers returns the symbols of the methods the rename changes, the
// requested one first. It fails when a renamed method satisfies an interface
// declared outside the workspace, which no edit here can keep satisfied.
func (op *RenameMethodOperation) renamedMembers(ws *types.Workspace) ([]*types.Symbol, error) {
	if op.members != nil {
		return op.members, nil
	}

	seed, err := op.targetFunc(ws)
	if err != nil {
		return nil, err
	}

	funcs, err := methodClosure(surveyInterfaces(ws), seed, func(f *gotypes.Func, ext ifaceCandidate) error {
		return externalInterfaceError("rename", f, ext)
	})
	if err != nil {
		return nil, err
	}

	members := make([]*types.Symbol, 0, len(funcs))
	for _, f := range funcs {
		err := stdMethodError("rename", f)
		if err != nil {
			return nil, err
		}

		err = stdRenameError(f, op.Request.NewMethodName)
		if err != nil {
			return nil, err
		}

		member, err := memberSymbol(ws, f)
		if err != nil {
			return nil, err
		}

		members = append(members, member)
	}

	op.members = members

	return members, nil
}

// targetFunc returns the go/types method the request names.
func (op *RenameMethodOperation) targetFunc(ws *types.Workspace) (*gotypes.Func, error) {
	typeSymbol, methodSymbol, err := op.target(ws)
	if err != nil {
		return nil, fmt.Errorf("target func: %w", err)
	}

	return resolveMethod(resolveSymbolPackage(ws, typeSymbol), typeSymbol.Name, methodSymbol.Name, methodSymbol.Position)
}

// resolveMethod returns the go/types method typeName.name of pkg declared at
// pos. It fails rather than guessing when pkg has no type information.
func resolveMethod(pkg *types.Package, typeName, name string, pos token.Pos) (*gotypes.Func, error) {
	if pkg == nil || pkg.TypesPkg == nil {
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf("cannot resolve method %s.%s: type information for its package is unavailable; "+
				"method operations require a type-checked workspace", typeName, name),
		}
	}

	tn, ok := pkg.TypesPkg.Scope().Lookup(typeName).(*gotypes.TypeName)
	if ok {
		obj, _, _ := gotypes.LookupFieldOrMethod(tn.Type(), true, pkg.TypesPkg, name)

		f, ok := obj.(*gotypes.Func)
		if ok && f.Pos() == pos {
			return canonicalFunc(f), nil
		}
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("cannot resolve method %s.%s to a type-checked object", typeName, name),
	}
}

// receiverNamed returns the named type f is a method of, or nil.
func receiverNamed(f *gotypes.Func) *gotypes.Named {
	recv := methodRecvType(f)
	if recv == nil {
		return nil
	}

	recv = gotypes.Unalias(recv)

	ptr, ok := recv.(*gotypes.Pointer)
	if ok {
		recv = gotypes.Unalias(ptr.Elem())
	}

	named, _ := recv.(*gotypes.Named)

	return named
}

// memberSymbol returns the workspace symbol that declares f.
func memberSymbol(ws *types.Workspace, f *gotypes.Func) (*types.Symbol, error) {
	named := receiverNamed(f)
	if named == nil || f.Pkg() == nil {
		return nil, fmt.Errorf("%s is not a method of a named workspace type", funcLabel(f))
	}

	if named.TypeParams().Len() > 0 {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("cannot change %s: methods of generic types are not supported, and this one must change with the method requested", funcLabel(f)),
		}
	}

	sym := symbolForKey(ws, types.SymbolKey{PkgPath: f.Pkg().Path(), Recv: named.Obj().Name(), Name: f.Name()})
	if sym == nil || sym.Position != f.Pos() {
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf("cannot change %s.%s.%s: it is declared outside the workspace or in a file that is not part of it, "+
				"but must change with the method requested", f.Pkg().Path(), named.Obj().Name(), f.Name()),
		}
	}

	return sym, nil
}

// externalInterfaceError refuses to rename or change f, which satisfies ext.
func externalInterfaceError(verb string, f *gotypes.Func, ext ifaceCandidate) error {
	return &types.RefactorError{
		Type: types.InvalidOperation,
		Message: fmt.Sprintf("cannot %s method %s: it implements %s, which is declared outside the workspace and cannot be changed; "+
			"the %s would stop %s satisfying %s. No changes were made.",
			verb, funcLabel(f), ext.label(), verb, gotypes.TypeString(methodRecvType(f), nil), ext.label()),
	}
}
