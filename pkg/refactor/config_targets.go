package refactor

import (
	"go/ast"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// planAccum combines the plans of several operations run in one view, keeping
// each distinct change once.
type planAccum struct {
	out  *types.RefactoringPlan
	seen map[changeIdentity]bool
}

// add combines plan into the accumulated plan.
func (a *planAccum) add(plan *types.RefactoringPlan) {
	if a.out == nil {
		a.out = plan
		a.seen = make(map[changeIdentity]bool)
		for _, c := range plan.Changes {
			a.seen[identityOf(c)] = true
		}
		return
	}
	a.merge(plan.Changes, plan.AffectedFiles)
}

// merge adds the changes not already in the plan, and the files they touch.
func (a *planAccum) merge(changes []types.Change, files []string) {
	for _, c := range changes {
		id := identityOf(c)
		if a.seen[id] {
			continue
		}
		a.seen[id] = true
		a.out.Changes = append(a.out.Changes, c)
		files = append(files, c.File)
	}
	for _, f := range files {
		if !slices.Contains(a.out.AffectedFiles, f) {
			a.out.AffectedFiles = append(a.out.AffectedFiles, f)
		}
	}
}

// plan returns the combined plan, empty when nothing was added.
func (a *planAccum) plan() *types.RefactoringPlan {
	if a.out == nil {
		return &types.RefactoringPlan{Reversible: true}
	}
	if a.out.Impact != nil {
		a.out.Impact.AffectedFiles = a.out.AffectedFiles
	}
	return a.out
}

// reach returns the directories of every file the plan rewrites and names.
func (a *planAccum) reach(names []string) reach {
	return reachOf(a.out, names)
}

// reachOf returns the directories of every file plan rewrites, and the distinct names.
func reachOf(plan *types.RefactoringPlan, names []string) reach {
	var out reach
	if plan == nil {
		return out
	}
	for _, c := range plan.Changes {
		if dir := filepath.Dir(c.File); !slices.Contains(out.dirs, dir) {
			out.dirs = append(out.dirs, dir)
		}
	}
	for _, name := range names {
		if !slices.Contains(out.names, name) {
			out.names = append(out.names, name)
		}
	}
	return out
}

// funcTarget is a function or interface method declared in a view.
type funcTarget struct {
	key  types.SymbolKey
	name string // as ChangeSignatureOperation names it: "f" or "Type.Method"
	file string
	ft   *ast.FuncType
}

// funcTargetsByKey returns the functions and interface methods ws declares.
func funcTargetsByKey(ws *types.Workspace) map[types.SymbolKey]funcTarget {
	targets := make(map[types.SymbolKey]funcTarget)
	for fn, decl := range buildFuncDeclIndex(ws) {
		key, ok := types.SymbolKeyOf(fn)
		if !ok {
			continue
		}
		ft := decl.IfaceFuncType
		if decl.Decl != nil {
			ft = decl.Decl.Type
		}
		targets[key] = funcTarget{key: key, name: qualifiedName(key), file: decl.File.Path, ft: ft}
	}
	return targets
}

// linkedFuncTargets returns the declared targets whose key is linked, in key order.
func linkedFuncTargets(declared map[types.SymbolKey]funcTarget, linked types.SymbolKeySet) []funcTarget {
	var targets []funcTarget
	for key := range linked {
		if t, ok := declared[key]; ok {
			targets = append(targets, t)
		}
	}
	slices.SortFunc(targets, compareTargets)
	return targets
}

func compareTargets(a, b funcTarget) int { return strings.Compare(a.key.String(), b.key.String()) }

func qualifiedName(key types.SymbolKey) string {
	if key.Recv == "" {
		return key.Name
	}
	return key.Recv + "." + key.Name
}

// signatureRun accumulates the signature changes of every target in one view.
type signatureRun struct {
	planAccum
	view *analysis.View
	idx  *analysis.ReferenceIndex
	// covered holds the keys of the declarations altered so far; a target
	// among them needs no run of its own.
	covered types.SymbolKeySet
	altered []AlteredDecl
	decls   map[types.SymbolKey]funcTarget
}

// execute runs op, adds its plan and returns the declarations it altered.
func (r *signatureRun) execute(op *ChangeSignatureOperation) ([]AlteredDecl, error) {
	op.CachedIndex = r.idx
	plan, err := op.Execute(r.view.Workspace)
	if err != nil {
		return nil, err
	}
	r.add(plan)
	for _, d := range op.AlteredDecls() {
		r.covered[d.Key] = struct{}{}
		r.altered = append(r.altered, d)
	}
	return op.AlteredDecls(), nil
}

// signatureStrategy is what an operation built on ChangeSignatureOperation
// supplies to planSignatureView.
type signatureStrategy interface {
	// requestedIn returns the requested function in ws, or a SymbolNotFound
	// error when ws does not declare it.
	requestedIn(ws *types.Workspace) (*funcTarget, error)
	// run plans t into r; requested is set for the requested function.
	run(r *signatureRun, t funcTarget, requested bool) error
	// complete adds the changes that follow from all targets having run.
	complete(r *signatureRun)
}

// planSignatureView plans the requested function and the linked declarations
// in v with s. A view that declares neither reports the not-found error.
func planSignatureView(c *viewIndexes, s signatureStrategy, v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	ws := v.Workspace
	idx := c.indexOf(v)

	requested, err := s.requestedIn(ws)
	if err != nil && !isSymbolNotFound(err) {
		return configResult{}, err
	}
	notFound := err
	decls := funcTargetsByKey(ws)
	declared := make(types.SymbolKeySet, len(decls))
	for key := range decls {
		declared[key] = struct{}{}
	}
	targets := linkedFuncTargets(decls, linked)
	if requested != nil {
		targets = append([]funcTarget{*requested}, targets...)
	}
	if len(targets) == 0 {
		c.planned(v, nil)
		return configResult{absent: notFound, declared: declared}, nil
	}

	run := &signatureRun{view: v, idx: idx, covered: make(types.SymbolKeySet), decls: decls}
	for i, t := range targets {
		isRequested := i == 0 && requested != nil
		if !isRequested && run.covered.Has(t.key) {
			continue
		}
		if err := s.run(run, t, isRequested); err != nil {
			return configResult{}, err
		}
	}
	s.complete(run)
	names := make([]string, len(run.altered))
	for i, d := range run.altered {
		names[i] = d.Key.Name
	}
	res := configResult{plan: run.plan(), changed: run.covered, reach: run.reach(names), declared: declared}
	c.planned(v, res.plan)
	return res, nil
}

// addImports imports each path in the file of every altered declaration that
// lacks it. Call-site files are covered by the operation's default-value import.
func (r *signatureRun) addImports(paths []string) {
	ws := r.view.Workspace
	for _, d := range r.altered {
		for _, path := range paths {
			if hasImport(ws, d.File, path) {
				continue
			}
			if c := generateAddImportChange(ws, d.File, "", path); c != nil {
				r.merge([]types.Change{*c}, nil)
			}
		}
	}
}
