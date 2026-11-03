package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// ctxSource describes where a call site's ctx argument comes from.
type ctxSource struct {
	Expr string // the Go expression to pass, e.g. "ctx", "c", "r.Context()", "context.TODO()", "t.Context()"
	Kind ctxSourceKind
}

type ctxSourceKind int

const (
	ctxUnknown     ctxSourceKind = iota // type info missing — caller must abort, never guess
	ctxInScope                          // a real context.Context is in scope
	ctxFromRequest                      // derived via r.Context()
	ctxRootLiteral                      // a root: context.TODO() / t.Context()
	ctxNeedsParam                       // nothing available; enclosing func must gain a param
)

// findCtxSource walks the go/types scope chain enclosing call, looking for
// a context.Context (or, failing that, an *http.Request whose .Context()
// can stand in for one) that the call site can legally reference.
//
// The returned bool is true only when a usable source (ctxInScope or
// ctxFromRequest) was actually found. A false result still carries a
// meaningful Kind — ctxUnknown or ctxNeedsParam — that the caller must
// branch on; it is not a bare "error".
func findCtxSource(pkg *pkgtypes.Package, fn *ast.FuncDecl, call *ast.CallExpr) (ctxSource, bool) {
	if pkg.TypesPkg == nil || pkg.TypesInfo == nil {
		// The workspace loader type-checks lazily and swallows errors,
		// so a nil TypesPkg/TypesInfo here is a real, silent condition,
		// not a "shouldn't happen". Falling back to matching the `ctx`
		// identifier by name would silently thread the wrong value (or
		// a package-level unrelated var) whenever type info is missing.
		// Aborting is the only safe move; the caller must not guess.
		return ctxSource{Kind: ctxUnknown}, false
	}

	pkgScope, inner := scopeAt(pkg, call.Pos())

	var scopes []*gotypes.Scope
	// Climb from the innermost scope containing the call upward, but stop
	// BEFORE reaching package scope. A package-level `var ctx
	// context.Context` belongs to the package, not to whoever is calling
	// into this function — picking it up here would thread a shared
	// global instead of a caller-supplied context.
	for s := inner; s != nil && s != pkgScope; s = s.Parent() {
		scopes = append(scopes, s)
	}

	for _, s := range scopes {
		v, ok := scopeCtxVar(s, call.Pos())
		if ok {
			return ctxSource{Expr: v.Name(), Kind: ctxInScope}, true
		}
	}

	src, ok := requestParamCtx(pkg, fn)
	if ok {
		return src, true
	}

	return ctxSource{Kind: ctxNeedsParam}, false
}

// scopeCtxVar looks for a context.Context-typed variable declared directly
// in s (not in an ancestor scope) and visible at callPos. It prefers a
// variable named exactly "ctx"; failing that, the first match in s.Names()
// order (which go/types returns sorted, so this is deterministic).
func scopeCtxVar(s *gotypes.Scope, callPos token.Pos) (*gotypes.Var, bool) {
	obj := s.Lookup("ctx")
	if obj != nil {
		v, ok := acceptScopeVar(obj, callPos, analysis.IsContextType)
		if ok {
			return v, true
		}
	}
	for _, name := range s.Names() {
		if name == "ctx" {
			continue // already tried above
		}
		v, ok := acceptScopeVar(s.Lookup(name), callPos, analysis.IsContextType)
		if ok {
			return v, true
		}
	}
	return nil, false
}

// acceptScopeVar reports whether obj is a *gotypes.Var matching typeOK and
// declared (Pos) strictly before callPos. A variable declared after the
// call site — however it may be named or typed — is not in scope there.
func acceptScopeVar(obj gotypes.Object, callPos token.Pos, typeOK func(gotypes.Type) bool) (*gotypes.Var, bool) {
	v, ok := obj.(*gotypes.Var)
	if !ok {
		return nil, false
	}
	if !typeOK(v.Type()) {
		return nil, false
	}
	if v.Pos() >= callPos {
		return nil, false
	}
	return v, true
}

// requestParamCtx looks for an *http.Request parameter on fn itself (the
// enclosing function declaration) and, if found and named, returns
// "<name>.Context()". An unnamed or blank ("_") request parameter cannot be
// referenced by an expression, so it is not returned as usable.
func requestParamCtx(pkg *pkgtypes.Package, fn *ast.FuncDecl) (ctxSource, bool) {
	if fn == nil || fn.Type.Params == nil {
		return ctxSource{}, false
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			obj := pkg.TypesInfo.Defs[name]
			if obj == nil {
				continue
			}
			if analysis.IsHTTPRequestPtr(obj.Type()) {
				return ctxSource{Expr: name.Name + ".Context()", Kind: ctxFromRequest}, true
			}
		}
	}
	return ctxSource{}, false
}

// isFakeRootCtxCreation detects a top-level statement in fn.Body.List of the
// form `ctx := context.Background()` / `ctx := context.TODO()`.
//
// This is deliberately narrow: only a literal context.Background()/TODO()
// call counts. `ctx := r.Context()`, `ctx := context.WithTimeout(parent,
// ...)`, and `ctx := someFunc()` all produce a REAL context derived from
// something else in scope — findCtxSource's scope walk will find that
// variable and stop the climb there, which is correct. Reporting those as
// fake roots would cause a real, already-threaded context to be discarded.
//
// Only the top level of the body is scanned, not nested closures — this
// mirrors RemoveInternalCtxCreation in ctx_creation.go, which has its own
// regression test for exactly this boundary.
func isFakeRootCtxCreation(fn *ast.FuncDecl) (*ast.AssignStmt, bool) {
	if fn == nil || fn.Body == nil {
		return nil, false
	}
	for _, stmt := range fn.Body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE {
			continue
		}
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			continue
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || lhs.Name != "ctx" {
			continue
		}
		if !isRootContextCall(assign.Rhs[0]) {
			continue
		}
		return assign, true
	}
	return nil, false
}

// classifyRoot reports whether the call site sitting in fn (or, if non-nil,
// the more specific enclosing FuncLit lit) is a threading root, and if so
// which literal expression to inject there.
//
// Any function, method or closure with a named, non-blank *testing.T/B/F or
// testing.TB parameter that call can still reference is a root and passes
// <param>.Context(); test helpers are roots, not climbed. When call shadows
// the parameter, a closure or Test/Benchmark/Fuzz declaration is a
// context.TODO() root and any other function is not a root.
//
// t.Context() is cancelled before Cleanup functions run, so nothing inside a
// Cleanup closure uses it, and a testing-param function that cleanupHelper
// marks as called from one is not a root.
func classifyRoot(pkg *pkgtypes.Package, file *pkgtypes.File, fn *ast.FuncDecl, lit *ast.FuncLit, call *ast.CallExpr, cleanupHelper bool) (ctxSource, bool) {
	if lit != nil {
		// t.Run(name, func(t *testing.T) { ... }) and similar: a closure
		// whose own signature carries a *testing.T/B/F root gets
		// t.Context(); any other closure (go func(){}, defer func(){},
		// a generic callback) gets context.TODO() since it may run
		// outside any traceable synchronous caller chain.
		if pkg != nil && pkg.TypesInfo != nil && !inCleanupClosure(pkg, fn, call) {
			p, ok := anyTestingTBParam(pkg, lit.Type, call.Pos())
			if ok {
				return ctxSource{Expr: p.Name + ".Context()", Kind: ctxRootLiteral}, true
			}
		}
		return ctxSource{Expr: "context.TODO()", Kind: ctxRootLiteral}, true
	}

	if fn == nil {
		// No enclosing function at all: a package-level var initializer.
		return ctxSource{Expr: "context.TODO()", Kind: ctxRootLiteral}, true
	}

	if fn.Recv == nil {
		switch {
		case fn.Name.Name == "main" && pkg != nil && pkg.Name == "main":
			return ctxSource{Expr: "context.TODO()", Kind: ctxRootLiteral}, true
		case fn.Name.Name == "init":
			return ctxSource{Expr: "context.TODO()", Kind: ctxRootLiteral}, true
		}

		if file != nil && strings.HasSuffix(file.Path, "_test.go") && fn.Name.Name == "TestMain" && pkg != nil && pkg.TypesInfo != nil {
			p, ok := firstParamIdent(fn.Type)
			if ok && analysis.IsTestingM(paramType(pkg, p)) {
				// testing.M has no Context() method.
				return ctxSource{Expr: "context.TODO()", Kind: ctxRootLiteral}, true
			}
		}
	}

	if pkg != nil && pkg.TypesInfo != nil {
		p, ok := anyTestingTBParam(pkg, fn.Type, call.Pos())
		if ok && !cleanupHelper {
			return ctxSource{Expr: p.Name + ".Context()", Kind: ctxRootLiteral}, true
		}
		if file != nil && isTestDecl(file, fn) && hasTestingTBParam(pkg, fn.Type) {
			return ctxSource{Expr: "context.TODO()", Kind: ctxRootLiteral}, true
		}
	}

	return ctxSource{}, false
}

// inCleanupClosure reports whether call sits lexically inside a function
// literal passed to a Cleanup method of a *testing.T/B/F or testing.TB value.
func inCleanupClosure(pkg *pkgtypes.Package, fn *ast.FuncDecl, call *ast.CallExpr) bool {
	if fn == nil || pkg == nil || pkg.TypesInfo == nil {
		return false
	}
	var stack []ast.Node
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if found || call.Pos() < n.Pos() || call.End() > n.End() {
			return false
		}
		stack = append(stack, n)
		if n == call {
			found = anyCleanupArg(pkg, stack)
			return false
		}
		return true
	})
	return found
}

// anyCleanupArg reports whether a function literal on stack is an argument of
// a testing Cleanup call that also lies on stack.
func anyCleanupArg(pkg *pkgtypes.Package, stack []ast.Node) bool {
	for i := 1; i < len(stack); i++ {
		lit, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		parent, ok := stack[i-1].(*ast.CallExpr)
		if !ok || !slices.Contains(parent.Args, ast.Expr(lit)) {
			continue
		}
		sel, ok := parent.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Cleanup" && analysis.IsTestingTB(pkg.TypesInfo.TypeOf(sel.X)) {
			return true
		}
	}
	return false
}

// isTestDecl reports whether fn is a Test/Benchmark/Fuzz function declared in
// a _test.go file, which go test calls and so cannot gain a parameter.
func isTestDecl(file *pkgtypes.File, fn *ast.FuncDecl) bool {
	if fn.Recv != nil || !strings.HasSuffix(file.Path, "_test.go") {
		return false
	}
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz"} {
		if strings.HasPrefix(fn.Name.Name, prefix) {
			return true
		}
	}
	return false
}

// firstParamIdent returns the first parameter's identifier, if it is
// named (not blank, not unnamed).
func firstParamIdent(ft *ast.FuncType) (*ast.Ident, bool) {
	if ft == nil || ft.Params == nil || len(ft.Params.List) == 0 {
		return nil, false
	}
	field := ft.Params.List[0]
	if len(field.Names) == 0 {
		return nil, false
	}
	name := field.Names[0]
	if name.Name == "_" {
		return nil, false
	}
	return name, true
}

// anyTestingTBParam returns the first named parameter of ft (in any
// position) whose type satisfies analysis.IsTestingTB and whose name still
// resolves to that parameter at pos.
func anyTestingTBParam(pkg *pkgtypes.Package, ft *ast.FuncType, pos token.Pos) (*ast.Ident, bool) {
	return testingTBParam(pkg, ft, func(name *ast.Ident) bool {
		return paramVisibleAt(pkg, name, pos)
	})
}

// hasTestingTBParam reports whether ft has a named testing parameter,
// regardless of shadowing.
func hasTestingTBParam(pkg *pkgtypes.Package, ft *ast.FuncType) bool {
	_, ok := testingTBParam(pkg, ft, func(*ast.Ident) bool { return true })
	return ok
}

func testingTBParam(pkg *pkgtypes.Package, ft *ast.FuncType, accept func(*ast.Ident) bool) (*ast.Ident, bool) {
	if ft == nil || ft.Params == nil {
		return nil, false
	}
	for _, field := range ft.Params.List {
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			if analysis.IsTestingTB(paramType(pkg, name)) && accept(name) {
				return name, true
			}
		}
	}
	return nil, false
}

// paramVisibleAt reports whether param's name resolves to param itself at pos,
// i.e. no inner declaration shadows it there.
func paramVisibleAt(pkg *pkgtypes.Package, param *ast.Ident, pos token.Pos) bool {
	if pkg.TypesPkg == nil {
		return false
	}
	want := pkg.TypesInfo.Defs[param]
	_, inner := scopeAt(pkg, pos)
	_, got := inner.LookupParent(param.Name, pos)
	return want != nil && got == want
}

// scopeAt returns the package scope covering pos and the innermost scope
// within it that contains pos (nil when pos lies in no scope of that package).
// A position in an external test file belongs to the external test package.
func scopeAt(pkg *pkgtypes.Package, pos token.Pos) (pkgScope, inner *gotypes.Scope) {
	for _, tp := range []*gotypes.Package{pkg.ExternalTestTypesPkg, pkg.TypesPkg} {
		if tp == nil {
			continue
		}
		pkgScope = tp.Scope()
		inner = pkgScope.Innermost(pos)
		if inner != nil {
			return pkgScope, inner
		}
	}
	return pkgScope, nil
}

// paramType resolves the go/types type of a parameter identifier via the
// package's type-checked Defs.
func paramType(pkg *pkgtypes.Package, ident *ast.Ident) gotypes.Type {
	obj := pkg.TypesInfo.Defs[ident]
	if obj == nil {
		return nil
	}
	return obj.Type()
}

// uniqueCtxName returns the name to use for a newly injected ctx parameter
// on fn: normally "ctx", or the first of ctx0, ctx1, ... that is free if
// "ctx" is already bound in fn to something other than a context.Context.
func uniqueCtxName(pkg *pkgtypes.Package, fn *ast.FuncDecl) string {
	if pkg == nil || pkg.TypesInfo == nil || fn == nil {
		return "ctx"
	}
	if !fnBindsNonCtxName(pkg, fn, "ctx") {
		return "ctx"
	}
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("ctx%d", i)
		if !fnBindsNonCtxName(pkg, fn, candidate) {
			return candidate
		}
	}
}

// fnBindsNonCtxName reports whether fn declares name anywhere inside it —
// as a parameter, a named result, or a local at any nesting depth (e.g.
// `ctx := 5` inside an if-block) — bound to something that is not a
// context.Context.
//
// This walks pkg.TypesInfo.Defs rather than go/types Scopes: this
// workspace's type-checker (see analysis.GoParser.TypeCheckPackage) does
// not populate Info.Scopes, only Types/Defs/Uses. Defs still works for
// this purpose because it contains exactly the identifiers at their
// declaration site, at any depth — so filtering fn's identifiers down to
// those that are Defs keys already gives "does fn bind this name", without
// needing a scope tree at all. A nested declaration shadows only within
// its own block under ordinary Go rules, but it would still collide with
// what callers mean by the injected `ctx` parameter, so any depth counts.
func fnBindsNonCtxName(pkg *pkgtypes.Package, fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if found {
			return false
		}
		ident, ok := n.(*ast.Ident)
		if !ok || ident.Name != name {
			return true
		}
		obj, ok := pkg.TypesInfo.Defs[ident]
		if !ok || obj == nil {
			return true
		}
		if !analysis.IsContextType(obj.Type()) {
			found = true
			return false
		}
		return true
	})
	return found
}
