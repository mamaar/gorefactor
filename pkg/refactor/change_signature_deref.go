package refactor

import (
	"fmt"
	"go/ast"
	gotypes "go/types"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// derefTarget returns the index of the one parameter of decl whose type *T the
// new parameter list turns into T.
func (op *ChangeSignatureOperation) derefTarget(decl *ast.FuncDecl) (int, error) {
	old := signatureOf(decl.Type).params
	if len(old) != len(op.NewParams) {
		return 0, fmt.Errorf("deref_param: params must list all %d parameters, got %d", len(old), len(op.NewParams))
	}
	idx := -1
	for i, o := range old {
		n := op.NewParams[i]
		if paramEqual(o, n) {
			continue
		}
		if idx >= 0 || o.Name != n.Name || o.Type != "*"+normalizeType(n.Type) {
			return 0, fmt.Errorf("deref_param: only one parameter may change, from *T to T; %s %s -> %s %s does not", o.Name, o.Type, n.Name, n.Type)
		}
		idx = i
	}
	if idx < 0 {
		return 0, fmt.Errorf("deref_param: no parameter changes from *T to T")
	}
	if old[idx].Name == "" || old[idx].Name == "_" {
		return 0, fmt.Errorf("deref_param: parameter %d is unnamed", idx)
	}
	return idx, nil
}

// planDerefBody drops the * of every dereference of the parameter at op.derefIdx,
// and refuses when the parameter is used in any other way.
func (op *ChangeSignatureOperation) planDerefBody(ws *pkgtypes.Workspace, pkg *pkgtypes.Package, file *pkgtypes.File, decl *ast.FuncDecl, plan *pkgtypes.RefactoringPlan) error {
	info := pkg.TypesInfo
	if info == nil || decl.Body == nil {
		return fmt.Errorf("deref_param: %s needs type information and a body", op.FunctionName)
	}
	var param *ast.Ident
	i := 0
	for _, f := range decl.Type.Params.List {
		for _, n := range f.Names {
			if i == op.derefIdx {
				param = n
			}
			i++
		}
	}
	obj := info.Defs[param]
	if obj == nil {
		return fmt.Errorf("deref_param: no type information for parameter %s", param.Name)
	}

	refuse := func(n ast.Node, why string) error {
		pos := ws.FileSet.Position(n.Pos())
		return fmt.Errorf("deref_param: parameter %s %s at %s:%d; only plain reads of *%s can be rewritten", param.Name, why, pos.Filename, pos.Line, param.Name)
	}

	var stack []ast.Node
	var err error
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		id, ok := n.(*ast.Ident)
		if err != nil || !ok || info.Uses[id] != obj {
			return err == nil
		}
		star, ok := stack[len(stack)-2].(*ast.StarExpr)
		if !ok {
			err = refuse(id, "is used other than as *"+param.Name)
			return false
		}
		if why := derefWriteOrAlias(stack[:len(stack)-2], star, info); why != "" {
			err = refuse(star, why)
			return false
		}
		start := op.tokenPosToOffset(ws, star.Star)
		plan.Changes = append(plan.Changes, pkgtypes.Change{
			File:        file.Path,
			Start:       start,
			End:         start + 1,
			OldText:     "*",
			NewText:     "",
			Description: fmt.Sprintf("Drop dereference of parameter %s", param.Name),
		})
		return true
	})
	return err
}

// derefWriteOrAlias explains why star, whose ancestors are outer, cannot become
// a read of a by-value copy: it is written through, has its address taken, or
// reaches a pointer-receiver method or an array slice.
func derefWriteOrAlias(outer []ast.Node, star *ast.StarExpr, info *gotypes.Info) string {
	var top ast.Expr = star
	for i := len(outer) - 1; i >= 0; i-- {
		switch p := outer[i].(type) {
		case *ast.ParenExpr:
			top = p
			continue
		case *ast.SelectorExpr:
			if p.X != top {
				return ""
			}
			if sel := info.Selections[p]; sel != nil && sel.Kind() == gotypes.MethodVal {
				if recv := sel.Obj().(*gotypes.Func).Type().(*gotypes.Signature).Recv(); recv != nil {
					if _, ptr := recv.Type().(*gotypes.Pointer); ptr {
						return "is the receiver of a pointer method"
					}
				}
			}
			top = p
			continue
		case *ast.IndexExpr:
			if p.X != top {
				return ""
			}
			top = p
			continue
		case *ast.SliceExpr:
			if p.X != top {
				return ""
			}
			if _, arr := info.TypeOf(star).Underlying().(*gotypes.Array); arr {
				return "slices an array it points to"
			}
			top = p
			continue
		case *ast.AssignStmt:
			for _, l := range p.Lhs {
				if l == top {
					return "is written through"
				}
			}
		case *ast.IncDecStmt:
			if p.X == top {
				return "is written through"
			}
		case *ast.RangeStmt:
			if p.Key == top || p.Value == top {
				return "is written through"
			}
		case *ast.UnaryExpr:
			if p.Op.String() == "&" {
				return "has its address taken"
			}
		}
		return ""
	}
	return ""
}

// derefArg rewrites the argument at op.derefIdx of a call to pass a value: &x
// becomes x and any other expression e becomes *e. It reports why a call
// cannot be rewritten, or "" when it can.
func (op *ChangeSignatureOperation) derefArgs(args []string, callExpr *ast.CallExpr, ws *pkgtypes.Workspace, content []byte) string {
	if len(callExpr.Args) != len(op.NewParams) {
		return "arguments do not match the parameters one to one"
	}
	arg := callExpr.Args[op.derefIdx]
	for {
		p, ok := arg.(*ast.ParenExpr)
		if !ok {
			break
		}
		arg = p.X
	}
	text := func(e ast.Expr) string {
		return string(content[op.tokenPosToOffset(ws, e.Pos()):op.tokenPosToOffset(ws, e.End())])
	}
	switch a := arg.(type) {
	case *ast.UnaryExpr:
		if a.Op.String() == "&" {
			args[op.derefIdx] = text(a.X)
			return ""
		}
		args[op.derefIdx] = "*" + text(a)
	case *ast.Ident:
		if a.Name == "nil" {
			return "nil argument has no value to pass"
		}
		args[op.derefIdx] = "*" + text(a)
	case *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr, *ast.StarExpr, *ast.TypeAssertExpr:
		args[op.derefIdx] = "*" + text(a)
	default:
		args[op.derefIdx] = "*(" + text(a) + ")"
	}
	return ""
}

// skipSite records a reference deref_param could not rewrite.
func (op *ChangeSignatureOperation) skipSite(ref *pkgtypes.Reference, reason, snippet string) {
	op.derefSkips = append(op.derefSkips, pkgtypes.SkippedSite{File: ref.File, Line: ref.Line, Reason: reason, Snippet: snippet})
}
