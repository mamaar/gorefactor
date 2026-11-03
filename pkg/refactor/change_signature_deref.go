package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"

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

// derefNilMode records how the body compares the parameter with nil, which
// decides what arguments the call sites may pass.
type derefNilMode int

const (
	derefNoNilCheck  derefNilMode = iota
	derefNilCombined              // only `p == nil || *p == nil`-style pairs: a nil argument stays caught
	derefNilBare                  // a lone nil comparison folds to a constant: arguments must be non-nil
)

// derefBody is the state of one planDerefBody walk.
type derefBody struct {
	op      *ChangeSignatureOperation
	ws      *pkgtypes.Workspace
	info    *gotypes.Info
	file    *pkgtypes.File
	plan    *pkgtypes.RefactoringPlan
	obj     gotypes.Object
	param   *ast.Ident
	fn      gotypes.Object // the function being changed
	nilable bool           // T is a slice or map
	dropped [][2]token.Pos
	handled map[ast.Node]bool
	err     error
}

// planDerefBody drops the * of every dereference of the parameter at op.derefIdx,
// folds its nil comparisons, and refuses when the parameter is used in any
// other way.
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
	op.derefParamObj = obj
	b := &derefBody{op: op, ws: ws, info: info, file: file, plan: plan, obj: obj, param: param, fn: info.Defs[decl.Name], handled: map[ast.Node]bool{}}
	switch obj.Type().(*gotypes.Pointer).Elem().Underlying().(type) {
	case *gotypes.Slice, *gotypes.Map:
		b.nilable = true
	}
	op.derefNil = derefNoNilCheck
	op.derefErr = nil

	var stack []ast.Node
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if b.err != nil || b.isDropped(n) {
			return false
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if (n.Op == token.LOR || n.Op == token.LAND) && !continuesChain(stack) {
				b.foldChain(n, stack[len(stack)-2])
			}
		case *ast.Ident:
			if info.Uses[n] == obj {
				b.useOfParam(n, stack)
			}
		}
		if b.err != nil {
			stack = stack[:len(stack)-1]
			return false
		}
		return true
	})
	return b.err
}

// continuesChain reports whether the last node on stack is an operand of an
// enclosing binary expression with the same operator.
func continuesChain(stack []ast.Node) bool {
	if len(stack) < 2 {
		return false
	}
	n := stack[len(stack)-1].(*ast.BinaryExpr)
	outer, ok := stack[len(stack)-2].(*ast.BinaryExpr)
	return ok && outer.Op == n.Op
}

// isDropped reports whether n lies wholly inside source that is being deleted.
func (b *derefBody) isDropped(n ast.Node) bool {
	for _, r := range b.dropped {
		if r[0] <= n.Pos() && n.End() <= r[1] {
			return true
		}
	}
	return false
}

func (b *derefBody) refuse(n ast.Node, why string) {
	pos := b.ws.FileSet.Position(n.Pos())
	b.err = fmt.Errorf("deref_param: parameter %s %s at %s:%d; only plain reads of *%s and foldable nil comparisons can be rewritten", b.param.Name, why, pos.Filename, pos.Line, b.param.Name)
}

func (b *derefBody) useOfParam(id *ast.Ident, stack []ast.Node) {
	parent := stack[len(stack)-2]
	bin, ok := parent.(*ast.BinaryExpr)
	if ok && nilComparison(b.info, b.obj, bin) != token.ILLEGAL {
		if b.handled[bin] {
			return
		}
		b.op.derefNil = derefNilBare
		b.foldConstant(bin, stack[len(stack)-3], nilComparison(b.info, b.obj, bin) == token.NEQ)
		return
	}
	if b.isRecursivePass(id, parent) {
		return
	}
	star, ok := parent.(*ast.StarExpr)
	if !ok {
		b.refuse(id, "is used other than as *"+b.param.Name+" or in a nil comparison")
		return
	}
	why := derefWriteOrAlias(stack[:len(stack)-2], star, b.info)
	if why != "" {
		b.refuse(star, why)
		return
	}
	// (*p) becomes p, not (p)
	from, to := star.Star, star.End()
	for i := len(stack) - 3; i >= 0; i-- {
		paren, ok := stack[i].(*ast.ParenExpr)
		if !ok {
			break
		}
		from, to = paren.Lparen, paren.End()
	}
	start, end := b.op.tokenPosToOffset(b.ws, from), b.op.tokenPosToOffset(b.ws, to)
	b.plan.Changes = append(b.plan.Changes, pkgtypes.Change{
		File:        b.file.Path,
		Start:       start,
		End:         end,
		OldText:     string(b.file.OriginalContent[start:end]),
		NewText:     id.Name,
		Description: fmt.Sprintf("Drop dereference of parameter %s", b.param.Name),
	})
}

// isRecursivePass reports whether id is the argument at the parameter's own
// position in a direct recursive call: both sides change type together.
func (b *derefBody) isRecursivePass(id *ast.Ident, parent ast.Node) bool {
	call, ok := parent.(*ast.CallExpr)
	if !ok || b.op.derefIdx >= len(call.Args) || call.Args[b.op.derefIdx] != id {
		return false
	}
	var callee *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.Ident:
		callee = f
	case *ast.SelectorExpr:
		callee = f.Sel
	default:
		return false
	}
	return b.fn != nil && b.info.Uses[callee] == b.fn
}

func (b *derefBody) isParam(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && b.info.Uses[id] == b.obj
}

func isNilIdent(e ast.Expr, info *gotypes.Info) bool {
	id, ok := e.(*ast.Ident)
	if !ok || info == nil {
		return false
	}
	_, isNil := info.Uses[id].(*gotypes.Nil)
	return isNil
}

// nilComparison returns == or != when e compares obj with nil, else ILLEGAL.
func nilComparison(info *gotypes.Info, obj gotypes.Object, e ast.Expr) token.Token {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || (bin.Op != token.EQL && bin.Op != token.NEQ) {
		return token.ILLEGAL
	}
	isObj := func(x ast.Expr) bool { id, ok := x.(*ast.Ident); return ok && info.Uses[id] == obj }
	if isObj(bin.X) && isNilIdent(bin.Y, info) || isNilIdent(bin.X, info) && isObj(bin.Y) {
		return bin.Op
	}
	return token.ILLEGAL
}

func (b *derefBody) isDerefParam(e ast.Expr) bool {
	star, ok := e.(*ast.StarExpr)
	return ok && b.isParam(star.X)
}

// pairedCheck classifies e as the second half of a combined form: sameNil is
// `*p == nil` (`*p != nil` under &&), emptiness is `len(*p) == 0`
// (`len(*p) > 0` or `!= 0` under &&).
func (b *derefBody) pairedCheck(e ast.Expr, isOr bool) (sameNil, emptiness bool) {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok {
		return false, false
	}
	nilOp, lenOps := token.NEQ, []token.Token{token.GTR, token.NEQ}
	if isOr {
		nilOp, lenOps = token.EQL, []token.Token{token.EQL}
	}
	if bin.Op == nilOp && b.isDerefParam(bin.X) && isNilIdent(bin.Y, b.info) {
		return true, false
	}
	call, ok := bin.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || !b.isDerefParam(call.Args[0]) || !slices.Contains(lenOps, bin.Op) {
		return false, false
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false, false
	}
	_, builtin := b.info.Uses[fn].(*gotypes.Builtin)
	if !builtin || fn.Name != "len" {
		return false, false
	}
	zero, ok := bin.Y.(*ast.BasicLit)
	return false, ok && zero.Kind == token.INT && zero.Value == "0"
}

// foldChain rewrites the operands of the || / && chain n that compare the
// parameter with nil: a combined pair shrinks to one operand, a lone
// `p == nil` under || or `p != nil` under && is dropped as constant false / true.
func (b *derefBody) foldChain(n *ast.BinaryExpr, parent ast.Node) {
	ops := flattenChain(n)
	isOr := n.Op == token.LOR
	remove := make([]bool, len(ops))
	for i, e := range ops {
		tok := nilComparison(b.info, b.obj, e)
		if tok == token.ILLEGAL {
			continue
		}
		b.handled[e] = true
		if (tok == token.EQL) != isOr {
			b.refuse(e, "makes the whole "+n.Op.String()+" chain a constant")
			return
		}
		if b.nilable && i+1 < len(ops) {
			sameNil, emptiness := b.pairedCheck(ops[i+1], isOr)
			if sameNil || emptiness {
				if b.op.derefNil == derefNoNilCheck {
					b.op.derefNil = derefNilCombined
				}
				remove[i+1], remove[i] = sameNil, emptiness
				continue
			}
		}
		b.op.derefNil = derefNilBare
		remove[i] = true
	}
	if slices.Contains(remove, false) {
		for _, r := range chainDeletions(ops, remove) {
			b.drop(r[0], r[1])
		}
		return
	}
	b.foldConstant(n, parent, !isOr)
}

// chainDeletions returns the source ranges whose deletion removes the operands
// of the chain ops marked in remove, with their operators; at least one
// operand must stay.
func chainDeletions(ops []ast.Expr, remove []bool) [][2]token.Pos {
	end := len(ops)
	for end > 0 && remove[end-1] {
		end--
	}
	var dels [][2]token.Pos
	for i := 0; i < end; i++ {
		if remove[i] {
			dels = append(dels, [2]token.Pos{ops[i].Pos(), ops[i+1].Pos()})
		}
	}
	if end < len(ops) {
		dels = append(dels, [2]token.Pos{ops[end-1].End(), ops[len(ops)-1].End()})
	}
	return dels
}

// flattenChain lists the operands of a chain of one || or && operator.
func flattenChain(e *ast.BinaryExpr) []ast.Expr {
	var ops []ast.Expr
	for _, side := range []ast.Expr{e.X, e.Y} {
		bin, ok := side.(*ast.BinaryExpr)
		if ok && bin.Op == e.Op {
			ops = append(ops, flattenChain(bin)...)
		} else {
			ops = append(ops, side)
		}
	}
	return ops
}

// foldConstant handles a nil check (a comparison or a whole chain of them) that
// folds to value: only a false condition of an if with no init and no else can
// go, together with the if.
func (b *derefBody) foldConstant(cond ast.Expr, parent ast.Node, value bool) {
	ifs, ok := parent.(*ast.IfStmt)
	if !ok || ifs.Cond != cond || ifs.Init != nil || ifs.Else != nil || value {
		b.refuse(cond, fmt.Sprintf("is nil-checked where the check folds to %t and cannot simply be removed", value))
		return
	}
	start, end := b.op.tokenPosToOffset(b.ws, ifs.Pos()), b.op.tokenPosToOffset(b.ws, ifs.End())
	content := b.file.OriginalContent
	start, end = wholeLines(content, start, end)
	b.dropped = append(b.dropped, [2]token.Pos{ifs.Pos(), ifs.End()})
	b.plan.Changes = append(b.plan.Changes, pkgtypes.Change{
		File:        b.file.Path,
		Start:       start,
		End:         end,
		OldText:     string(content[start:end]),
		Description: fmt.Sprintf("Remove nil check of parameter %s that is always false", b.param.Name),
	})
}

// wholeLines widens [start, end) to the lines it fills when only blanks share
// them, so deleting it leaves no empty line behind.
func wholeLines(content []byte, start, end int) (int, int) {
	lineStart, lineEnd := start, end
	for lineStart > 0 && (content[lineStart-1] == ' ' || content[lineStart-1] == '\t') {
		lineStart--
	}
	for lineEnd < len(content) && (content[lineEnd] == ' ' || content[lineEnd] == '\t') {
		lineEnd++
	}
	if (lineStart == 0 || content[lineStart-1] == '\n') && lineEnd < len(content) && content[lineEnd] == '\n' {
		return lineStart, lineEnd + 1
	}
	return start, end
}

// drop deletes the source between from and to.
func (b *derefBody) drop(from, to token.Pos) {
	start, end := b.op.tokenPosToOffset(b.ws, from), b.op.tokenPosToOffset(b.ws, to)
	b.dropped = append(b.dropped, [2]token.Pos{from, to})
	b.plan.Changes = append(b.plan.Changes, pkgtypes.Change{
		File:        b.file.Path,
		Start:       start,
		End:         end,
		OldText:     string(b.file.OriginalContent[start:end]),
		Description: fmt.Sprintf("Drop nil comparison of parameter %s", b.param.Name),
	})
}

// derefWriteOrAlias explains why star, whose ancestors are outer, cannot become
// a read of a by-value copy: it is written through, has its address taken, or
// reaches a pointer-receiver method or an array slice. When the pointee is a
// slice or map, uses that go through an element of it are fine: the copy shares
// that storage with the caller.
func derefWriteOrAlias(outer []ast.Node, star *ast.StarExpr, info *gotypes.Info) string {
	var top ast.Expr = star
	var direct, shared bool // top is star itself (pointee a slice or map) / top is reached through an element of one
	switch info.TypeOf(star).Underlying().(type) {
	case *gotypes.Slice, *gotypes.Map:
		direct = true
	}
	for _, o := range slices.Backward(outer) {
		switch p := o.(type) {
		case *ast.ParenExpr:
			top = p
			continue
		case *ast.SelectorExpr:
			if p.X != top {
				return ""
			}
			sel := info.Selections[p]
			if !shared && sel != nil && sel.Kind() == gotypes.MethodVal {
				recv := sel.Obj().(*gotypes.Func).Type().(*gotypes.Signature).Recv()
				if recv != nil {
					_, ptr := recv.Type().(*gotypes.Pointer)
					if ptr {
						return "is the receiver of a pointer method"
					}
				}
			}
			top, direct = p, false
			continue
		case *ast.IndexExpr:
			if p.X != top {
				return ""
			}
			shared = shared || direct
			top, direct = p, false
			continue
		case *ast.SliceExpr:
			if p.X != top {
				return ""
			}
			_, arr := info.TypeOf(star).Underlying().(*gotypes.Array)
			if arr {
				return "slices an array it points to"
			}
			shared = shared || direct
			top, direct = p, false
			continue
		case *ast.AssignStmt:
			for _, l := range p.Lhs {
				if l == top && !shared {
					return "is written through"
				}
			}
		case *ast.IncDecStmt:
			if p.X == top && !shared {
				return "is written through"
			}
		case *ast.RangeStmt:
			if (p.Key == top || p.Value == top) && !shared {
				return "is written through"
			}
		case *ast.UnaryExpr:
			if p.Op.String() == "&" && !shared {
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
func (op *ChangeSignatureOperation) derefArgs(args []string, callExpr *ast.CallExpr, ws *pkgtypes.Workspace, content []byte, info *gotypes.Info) string {
	if !op.argsMatchParams(callExpr) {
		return "arguments do not match the parameters"
	}
	arg := callExpr.Args[op.derefIdx]
	for {
		p, ok := arg.(*ast.ParenExpr)
		if !ok {
			break
		}
		arg = p.X
	}
	id, ok := arg.(*ast.Ident)
	if ok && info.Uses[id] == op.derefParamObj {
		return "" // recursive pass of the parameter itself, left as is
	}
	done, reason := op.derefNilGuard(args, arg, callExpr, ws, info)
	if done {
		return reason
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

// derefNilGuard checks arg against the body's nil comparisons of the parameter:
// they were folded assuming a non-nil argument (&x, &T{...}), or for combined
// forms also a literal nil, so any other argument fails the whole change. It
// reports done when the argument is settled and no further rewrite applies.
func (op *ChangeSignatureOperation) derefNilGuard(args []string, arg ast.Expr, callExpr *ast.CallExpr, ws *pkgtypes.Workspace, info *gotypes.Info) (done bool, reason string) {
	if op.derefNil == derefNoNilCheck {
		return false, ""
	}
	u, ok := arg.(*ast.UnaryExpr)
	if ok && u.Op == token.AND {
		return false, ""
	}
	if op.derefNil == derefNilCombined && isNilIdent(arg, info) {
		args[op.derefIdx] = "nil"
		return true, ""
	}
	pos := ws.FileSet.Position(callExpr.Pos())
	op.failDeref(fmt.Errorf("deref_param: %s checks parameter %s against nil, but the argument at %s:%d may be nil, so *e would panic where the callee handled nil", op.FunctionName, op.NewParams[op.derefIdx].Name, pos.Filename, pos.Line))
	return true, "argument may be nil"
}

// failDeref keeps the first error that must abort the whole change.
func (op *ChangeSignatureOperation) failDeref(err error) {
	if op.derefErr == nil {
		op.derefErr = err
	}
}

// skipSite records a reference deref_param or remove_param could not rewrite.
func (op *ChangeSignatureOperation) skipSite(ref *pkgtypes.Reference, reason, snippet string) {
	if op.derefNil != derefNoNilCheck {
		op.failDeref(fmt.Errorf("deref_param: %s checks its parameter against nil, so every reference must be rewritable, but %s:%d: %s", op.FunctionName, ref.File, ref.Line, reason))
	}
	op.derefSkips = append(op.derefSkips, pkgtypes.SkippedSite{File: ref.File, Line: ref.Line, Reason: reason, Snippet: snippet})
}

// isVariadic reports whether the new parameter list ends in a ...T parameter.
func (op *ChangeSignatureOperation) isVariadic() bool {
	n := len(op.NewParams)
	return n > 0 && strings.HasPrefix(op.NewParams[n-1].Type, "...")
}

// argsMatchParams reports whether the call passes one argument per fixed
// parameter, plus any number for a trailing variadic one.
func (op *ChangeSignatureOperation) argsMatchParams(callExpr *ast.CallExpr) bool {
	if op.isVariadic() {
		return len(callExpr.Args) >= len(op.NewParams)-1
	}
	return len(callExpr.Args) == len(op.NewParams)
}
