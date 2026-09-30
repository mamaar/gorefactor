package refactor

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

const (
	ctxParamName = "ctx"
	ctxParamType = "context.Context"
	ctxImport    = "context"
)

// addContextParameterPlanner plans add_context_parameter in one view.
type addContextParameterPlanner struct {
	viewIndexes
	engine *DefaultEngine
	req    types.AddContextParameterRequest
}

// ctxTarget is a declaration that gains the ctx parameter.
type ctxTarget struct {
	key    types.SymbolKey
	name   string // as ChangeSignatureOperation names it: "f" or "Type.Method"
	file   string
	params []analysis.FuncParam
}

func (p *addContextParameterPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	ws := v.Workspace
	idx := p.indexOf(v)

	requested, err := p.requestedIn(ws)
	if err != nil && !isSymbolNotFound(err) {
		return configResult{}, err
	}
	notFound := err
	targets := linkedTargets(ws, linked)
	if requested != nil {
		targets = append([]ctxTarget{*requested}, targets...)
	}
	declared := declaredFuncKeys(ws)
	if len(targets) == 0 {
		p.planned(v, nil)
		return configResult{absent: notFound, declared: declared}, nil
	}

	run := &ctxRun{planner: p, ws: ws, idx: idx, covered: make(types.SymbolKeySet)}
	for i, t := range targets {
		if err := run.add(t, i == 0 && requested != nil); err != nil {
			return configResult{}, err
		}
	}
	run.addContextImports()
	res := configResult{plan: run.plan(), changed: run.covered, reach: run.reach(), declared: declared}
	p.planned(v, res.plan)
	return res, nil
}

// requestedIn finds the requested function in ws. It is a SymbolNotFound error
// when ws does not declare it, and a plain error when it already takes a ctx.
func (p *addContextParameterPlanner) requestedIn(ws *types.Workspace) (*ctxTarget, error) {
	params, err := analysis.ExtractFuncParams(ws, p.req.SourceFile, p.req.FunctionName)
	if err != nil {
		return nil, err
	}
	if hasLeadingCtx(params) {
		return nil, fmt.Errorf("function %s already has context.Context as first parameter", p.req.FunctionName)
	}
	return &ctxTarget{name: p.req.FunctionName, file: p.req.SourceFile, params: params}, nil
}

func hasLeadingCtx(params []analysis.FuncParam) bool {
	return len(params) > 0 && params[0].Type == ctxParamType
}

// linkedTargets returns the declarations in ws whose key is linked and that do
// not take a ctx yet, in key order.
func linkedTargets(ws *types.Workspace, linked types.SymbolKeySet) []ctxTarget {
	if len(linked) == 0 {
		return nil
	}
	var targets []ctxTarget
	for fn, decl := range buildFuncDeclIndex(ws) {
		key, ok := types.SymbolKeyOf(fn)
		if !ok || !linked.Has(key) {
			continue
		}
		ft := decl.IfaceFuncType
		if decl.Decl != nil {
			ft = decl.Decl.Type
		}
		params := analysis.FuncTypeParams(ft)
		if hasLeadingCtx(params) {
			continue
		}
		targets = append(targets, ctxTarget{key: key, name: qualifiedName(key), file: decl.File.Path, params: params})
	}
	slices.SortFunc(targets, compareTargets)
	return targets
}

func compareTargets(a, b ctxTarget) int { return strings.Compare(a.key.String(), b.key.String()) }

func qualifiedName(key types.SymbolKey) string {
	if key.Recv == "" {
		return key.Name
	}
	return key.Recv + "." + key.Name
}

// ctxRun accumulates the changes of every target in one view.
type ctxRun struct {
	planner *addContextParameterPlanner
	ws      *types.Workspace
	idx     *analysis.ReferenceIndex
	out     *types.RefactoringPlan
	// covered holds the keys of the declarations altered so far; a target
	// among them needs no run of its own.
	covered types.SymbolKeySet
	altered []AlteredDecl
	seen    map[changeIdentity]bool
}

// add runs the signature change for t unless an earlier target already altered it.
func (r *ctxRun) add(t ctxTarget, requested bool) error {
	if !requested && r.covered.Has(t.key) {
		return nil
	}
	op := r.operation(t)
	if err := op.Validate(r.ws); err != nil {
		return err
	}
	plan, err := op.Execute(r.ws)
	if err != nil {
		return err
	}
	if r.out == nil {
		r.out = plan
		r.seen = make(map[changeIdentity]bool)
		for _, c := range plan.Changes {
			r.seen[identityOf(c)] = true
		}
	} else {
		r.merge(plan.Changes, plan.AffectedFiles)
	}
	for _, d := range op.AlteredDecls() {
		r.covered[d.Key] = struct{}{}
		r.altered = append(r.altered, d)
		r.merge(RemoveInternalCtxCreation(r.ws, d.File, qualifiedName(d.Key)), nil)
	}
	return nil
}

func (r *ctxRun) operation(t ctxTarget) *ChangeSignatureOperation {
	params := []Parameter{{Name: ctxParamName, Type: ctxParamType}}
	for _, fp := range t.params {
		params = append(params, Parameter{Name: fp.Name, Type: fp.Type})
	}
	defaultValue := r.planner.req.DefaultValue
	if defaultValue == "" {
		defaultValue = "context.TODO()"
	}
	return &ChangeSignatureOperation{
		FunctionName:         t.name,
		SourceFile:           t.file,
		NewParams:            params,
		Scope:                types.WorkspaceScope,
		PropagateToInterface: r.planner.req.Propagate,
		DefaultValue:         defaultValue,
		NewParamPosition:     0,
		NewReturnPosition:    -1,
		RemovedReturnIndex:   -1,
		CachedIndex:          r.idx,
		Logger:               r.planner.engine.logger,
	}
}

// merge adds the changes not already in the plan, and the files they touch.
func (r *ctxRun) merge(changes []types.Change, files []string) {
	for _, c := range changes {
		id := identityOf(c)
		if r.seen[id] {
			continue
		}
		r.seen[id] = true
		r.out.Changes = append(r.out.Changes, c)
		files = append(files, c.File)
	}
	for _, f := range files {
		if !slices.Contains(r.out.AffectedFiles, f) {
			r.out.AffectedFiles = append(r.out.AffectedFiles, f)
		}
	}
}

// addContextImports imports context in each altered declaration's file that
// lacks it. Call-site files are covered by the operation's default-value import.
func (r *ctxRun) addContextImports() {
	for _, d := range r.altered {
		if hasImport(r.ws, d.File, ctxImport) {
			continue
		}
		if c := generateAddImportChange(r.ws, d.File, "", ctxImport); c != nil {
			r.merge([]types.Change{*c}, nil)
		}
	}
}

// plan returns the combined plan; empty when every target already took a ctx.
func (r *ctxRun) plan() *types.RefactoringPlan {
	if r.out == nil {
		return &types.RefactoringPlan{Reversible: true}
	}
	if r.out.Impact != nil {
		r.out.Impact.AffectedFiles = r.out.AffectedFiles
	}
	return r.out
}

// reach returns the directories of the altered declarations and of every file
// the plan rewrites, and the names of the altered declarations.
func (r *ctxRun) reach() reach {
	var out reach
	if r.out == nil {
		return out
	}
	for _, c := range r.out.Changes {
		if dir := filepath.Dir(c.File); !slices.Contains(out.dirs, dir) {
			out.dirs = append(out.dirs, dir)
		}
	}
	for _, d := range r.altered {
		if !slices.Contains(out.names, d.Key.Name) {
			out.names = append(out.names, d.Key.Name)
		}
	}
	return out
}
