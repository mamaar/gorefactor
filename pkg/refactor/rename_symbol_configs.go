package refactor

import (
	"fmt"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// renameSymbolPlanner plans rename_symbol in one view.
type renameSymbolPlanner struct {
	viewIndexes
	req types.RenameSymbolRequest
}

// planIn plans a method target the way rename_method does, so the method's
// refusals apply, and any other target as a symbol.
func (p *renameSymbolPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	req, isMethod, err := p.methodRequestIn(v.Workspace)
	if err != nil {
		return configResult{}, err
	}

	if isMethod {
		return planRenameView(&p.viewIndexes, &renameMethodPlanner{req: req}, v, linked)
	}

	return planRenameView(&p.viewIndexes, p, v, linked)
}

// methodRequestIn returns the rename_method request for the target when it is a
// method of ws. A bare name that finds a method and other symbols is an error,
// since those symbols would be renamed without the method's checks.
func (p *renameSymbolPlanner) methodRequestIn(ws *types.Workspace) (types.RenameMethodRequest, bool, error) {
	typeName, method, dotted := strings.Cut(p.req.SymbolName, ".")
	if dotted {
		return types.RenameMethodRequest{TypeName: typeName, MethodName: method, NewMethodName: p.req.NewName, PackagePath: p.req.Package}, true, nil
	}

	op := &RenameSymbolOperation{Request: p.req}
	targets, _ := op.findTargetSymbols(ws, quietResolver(ws))
	methods := 0

	for _, t := range targets {
		if t.Kind == types.MethodSymbol {
			methods++
		}
	}

	if methods == 0 {
		return types.RenameMethodRequest{}, false, nil
	}

	if len(targets) != 1 {
		return types.RenameMethodRequest{}, false, fmt.Errorf("rename operation validation failed: %s names a method and %d other symbols; name the method as Type.Method", p.req.SymbolName, len(targets)-1)
	}

	key, ok := symbolKeyOf(targets[0])
	if !ok {
		return types.RenameMethodRequest{}, false, fmt.Errorf("rename operation validation failed: cannot resolve the receiver of method %s", p.req.SymbolName)
	}

	return types.RenameMethodRequest{TypeName: key.Recv, MethodName: key.Name, NewMethodName: p.req.NewName, PackagePath: key.PkgPath}, true, nil
}

func (p *renameSymbolPlanner) oldName() string { return p.req.SymbolName }

// planWorkspace renames the requested symbols of ws and the linked ones it
// declares. The new name must be free for each of them.
func (p *renameSymbolPlanner) planWorkspace(ws *types.Workspace, _ *analysis.ReferenceIndex, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error) {
	op := &RenameSymbolOperation{Request: p.req}
	resolver := quietResolver(ws)

	targets, notFound := op.findTargetSymbols(ws, resolver)
	if notFound != nil && !isSymbolNotFound(notFound) {
		return nil, nil, notFound
	}

	targets = withLinkedSymbols(ws, targets, linked)
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("rename operation validation failed: %w", notFound)
	}

	err := op.validateTargets(ws, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("rename operation validation failed: %w", err)
	}

	plan, err := op.planSymbols(ws, resolver, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate rename plan: %w", err)
	}

	return keysOf(targets), plan, nil
}
