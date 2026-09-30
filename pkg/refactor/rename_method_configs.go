package refactor

import (
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

func (p *renameMethodPlanner) label() string { return "rename method" }

func (p *renameMethodPlanner) planWorkspace(ws *types.Workspace, idx *analysis.ReferenceIndex, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error) {
	return planMemberRenames(ws, idx, p, linked)
}

func (p *renameMethodPlanner) requestedOp(*analysis.ReferenceIndex) memberRename {
	req := p.req
	req.UpdateImplementations = true
	return &RenameMethodOperation{Request: req}
}

// linkedOp returns the operation renaming the method key names, or nil when
// key is not a method of the requested name or ws has no such package.
func (p *renameMethodPlanner) linkedOp(ws *types.Workspace, _ *analysis.ReferenceIndex, key types.SymbolKey) memberRename {
	pkg := resolveSymbolPackage(ws, &types.Symbol{Package: key.PkgPath})
	if pkg == nil || key.Recv == "" || key.Name != p.req.MethodName {
		return nil
	}
	return &RenameMethodOperation{Request: types.RenameMethodRequest{
		TypeName:              key.Recv,
		MethodName:            key.Name,
		NewMethodName:         p.req.NewMethodName,
		PackagePath:           pkg.Path,
		UpdateImplementations: true,
	}}
}

// validateIn validates the rename and returns the keys of the methods it
// renames: the requested one and, for an interface, the same-named methods of
// struct types.
func (op *RenameMethodOperation) validateIn(ws *types.Workspace) (types.SymbolKeySet, error) {
	typeSymbol, methodSymbol, err := op.target(ws)
	if err != nil {
		return nil, err
	}
	if err := op.validateNames(ws, typeSymbol); err != nil {
		return nil, err
	}
	keys := make(types.SymbolKeySet)
	addSymbolKey(keys, methodSymbol)
	if typeSymbol.Kind == types.InterfaceSymbol && op.Request.UpdateImplementations {
		for _, method := range op.implementationMethods(ws) {
			addSymbolKey(keys, method)
		}
	}
	return keys, nil
}
