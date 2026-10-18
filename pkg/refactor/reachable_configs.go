package refactor

import (
	"cmp"
	"errors"
	"fmt"
	gotypes "go/types"
	"maps"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// ReachableRequest selects what ReachableAcrossConfigs reports.
type ReachableRequest struct {
	Entry        string // a package main, as import path, directory or workspace-relative path
	Targets      []string
	IncludeTests bool
	// Within limits the report to what this function (pkg.Func or pkg.Type.Method)
	// reaches; the live types still come from the whole entry program.
	Within string
}

// ReachableFinding is a reference to a target from code the entry program runs.
type ReachableFinding struct {
	analysis.ReachableSite
	// Configs names the configurations that reach it; empty when all do.
	Configs []string
}

// BodilessFinding is a package whose code the analysis could not see.
type BodilessFinding struct {
	analysis.BodilessPackage
	// Configs names the configurations that cannot see it; empty when all do.
	Configs []string
}

// ReachableReport is the outcome of ReachableAcrossConfigs.
type ReachableReport struct {
	Configs []string // display names, primary first
	Sites   []ReachableFinding
	// Bodiless lists packages whose code the analysis cannot see, so calls
	// inside them are missing from Sites.
	Bodiless []BodilessFinding
	// Unchecked holds files of examined packages that no configuration type-checks.
	Unchecked []analysis.UncheckedFile
}

// ReachableAcrossConfigs finds the references to the targets that run when the
// entry program runs, under each build configuration covering the workspace.
// A site found in several configurations is one finding.
func (e *DefaultEngine) ReachableAcrossConfigs(req ReachableRequest) (*ReachableReport, error) {
	if req.Entry == "" || len(req.Targets) == 0 {
		return nil, fmt.Errorf("entry and targets are required")
	}
	run := &reachableRun{req: req, found: make(map[declSite]*ReachableFinding), bodiless: make(map[analysis.BodilessPackage]*BodilessFinding), resolved: make(map[string]bool), dirs: make(map[string]bool)}
	views, err := e.beginRun(run)
	if err != nil {
		return nil, err
	}
	defer e.endRun(run)

	out := &ReachableReport{Configs: make([]string, len(views))}
	entryViews := 0
	for i, v := range views {
		out.Configs[i] = v.Config.String()
		ok, err := run.visit(e, v)
		if err != nil {
			return nil, viewError(views, v, err)
		}
		if ok {
			entryViews++
		}
	}
	if entryViews == 0 && run.entryErr != nil {
		return nil, run.entryErr
	}
	if entryViews == 0 {
		return nil, fmt.Errorf("entry package %q not found in any build configuration", req.Entry)
	}
	for _, spec := range req.Targets {
		if !run.resolved[spec] {
			return nil, fmt.Errorf("unknown target %q", spec)
		}
	}
	if req.Within != "" && !run.withinReached {
		return nil, cmp.Or(run.withinErr, fmt.Errorf("unknown within_function %q", req.Within))
	}

	for _, f := range run.found {
		if len(f.Configs) == len(views) {
			f.Configs = nil
		}
		out.Sites = append(out.Sites, *f)
	}
	for _, b := range run.bodiless {
		if len(b.Configs) == len(views) {
			b.Configs = nil
		}
		out.Bodiless = append(out.Bodiless, *b)
	}
	slices.SortFunc(out.Bodiless, func(a, b BodilessFinding) int { return cmp.Compare(a.Package, b.Package) })
	slices.SortFunc(out.Sites, func(a, b ReachableFinding) int {
		return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.File, b.File), cmp.Compare(a.Pos, b.Pos), cmp.Compare(a.Target, b.Target))
	})
	out.Unchecked = analysis.UncheckedFiles(views, slices.Sorted(maps.Keys(run.dirs)))
	return out, nil
}

type reachableRun struct {
	viewIndexes
	req      ReachableRequest
	found    map[declSite]*ReachableFinding
	bodiless map[analysis.BodilessPackage]*BodilessFinding
	entryErr error // the first configuration whose entry package is bodiless
	resolved map[string]bool // specs some view resolves
	dirs     map[string]bool // directories examined in any view

	withinReached bool  // some view's entry program reaches req.Within
	withinErr     error // the last view that declares req.Within but does not reach it
}

// visit adds the sites v reaches. It reports false when v lacks the entry
// package, which a build tag may exclude.
func (r *reachableRun) visit(e *DefaultEngine, v *analysis.View) (bool, error) {
	defer r.planned(v, nil)
	idx := r.indexOf(v)
	entry := v.Workspace.Packages[types.ResolvePackagePath(v.Workspace, r.req.Entry)]
	if entry == nil {
		return false, nil
	}

	var specs []string
	for _, spec := range r.req.Targets {
		if _, err := analysis.ResolveTargets(v.Workspace, []string{spec}); err == nil {
			r.resolved[spec] = true
			specs = append(specs, spec)
		}
	}
	targets, err := analysis.ResolveTargets(v.Workspace, specs)
	if err != nil {
		return true, err
	}

	a := analysis.NewUnusedAnalyzer(v.Workspace, e.logger)
	a.SetIndex(idx)
	if r.req.Within != "" {
		key, ok, err := withinKey(v.Workspace, r.req.Within)
		if err != nil {
			return true, err
		}
		if !ok {
			return true, nil // another build configuration may declare it
		}
		a.SetWithin(key)
	}
	sites, bodiless, err := a.ReachableSites(entry, targets, r.req.IncludeTests)
	var unreached *analysis.WithinUnreachedError
	if errors.As(err, &unreached) {
		r.withinErr = err
		return true, nil
	}
	var entryBodiless *analysis.EntryBodilessError
	if errors.As(err, &entryBodiless) {
		r.entryErr = cmp.Or(r.entryErr, err)
		bodiless, err = []analysis.BodilessPackage{{Package: entryBodiless.Package, Reason: entryBodiless.Reason}}, nil
	}
	if err != nil {
		return true, err
	}
	for _, b := range bodiless {
		f := r.bodiless[b]
		if f == nil {
			f = &BodilessFinding{BodilessPackage: b}
			r.bodiless[b] = f
		}
		f.Configs = append(f.Configs, v.Config.String())
	}
	if entryBodiless != nil {
		return false, nil
	}
	r.withinReached = true
	for _, dir := range a.ReachableDirs(entry, r.req.IncludeTests) {
		r.dirs[dir] = true
	}
	for _, s := range sites {
		key := declSite{s.File, s.Pos}
		f := r.found[key]
		if f == nil {
			f = &ReachableFinding{ReachableSite: s}
			r.found[key] = f
		}
		f.Configs = append(f.Configs, v.Config.String())
	}
	return true, nil
}

// withinKey resolves spec, one function or method, to its key in ws. ok is
// false when ws does not declare it.
func withinKey(ws *types.Workspace, spec string) (key types.SymbolKey, ok bool, err error) {
	targets, rerr := analysis.ResolveTargets(ws, []string{spec})
	if rerr != nil {
		return key, false, nil
	}
	if len(targets) != 1 {
		return key, false, fmt.Errorf("within_function %q must name one function or method", spec)
	}
	fn, isFunc := targets[0].Obj.(*gotypes.Func)
	if !isFunc {
		return key, false, fmt.Errorf("within_function %q is not a function or method", spec)
	}
	key, ok = types.SymbolKeyOf(canonicalFunc(fn))
	return key, ok, nil
}
