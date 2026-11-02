package refactor

import (
	"fmt"
	gotypes "go/types"
	"maps"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// This file owns call-site seeding: the climb starts at the declarations
// enclosing the reached call sites of the CallSitesOf targets, and everything
// it cannot or should not change is reported rather than aborting the plan.

// callSiteSeeds returns the declarations that need a ctx so that every reached
// call site of the targets has one in scope. Sites that cannot be fixed are
// recorded in plan.Notes.
func (op *ThreadContextOperation) callSiteSeeds(ws *pkgtypes.Workspace, idx *analysis.ReferenceIndex, plan *threadPlan) ([]*gotypes.Func, error) {
	if op.Reach == nil {
		return nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "call_sites_of needs entry_package: only call sites the entry program reaches are seeded",
		}
	}
	op.resolved = make(map[string]bool)
	var all []analysis.Target
	for _, spec := range op.CallSitesOf {
		targets, err := analysis.ResolveTargets(ws, []string{spec})
		if err != nil {
			continue // another build configuration may declare it
		}
		op.resolved[spec] = true
		all = append(all, targets...)
	}
	// A target never gains a ctx: the climb stops at every one of them.
	op.StopAt = maps.Clone(op.StopAt)
	if op.StopAt == nil {
		op.StopAt = make(pkgtypes.SymbolKeySet)
	}
	op.targets = make(pkgtypes.SymbolKeySet)
	for _, t := range all {
		if fn, ok := t.Obj.(*gotypes.Func); ok {
			if key, ok := pkgtypes.SymbolKeyOf(canonicalFunc(fn)); ok {
				op.StopAt[key] = struct{}{}
				op.targets[key] = struct{}{}
			}
		}
	}
	var seeds []*gotypes.Func
	for _, t := range all {
		for _, site := range idx.CallSitesOf(t.Obj) {
			seed, err := op.callSiteSeed(idx, plan, site)
			if err != nil {
				return nil, err
			}
			if seed != nil {
				seeds = append(seeds, seed)
			}
		}
	}
	return seeds, nil
}

// callSiteSeed returns the declaration that must gain a ctx for site to have
// one, or nil when site has one, is not reached, or cannot be fixed.
func (op *ThreadContextOperation) callSiteSeed(idx *analysis.ReferenceIndex, plan *threadPlan, site analysis.CallSite) (*gotypes.Func, error) {
	if strings.HasSuffix(site.File.Path, "_test.go") {
		return nil, nil
	}
	if site.Fn == nil {
		if op.Reach.reachesPackage(site.Pkg.ImportPath) {
			op.note(plan, site, "(package-level var)", "var initializer: it has no ctx in scope and cannot take a parameter")
		}
		return nil, nil
	}
	enclosing, err := op.callerObject(site)
	if err != nil {
		return nil, err
	}
	if key, ok := pkgtypes.SymbolKeyOf(enclosing); ok && op.targets.Has(key) {
		return nil, nil
	}
	if op.Reach.outside(enclosing) || op.Reach.outsideWithin(enclosing) {
		return nil, nil
	}

	// A closure captures the ctx of its declaration, so the declaration is the seed.
	site.Lit = nil
	if src, found := findCtxSource(site.Pkg, site.Fn, site.Call); !found && src.Kind != ctxUnknown {
		if _, isRoot := classifyRoot(site.Pkg, site.File, site.Fn, nil, site.Call, false); isRoot {
			kind := "root"
			if site.Fn.Recv == nil && site.Fn.Name.Name == "init" {
				kind = "init"
			}
			op.note(plan, site, funcLabel(enclosing), fmt.Sprintf("%s: %s has no ctx in scope and cannot take a parameter", kind, funcLabel(enclosing)))
			return nil, nil
		}
	}
	caller, climb, err := op.climbTarget(idx, plan, site)
	if err != nil || !climb {
		return nil, err
	}
	if fd := op.declIndex[caller]; fd != nil && fd.File != nil && fd.File.Generated {
		op.note(plan, site, funcLabel(caller), fmt.Sprintf("generated: %s is declared in generated code, which is not edited", funcLabel(caller)))
		return nil, nil
	}
	return caller, nil
}

func (op *ThreadContextOperation) note(plan *threadPlan, site analysis.CallSite, snippet, reason string) {
	plan.Notes = append(plan.Notes, pkgtypes.SkippedSite{
		File:    site.File.Path,
		Line:    op.siteLine(site),
		Reason:  reason,
		Snippet: snippet,
	})
}

// gainBlocker says why f cannot gain a ctx parameter without breaking the
// build, or returns "" when it can.
func (op *ThreadContextOperation) gainBlocker(ws *pkgtypes.Workspace, idx *analysis.ReferenceIndex, f *gotypes.Func) string {
	if !funcHasContextParam(f) {
		if uses := idx.NonCallUsesOf(f); len(uses) > 0 {
			pos := ws.FileSet.Position(uses[0].Pos)
			return fmt.Sprintf("func value: %s is used as a value at %s:%d, so its signature stays", funcLabel(f), pos.Filename, pos.Line)
		}
	}
	if ext, ok := op.interfaceSurvey(ws).externalInterfaceOf(f); ok {
		return fmt.Sprintf("external interface: %s implements %s, declared outside the workspace, so its signature stays", funcLabel(f), ext.label())
	}
	if m := stdMethodOf(f.Name(), f); m != nil {
		return fmt.Sprintf("standard method: %s: %s; its signature stays", funcLabel(f), stdMethodWhy(m))
	}
	return ""
}

// blockedError reports a function that must gain a ctx together with an
// interface it shares but cannot.
func blockedError(f *gotypes.Func, reason string) error {
	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf("cannot thread context through %s: it must change together with the interface it "+
			"shares, but %s. No changes were made.", funcLabel(f), reason),
	}
}
