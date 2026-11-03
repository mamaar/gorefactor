package refactor

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"log/slog"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// AlwaysNilErrorsRequest selects what AlwaysNilErrorsAcrossConfigs reports.
type AlwaysNilErrorsRequest struct {
	// Packages keeps the declarations whose package is one of these import
	// paths or directories; empty keeps every package. Calls count regardless.
	Packages []string
}

// AlwaysNilErrorFinding is a function whose last error result is nil on every
// return, and whether change_signature remove_return accepts removing it.
type AlwaysNilErrorFinding struct {
	Function  string
	Signature string
	File      string
	Line      int
	Column    int
	// Configs names the configurations that declare it; empty when all do.
	Configs   []string
	CallSites int
	Fixable   bool
	// Propagate is set when remove_return needs propagate: the function
	// satisfies workspace interfaces, and every method they change returns nil.
	Propagate bool
	// Reason says what stops remove_return when Fixable is false.
	Reason string
}

// AlwaysNilErrorsReport is the outcome of AlwaysNilErrorsAcrossConfigs.
type AlwaysNilErrorsReport struct {
	DetectHeader
	Findings []AlwaysNilErrorFinding
}

// AlwaysNilErrorsAcrossConfigs finds the functions whose last result is error
// and that return nil on every return under each build configuration declaring
// them, and judges them with the checks change_signature remove_return makes.
func (e *DefaultEngine) AlwaysNilErrorsAcrossConfigs(req AlwaysNilErrorsRequest) (*AlwaysNilErrorsReport, error) {
	run := &alwaysNilRun{
		detectRun:  newDetectRun(req.Packages),
		decls:      make(map[declSite]*alwaysNilDecl),
		declaredIn: make(map[declSite][]string),
		declViews:  make(map[declKey]int),
		nilViews:   make(map[declKey]int),
		calls:      make(map[declKey]map[token.Pos]bool),
		logger:     e.logger,
	}

	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("always nil errors across configs: %w", err)
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

// alwaysNilDecl is a declaration that returns nil in some view.
type alwaysNilDecl struct {
	AlwaysNilErrorFinding
	key   declKey
	shape string
}

// alwaysNilRun gathers the always-nil declarations and the calls of each view.
type alwaysNilRun struct {
	detectRun
	decls map[declSite]*alwaysNilDecl
	// declaredIn names the configurations declaring each site.
	declaredIn map[declSite][]string
	// declViews and nilViews count the views declaring a function and those in
	// which every return of it is nil.
	declViews map[declKey]int
	nilViews  map[declKey]int
	// calls is keyed by Lparen, which every link of a method chain shares.
	calls  map[declKey]map[token.Pos]bool
	logger *slog.Logger
}

func (r *alwaysNilRun) visit(v *analysis.View) error {
	idx := r.indexOf(v)
	if !idx.HasTypeInfo() {
		return fmt.Errorf("no package has type information")
	}

	ws := v.Workspace
	decls := buildFuncDeclIndex(ws)
	survey := surveyInterfaces(ws)

	for fn, decl := range decls {
		if decl.Decl == nil || decl.Decl.Body == nil || decl.File.Generated || !r.wanted(decl.Pkg) {
			continue
		}

		symbol, ok := types.SymbolKeyOf(fn)
		if !ok {
			continue
		}

		key := declKey{symbol, fn.Pkg().Name()}
		site := declSite{decl.File.Path, fn.Pos()}
		r.declaredIn[site] = append(r.declaredIn[site], v.Config.String())
		r.declViews[key]++
		r.recordSignature(key, declShape(decl))

		if !alwaysNilError(decl.Pkg.TypesInfo, fn, decl.Decl) {
			continue
		}

		r.nilViews[key]++
		j := r.judge(v, idx, decls, survey, fn, key, decl)
		r.add(v, fn, decl, key, j)

		if r.calls[key] == nil {
			r.calls[key] = make(map[token.Pos]bool)
		}

		for _, call := range idx.CallSitesOf(fn) {
			r.calls[key][call.Call.Lparen] = true
		}
	}

	r.collectTypeErrors(v)
	r.planned(v, nil)

	return nil
}

// nilJudgement is how remove_return judges fn in one view: why it refuses, or
// that it accepts, with propagate when the interfaces fn satisfies must change too.
type nilJudgement struct {
	reason    string
	propagate bool
}

// judge runs the remove_return operation that changes fn, the way change_signature
// does in a view, and the checks it runs on the finished plan.
func (r *alwaysNilRun) judge(v *analysis.View, idx *analysis.ReferenceIndex, decls map[*gotypes.Func]*funcDecl, survey *ifaceSurvey, fn *gotypes.Func, key declKey, decl *funcDecl) nilJudgement {
	var members []*gotypes.Func

	if fn.Signature().Recv() != nil {
		var err error

		members, err = propagatedMembers(survey, fn, func(f *gotypes.Func, ext ifaceCandidate) error {
			return externalInterfaceError("change", f, ext)
		})
		if err != nil {
			return nilJudgement{reason: err.Error()}
		}

		for _, m := range members {
			why := propagatedRefusal(decls, m)
			if why != "" {
				return nilJudgement{reason: why}
			}
		}
	}

	// The results left after removal, as the declaration writes them.
	returns := signatureOf(decl.Decl.Type).returns
	op := ChangeSignatureRequest{
		FunctionName: key.displayName(), SourceFile: decl.File.Path, Subcommand: "remove_return",
		NewReturns: returns[:len(returns)-1], PropagateToInterface: len(members) > 0, CachedIndex: idx,
	}.operation(r.logger)
	ws := v.Workspace

	err := op.Validate(ws)
	if err != nil {
		return nilJudgement{reason: err.Error()}
	}

	plan, err := op.Execute(ws)
	if err != nil {
		if len(op.refusals) > 0 {
			return nilJudgement{reason: slices.MinFunc(op.refusals, compareRefusals).String()}
		}

		return nilJudgement{reason: err.Error()}
	}

	err = GeneratedFileEdits(plan.Changes)
	if err != nil {
		return nilJudgement{reason: err.Error()}
	}

	check := VerifyPlanTypesWithImporter(ws, plan, v.Parser.Importer())
	if len(check.NewErrors) > 0 {
		return nilJudgement{reason: "the change would introduce type errors: " + check.NewErrors[0]}
	}

	if check.Skipped != "" {
		return nilJudgement{reason: "the change could not be type-checked and is unverified: " + check.Skipped}
	}

	return nilJudgement{propagate: len(members) > 0}
}

// propagatedRefusal returns why propagate cannot change the method m, which fn
// satisfies, or "": its error result must be nil on every return too.
func propagatedRefusal(decls map[*gotypes.Func]*funcDecl, m *gotypes.Func) string {
	d := decls[canonicalFunc(m)]
	if d == nil {
		return fmt.Sprintf("it changes with %s, whose declaration is not in the workspace", funcLabel(m))
	}

	if d.Decl == nil || alwaysNilError(d.Pkg.TypesInfo, m, d.Decl) {
		return "" // an interface method has no body
	}

	return fmt.Sprintf("%s does not always return nil, so propagate would drop its error result", funcLabel(m))
}

// add records fn as always nil in one view. The first reason any view gives is kept.
func (r *alwaysNilRun) add(v *analysis.View, fn *gotypes.Func, decl *funcDecl, key declKey, j nilJudgement) {
	site := declSite{decl.File.Path, fn.Pos()}

	d := r.decls[site]
	if d == nil {
		pos := v.Workspace.FileSet.Position(fn.Pos())
		d = &alwaysNilDecl{key: key, shape: declShape(decl)}
		d.Function, d.Signature = key.displayName(), funcSignature(fn)
		d.File, d.Line, d.Column = decl.File.Path, pos.Line, pos.Column
		r.decls[site] = d
	}

	if d.Reason == "" {
		d.Reason = j.reason
	}

	d.Propagate = d.Propagate || j.propagate
}

func (r *alwaysNilRun) report(views []*analysis.View) *AlwaysNilErrorsReport {
	out := &AlwaysNilErrorsReport{DetectHeader: r.header(views)}
	for site, d := range r.decls {
		if r.nilViews[d.key] != r.declViews[d.key] {
			continue // some configuration returns a non-nil error
		}

		f := d.AlwaysNilErrorFinding

		declaring := r.declaredIn[site]
		if len(declaring) != len(views) {
			f.Configs = declaring
		}

		f.CallSites = len(r.calls[d.key])

		f.Reason = d.Reason
		if f.Reason == "" {
			f.Reason = r.signatureRefusal(d.key, d.Function, d.File, d.shape)
		}

		f.Fixable = f.Reason == ""
		f.Propagate = f.Fixable && d.Propagate
		out.Findings = append(out.Findings, f)
	}

	slices.SortFunc(out.Findings, func(a, b AlwaysNilErrorFinding) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})

	return out
}

var errorType = gotypes.Universe.Lookup("error").Type()

// alwaysNilError reports whether the last result of fn is error and every
// return of decl's body, outside func literals, returns the predeclared nil in
// that position. A function with no return statement does not qualify, and
// neither does a bare return or one that forwards another call's results.
func alwaysNilError(info *gotypes.Info, fn *gotypes.Func, decl *ast.FuncDecl) bool {
	results := fn.Signature().Results()

	n := results.Len()
	if n == 0 || !gotypes.Identical(results.At(n-1).Type(), errorType) || mentionsNamedError(info, decl) {
		return false
	}

	returns, nilOnly := 0, true

	walkBodyForReturnStmts(decl.Body, func(ret *ast.ReturnStmt) {
		returns++
		nilOnly = nilOnly && len(ret.Results) == n && isUniverseNil(info, ret.Results[n-1])
	})

	return returns > 0 && nilOnly
}

// mentionsNamedError reports whether the body of decl, closures included,
// mentions its last result when that result is named: a write or read of it
// can make the returned error non-nil.
func mentionsNamedError(info *gotypes.Info, decl *ast.FuncDecl) bool {
	fields := listOf(decl.Type.Results)
	if len(fields) == 0 || len(fields[len(fields)-1].Names) == 0 {
		return false
	}

	names := fields[len(fields)-1].Names

	name := names[len(names)-1]
	if name.Name == "_" {
		return false
	}

	obj := info.Defs[name]
	mentioned := false

	ast.Inspect(decl.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && info.Uses[id] == obj {
			mentioned = true
		}

		return !mentioned
	})

	return mentioned
}

// isUniverseNil reports whether e is the predeclared nil, not a shadowing identifier.
func isUniverseNil(info *gotypes.Info, e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	if !ok {
		return false
	}

	_, isNil := info.Uses[id].(*gotypes.Nil)

	return isNil
}
