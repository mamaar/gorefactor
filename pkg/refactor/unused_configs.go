package refactor

import (
	"cmp"
	"fmt"
	"go/token"
	"math"
	"path/filepath"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// UnusedRequest selects what UnusedAcrossConfigs reports.
type UnusedRequest struct {
	IncludeExported bool
	// Packages keeps the symbols whose package is one of these import paths
	// or directories; empty keeps every package.
	Packages []string
}

// UnusedFinding is one declaration no build configuration uses.
type UnusedFinding struct {
	analysis.UnusedSymbol
	// Configs names the configurations that declare it; empty when all do.
	Configs []string
	// PossibleUse is where a file no configuration type-checks, or an
	// erroring expression in one that some do, names the symbol.
	PossibleUse *types.Blocker
}

// UnusedReport is the outcome of UnusedAcrossConfigs.
type UnusedReport struct {
	Configs []string // display names, primary first
	Unused  []UnusedFinding
	// PossiblyUsed holds the findings with a PossibleUse.
	PossiblyUsed []UnusedFinding
}

// UnusedAcrossConfigs finds the symbols that are unused under every build
// configuration covering the workspace that declares them. Declarations
// sharing a key across configurations are one symbol: a use of any keeps all.
func (e *DefaultEngine) UnusedAcrossConfigs(req UnusedRequest) (*UnusedReport, error) {
	run := &unusedRun{engine: e, req: req, used: make(types.SymbolKeySet), decls: make(map[declSite]*unusedDecl)}

	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("unused across configs: %w", err)
	}
	defer e.endRun(run)

	for _, v := range views {
		err := run.visit(v)
		if err != nil {
			return nil, viewError(views, v, err)
		}
	}

	return run.report(views), nil
}

// unusedRun gathers the unused declarations of each view.
type unusedRun struct {
	viewIndexes
	engine *DefaultEngine
	req    UnusedRequest
	// used holds the keys some view declares and does not report unused.
	used  types.SymbolKeySet
	decls map[declSite]*unusedDecl
}

// declSite identifies a declaration across views, which share the file set.
type declSite struct {
	file string
	pos  token.Pos
}

// unusedDecl is a declaration at least one view reports unused.
type unusedDecl struct {
	analysis.UnusedSymbol
	key     types.SymbolKey
	keyed   bool
	configs []string // the views that report it
	// unresolved is where a view left an identifier of this name unresolved
	// in a file with a type error.
	unresolved *types.Blocker
}

// merge folds in the finding of another view: the declaration is safe to delete
// only if every view says so, and keeps the strongest cause.
func (d *unusedDecl) merge(u *analysis.UnusedSymbol) {
	d.SafeToDelete = d.SafeToDelete && u.SafeToDelete
	if u.Cause > d.Cause {
		d.Cause, d.Reason = u.Cause, u.Reason
	}

	d.Referrers = appendSymbols(d.Referrers, u.Referrers, analysis.MaxReferrers)
	d.UnusedCallers = appendSymbols(d.UnusedCallers, u.UnusedCallers, math.MaxInt)
}

// appendSymbols appends the symbols of more that dst lacks, up to limit in all.
func appendSymbols(dst, more []*types.Symbol, limit int) []*types.Symbol {
	for _, s := range more {
		if len(dst) >= limit {
			break
		}

		if !declaredIn(dst, s) {
			dst = append(dst, s)
		}
	}

	return dst
}

// declaredIn reports whether list holds the declaration of s, which other
// views know under their own symbol.
func declaredIn(list []*types.Symbol, s *types.Symbol) bool {
	for _, other := range list {
		if other.File == s.File && other.Position == s.Position {
			return true
		}
	}

	return false
}

// visit records what v reports unused and marks the rest of its symbols used.
func (r *unusedRun) visit(v *analysis.View) error {
	a := analysis.NewUnusedAnalyzer(v.Workspace, r.engine.logger)
	a.SetIncludeExported(r.req.IncludeExported)
	a.SetIndex(r.indexOf(v))

	unused, err := a.FindUnusedSymbols()
	if err != nil {
		return fmt.Errorf("visit: %w", err)
	}

	reported := make(types.SymbolKeySet)

	names := make(map[string]bool, len(unused))
	for _, u := range unused {
		names[u.Symbol.Name] = true
		site := declSite{u.Symbol.File, u.Symbol.Position}

		d := r.decls[site]
		if d == nil {
			d = &unusedDecl{UnusedSymbol: *u}
			d.key, d.keyed = symbolKeyOf(u.Symbol)
			r.decls[site] = d
		} else {
			d.merge(u)
		}

		d.configs = append(d.configs, v.Config.String())
		if d.keyed {
			reported[d.key] = struct{}{}
		}
	}

	for key := range declaredSymbolKeys(v.Workspace) {
		if !reported.Has(key) {
			r.used[key] = struct{}{}
		}
	}

	r.markUnresolved(v, unused, unresolvedMentions(v, names))
	r.planned(v, nil)

	return nil
}

// markUnresolved records, for each declaration v reports unused, the first of
// mentions that could be a use of it.
func (r *unusedRun) markUnresolved(v *analysis.View, unused []*analysis.UnusedSymbol, mentions []unresolvedMention) {
	for _, m := range mentions {
		for _, u := range unused {
			d := r.decls[declSite{u.Symbol.File, u.Symbol.Position}]
			if u.Symbol.Name != m.name || !d.keyed || d.unresolved != nil || m.Position == u.Symbol.Position {
				continue
			}

			if !mayUse(filepath.Dir(m.File), m.file.AST, d.key, filepath.Dir(u.Symbol.File)) {
				continue
			}

			use := m.blocker()
			use.Configs = []string{v.Config.String()}
			d.unresolved = &use
		}
	}
}

// report keeps the declarations unused in every view that has them and sets
// apart those a file no view type-checks may use.
func (r *unusedRun) report(views []*analysis.View) *UnusedReport {
	out := &UnusedReport{Configs: make([]string, len(views))}
	for i, v := range views {
		out.Configs[i] = v.Config.String()
	}

	r.keepCalleesOfUsedCallers()

	var kept []*unusedDecl
	keys := make(types.SymbolKeySet)

	for _, d := range r.decls {
		if !r.unusedEverywhere(d, views) || !r.wanted(d) {
			continue
		}

		kept = append(kept, d)
		if d.keyed {
			keys[d.key] = struct{}{}
		}
	}

	slices.SortFunc(kept, compareDecls)

	possible := make(map[types.SymbolKey]*types.Blocker)
	for _, m := range uncheckedMentions(views, keys) {
		if possible[m.key] == nil {
			possible[m.key] = &m.site
		}
	}

	for _, d := range kept {
		f := UnusedFinding{UnusedSymbol: d.UnusedSymbol}
		if len(d.configs) < len(views) {
			f.Configs = d.configs
		}

		use := d.unresolved
		if use == nil && d.keyed {
			use = possible[d.key]
		}

		if use != nil {
			f.PossibleUse, f.SafeToDelete = use, false
			out.PossiblyUsed = append(out.PossiblyUsed, f)

			continue
		}

		out.Unused = append(out.Unused, f)
	}

	return out
}

// keepCalleesOfUsedCallers marks as used each declaration whose only users are
// unused code, one of which is used in another view or, in the default mode, is
// merely unreported for being exported. Repeats until nothing changes.
func (r *unusedRun) keepCalleesOfUsedCallers() {
	for changed := true; changed; {
		changed = false

		for _, d := range r.decls {
			if !d.keyed || r.used.Has(d.key) || d.Cause != analysis.CauseOnlyUnusedCode || !r.anyCallerUsed(d) {
				continue
			}

			r.used[d.key] = struct{}{}
			changed = true
		}
	}
}

func (r *unusedRun) anyCallerUsed(d *unusedDecl) bool {
	for _, c := range d.UnusedCallers {
		key, ok := symbolKeyOf(c)
		if ok && r.used.Has(key) {
			return true
		}
	}

	return false
}

// unusedEverywhere reports whether no view uses d: its key is not used, or
// for a declaration without a key, every view that includes its file reports it.
func (r *unusedRun) unusedEverywhere(d *unusedDecl, views []*analysis.View) bool {
	if d.keyed {
		return !r.used.Has(d.key)
	}

	src := r.engine.set.Sources().File(d.Symbol.File)
	if src == nil {
		return false
	}

	including := 0

	for _, v := range views {
		if v.Config.MatchesFile(src) {
			including++
		}
	}

	return len(d.configs) == including
}

func (r *unusedRun) wanted(d *unusedDecl) bool {
	return len(r.req.Packages) == 0 || slices.Contains(r.req.Packages, d.Symbol.Package)
}

func compareDecls(a, b *unusedDecl) int {
	return cmp.Or(cmp.Compare(a.Symbol.File, b.Symbol.File), cmp.Compare(a.Symbol.Position, b.Symbol.Position))
}
