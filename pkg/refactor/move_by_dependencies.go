package refactor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// moveSharedSymbols moves the shared-symbol candidates of op to the target
// package. Each candidate is first planned alone with MoveSymbolAcrossConfigs;
// one that fails or is refused stays in place and is listed as a skipped site.
// The accepted ones are then planned as a single move, so the target file,
// the source files and every referrer are edited once for the whole set.
func (e *DefaultEngine) moveSharedSymbols(ws *types.Workspace, op *MoveByDependenciesOperation) (*types.RefactoringPlan, error) {
	if op.Request.MoveSharedTo == "" {
		return nil, fmt.Errorf("MoveSharedTo must be specified when AnalyzeOnly is false")
	}

	toPackage := op.getTargetFSPath(ws)
	var skipped []types.SkippedSite
	skip := func(c sharedSymbolCandidate, reason string) {
		reason = strings.ReplaceAll(reason, ws.RootPath+string(filepath.Separator), "")
		skipped = append(skipped, types.SkippedSite{
			File:   c.symbol.File,
			Line:   c.symbol.Line,
			Reason: fmt.Sprintf("%s not moved: %s", c.symbol.Name, reason),
		})
	}

	var accepted []sharedSymbolCandidate
	for _, c := range op.identifySharedSymbols(ws) {
		_, err := e.MoveSymbolAcrossConfigs(types.MoveSymbolRequest{
			SymbolName:  c.symbol.Name,
			FromPackage: c.pkg.Path,
			ToPackage:   toPackage,
		})
		if err != nil {
			skip(c, err.Error())
			continue
		}
		accepted = append(accepted, c)
	}

	for len(accepted) > 0 {
		req := types.MoveSymbolRequest{ToPackage: toPackage}
		for _, c := range accepted {
			req.Symbols = append(req.Symbols, types.QualifiedSymbol{Package: c.pkg.Path, Name: c.symbol.Name})
		}
		plan, err := e.MoveSymbolAcrossConfigs(req)
		var member *groupMemberError
		if errors.As(err, &member) && member.index < len(accepted) {
			skip(accepted[member.index], member.err.Error())
			accepted = append(accepted[:member.index:member.index], accepted[member.index+1:]...)
			continue
		}
		if err != nil {
			return nil, err
		}
		plan.SkippedSites = append(plan.SkippedSites, skipped...)
		return plan, nil
	}

	return &types.RefactoringPlan{
		Changes:       []types.Change{},
		AffectedFiles: []string{},
		Reversible:    true,
		SkippedSites:  skipped,
	}, nil
}
