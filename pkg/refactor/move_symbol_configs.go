package refactor

import (
	"fmt"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// moveSymbolPlanner plans move_symbol in the primary view and has every other
// view check that the plan holds there: the moved declarations are the same
// ones, every reference is one the primary rewrites, the names are free in the
// target package and the move creates no import cycle.
type moveSymbolPlanner struct {
	viewIndexes
	req types.MoveSymbolRequest

	// moved maps the key of each moved symbol to its declaring file.
	moved map[types.SymbolKey]string
	// rewritten holds the reference sites the primary view found.
	rewritten map[refSite]bool
	// objections holds what, other than a reference, makes the plan wrong
	// under some configuration.
	objections []string
}

type refSite struct {
	file   string
	offset int
}

func (p *moveSymbolPlanner) planIn(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
	// Type-checked so the run can tell which files no view checks.
	p.indexOf(v)
	var res configResult
	var err error
	if v == p.views[0] {
		res, err = p.planPrimary(v)
	} else {
		res = p.check(v)
	}
	if err != nil {
		return configResult{}, err
	}
	res.declared = types.SymbolKeySet{}
	p.planned(v, res.plan)
	return res, nil
}

func (p *moveSymbolPlanner) planPrimary(v *analysis.View) (configResult, error) {
	op := &MoveSymbolOperation{Request: p.req}
	if err := op.Validate(v.Workspace); err != nil {
		err = fmt.Errorf("move operation validation failed: %w", err)
		if isSymbolNotFound(err) {
			return configResult{absent: err}, nil
		}
		return configResult{}, err
	}
	plan, err := op.Execute(v.Workspace)
	if err != nil {
		return configResult{}, fmt.Errorf("failed to generate move plan: %w", err)
	}
	if plan.Impact, err = v.Analyzer.AnalyzeImpact(op); err != nil {
		return configResult{}, fmt.Errorf("failed to analyze impact: %w", err)
	}
	plan.Operations = []types.Operation{op}

	p.moved = make(map[types.SymbolKey]string, len(op.moved))
	for _, sym := range op.moved {
		if key, ok := symbolKeyOf(sym); ok {
			p.moved[key] = sym.File
		}
	}
	p.rewritten = make(map[refSite]bool, len(op.references))
	for _, ref := range op.references {
		p.rewritten[refSite{ref.File, ref.Offset}] = true
	}
	op.moved, op.references = nil, nil
	return configResult{plan: plan, changed: keySet(p.moved)}, nil
}

// check reports what makes the primary view's plan wrong in v: references it
// does not rewrite as blockers, anything else as an objection.
func (p *moveSymbolPlanner) check(v *analysis.View) configResult {
	ws := v.Workspace
	resolver := quietResolver(ws)
	if len(p.moved) == 0 {
		if pkg := ws.Packages[p.req.FromPackage]; pkg != nil && pkg.Symbols != nil {
			if sym, err := resolver.ResolveSymbol(pkg, p.req.SymbolName); err == nil {
				p.object(v, "%s is declared only outside the default build configuration, in %s", sym.Name, sym.File)
			}
		}
		return configResult{}
	}

	var res configResult
	target := ws.Packages[p.req.ToPackage]
	for _, key := range orderedKeys(keySet(p.moved)) {
		sym := symbolForKey(ws, key)
		switch {
		case sym == nil:
			p.object(v, "%s is not declared (%s is not part of the build); the move would add it", key.Name, p.moved[key])
			continue
		case sym.File != p.moved[key]:
			p.object(v, "%s is declared in %s instead of %s; the move would leave that declaration behind", key.Name, sym.File, p.moved[key])
			continue
		}
		if target != nil && target.Symbols != nil {
			if _, err := resolver.ResolveSymbol(target, key.Name); err == nil {
				p.object(v, "symbol %s already exists in target package %s", key.Name, p.req.ToPackage)
			}
		}
		refs, err := resolver.FindReferences(sym)
		if err != nil {
			p.object(v, "references to %s could not be found: %v", key.Name, err)
			continue
		}
		for _, ref := range refs {
			if !p.rewritten[refSite{ref.File, ref.Offset}] {
				res.blockers = append(res.blockers, types.Blocker{Reference: *ref})
			}
		}
	}
	if wouldCreateImportCycle(ws, p.req.FromPackage, p.req.ToPackage) {
		p.object(v, "the move would create an import cycle between %s and %s", p.req.FromPackage, p.req.ToPackage)
	}
	return res
}

func keySet(moved map[types.SymbolKey]string) types.SymbolKeySet {
	keys := make(types.SymbolKeySet, len(moved))
	for key := range moved {
		keys[key] = struct{}{}
	}
	return keys
}

func (p *moveSymbolPlanner) object(v *analysis.View, format string, args ...any) {
	p.objections = append(p.objections, fmt.Sprintf("under %s, ", v.Config)+fmt.Sprintf(format, args...))
}

// refuse stops the move when it would be wrong under some configuration.
// Mentions in files no view type-checks do not: they go on the plan as
// skipped sites.
func (p *moveSymbolPlanner) refuse(blockers []types.Blocker) error {
	var b strings.Builder
	for _, o := range p.objections {
		fmt.Fprintf(&b, "\n  %s", o)
	}
	missed := 0
	for _, blocker := range blockers {
		if blocker.Unverified != "" {
			continue
		}
		if missed++; missed <= maxListedBlockers {
			fmt.Fprintf(&b, "\n  %s:%d%s is a reference the move would not rewrite", blocker.File, blocker.Line, blocker.Note())
		}
	}
	if missed > maxListedBlockers {
		fmt.Fprintf(&b, "\n  …and %d more references", missed-maxListedBlockers)
	}
	if b.Len() == 0 {
		return nil
	}
	return &types.RefactorError{
		Type: types.InvalidOperation,
		Message: fmt.Sprintf("cannot move %s: the move is planned for the default build configuration and does not hold under the others:%s",
			p.req.SymbolName, b.String()),
	}
}
