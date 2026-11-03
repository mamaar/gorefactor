package extractsig

import (
	"go/ast"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"strconv"

	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
)

const fallThroughRefusal = "block mixes early returns with fall-through: some paths return from the enclosing function and others continue past the block, which cannot be expressed by a single extracted call"

var errorType = gotypes.Universe.Lookup("error").Type()

// analyzeErrGuard accepts a block that falls through on some paths and
// returns (zero..., err) on the others, in a function whose last result is
// error. The extracted function returns its outputs plus that error and the
// call site returns when it is non-nil.
func (a *analyzer) analyzeErrGuard(res *Analysis) error {
	enclosing := a.innerSig.Results()
	n := enclosing.Len()
	if n == 0 || !gotypes.Identical(enclosing.At(n-1).Type(), errorType) {
		return refuse("%s; the enclosing function's last result is not error, so the early returns cannot become a call-site guard", fallThroughRefusal)
	}

	for _, ret := range a.boundReturns() {
		err := a.checkGuardReturn(ret, n)
		if err != nil {
			return err
		}
		a.guardReturns = append(a.guardReturns, ret)
	}

	for i := range n - 1 {
		zero, err := a.zeroExpr(enclosing.At(i).Type())
		if err != nil {
			return err
		}
		res.GuardZeros = append(res.GuardZeros, zero)
	}
	res.ErrGuard = true
	var errVar *gotypes.Var
	res.ErrName, errVar = a.guardErrName(res.Results)
	res.ErrExists = errVar != nil
	res.ErrInline = len(res.Results) == 0 && (errVar == nil || !a.liveOut(errVar))
	if res.ErrInline {
		res.ErrName = "err" // scoped to the if, so nothing else can collide
	}
	return nil
}

func (a *analyzer) checkGuardReturn(ret *ast.ReturnStmt, n int) error {
	if len(ret.Results) != n {
		return refuse("an early return in the block does not list all %d results of %s, so it cannot become a call-site guard", n, a.fn.Name.Name)
	}
	for _, e := range ret.Results[:n-1] {
		if !a.isZeroExpr(e) {
			return refuse("an early return in the block returns %s, which is not a zero value; only `return zero..., err` can become a call-site guard", gotypes.ExprString(e))
		}
	}
	if !a.provablyNonNilError(ret, ret.Results[n-1]) {
		return refuse("an early return in the block returns %s, which may return a nil error; the call site could not tell that from falling through, so only a provably non-nil error (errors.New, fmt.Errorf, &T{}, or a variable checked != nil in an enclosing if) can become a guard", gotypes.ExprString(ret.Results[n-1]))
	}
	return nil
}

// provablyNonNilError reports whether e, returned by ret, is never nil.
func (a *analyzer) provablyNonNilError(ret *ast.ReturnStmt, e ast.Expr) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return stmtfacts.ProvablyNonNil(a.pathTo(ret), ret, x.Name)
	case *ast.UnaryExpr:
		_, ok := ast.Unparen(x.X).(*ast.CompositeLit)
		return x.Op == token.AND && ok
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		fn, ok := a.info.Uses[sel.Sel].(*gotypes.Func)
		if !ok || fn.Pkg() == nil {
			return false
		}
		switch fn.Pkg().Path() + "." + fn.Name() {
		case "errors.New", "fmt.Errorf":
			return true
		}
	}
	return false
}

// pathTo returns the nodes from the enclosing function body down to target.
func (a *analyzer) pathTo(target ast.Node) []ast.Node {
	var stack, found []ast.Node
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if n == target {
			found = append([]ast.Node(nil), stack...)
		}
		return found == nil
	})
	return found
}

// boundReturns lists the returns in the selection that exit the function the
// selection belongs to.
func (a *analyzer) boundReturns() []*ast.ReturnStmt {
	var out []*ast.ReturnStmt
	for _, s := range a.stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.ReturnStmt:
				out = append(out, x)
			}
			return true
		})
	}
	return out
}

func (a *analyzer) isZeroExpr(e ast.Expr) bool {
	tv, ok := a.info.Types[e]
	if !ok {
		return false
	}
	if tv.IsNil() {
		return true
	}
	if tv.Value != nil {
		switch tv.Value.Kind() {
		case constant.Bool:
			return !constant.BoolVal(tv.Value)
		case constant.String:
			return constant.StringVal(tv.Value) == ""
		case constant.Int, constant.Float, constant.Complex:
			return constant.Sign(tv.Value) == 0
		}
		return false
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 0 {
		return false
	}
	switch deref(tv.Type).Underlying().(type) {
	case *gotypes.Struct, *gotypes.Array:
		return tv.Type == deref(tv.Type)
	}
	return false
}

// zeroExpr renders an expression for the zero value of t.
func (a *analyzer) zeroExpr(t gotypes.Type) (string, error) {
	if _, ok := t.(*gotypes.TypeParam); ok {
		s, err := a.renderType(t, "a zero value")
		return "*new(" + s + ")", err
	}
	switch u := t.Underlying().(type) {
	case *gotypes.Basic:
		switch {
		case u.Info()&gotypes.IsBoolean != 0:
			return "false", nil
		case u.Info()&gotypes.IsString != 0:
			return `""`, nil
		case u.Info()&gotypes.IsNumeric != 0:
			return "0", nil
		}
		return "nil", nil
	case *gotypes.Struct, *gotypes.Array:
		s, err := a.renderType(t, "a zero value")
		return s + "{}", err
	}
	return "nil", nil
}

// guardErrName picks the variable that receives the extracted function's
// error. A visible error variable of the enclosing function is reused (and
// returned); a name taken by anything else, or by a result, is skipped.
func (a *analyzer) guardErrName(results []Result) (string, *gotypes.Var) {
	// One byte earlier, so a block that is itself a for/if/switch does not
	// resolve to the scope that statement opens.
	scope := a.pkg.TypesPkg.Scope().Innermost(a.blockStart - 1)
	taken := map[string]bool{}
	for _, r := range results {
		taken[r.Name] = true
	}
	for i := 1; ; i++ {
		name := "err"
		if i > 1 {
			name += strconv.Itoa(i)
		}
		if taken[name] {
			continue
		}
		_, obj := scope.LookupParent(name, a.blockStart)
		if obj == nil && scope.Lookup(name) != nil {
			continue // declared later in the call site's own scope
		}
		if obj == nil || obj.Pos() < a.fn.Pos() || obj.Pos() >= a.fn.End() {
			return name, nil
		}
		v, ok := obj.(*gotypes.Var)
		if ok && gotypes.Identical(v.Type(), errorType) {
			return name, v
		}
	}
}
