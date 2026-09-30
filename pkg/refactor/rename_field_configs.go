package refactor

import (
	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// renameFieldPlanner plans rename_field in one view.
type renameFieldPlanner struct {
	viewIndexes
	engine *DefaultEngine
	req    types.RenameFieldRequest
}

func (p *renameFieldPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	return planRenameView(&p.viewIndexes, p, v, linked)
}

func (p *renameFieldPlanner) oldName() string { return p.req.FieldName }

func (p *renameFieldPlanner) label() string { return "rename field" }

func (p *renameFieldPlanner) planWorkspace(ws *types.Workspace, idx *analysis.ReferenceIndex, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error) {
	return planMemberRenames(ws, idx, p, linked)
}

func (p *renameFieldPlanner) requestedOp(idx *analysis.ReferenceIndex) memberRename {
	return p.operation(p.req, idx)
}

// linkedOp returns the operation renaming the field key names, or nil when
// key is not a field of the requested name or ws has no such package.
func (p *renameFieldPlanner) linkedOp(ws *types.Workspace, idx *analysis.ReferenceIndex, key types.SymbolKey) memberRename {
	pkg := resolveSymbolPackage(ws, &types.Symbol{Package: key.PkgPath})
	if pkg == nil || key.Recv == "" || key.Name != p.req.FieldName {
		return nil
	}
	req := p.req
	req.TypeName, req.PackagePath = key.Recv, pkg.Path
	return p.operation(req, idx)
}

func (p *renameFieldPlanner) operation(req types.RenameFieldRequest, idx *analysis.ReferenceIndex) *RenameFieldOperation {
	op := &RenameFieldOperation{Request: req, CachedIndex: idx}
	if p.engine.config != nil {
		op.AllowBreaking = p.engine.config.AllowBreaking
	}
	return op
}

// validateIn validates the rename and returns the key of the field it renames.
func (op *RenameFieldOperation) validateIn(ws *types.Workspace) (types.SymbolKeySet, error) {
	if err := op.Validate(ws); err != nil {
		return nil, err
	}
	pkg, err := op.findOwningPackage(ws)
	if err != nil {
		return nil, err
	}
	keys := make(types.SymbolKeySet)
	addSymbolKey(keys, op.fieldSymbol(pkg))
	return keys, nil
}
