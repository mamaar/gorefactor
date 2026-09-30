package refactor

import (
	"fmt"

	"github.com/mamaar/gorefactor/pkg/types"
)

// moveSharedSymbols moves each shared-symbol candidate of op to the target
// package with MoveSymbolAcrossConfigs and merges the plans. A candidate whose
// move fails, is refused, or conflicts with the moves before it is left in
// place and listed as a skipped site.
func (e *DefaultEngine) moveSharedSymbols(ws *types.Workspace, op *MoveByDependenciesOperation) (*types.RefactoringPlan, error) {
	if op.Request.MoveSharedTo == "" {
		return nil, fmt.Errorf("MoveSharedTo must be specified when AnalyzeOnly is false")
	}

	toPackage := op.getTargetFSPath(ws)
	var accepted []*types.RefactoringPlan
	var acceptedNames []string
	var skipped []types.SkippedSite
	skip := func(c sharedSymbolCandidate, err error) {
		skipped = append(skipped, types.SkippedSite{
			File:   c.symbol.File,
			Line:   c.symbol.Line,
			Reason: fmt.Sprintf("%s not moved: %v", c.symbol.Name, err),
		})
	}
	for _, c := range op.identifySharedSymbols(ws) {
		plan, err := e.MoveSymbolAcrossConfigs(types.MoveSymbolRequest{
			SymbolName:  c.symbol.Name,
			FromPackage: c.pkg.Path,
			ToPackage:   toPackage,
		})
		if err != nil {
			skip(c, err)
			continue
		}
		if _, err := MergeStepPlans(append(accepted, plan)); err != nil {
			skip(c, conflictWithEarlierMove(accepted, acceptedNames, plan, err))
			continue
		}
		accepted = append(accepted, plan)
		acceptedNames = append(acceptedNames, c.symbol.Name)
	}

	merged, err := MergeStepPlans(accepted)
	if err != nil {
		return nil, err
	}
	merged.SkippedSites = append(merged.SkippedSites, skipped...)
	return merged, nil
}

// conflictWithEarlierMove names the accepted move that plan conflicts with, or
// returns mergeErr when no single earlier move accounts for it.
func conflictWithEarlierMove(accepted []*types.RefactoringPlan, names []string, plan *types.RefactoringPlan, mergeErr error) error {
	for i, earlier := range accepted {
		if _, err := MergeStepPlans([]*types.RefactoringPlan{earlier, plan}); err != nil {
			return fmt.Errorf("conflicts with the move of %s in this run; run again to move it", names[i])
		}
	}
	return mergeErr
}
