package refactor

import (
	"fmt"
	"path/filepath"
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
	res.blockers = append(res.blockers, unresolvedUses(v, targets)...)
	op.dropAnalysis()
	p.planned(v, plan)
	return res, nil
}

// unresolvedUses returns a blocker for each identifier outside targets that
// the type-check of v left unresolved in a file with a type error and that
// could name a target: inside an erroring expression a use has no type.
func unresolvedUses(v *analysis.View, targets []deletionTarget) []types.Blocker {
	ws := v.Workspace
	names := make(map[string]bool, len(targets))
	for _, t := range targets {
		names[t.symbol.Name] = true
	}
	var out []types.Blocker
	for _, m := range unresolvedMentions(v, names) {
		if insideTargets(ws, targets, &m.Reference) || !usesAnyTarget(m, targets) {
			continue
		}
		out = append(out, m.blocker())
	}
	return out
}

// usesAnyTarget reports whether m could be a use of a target of its name.
func usesAnyTarget(m unresolvedMention, targets []deletionTarget) bool {
	for _, t := range targets {
		key, ok := symbolKeyOf(t.symbol)
		if !ok || t.symbol.Name != m.name {
			continue
		}
		if mayUse(filepath.Dir(m.File), m.file.AST, key, filepath.Dir(t.file.Path)) {
			return true
		}
	}
	return false
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
