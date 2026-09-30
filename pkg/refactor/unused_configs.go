package refactor

import (
	"cmp"
	"go/token"
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
	// PossibleUse is where a file no configuration type-checks names the
	// symbol; nil when none does.
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
		return nil, err
	}
	defer e.endRun(run)
	for _, v := range views {
		if err := run.visit(v); err != nil {
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
}

// visit records what v reports unused and marks the rest of its symbols used.
func (r *unusedRun) visit(v *analysis.View) error {
	a := analysis.NewUnusedAnalyzer(v.Workspace, r.engine.logger)
	a.SetIncludeExported(r.req.IncludeExported)
	a.SetIndex(r.indexOf(v))
	find := a.GetUnusedUnexportedSymbols
	if r.req.IncludeExported {
		find = a.FindUnusedSymbols
	}
	unused, err := find()
	if err != nil {
		return err
	}

	reported := make(types.SymbolKeySet)
	for _, u := range unused {
		site := declSite{u.Symbol.File, u.Symbol.Position}
		d := r.decls[site]
		if d == nil {
			d = &unusedDecl{UnusedSymbol: *u}
			d.key, d.keyed = symbolKeyOf(u.Symbol)
			r.decls[site] = d
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
	r.planned(v, nil)
	return nil
}

// report keeps the declarations unused in every view that has them and sets
// apart those a file no view type-checks may use.
func (r *unusedRun) report(views []*analysis.View) *UnusedReport {
	out := &UnusedReport{Configs: make([]string, len(views))}
	for i, v := range views {
		out.Configs[i] = v.Config.String()
	}

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
		if use := possible[d.key]; d.keyed && use != nil {
			f.PossibleUse, f.SafeToDelete = use, false
			out.PossiblyUsed = append(out.PossiblyUsed, f)
			continue
		}
		out.Unused = append(out.Unused, f)
	}
	return out
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
