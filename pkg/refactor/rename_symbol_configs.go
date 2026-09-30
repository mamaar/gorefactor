package refactor

import (
	"fmt"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// renameSymbolPlanner plans rename_symbol in one view.
type renameSymbolPlanner struct {
	viewIndexes
	req types.RenameSymbolRequest
}

func (p *renameSymbolPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	return planRenameView(&p.viewIndexes, p, v, linked)
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
	if err := op.validateTargets(ws, targets); err != nil {
		return nil, nil, fmt.Errorf("rename operation validation failed: %w", err)
	}
	plan, err := op.planSymbols(ws, resolver, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate rename plan: %w", err)
	}
	return keysOf(targets), plan, nil
}
