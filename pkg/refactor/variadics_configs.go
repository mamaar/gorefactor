package refactor

import (
	"cmp"
	"fmt"
	"go/token"
	gotypes "go/types"
	"maps"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// VariadicsRequest selects what VariadicsAcrossConfigs reports.
type VariadicsRequest struct {
	// Packages keeps the declarations whose package is one of these import
	// paths or directories; empty keeps every package. Calls count regardless.
	Packages []string
}

// VariadicFinding is one variadic declaration and how the workspace calls it.
type VariadicFinding struct {
	Function  string
	Signature string
	File      string
	Line      int
	Column    int
	// Configs names the configurations that declare it; empty when all do.
	Configs []string

	CallSites int
	ZeroArgs  int
	OneArg    int
	ManyArgs  int
	Spread    int
	ValueUses int
}

// VariadicsReport is the outcome of VariadicsAcrossConfigs.
type VariadicsReport struct {
	DetectHeader
	Findings []VariadicFinding
}

// VariadicsAcrossConfigs finds the hand-written variadic functions, methods and
// interface methods under every build configuration covering the workspace, and
// counts their calls. Declarations sharing a key across configurations share
// their counts, and a call seen by several configurations counts once.
func (e *DefaultEngine) VariadicsAcrossConfigs(req VariadicsRequest) (*VariadicsReport, error) {
	run := &variadicsRun{
		detectRun: newDetectRun(req.Packages),
		decls:     make(map[declSite]*variadicDecl),
		calls:     make(map[declKey]map[token.Pos]variadicCall),
		values:    make(map[declKey]map[token.Pos]bool),
	}

	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("variadics across configs: %w", err)
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

// declKey identifies a function declaration across views. An external test
// package is checked under the import path of the package it tests, so only its
// name keeps a helper declared in both apart.
type declKey struct {
	types.SymbolKey
	pkgName string
}

// variadicCall is how one call passes the variadic parameter.
type variadicCall int

const (
	variadicZeroArgs variadicCall = iota
	variadicOneArg
	variadicManyArgs
	variadicSpread
)

// variadicDecl is one declaration some view reports.
type variadicDecl struct {
	VariadicFinding
	key declKey
}

// variadicsRun gathers the variadic declarations and calls of each view.
type variadicsRun struct {
	detectRun
	decls map[declSite]*variadicDecl
	// calls is keyed by Lparen: the Pos of a call is where its expression
	// starts, which every link of a method chain shares.
	calls  map[declKey]map[token.Pos]variadicCall
	values map[declKey]map[token.Pos]bool
}

func (r *variadicsRun) visit(v *analysis.View) error {
	idx := r.indexOf(v)
	if !idx.HasTypeInfo() {
		return fmt.Errorf("no package has type information")
	}

	for fn, decl := range buildFuncDeclIndex(v.Workspace) {
		sig := fn.Signature()
		if !sig.Variadic() || !r.wanted(decl.Pkg) {
			continue
		}

		symbol, ok := types.SymbolKeyOf(fn)
		if !ok {
			continue // a blank function has no callers
		}

		key := declKey{symbol, fn.Pkg().Name()}
		if !decl.File.Generated {
			r.addDecl(v, fn, decl, key)
		}

		// Collected for a generated declaration too: another configuration may
		// declare the same function by hand.
		if r.calls[key] == nil {
			r.calls[key] = make(map[token.Pos]variadicCall)
			r.values[key] = make(map[token.Pos]bool)
		}

		for _, site := range idx.CallSitesOf(fn) {
			r.calls[key][site.Call.Lparen] = classifyVariadicCall(site, sig)
		}

		for _, use := range idx.NonCallUsesOf(fn) {
			r.values[key][use.Pos] = true
		}
	}

	r.collectTypeErrors(v)
	r.planned(v, nil)

	return nil
}

func (r *variadicsRun) addDecl(v *analysis.View, fn *gotypes.Func, decl *funcDecl, key declKey) {
	site := declSite{decl.File.Path, fn.Pos()}

	d := r.decls[site]
	if d == nil {
		pos := v.Workspace.FileSet.Position(fn.Pos())
		d = &variadicDecl{key: key,
			Function:  key.displayName(),
			Signature: funcSignature(fn),
			File:      decl.File.Path,
			Line:      pos.Line,
			Column:    pos.Column}
		r.decls[site] = d
	}

	d.Configs = append(d.Configs, v.Config.String())
}

// displayName is the name with its receiver, as T.M.
func (k declKey) displayName() string {
	if k.Recv == "" {
		return k.Name
	}

	return k.Recv + "." + k.Name
}

// funcSignature renders fn's declaration without qualifying its own package.
func funcSignature(fn *gotypes.Func) string {
	return gotypes.ObjectString(fn, func(p *gotypes.Package) string {
		if p == fn.Pkg() {
			return ""
		}

		return p.Name()
	})
}

// classifyVariadicCall measures the call against the type of its own callee,
// which unlike declared counts the receiver of a method expression (T.M(t, 1))
// as a parameter. A callee without type information falls back to declared.
func classifyVariadicCall(site analysis.CallSite, declared *gotypes.Signature) variadicCall {
	call := site.Call
	if call.Ellipsis.IsValid() {
		return variadicSpread
	}

	info := site.Pkg.TypesInfo

	sig, ok := info.TypeOf(call.Fun).(*gotypes.Signature)
	if !ok {
		sig = declared
	}

	args := len(call.Args)
	if args == 1 {
		// f(g()) with g returning several values.
		tuple, ok := info.TypeOf(call.Args[0]).(*gotypes.Tuple)
		if ok {
			args = tuple.Len()
		}
	}

	passed := args - (sig.Params().Len() - 1)
	switch {
	case passed <= 0:
		return variadicZeroArgs
	case passed == 1:
		return variadicOneArg
	default:
		return variadicManyArgs
	}
}

func (r *variadicsRun) report(views []*analysis.View) *VariadicsReport {
	out := &VariadicsReport{DetectHeader: r.header(views)}

	decls := slices.Collect(maps.Values(r.decls))
	slices.SortFunc(decls, func(a, b *variadicDecl) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})

	for _, d := range decls {
		f := d.VariadicFinding
		if len(f.Configs) == len(views) {
			f.Configs = nil
		}

		f.CallSites = len(r.calls[d.key])

		f.ValueUses = len(r.values[d.key])
		for _, call := range r.calls[d.key] {
			switch call {
			case variadicZeroArgs:
				f.ZeroArgs++
			case variadicOneArg:
				f.OneArg++
			case variadicManyArgs:
				f.ManyArgs++
			case variadicSpread:
				f.Spread++
			}
		}

		out.Findings = append(out.Findings, f)
	}

	return out
}
