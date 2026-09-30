package refactor

import (
	"fmt"

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

// planWorkspace renames the requested method of ws and the linked methods it
// declares. The new name must be free on each owning type.
func (p *renameMethodPlanner) planWorkspace(ws *types.Workspace, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error) {
	req := p.req
	req.UpdateImplementations = true
	requested := &RenameMethodOperation{Request: req}
	ops, renamed, notFound, err := p.operationsIn(ws, requested, linked)
	if err != nil {
		return nil, nil, err
	}
	if len(ops) == 0 {
		return nil, nil, fmt.Errorf("rename method operation validation failed: %w", notFound)
	}
	var acc planAccum
	for _, op := range ops {
		plan, err := op.Execute(ws)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to generate rename method plan: %w", err)
		}
		acc.add(plan)
	}
	return renamed, acc.plan(), nil
}

// operationsIn returns the validated operations that rename the requested
// method and the linked methods ws declares, and the keys of the methods they
// rename. When ws has none it returns the not-found error of the request.
func (p *renameMethodPlanner) operationsIn(ws *types.Workspace, requested *RenameMethodOperation, linked types.SymbolKeySet) (ops []*RenameMethodOperation, renamed types.SymbolKeySet, notFound, err error) {
	renamed = make(types.SymbolKeySet)
	typeSymbol, methodSymbol, notFound := requested.target(ws)
	if notFound != nil && !isSymbolNotFound(notFound) {
		return nil, nil, nil, notFound
	}
	if notFound == nil {
		if err := requested.validateNames(ws, typeSymbol); err != nil {
			return nil, nil, nil, fmt.Errorf("rename method operation validation failed: %w", err)
		}
		ops = append(ops, requested)
		requested.addRenamedKeys(ws, typeSymbol, methodSymbol, renamed)
	}
	for _, key := range orderedKeys(linked) {
		if renamed.Has(key) || key.Name != p.req.MethodName || key.Recv == "" {
			continue
		}
		op := p.linkedOperation(ws, key)
		if op == nil {
			continue
		}
		typeSymbol, methodSymbol, err := op.target(ws)
		if err != nil {
			continue
		}
		if err := op.validateNames(ws, typeSymbol); err != nil {
			return nil, nil, nil, fmt.Errorf("rename method operation validation failed: %w", err)
		}
		ops = append(ops, op)
		op.addRenamedKeys(ws, typeSymbol, methodSymbol, renamed)
	}
	return ops, renamed, notFound, nil
}

// linkedOperation returns the operation renaming the method key names, or nil
// when ws has no such package.
func (p *renameMethodPlanner) linkedOperation(ws *types.Workspace, key types.SymbolKey) *RenameMethodOperation {
	pkg := resolveSymbolPackage(ws, &types.Symbol{Package: key.PkgPath})
	if pkg == nil {
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

// addRenamedKeys adds the keys of the methods the operation renames: the
// requested one and, for an interface, the same-named methods of struct types.
func (op *RenameMethodOperation) addRenamedKeys(ws *types.Workspace, typeSymbol, methodSymbol *types.Symbol, keys types.SymbolKeySet) {
	addSymbolKey(keys, methodSymbol)
	if typeSymbol.Kind != types.InterfaceSymbol || !op.Request.UpdateImplementations {
		return
	}
	for _, method := range op.implementationMethods(ws) {
		addSymbolKey(keys, method)
	}
}
