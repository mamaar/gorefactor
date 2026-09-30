package refactor

import (
	"fmt"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// maxListedBlockers bounds how many blockers a refusal spells out.
const maxListedBlockers = 10

// safeDeletePlanner plans safe_delete in one view. A view deletes the
// requested symbol and the declarations with a key deleted in another view,
// and reports the references to any of them as blockers.
type safeDeletePlanner struct {
	viewIndexes
	req types.SafeDeleteRequest
	// errPrefix wraps every error of the run when set.
	errPrefix string
}

func (p *safeDeletePlanner) operation(idx *analysis.ReferenceIndex) *SafeDeleteOperation {
	return &SafeDeleteOperation{
		SymbolName:  p.req.Symbol,
		SourceFile:  p.req.SourceFile,
		Scope:       types.WorkspaceScope,
		Force:       p.req.Force,
		CachedIndex: idx,
	}
}

func (p *safeDeletePlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	ws := v.Workspace
	op := p.operation(p.indexOf(v))
	declared := declaredSymbolKeys(ws)

	roots, notFound := op.rootsIn(ws, linked)
	if len(roots) == 0 {
		p.planned(v, nil)
		return configResult{absent: p.wrap(notFound), declared: declared}, nil
	}
	targets, err := op.targetsOf(ws, roots)
	if err != nil {
		return configResult{}, p.wrap(err)
	}
	blocking, err := op.blockingAmong(ws, targets)
	if err != nil {
		return configResult{}, p.wrap(err)
	}
	blocking = outsideFiles(blocking, v.ExcludedTestFiles())

	plan := op.removalPlan(ws, targets, blocking)
	impact, err := v.Analyzer.AnalyzeImpact(op)
	if err != nil {
		return configResult{}, fmt.Errorf("failed to analyze impact: %w", err)
	}
	plan.Impact = impact
	plan.Operations = []types.Operation{op}

	res := configResult{
		plan:     plan,
		changed:  make(types.SymbolKeySet),
		reach:    reachOf(plan, nil),
		declared: declared,
		blockers: make([]types.Blocker, len(blocking)),
	}
	for _, t := range targets {
		addSymbolKey(res.changed, t.symbol)
		res.reach.names = append(res.reach.names, t.symbol.Name)
	}
	for i, ref := range blocking {
		res.blockers[i] = types.Blocker{Reference: ref}
	}
	op.dropAnalysis()
	p.planned(v, plan)
	return res, nil
}

// refuse stops an unforced delete that has blockers in any view.
func (p *safeDeletePlanner) refuse(blockers []types.Blocker) error {
	if p.req.Force || len(blockers) == 0 {
		return nil
	}
	err := p.operation(nil).refusal(len(blockers))
	err.Message += blockerNotes(blockers)
	return p.wrap(err)
}

func (p *safeDeletePlanner) wrap(err error) error {
	if p.errPrefix == "" {
		return err
	}
	return fmt.Errorf("%s: %w", p.errPrefix, err)
}

// blockerNotes lists the blockers that only some configurations see or that
// could not be type-checked, one per line. It is empty when there are none.
func blockerNotes(blockers []types.Blocker) string {
	var b strings.Builder
	listed := 0
	for _, blocker := range blockers {
		note := blocker.Note()
		if note == "" {
			continue
		}
		if listed++; listed <= maxListedBlockers {
			fmt.Fprintf(&b, "\n  %s:%d%s", blocker.File, blocker.Line, note)
		}
	}
	if listed > maxListedBlockers {
		fmt.Fprintf(&b, "\n  …and %d more", listed-maxListedBlockers)
	}
	return b.String()
}

// outsideFiles returns the references that are not in the given files. A
// reference found by name in a test file the view could not type-check is left
// to the views that check the file, or to the scan of unchecked files.
func outsideFiles(refs []types.Reference, skip map[string]bool) []types.Reference {
	if len(skip) == 0 {
		return refs
	}
	kept := make([]types.Reference, 0, len(refs))
	for _, ref := range refs {
		if !skip[ref.File] {
			kept = append(kept, ref)
		}
	}
	return kept
}
