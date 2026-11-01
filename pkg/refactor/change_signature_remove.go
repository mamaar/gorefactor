package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// removedParamIndex returns the one index whose deletion from old gives updated.
func removedParamIndex(old, updated []Parameter) (int, error) {
	var found []int
	if len(old) == len(updated)+1 {
		for i := range old {
			if paramsEqual(slices.Delete(slices.Clone(old), i, i+1), updated) {
				found = append(found, i)
			}
		}
	}
	switch len(found) {
	case 0:
		return 0, fmt.Errorf("remove_param: params must be the current %d parameters with exactly one removed", len(old))
	case 1:
		return found[0], nil
	}
	return 0, fmt.Errorf("remove_param: removal is ambiguous, parameters %v are identical and unnamed or equal; name them first", found)
}

// planRemoveParam finds the removed parameter of the target and refuses when
// the target's body uses it.
func (op *ChangeSignatureOperation) planRemoveParam(ws *pkgtypes.Workspace, pkg *pkgtypes.Package, file *pkgtypes.File, typeName, methodName string, isIface bool) error {
	var decl *ast.FuncDecl
	var old []Parameter
	if isIface {
		old = op.extractInterfaceMethodParams(file, typeName, methodName)
	} else {
		decl = op.findFunction(file, op.FunctionName)
		if decl == nil {
			return nil // collectConcreteChanges reports it
		}
		old = op.extractParamList(decl.Type.Params)
	}
	idx, err := removedParamIndex(old, op.NewParams)
	if err != nil {
		return err
	}
	op.removedIdx, op.removedCount = idx, len(old)
	op.removedVariadic = strings.HasPrefix(old[len(old)-1].Type, "...")
	if decl != nil {
		op.checkRemovedUnused(ws, pkg, decl)
	}
	return nil
}

// checkRemovedUnused records every use of the removed parameter in decl's body.
func (op *ChangeSignatureOperation) checkRemovedUnused(ws *pkgtypes.Workspace, pkg *pkgtypes.Package, decl *ast.FuncDecl) {
	var param *ast.Ident
	i := 0
	for _, f := range decl.Type.Params.List {
		for _, n := range f.Names {
			if i == op.removedIdx {
				param = n
			}
			i++
		}
		if len(f.Names) == 0 {
			i++
		}
	}
	if param == nil || param.Name == "_" || decl.Body == nil {
		return
	}
	info := pkg.TypesInfo
	if info == nil || info.Defs[param] == nil {
		op.refuseRemove(ws, param.Pos(), fmt.Sprintf("no type information to check whether parameter %s of %s is used", param.Name, decl.Name.Name))
		return
	}
	obj := info.Defs[param]
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && info.Uses[id] == obj {
			op.refuseRemove(ws, id.Pos(), fmt.Sprintf("parameter %s of %s is used in its body", param.Name, decl.Name.Name))
		}
		return true
	})
}

// refuseRemove records a site that stops the remove_param.
func (op *ChangeSignatureOperation) refuseRemove(ws *pkgtypes.Workspace, pos token.Pos, reason string) {
	p := ws.FileSet.Position(pos)
	msg := fmt.Sprintf("%s:%d: %s", p.Filename, p.Line, reason)
	if !slices.Contains(op.removeRefusals, msg) {
		op.removeRefusals = append(op.removeRefusals, msg)
	}
}

// removeArg drops the argument for the removed parameter from args. It reports
// why the call cannot be rewritten, or "" when it can.
func (op *ChangeSignatureOperation) removeArg(callExpr *ast.CallExpr, args []string, info *gotypes.Info) ([]string, string) {
	n, variadic := op.removedCount, op.removedVariadic
	if sel, ok := callExpr.Fun.(*ast.SelectorExpr); ok && info != nil {
		if s := info.Selections[sel]; s != nil && s.Kind() == gotypes.MethodExpr {
			return nil, "method expression passes the receiver as an argument"
		}
	}
	if len(args) == 1 && info != nil {
		if tuple, ok := info.TypeOf(callExpr.Args[0]).(*gotypes.Tuple); ok && tuple.Len() > 1 {
			return nil, "passes a multi-value call as the arguments"
		}
	}
	if variadic && len(args) < n-1 || !variadic && len(args) != n {
		return nil, "arguments do not match the parameters"
	}
	if variadic && op.removedIdx == n-1 && len(args) > n-1 {
		return nil, "passes variadic arguments to the parameter being removed"
	}
	out := slices.Clone(args)
	if op.removedIdx < len(out) {
		if stmtfacts.HasCallOrReceive(info, []ast.Node{callExpr.Args[op.removedIdx]}) {
			return nil, "the argument being dropped has side effects (a call or a receive)"
		}
		out = slices.Delete(out, op.removedIdx, op.removedIdx+1)
	}
	if callExpr.Ellipsis.IsValid() {
		out[len(out)-1] += "..."
	}
	return out, ""
}
