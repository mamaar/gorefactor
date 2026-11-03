package refactor

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"maps"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// UnusedParamsRequest selects what UnusedParamsAcrossConfigs reports.
type UnusedParamsRequest struct {
	// Packages keeps the declarations whose package is one of these import
	// paths or directories; empty keeps every package. Calls count regardless.
	Packages []string
}

// UnusedParamFinding is one parameter no body reads and whether
// change_signature remove_param accepts removing it.
type UnusedParamFinding struct {
	Function  string
	Signature string
	Param     string // empty for an unnamed parameter
	Index     int
	Blank     bool // the parameter is unnamed or named _
	File      string
	Line      int
	Column    int
	// Configs names the configurations that declare it; empty when all do.
	Configs []string

	CallSites int
	Fixable   bool
	// Propagate is set when remove_param needs propagate: the method satisfies
	// workspace interfaces, and every method it changes with leaves the
	// parameter unused.
	Propagate bool
	// Reason says what stops remove_param when Fixable is false.
	Reason string
}

// UnusedParamsReport is the outcome of UnusedParamsAcrossConfigs.
type UnusedParamsReport struct {
	DetectHeader
	Findings []UnusedParamFinding
}

// UnusedParamsAcrossConfigs finds the parameters of hand-written functions and
// methods that no body reads under any build configuration covering the
// workspace, and judges them with the checks change_signature remove_param
// makes. Declarations sharing a key across configurations share their calls.
func (e *DefaultEngine) UnusedParamsAcrossConfigs(req UnusedParamsRequest) (*UnusedParamsReport, error) {
	run := &unusedParamsRun{
		detectRun:  newDetectRun(req.Packages),
		decls:      make(map[declSite]*unusedParamDecl),
		declaredIn: make(map[declSite][]string),
		calls:      make(map[declKey]map[token.Pos]bool),
		blockers:   make(map[paramKey]map[string]blocker),
		readBy:     make(map[paramKey][]declSite),
	}

	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("unused params across configs: %w", err)
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

// paramKey identifies one parameter of a function across views.
type paramKey struct {
	declKey
	index int
}

// unusedParam is a parameter some view reports unused.
type unusedParam struct {
	name      string
	blank     bool
	views     int // the views in which no body reads it
	reason    string
	propagate bool
}

// unusedParamDecl is a declaration with an unused parameter in some view.
type unusedParamDecl struct {
	UnusedParamFinding
	key    declKey
	shape  string
	params map[int]*unusedParam
}

// unusedParamsRun gathers the unused parameters and the calls of each view.
type unusedParamsRun struct {
	detectRun
	decls map[declSite]*unusedParamDecl
	// declaredIn names the configurations declaring each candidate.
	declaredIn map[declSite][]string
	// calls is keyed by Lparen, which every link of a method chain shares.
	calls map[declKey]map[token.Pos]bool
	// blockers maps a position to what remove_param refuses there.
	blockers map[paramKey]map[string]blocker
	// readBy lists the declarations whose body reads the parameter.
	readBy map[paramKey][]declSite
}

// blocker is a site remove_param refuses.
type blocker struct {
	pos    token.Position
	reason string
}

func (r *unusedParamsRun) visit(v *analysis.View) error {
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
		r.recordSignature(key, declShape(decl))
		old := (&ChangeSignatureOperation{}).extractParamList(decl.Decl.Type.Params)
		found := false

		for i := range old {
			pk := paramKey{key, i}

			name := paramIdent(decl.Decl, i)
			if name != nil && name.Name != "_" {
				uses, ok := paramUses(decl.Pkg.TypesInfo, decl.Decl, name)
				if !ok {
					continue // not judged without type information
				}

				if len(uses) > 0 {
					r.readBy[pk] = append(r.readBy[pk], site)
					continue
				}
			}

			reason, propagate := r.judge(ws, idx, decls, survey, fn, key, old, i)
			r.add(v, fn, decl, key, i, name, reason, propagate)

			found = true
		}

		if !found {
			continue
		}

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

// judge returns why remove_param refuses parameter i of fn, or "" when it
// accepts, and whether it accepts only with propagate. Call sites and func
// value uses it refuses are recorded under the parameter's key.
func (r *unusedParamsRun) judge(
	ws *types.Workspace, idx *analysis.ReferenceIndex, decls map[*gotypes.Func]*funcDecl, survey *ifaceSurvey,
	fn *gotypes.Func, key declKey, old []Parameter, i int,
) (reason string, propagate bool) {
	d := decls[fn]
	if isTestEntryPoint(d.File.Path, d.Decl) {
		return fn.Name() + " " + testEntryPointRefusal, false
	}

	if hasTypeParams(decls[fn].Decl) {
		return fn.Name() + " " + genericUnsupported, false
	}

	_, err := removedParamIndex(old, slices.Delete(slices.Clone(old), i, i+1))
	if err != nil {
		return err.Error(), false
	}

	targets := []*gotypes.Func{fn}
	if fn.Signature().Recv() != nil {
		members, err := propagatedMembers(survey, fn, func(f *gotypes.Func, ext ifaceCandidate) error {
			return fmt.Errorf("%s implements %s, which is declared outside the workspace and cannot be changed", funcLabel(f), ext.label())
		})
		if err != nil {
			return err.Error(), false
		}

		for _, m := range members {
			reason := propagateRefusal(decls, members, m, i)
			if reason != "" {
				return reason, false
			}
		}

		targets = append(targets, members...)
		propagate = len(members) > 0
	}

	variadic := strings.HasPrefix(old[len(old)-1].Type, "...")
	blockers := r.blockersOf(paramKey{key, i})

	for _, t := range targets {
		err := stdMethodError("remove a parameter of", t)
		if err != nil {
			return err.Error(), false
		}

		for _, u := range idx.NonCallUsesOf(t) {
			p := ws.FileSet.Position(u.Pos)
			blockers[p.String()] = blocker{p, fmt.Sprintf("%s is used as a func value at %s", t.Name(), p)}
		}

		for _, site := range idx.CallSitesOf(t) {
			why := dropArgRefusal(site.Call, site.Pkg.TypesInfo, i, len(old), variadic)
			if why != "" {
				p := ws.FileSet.Position(site.Call.Pos())
				blockers[p.String()] = blocker{p, fmt.Sprintf("the call at %s %s", p, why)}
			}
		}
	}

	return "", propagate
}

// propagateRefusal returns why propagate cannot drop parameter i of m, one of
// members, or "".
func propagateRefusal(decls map[*gotypes.Func]*funcDecl, members []*gotypes.Func, m *gotypes.Func, i int) string {
	d := decls[canonicalFunc(m)]
	if d == nil {
		return fmt.Sprintf("it changes with %s, whose declaration is not in the workspace", funcLabel(m))
	}

	if d.Decl == nil {
		return "" // an interface method has no body
	}

	name := paramIdent(d.Decl, i)
	if name == nil || name.Name == "_" || d.Decl.Body == nil {
		return ""
	}

	uses, ok := paramUses(d.Pkg.TypesInfo, d.Decl, name)
	if ok && len(uses) == 0 {
		return ""
	}
	var ifaces []*gotypes.Func

	for _, o := range members {
		od := decls[canonicalFunc(o)]
		if od != nil && od.Decl == nil {
			ifaces = append(ifaces, o)
		}
	}

	return fmt.Sprintf("it satisfies %s, so propagate would also change %s, which reads the parameter", labelsOf(ifaces), funcLabel(m))
}

func (r *unusedParamsRun) blockersOf(pk paramKey) map[string]blocker {
	if r.blockers[pk] == nil {
		r.blockers[pk] = make(map[string]blocker)
	}

	return r.blockers[pk]
}

func (r *unusedParamsRun) add(v *analysis.View, fn *gotypes.Func, decl *funcDecl, key declKey, i int, name *ast.Ident, reason string, propagate bool) {
	site := declSite{decl.File.Path, fn.Pos()}

	d := r.decls[site]
	if d == nil {
		pos := v.Workspace.FileSet.Position(fn.Pos())
		d = &unusedParamDecl{key: key, shape: declShape(decl), params: make(map[int]*unusedParam)}
		d.Function, d.Signature = key.displayName(), funcSignature(fn)
		d.File, d.Line, d.Column = decl.File.Path, pos.Line, pos.Column
		r.decls[site] = d
	}

	p := d.params[i]
	if p == nil {
		p = &unusedParam{blank: name == nil || name.Name == "_"}
		if name != nil {
			p.name = name.Name
		}

		d.params[i] = p
	}

	p.views++
	if p.reason == "" {
		p.reason = reason
	}

	p.propagate = p.propagate || propagate
}

func (r *unusedParamsRun) report(views []*analysis.View) *UnusedParamsReport {
	out := &UnusedParamsReport{DetectHeader: r.header(views)}

	for site, d := range r.decls {
		declaring := r.declaredIn[site]
		for i, p := range d.params {
			if p.views != len(declaring) {
				continue // some configuration reads it
			}

			f := d.UnusedParamFinding

			f.Param, f.Index, f.Blank, f.Propagate = p.name, i, p.blank, p.propagate
			if len(declaring) != len(views) {
				f.Configs = declaring
			}

			f.CallSites = len(r.calls[d.key])
			f.Reason = r.reasonFor(paramKey{d.key, i}, site, d, p)
			f.Fixable = f.Reason == ""
			out.Findings = append(out.Findings, f)
		}
	}

	slices.SortFunc(out.Findings, func(a, b UnusedParamFinding) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column), cmp.Compare(a.Index, b.Index))
	})

	return out
}

// reasonFor returns why remove_param refuses the parameter, or "": the
// refusal of the declaration itself first, then a variant of the function that
// differs in signature or reads the parameter, then the first refused site by
// position.
func (r *unusedParamsRun) reasonFor(pk paramKey, site declSite, d *unusedParamDecl, p *unusedParam) string {
	if p.reason != "" {
		return p.reason
	}

	reason := r.signatureRefusal(d.key, d.Function, d.File, d.shape)
	if reason != "" {
		return reason
	}

	if slices.ContainsFunc(r.readBy[pk], func(s declSite) bool { return s != site }) {
		return "another build configuration's declaration of the function reads the parameter"
	}

	blockers := slices.Collect(maps.Values(r.blockers[pk]))
	if len(blockers) == 0 {
		return ""
	}

	first := slices.MinFunc(blockers, func(a, b blocker) int {
		return cmp.Or(strings.Compare(a.pos.Filename, b.pos.Filename), cmp.Compare(a.pos.Line, b.pos.Line), cmp.Compare(a.pos.Column, b.pos.Column))
	})

	return first.reason
}
