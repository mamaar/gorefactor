package refactor

import (
	"errors"
	"fmt"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// inlinedDecl is the declaration one view takes the inlined body from.
type inlinedDecl struct {
	view *analysis.View
	file string
	impl MethodImpl
}

// inlineDecls records the declaration each view inlines. An inline whose
// views take different bodies is refused: a call site several configurations
// share has no single replacement.
type inlineDecls struct {
	name  string
	decls []inlinedDecl
}

func (d *inlineDecls) record(v *analysis.View, file string, impl MethodImpl) {
	decl := inlinedDecl{view: v, file: file, impl: impl}
	for i := range d.decls {
		if d.decls[i].view == v {
			d.decls[i] = decl
			return
		}
	}
	d.decls = append(d.decls, decl)
}

// refuse stops the inline when two views recorded declarations that differ.
// The declaration is kept, so uses the views could not resolve do not block.
func (d *inlineDecls) refuse([]types.Blocker) error {
	for _, other := range d.decls {
		first := d.decls[0]
		if other.impl.Body == first.impl.Body && slices.Equal(other.impl.ParamNames, first.impl.ParamNames) {
			continue
		}
		return &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf(
				"cannot inline %s: its declarations differ between build configurations (%s under %s, %s under %s), "+
					"so a call site they share has no single replacement",
				d.name, first.file, first.view.Config, other.file, other.view.Config),
		}
	}
	return nil
}

// isAbsentTarget reports whether err says a view has no file or symbol for
// the request.
func isAbsentTarget(err error) bool {
	var re *types.RefactorError
	return errors.As(err, &re) && (re.Type == types.SymbolNotFound || re.Type == types.FileSystemError)
}

// inlineFunctionPlanner plans inline_function in one view: it inlines the
// requested function, or the declaration with its key in a view that lacks
// the requested file, at the calls the view resolves to it.
type inlineFunctionPlanner struct {
	viewIndexes
	inlineDecls
	req types.InlineFunctionRequest
}

func (p *inlineFunctionPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	ws := v.Workspace
	op := &InlineFunctionOperation{
		FunctionName: p.req.FunctionName,
		SourceFile:   p.req.SourceFile,
		TargetFiles:  p.targetFiles(ws),
		CachedIndex:  p.indexOf(v),
	}
	declared := declaredSymbolKeys(ws)
	if err := op.Validate(ws); err != nil {
		return configResult{}, fmt.Errorf("inline function operation validation failed: %w", err)
	}

	sym, _ := op.functionSymbol(ws)
	if sym == nil {
		if sym = linkedFunction(ws, linked, op.FunctionName); sym != nil {
			op.SourceFile = sym.File
		}
	}
	impl, err := op.findFunctionImplementation(ws)
	if err != nil {
		err = fmt.Errorf("failed to generate inline function plan: %w", err)
		if sym != nil || !isAbsentTarget(err) {
			return configResult{}, err
		}
		p.planned(v, nil)
		return configResult{absent: err, declared: declared}, nil
	}
	plan, err := op.Execute(ws)
	if err != nil {
		return configResult{}, fmt.Errorf("failed to generate inline function plan: %w", err)
	}

	p.record(v, sym.File, impl)
	op.dropAnalysis()
	p.planned(v, plan)
	return configResult{
		plan:     plan,
		changed:  keysOf([]*types.Symbol{sym}),
		reach:    reachOf(plan, []string{op.FunctionName}),
		declared: declared,
	}, nil
}

// targetFiles returns the request's target files, or every file of ws, test
// files included, when it names none.
func (p *inlineFunctionPlanner) targetFiles(ws *types.Workspace) []string {
	if len(p.req.TargetFiles) > 0 {
		return p.req.TargetFiles
	}
	var files []string
	for _, pkg := range ws.Packages {
		for _, f := range packageAllFiles(pkg) {
			files = append(files, f.Path)
		}
	}
	slices.Sort(files)
	return files
}

// linkedFunction returns the function of ws named name that a linked key
// names, or nil.
func linkedFunction(ws *types.Workspace, linked types.SymbolKeySet, name string) *types.Symbol {
	for _, key := range orderedKeys(linked) {
		if sym := symbolForKey(ws, key); sym != nil && sym.Kind == types.FunctionSymbol && sym.Name == name {
			return sym
		}
	}
	return nil
}

// inlineMethodPlanner plans inline_method in one view that includes the
// target file.
type inlineMethodPlanner struct {
	viewIndexes
	inlineDecls
	req types.InlineMethodRequest
}

func (p *inlineMethodPlanner) planIn(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
	ws := v.Workspace
	op := &InlineMethodOperation{
		MethodName:   p.req.MethodName,
		SourceStruct: p.req.SourceStruct,
		TargetFile:   p.req.TargetFile,
		CachedIndex:  p.indexOf(v),
	}
	if err := op.Validate(ws); err != nil {
		err = fmt.Errorf("inline method operation validation failed: %w", err)
		if !isAbsentTarget(err) {
			return configResult{}, err
		}
		p.planned(v, nil)
		// Linking a key cannot make the target file part of the view.
		return configResult{absent: err, declared: types.SymbolKeySet{}}, nil
	}
	impl, err := op.findMethodImplementation(ws)
	if err != nil {
		return configResult{}, fmt.Errorf("failed to generate inline method plan: %w", err)
	}
	plan, err := op.Execute(ws)
	if err != nil {
		return configResult{}, fmt.Errorf("failed to generate inline method plan: %w", err)
	}

	sym, _ := op.methodSymbol(ws)
	p.record(v, sym.File, impl)
	op.dropAnalysis()
	p.planned(v, plan)
	return configResult{
		plan:     plan,
		changed:  keysOf([]*types.Symbol{sym}),
		reach:    reachOf(plan, []string{op.MethodName}),
		declared: types.SymbolKeySet{},
	}, nil
}
