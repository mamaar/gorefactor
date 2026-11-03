package stmtfacts

import (
	"go/ast"
	"go/token"
	"slices"
)

// ProvablyNonNil reports whether the variable name is non-nil at ret: ret sits
// in a branch of an enclosing if whose condition proves it, and nothing in
// that branch assigns the name before ret.
func ProvablyNonNil(stack []ast.Node, ret *ast.ReturnStmt, name string) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		ifStmt, ok := stack[i].(*ast.IfStmt)
		if !ok {
			continue
		}
		branch := stack[i+1]
		var proven bool
		switch branch {
		case ifStmt.Body:
			proven = nonNilWhen(ifStmt.Cond, name, true)
		case ifStmt.Else:
			proven = nonNilWhen(ifStmt.Cond, name, false)
		}
		if proven && !assignsBefore(branch, name, ret.Pos()) {
			return true
		}
	}
	return false
}

// nonNilWhen reports whether cond evaluating to truth implies name != nil.
func nonNilWhen(cond ast.Expr, name string, truth bool) bool {
	switch c := ast.Unparen(cond).(type) {
	case *ast.UnaryExpr:
		return c.Op == token.NOT && nonNilWhen(c.X, name, !truth)
	case *ast.BinaryExpr:
		switch {
		case c.Op == token.LAND && truth, c.Op == token.LOR && !truth:
			return nonNilWhen(c.X, name, truth) || nonNilWhen(c.Y, name, truth)
		case c.Op == token.NEQ && truth, c.Op == token.EQL && !truth:
			return comparesToNil(c, name)
		}
	}
	return false
}

func comparesToNil(c *ast.BinaryExpr, name string) bool {
	x, xok := c.X.(*ast.Ident)
	y, yok := c.Y.(*ast.Ident)
	return xok && yok && (x.Name == name && y.Name == "nil" || y.Name == name && x.Name == "nil")
}

// assignsBefore reports whether anything under root before pos may write name.
func assignsBefore(root ast.Node, name string, pos token.Pos) bool {
	isName := func(e ast.Expr) bool {
		id, ok := ast.Unparen(e).(*ast.Ident)
		return ok && id.Name == name
	}
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil || n.Pos() >= pos || found {
			return false
		}
		switch s := n.(type) {
		case *ast.AssignStmt:
			found = slices.ContainsFunc(s.Lhs, isName)
		case *ast.IncDecStmt:
			found = isName(s.X)
		case *ast.RangeStmt:
			found = s.Key != nil && isName(s.Key) || s.Value != nil && isName(s.Value)
		case *ast.UnaryExpr:
			found = s.Op == token.AND && isName(s.X)
		case *ast.ValueSpec:
			found = slices.ContainsFunc(s.Names, func(id *ast.Ident) bool { return id.Name == name })
		}
		return !found
	})
	return found
}
