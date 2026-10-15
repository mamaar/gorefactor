package refactor

import (
	"cmp"
	"fmt"
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
}

// ReachableFinding is a reference to a target from code the entry program runs.
type ReachableFinding struct {
	analysis.ReachableSite
	// Configs names the configurations that reach it; empty when all do.
	Configs []string
}

// ReachableReport is the outcome of ReachableAcrossConfigs.
type ReachableReport struct {
	Configs []string // display names, primary first
	Sites   []ReachableFinding
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
	run := &reachableRun{req: req, found: make(map[declSite]*ReachableFinding), resolved: make(map[string]bool), dirs: make(map[string]bool)}
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
	if entryViews == 0 {
		return nil, fmt.Errorf("entry package %q not found in any build configuration", req.Entry)
	}
	for _, spec := range req.Targets {
		if !run.resolved[spec] {
			return nil, fmt.Errorf("unknown target %q", spec)
		}
	}

	for _, f := range run.found {
		if len(f.Configs) == len(views) {
			f.Configs = nil
		}
		out.Sites = append(out.Sites, *f)
	}
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
	resolved map[string]bool // specs some view resolves
	dirs     map[string]bool // directories examined in any view
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
	sites, err := a.ReachableSites(entry, targets, r.req.IncludeTests)
	if err != nil {
		return true, err
	}
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
