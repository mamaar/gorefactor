package refactor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// moveSymbolPlanFunc plans a move of symbols across the build configurations.
type moveSymbolPlanFunc func(types.MoveSymbolRequest) (*types.RefactoringPlan, error)

// moveSharedSymbols moves the shared-symbol candidates of op to the target
// package. Each candidate is first planned alone with MoveSymbolAcrossConfigs;
// one that fails, is refused or leaves a site to update by hand stays in place
// and is listed as a skipped site.
// The accepted ones are then planned as a single move, so the target file,
// the source files and every referrer are edited once for the whole set.
func (e *DefaultEngine) moveSharedSymbols(ws *types.Workspace, op *MoveByDependenciesOperation, planMove moveSymbolPlanFunc) (*types.RefactoringPlan, error) {
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
		plan, err := planMove(types.MoveSymbolRequest{
			SymbolName:  c.symbol.Name,
			FromPackage: c.pkg.Path,
			ToPackage:   toPackage,
		})
		if err != nil {
			skip(c, err.Error())
			continue
		}
		// A site the plan leaves to be updated by hand would not build after
		// a move applied in one call.
		if len(plan.SkippedSites) > 0 {
			site := plan.SkippedSites[0]
			skip(c, fmt.Sprintf("%s:%d %s", site.File, site.Line, site.Reason))
			continue
		}
		accepted = append(accepted, c)
	}

	planGroup := func(group []sharedSymbolCandidate) (*types.RefactoringPlan, error) {
		req := types.MoveSymbolRequest{ToPackage: toPackage}
		for _, c := range group {
			req.Symbols = append(req.Symbols, types.QualifiedSymbol{Package: c.pkg.Path, Name: c.symbol.Name})
		}
		return planMove(req)
	}
	drop := func(i int, reason string) {
		skip(accepted[i], reason)
		accepted = append(accepted[:i:i], accepted[i+1:]...)
	}

	for len(accepted) > 0 {
		plan, err := planGroup(accepted)
		var member *groupMemberError
		if errors.As(err, &member) && member.index < len(accepted) {
			drop(member.index, member.err.Error())
			continue
		}
		if err != nil {
			// No member is to blame: skip the first candidate whose
			// addition to the group breaks it.
			good, bad := 0, len(accepted)
			for bad-good > 1 {
				mid := (good + bad) / 2
				_, midErr := planGroup(accepted[:mid])
				if midErr == nil {
					good = mid
				} else {
					bad, err = mid, midErr
				}
			}
			drop(good, err.Error())
			continue
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
