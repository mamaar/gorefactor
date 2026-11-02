package refactor

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"maps"
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
	param := paramIdent(decl, op.removedIdx)
	if param == nil || param.Name == "_" || decl.Body == nil {
		return
	}
	uses, ok := paramUses(pkg.TypesInfo, decl, param)
	if !ok {
		op.refuse(ws, param.Pos(), fmt.Sprintf("no type information to check whether parameter %s of %s is used", param.Name, decl.Name.Name))
		return
	}
	for _, id := range uses {
		op.refuse(ws, id.Pos(), fmt.Sprintf("parameter %s of %s is used in its body", param.Name, decl.Name.Name))
	}
}

// paramIdent returns the name of the idx-th parameter of decl, or nil when it
// is unnamed.
func paramIdent(decl *ast.FuncDecl, idx int) *ast.Ident {
	i := 0
	for _, f := range decl.Type.Params.List {
		for _, n := range f.Names {
			if i == idx {
				return n
			}
			i++
		}
		if len(f.Names) == 0 {
			i++
		}
	}
	return nil
}

// paramUses returns the identifiers in decl's body that read param. ok is
// false when info does not know param.
func paramUses(info *gotypes.Info, decl *ast.FuncDecl, param *ast.Ident) (uses []*ast.Ident, ok bool) {
	if info == nil || info.Defs[param] == nil {
		return nil, false
	}
	obj := info.Defs[param]
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && info.Uses[id] == obj {
			uses = append(uses, id)
		}
		return true
	})
	return uses, true
}

// siteRefusal is a site that stops the change.
type siteRefusal struct {
	file   string
	line   int
	reason string
}

func (r siteRefusal) String() string { return fmt.Sprintf("%s:%d: %s", r.file, r.line, r.reason) }

func compareRefusals(a, b siteRefusal) int {
	return cmp.Or(strings.Compare(a.file, b.file), cmp.Compare(a.line, b.line), strings.Compare(a.reason, b.reason))
}

// refuse records a site that stops the change.
func (op *ChangeSignatureOperation) refuse(ws *pkgtypes.Workspace, pos token.Pos, reason string) {
	p := ws.FileSet.Position(pos)
	r := siteRefusal{file: p.Filename, line: p.Line, reason: reason}
	if !slices.Contains(op.refusals, r) {
		op.refusals = append(op.refusals, r)
	}
}

// subcommandName names the subcommand the operation performs, for messages.
func (op *ChangeSignatureOperation) subcommandName() string {
	switch {
	case op.RemoveParam:
		return "remove_param"
	case op.NewReturnPosition >= 0:
		return "add_return"
	case op.RemovedReturnIndex >= 0:
		return "remove_return"
	}
	return "change_signature"
}

// removeArg drops the argument for the removed parameter from args. It reports
// why the call cannot be rewritten, or "" when it can.
func (op *ChangeSignatureOperation) removeArg(callExpr *ast.CallExpr, args []string, info *gotypes.Info) ([]string, string) {
	if reason := dropArgRefusal(callExpr, info, op.removedIdx, op.removedCount, op.removedVariadic); reason != "" {
		return nil, reason
	}
	out := slices.Clone(args)
	if op.removedIdx < len(out) {
		out = slices.Delete(out, op.removedIdx, op.removedIdx+1)
	}
	if callExpr.Ellipsis.IsValid() {
		out[len(out)-1] += "..."
	}
	return out, ""
}

// dropArgRefusal reports why the argument for parameter idx, of n parameters,
// cannot be dropped from call, or "" when it can.
func dropArgRefusal(call *ast.CallExpr, info *gotypes.Info, idx, n int, variadic bool) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && info != nil {
		if s := info.Selections[sel]; s != nil && s.Kind() == gotypes.MethodExpr {
			return "method expression passes the receiver as an argument"
		}
	}
	args := len(call.Args)
	if args == 1 && info != nil {
		if tuple, ok := info.TypeOf(call.Args[0]).(*gotypes.Tuple); ok && tuple.Len() > 1 {
			return "passes a multi-value call as the arguments"
		}
	}
	if variadic && args < n-1 || !variadic && args != n {
		return "arguments do not match the parameters"
	}
	if variadic && idx == n-1 && args > n-1 {
		return "passes variadic arguments to the parameter being removed"
	}
	if idx < args && stmtfacts.HasCallOrReceive(info, []ast.Node{call.Args[idx]}) {
		return "the argument being dropped has side effects (a call or a receive)"
	}
	return ""
}

// removedParamChange returns the change that deletes the removed parameter from
// params, a parameter list of a declaration in file, and nothing else, so
// comments and layout survive.
func (op *ChangeSignatureOperation) removedParamChange(ws *pkgtypes.Workspace, file *pkgtypes.File, params *ast.FieldList, description string) pkgtypes.Change {
	off := func(p token.Pos) int { return ws.FileSet.Position(p).Offset }
	start, end := removedParamSpan(file.OriginalContent, off, params, op.removedIdx)
	change := pkgtypes.Change{
		File:        file.Path,
		Start:       start,
		End:         end,
		OldText:     string(file.OriginalContent[start:end]),
		Description: description,
	}
	op.removedSpans = append(op.removedSpans, change)
	return change
}

// pruneRemovedImports adds the removal of each import that nothing in its file
// uses once the removed parameters and dropped arguments are gone.
func (op *ChangeSignatureOperation) pruneRemovedImports(ws *pkgtypes.Workspace, plan *pkgtypes.RefactoringPlan) {
	paths := map[string]bool{}
	for _, c := range op.removedSpans {
		paths[c.File] = true
	}
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		file, _ := findSourceFile(ws, path)
		for _, spec := range unusedImportsAfterRemoval(ws, file, op.removedSpans) {
			plan.Changes = append(plan.Changes, removeImportSpec(ws, file, spec))
		}
	}
}

// removedParamSpan returns the byte range to delete for parameter idx of
// params: the name and type with the comma that joins it to a neighbour, only
// the name when a group shares the type, or everything between the parentheses
// when it is the only parameter.
func removedParamSpan(src []byte, off func(token.Pos) int, params *ast.FieldList, idx int) (start, end int) {
	fields := params.List
	if params.NumFields() == 1 {
		return off(params.Opening) + 1, off(params.Closing)
	}
	fi, ni := 0, idx
	for i, f := range fields {
		n := max(len(f.Names), 1)
		if ni < n {
			fi = i
			break
		}
		ni -= n
	}
	f := fields[fi]
	switch {
	case len(f.Names) > 1 && ni < len(f.Names)-1:
		return off(f.Names[ni].Pos()), off(f.Names[ni+1].Pos())
	case len(f.Names) > 1:
		return off(f.Names[ni-1].End()), off(f.Names[ni].End())
	case fi < len(fields)-1:
		end = commaAfter(src, off(f.End())) + 1
		for end < len(src) && strings.IndexByte(" \t\r\n", src[end]) >= 0 {
			end++
		}
		return off(f.Pos()), end
	}
	return commaAfter(src, off(fields[fi-1].End())), off(f.End())
}

// commaAfter returns the offset of the first comma at or after i, skipping
// white space and comments.
func commaAfter(src []byte, i int) int {
	for i < len(src) {
		switch {
		case src[i] == ',':
			return i
		case bytes.HasPrefix(src[i:], []byte("/*")):
			i += 2 + max(bytes.Index(src[i+2:], []byte("*/")), 0) + 2
		case bytes.HasPrefix(src[i:], []byte("//")):
			i += max(bytes.IndexByte(src[i:], '\n'), 1)
		default:
			i++
		}
	}
	return i
}
