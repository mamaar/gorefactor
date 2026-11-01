package refactor

import (
	"fmt"

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

func (p *addContextParameterPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	return planSignatureView(&p.viewIndexes, p, v, linked)
}

// requestedIn finds the requested function in ws. It is a SymbolNotFound error
// when ws does not declare it, and a plain error when it already takes a ctx.
func (p *addContextParameterPlanner) requestedIn(ws *types.Workspace) (*funcTarget, error) {
	ft, err := analysis.FindFuncType(ws, p.req.SourceFile, p.req.FunctionName)
	if err != nil {
		return nil, err
	}
	if hasLeadingCtx(analysis.FuncTypeParams(ft)) {
		return nil, fmt.Errorf("function %s already has context.Context as first parameter", p.req.FunctionName)
	}
	return &funcTarget{name: p.req.FunctionName, file: p.req.SourceFile, ft: ft}, nil
}

func hasLeadingCtx(params []analysis.FuncParam) bool {
	return len(params) > 0 && params[0].Type == ctxParamType
}

// run adds ctx to t unless a linked declaration already takes one.
func (p *addContextParameterPlanner) run(r *signatureRun, t funcTarget, requested bool) error {
	params := analysis.FuncTypeParams(t.ft)
	if !requested && hasLeadingCtx(params) {
		return nil
	}
	ws := r.view.Workspace
	op := p.operation(t, params, bodyNeedsCtxName(ws, t))
	if err := op.Validate(ws); err != nil {
		return err
	}
	altered, err := r.execute(op)
	if err != nil {
		return err
	}
	for _, d := range altered {
		r.merge(RemoveInternalCtxCreation(ws, d.File, qualifiedName(d.Key)), nil)
	}
	return nil
}

// bodyNeedsCtxName reports whether the body of t refers to a ctx parameter once
// the creation statements it replaces are removed.
func bodyNeedsCtxName(ws *types.Workspace, t funcTarget) bool {
	return len(RemoveInternalCtxCreation(ws, t.file, t.name)) > 0
}

// operation adds the ctx parameter to current. Over a list of unnamed
// parameters it stays unnamed unless the body needs the name.
func (p *addContextParameterPlanner) operation(t funcTarget, current []analysis.FuncParam, needsName bool) *ChangeSignatureOperation {
	params := []Parameter{{Name: ctxParamName, Type: ctxParamType}}
	if len(current) > 0 && current[0].Name == "" && !needsName {
		params[0].Name = ""
	}
	for _, fp := range current {
		params = append(params, Parameter{Name: fp.Name, Type: fp.Type})
	}
	defaultValue := p.req.DefaultValue
	if defaultValue == "" {
		defaultValue = "context.TODO()"
	}
	return &ChangeSignatureOperation{
		FunctionName:         t.name,
		SourceFile:           t.file,
		NewParams:            params,
		Scope:                types.WorkspaceScope,
		PropagateToInterface: p.req.Propagate,
		DefaultValue:         defaultValue,
		NewParamPosition:     0,
		NewReturnPosition:    -1,
		RemovedReturnIndex:   -1,
		Logger:               p.engine.logger,
	}
}

// complete imports context in each altered declaration's file that lacks it.
func (p *addContextParameterPlanner) complete(r *signatureRun) {
	r.addImports([]string{ctxImport})
}
