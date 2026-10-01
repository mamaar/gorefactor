package refactor

import (
	"fmt"
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
	if err := op.validateNames(ws, typeSymbol); err != nil {
		return nil, err
	}
	members, err := op.renamedMembers(ws)
	if err != nil {
		return nil, err
	}
	keys := make(types.SymbolKeySet)
	for _, member := range members {
		if err := op.checkSiblingConflict(ws, member); err != nil {
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
	survey := surveyInterfaces(ws)
	var members []*types.Symbol
	visited := make(map[gotypes.Object]bool)
	for work := []*gotypes.Func{seed}; len(work) > 0; work = work[1:] {
		f := work[0]
		if visited[f] {
			continue
		}
		visited[f] = true
		if ext, ok := survey.externalInterfaceOf(f); ok {
			return nil, externalInterfaceRenameError(f, ext)
		}
		member, err := memberSymbol(ws, f)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
		for _, next := range survey.expansionOf(f) {
			work = append(work, canonicalFunc(next))
		}
	}
	op.members = members
	return members, nil
}

// targetFunc returns the go/types method the request names. It fails rather
// than guessing when the owning package has no type information.
func (op *RenameMethodOperation) targetFunc(ws *types.Workspace) (*gotypes.Func, error) {
	typeSymbol, methodSymbol, err := op.target(ws)
	if err != nil {
		return nil, err
	}
	pkg := resolveSymbolPackage(ws, typeSymbol)
	if pkg == nil || pkg.TypesPkg == nil {
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf("cannot rename method %s.%s: type information for package %q is unavailable; "+
				"method renames require a type-checked workspace", typeSymbol.Name, methodSymbol.Name, typeSymbol.Package),
		}
	}
	if tn, ok := pkg.TypesPkg.Scope().Lookup(typeSymbol.Name).(*gotypes.TypeName); ok {
		obj, _, _ := gotypes.LookupFieldOrMethod(tn.Type(), true, pkg.TypesPkg, methodSymbol.Name)
		if f, ok := obj.(*gotypes.Func); ok && f.Pos() == methodSymbol.Position {
			return canonicalFunc(f), nil
		}
	}
	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("cannot resolve method %s.%s to a type-checked object", typeSymbol.Name, methodSymbol.Name),
	}
}

// memberSymbol returns the workspace symbol that declares f.
func memberSymbol(ws *types.Workspace, f *gotypes.Func) (*types.Symbol, error) {
	recv, ok := methodRecvType(f).(gotypes.Type)
	if !ok || f.Pkg() == nil {
		return nil, fmt.Errorf("%s is not a method of a workspace type", f.Name())
	}
	if ptr, ok := recv.(*gotypes.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*gotypes.Named)
	if !ok {
		return nil, fmt.Errorf("cannot rename method %s: its receiver %s is not a named type", f.Name(), recv)
	}
	sym := symbolForKey(ws, types.SymbolKey{PkgPath: f.Pkg().Path(), Recv: named.Obj().Name(), Name: f.Name()})
	if sym == nil || sym.Position != f.Pos() {
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf("cannot rename method %s.%s.%s: it is declared outside the workspace or in a file that is not part of it, "+
				"but must be renamed with the method requested", f.Pkg().Path(), named.Obj().Name(), f.Name()),
		}
	}
	return sym, nil
}

func externalInterfaceRenameError(f *gotypes.Func, ext ifaceCandidate) error {
	return &types.RefactorError{
		Type: types.InvalidOperation,
		Message: fmt.Sprintf("cannot rename method %s: %s implements %s, which is declared outside the workspace and cannot be changed; "+
			"renaming it would stop %s satisfying %s. No changes were made.",
			f.Name(), funcLabel(f), ext.label(), gotypes.TypeString(methodRecvType(f), nil), ext.label()),
	}
}
