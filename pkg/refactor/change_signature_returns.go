package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/ast/edge"
	"golang.org/x/tools/go/ast/inspector"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// changesReturns reports whether op adds or removes a result.
func (op *ChangeSignatureOperation) changesReturns() bool {
	return op.NewReturnPosition >= 0 || op.RemovedReturnIndex >= 0
}

// returnTargets returns the functions whose results change: the target and,
// with propagate, the interface methods and implementations that change with it.
func (op *ChangeSignatureOperation) returnTargets(
	ws *pkgtypes.Workspace, pkg *pkgtypes.Package, file *pkgtypes.File,
	typeName, methodName string, isIface bool, members []*gotypes.Func,
) ([]*gotypes.Func, error) {
	var target *gotypes.Func

	if isIface {
		var err error

		target, err = methodObject(ws, pkg, typeName, methodName)
		if err != nil {
			return nil, fmt.Errorf("return targets: %w", err)
		}
	} else {
		decl := op.findFunction(file, op.FunctionName)
		if decl != nil && pkg.TypesInfo != nil {
			target, _ = pkg.TypesInfo.Defs[decl.Name].(*gotypes.Func)
		}
	}

	if target == nil {
		return nil, fmt.Errorf("%s: no type information for %s; its results cannot be changed by object identity", op.subcommandName(), op.FunctionName)
	}

	return append([]*gotypes.Func{target}, members...), nil
}

// planReturns rewrites the return statements of each target and the calls to
// it, and records every site it cannot rewrite as a refusal. It returns the
// number of call sites it rewrote.
func (op *ChangeSignatureOperation) planReturns(
	ws *pkgtypes.Workspace, idx *analysis.ReferenceIndex, targets []*gotypes.Func, plan *pkgtypes.RefactoringPlan,
) (int, error) {
	if !idx.HasTypeInfo() {
		return 0, fmt.Errorf("%s: no type information; the workspace must be type-checked first", op.subcommandName())
	}

	n := targets[0].Signature().Results().Len()

	want := n + 1
	if op.NewReturnPosition >= 0 && len(op.NewReturns) != want {
		return 0, fmt.Errorf("add_return: returns must list the current %d results plus the added one, got %d", n, len(op.NewReturns))
	}

	want = n - 1
	if op.RemovedReturnIndex >= 0 && len(op.NewReturns) != want {
		return 0, fmt.Errorf("remove_return: returns must list the current %d results minus the last, got %d", n, len(op.NewReturns))
	}

	decls := buildFuncDeclIndex(ws)
	var calls []plannedCall

	for _, t := range targets {
		d := decls[canonicalFunc(t)]
		if d != nil && d.Decl != nil {
			op.planReturnBody(ws, d, n, plan)
		}

		for _, u := range idx.NonCallUsesOf(t) {
			op.refuse(ws, u.Pos, fmt.Sprintf("%s is used as a func value, so its results cannot be changed", t.Name()))
		}

		for _, site := range idx.CallSitesOf(t) {
			fold := foldPlan{Refusals: addedResultRefusals(site)}
			if op.RemovedReturnIndex >= 0 {
				fold = removedResultFold(site.Pkg.TypesInfo, idx, site, op.RemovedReturnIndex)
			}

			calls = append(calls, plannedCall{site: site, target: t, fold: fold})
		}
	}

	live := op.liveCalls(ws, calls)

	sites := 0

	for _, c := range live {
		if op.planReturnCall(ws, c, plan) {
			sites++
		}
	}

	return sites, nil
}

// plannedCall is a call site of a target with how its result change folds.
type plannedCall struct {
	site   analysis.CallSite
	target *gotypes.Func
	fold   foldPlan
}

// liveCalls returns the calls that are not deleted with the dead branch of
// another call's fold, after recording the refusals of the rest. Folds that
// edit the same source are refused too.
func (op *ChangeSignatureOperation) liveCalls(ws *pkgtypes.Workspace, calls []plannedCall) []plannedCall {
	var live []plannedCall

	for i, c := range calls {
		deleted := slices.ContainsFunc(calls[:i:i], func(o plannedCall) bool { return o.site.File == c.site.File && foldDeletes(o.fold, c.site.Call.Pos()) }) ||
			slices.ContainsFunc(calls[i+1:], func(o plannedCall) bool { return o.site.File == c.site.File && foldDeletes(o.fold, c.site.Call.Pos()) })
		if !deleted {
			live = append(live, c)
		}
	}

	for i, c := range live {
		for _, r := range c.fold.Refusals {
			op.refuse(ws, r.Pos, r.Reason)
		}

		for _, o := range live[:i] {
			if o.site.File != c.site.File {
				continue
			}

			for _, e1 := range c.fold.Edits {
				for _, e2 := range o.fold.Edits {
					if e1.Start < e2.End && e2.Start < e1.End {
						op.refuse(ws, e1.Start, "this nil check is folded together with the one of another removed error result at "+ws.FileSet.Position(e2.Start).String()+", which cannot be done")
					}
				}
			}
		}
	}

	return live
}

// foldDeletes reports whether the fold deletes the source at pos.
func foldDeletes(f foldPlan, pos token.Pos) bool {
	return slices.ContainsFunc(f.Dropped, func(r [2]token.Pos) bool { return r[0] <= pos && pos < r[1] })
}

// namedResultsAfterChange returns the named results of decl with the change
// applied: the added result is blank, the removed one is the last.
func (op *ChangeSignatureOperation) namedResultsAfterChange(decl *ast.FuncDecl) []Parameter {
	results := op.extractNamedReturnParams(decl.Type)
	if op.RemovedReturnIndex >= 0 {
		return results[:len(results)-1]
	}

	return append(results, Parameter{Name: "_", Type: op.NewReturns[op.NewReturnPosition]})
}

// planReturnBody rewrites the return statements of d, a function with n results,
// and refuses the ones it cannot.
func (op *ChangeSignatureOperation) planReturnBody(ws *pkgtypes.Workspace, d *funcDecl, n int, plan *pkgtypes.RefactoringPlan) {
	if d.Decl.Body == nil {
		return
	}

	content := d.File.OriginalContent
	info := d.Pkg.TypesInfo

	list := d.Decl.Body.List
	if n == 0 && op.NewReturnPosition >= 0 && (len(list) == 0 || !stmtfacts.Terminates(info, list[len(list)-1])) {
		op.refuse(ws, d.Decl.Name.Pos(), fmt.Sprintf("%s has no results and does not end in a return, so an added result has nothing to return", d.Decl.Name.Name))
	}

	walkBodyForReturnStmts(d.Decl.Body, func(ret *ast.ReturnStmt) {
		if len(ret.Results) != n {
			reason := "return forwards a multi-value call, so its results cannot be changed"
			if len(ret.Results) == 0 {
				reason = "bare return of named results would change meaning"
			}

			op.refuse(ws, ret.Pos(), reason)

			return
		}

		values := splitReturnValueTexts(ret, content, ws)
		if op.RemovedReturnIndex >= 0 {
			if stmtfacts.HasCallOrReceive(info, []ast.Node{ret.Results[op.RemovedReturnIndex]}) {
				op.refuse(ws, ret.Results[op.RemovedReturnIndex].Pos(), "the returned value being dropped has side effects (a call or a receive)")
				return
			}

			values = append(values[:op.RemovedReturnIndex], values[op.RemovedReturnIndex+1:]...)
		} else {
			values = append(values, op.DefaultReturnValue)
		}

		start, end := ws.FileSet.Position(ret.Pos()).Offset, ws.FileSet.Position(ret.End()).Offset
		newText := strings.TrimSpace("return " + strings.Join(values, ", "))

		if len(values) == 0 {
			from, ok := finalReturnSpan(ws.FileSet, content, d.Decl.Body, ret)
			if ok {
				start, newText = from, ""
			}
		}

		plan.Changes = append(plan.Changes, pkgtypes.Change{
			File:        d.File.Path,
			Start:       start,
			End:         end,
			OldText:     string(content[start:end]),
			NewText:     newText,
			Description: fmt.Sprintf("Update return statement in %s", d.Decl.Name.Name),
		})
		addAffected(plan, d.File.Path)
	})
}

// finalReturnSpan returns where to start deleting ret when it is the last
// statement of body, together with the separator before it, so no redundant
// trailing return is left. It declines when a comment sits in that separator or follows the return.
func finalReturnSpan(fset *token.FileSet, content []byte, body *ast.BlockStmt, ret *ast.ReturnStmt) (int, bool) {
	list := body.List
	if list[len(list)-1] != ret {
		return 0, false
	}

	prev := body.Lbrace + 1
	if len(list) > 1 {
		prev = list[len(list)-2].End()
	}

	from := fset.Position(prev).Offset
	gap := string(content[from:fset.Position(ret.Pos()).Offset])
	rest, _, _ := strings.Cut(string(content[fset.Position(ret.End()).Offset:]), "\n")
	rest = strings.TrimLeft(rest, " \t;")

	return from, strings.Trim(gap, " \t\r\n;") == "" && !strings.HasPrefix(rest, "/")
}

// planReturnCall rewrites the call site of c and reports whether it did. A
// call with refusals is left as it is. The edits that fold nil checks are also
// recorded in op.removedSpans, to find the imports they free.
func (op *ChangeSignatureOperation) planReturnCall(
	ws *pkgtypes.Workspace, c plannedCall, plan *pkgtypes.RefactoringPlan,
) bool {
	if len(c.fold.Refusals) > 0 {
		return false
	}

	site, target := c.site, c.target

	content := site.File.OriginalContent
	off := func(p token.Pos) int { return ws.FileSet.Position(p).Offset }
	name := target.Name()
	_, parent := callUseOf(site.Cursor)
	var change pkgtypes.Change

	switch p := parent.(type) {
	case *ast.ExprStmt:
		if op.RemovedReturnIndex >= 0 {
			return false // an unused result needs no change
		}

		blanks := strings.TrimSuffix(strings.Repeat("_, ", len(op.NewReturns)), ", ")
		change = pkgtypes.Change{
			Start: off(p.Pos()), End: off(p.Pos()), NewText: blanks + " = ",
			Description: fmt.Sprintf("Capture discarded return values for call to %s", name),
		}
	case *ast.AssignStmt:
		change = op.assignChange(p, site.Call, site.Pkg.TypesInfo, content, off, name)
	default:
		return false // go and defer statements discard every result
	}

	change.File = site.File.Path
	change.OldText = string(content[change.Start:change.End])
	plan.Changes = append(plan.Changes, change)

	for _, e := range c.fold.Edits {
		fc := foldChange(ws, site.File, e)
		plan.Changes = append(plan.Changes, fc)
		op.removedSpans = append(op.removedSpans, fc)
	}

	addAffected(plan, site.File.Path)

	return true
}

// assignChange returns the change that adds a blank to, or drops the last
// variable from, the left side of assign, whose right side is call. A `:=`
// left with no new variable becomes `=`.
func (op *ChangeSignatureOperation) assignChange(assign *ast.AssignStmt, call *ast.CallExpr, info *gotypes.Info, content []byte, off func(token.Pos) int, name string) pkgtypes.Change {
	last := assign.Lhs[len(assign.Lhs)-1]
	if op.NewReturnPosition >= 0 {
		return pkgtypes.Change{
			Start: off(last.End()), End: off(last.End()), NewText: ", _",
			Description: fmt.Sprintf("Update assignment LHS for call to %s", name),
		}
	}

	if len(assign.Lhs) == 1 {
		return pkgtypes.Change{
			Start: off(assign.Pos()), End: off(assign.End()),
			NewText:     string(content[off(call.Pos()):off(call.End())]),
			Description: fmt.Sprintf("Remove assignment LHS for call to %s", name),
		}
	}

	change := pkgtypes.Change{
		Start: off(assign.Lhs[len(assign.Lhs)-2].End()), End: off(last.End()),
		Description: fmt.Sprintf("Update assignment LHS for call to %s", name),
	}

	declares := slices.ContainsFunc(assign.Lhs[:len(assign.Lhs)-1], func(l ast.Expr) bool {
		id, ok := l.(*ast.Ident)
		return ok && id.Name != "_" && info.Defs[id] != nil
	})
	if assign.Tok == token.DEFINE && !declares {
		change.End, change.NewText = off(assign.TokPos)+len(token.DEFINE.String()), " ="
	}

	return change
}

func addAffected(plan *pkgtypes.RefactoringPlan, file string) {
	if !contains(plan.AffectedFiles, file) {
		plan.AffectedFiles = append(plan.AffectedFiles, file)
	}
}

// returnRefusal is a site that stops a return change.
type returnRefusal struct {
	Pos    token.Pos
	Reason string
	// Read is the read of the removed result's variable that causes the refusal;
	// nil for any other.
	Read *analysis.ValueUse
}

// callUse is how the result of a call is used.
type callUse int

const (
	useDiscarded callUse = iota // an expression, go or defer statement
	useAssigned                 // the whole right side of = or :=
	useEmbedded                 // anything else: an argument, operand, return value, declaration
)

// callUseOf classifies the call at call by its parent node and returns that parent.
func callUseOf(call inspector.Cursor) (callUse, ast.Node) {
	parent := call.Parent().Node()
	switch p := parent.(type) {
	case *ast.ExprStmt, *ast.GoStmt, *ast.DeferStmt:
		return useDiscarded, parent
	case *ast.AssignStmt:
		if (p.Tok == token.ASSIGN || p.Tok == token.DEFINE) && len(p.Rhs) == 1 && p.Rhs[0] == call.Node() {
			return useAssigned, parent
		}
	}

	return useEmbedded, parent
}

// addedResultRefusals returns why the call at site cannot take an added
// result: only statements and assignments can.
func addedResultRefusals(site analysis.CallSite) []returnRefusal {
	use, _ := callUseOf(site.Cursor)
	if use == useEmbedded {
		return []returnRefusal{embeddedCallRefusal(site.Call, "extended")}
	}

	return nil
}

func embeddedCallRefusal(call *ast.CallExpr, verb string) returnRefusal {
	return returnRefusal{Pos: call.Pos(), Reason: fmt.Sprintf("the call is used in an expression or declaration (a return value, argument or operand), so its results cannot be %s", verb)}
}

// removedResultFold plans how the call at site loses its result idx. Its
// refusals say why it cannot: the call is used in an expression, or the variable
// it is assigned to is read in a way that cannot fold (the reads are in Read),
// or is a named result returned bare. No refusals means the call can be
// rewritten; the edits then fold the nil checks of an error variable that goes
// away with the result.
func removedResultFold(info *gotypes.Info, refs *analysis.ReferenceIndex, site analysis.CallSite, idx int) foldPlan {
	refuse := func(pos token.Pos, why string) foldPlan {
		return foldPlan{Refusals: []returnRefusal{{Pos: pos, Reason: why}}}
	}

	use, parent := callUseOf(site.Cursor)
	switch use {
	case useEmbedded:
		return foldPlan{Refusals: []returnRefusal{embeddedCallRefusal(site.Call, "reduced")}}
	case useDiscarded:
		return foldPlan{}
	}

	assign := parent.(*ast.AssignStmt)
	if idx >= len(assign.Lhs) {
		return refuse(site.Call.Pos(), "the assignment does not match the call's results")
	}

	id, ok := assign.Lhs[idx].(*ast.Ident)
	if !ok {
		return refuse(assign.Lhs[idx].Pos(), "the removed result is stored in a field or element, which would stop being assigned")
	}

	if id.Name == "_" {
		return foldPlan{}
	}

	obj := info.ObjectOf(id)
	if obj == nil {
		return refuse(id.Pos(), fmt.Sprintf("no type information to check whether %s is read", id.Name))
	}
	var reads []returnRefusal
	var writes []token.Pos

	fn := enclosingFunc(site.Cursor)
	// A pointer to the variable can read it from anywhere, so every read counts.
	local := obj.Pos() >= fn.Pos() && obj.Pos() < fn.End() && !stmtfacts.AddressTaken(info, funcBody(fn), obj)
	uses := refs.NonCallUsesOf(obj)

	var kill *ast.AssignStmt
	if local {
		kill = killingWrite(site.Cursor, uses)
	}

	kept := false // a read that stays after the fold

	for _, u := range uses {
		if u.Pos == id.Pos() {
			continue
		}

		target := isAssignTarget(u.Cursor)
		skipped := local && cannotReach(site.Cursor, u.Cursor) ||
			kill != nil && (u.Pos >= kill.End() || target && u.Cursor.Parent().Node() == kill)

		switch {
		case skipped:
			kept = kept || !target
		case target:
			writes = append(writes, u.Pos)
		default:
			reads = append(reads, returnRefusal{Pos: u.Pos, Reason: fmt.Sprintf("%s holds the result being removed and is read here", id.Name), Read: &u})
		}
	}

	plan := foldErrorReads(info, site, assign, idx, obj, reads, writes)

	declaredEarlier := obj.Pos() < id.Pos() && obj.Pos() >= funcBody(fn).Pos()
	if declaredEarlier && !kept && len(plan.Refusals) == 0 {
		plan.Refusals = append(plan.Refusals, returnRefusal{Pos: id.Pos(), Reason: fmt.Sprintf("removing the result leaves %s declared and not used", id.Name)})
	}

	ret := bareReturnOfResult(info, site.Cursor, obj)
	if ret != nil {
		plan.Refusals = append(plan.Refusals, returnRefusal{Pos: ret.Pos(), Reason: fmt.Sprintf("%s is a named result and this bare return reads it", id.Name)})
	}

	return plan
}

// cannotReach reports whether the statement at from cannot execute before the
// identifier at use: it sits earlier in the same function, which has no goto,
// and no loop holds both. Anything else, such as a closure, might run after it.
func cannotReach(from, use inspector.Cursor) bool {
	if use.Node().Pos() >= from.Node().Pos() {
		return false
	}

	for c := range from.Enclosing((*ast.FuncDecl)(nil), (*ast.FuncLit)(nil), (*ast.ForStmt)(nil), (*ast.RangeStmt)(nil)) {
		n := c.Node()
		holdsUse := n.Pos() <= use.Node().Pos() && use.Node().End() <= n.End()

		switch n.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			if holdsUse {
				return false
			}
		default:
			return holdsUse && enclosingFunc(use) == n && !stmtfacts.HasGoto(funcBody(n).List)
		}
	}

	return false
}

// enclosingFunc returns the innermost function declaration or literal around c.
func enclosingFunc(c inspector.Cursor) ast.Node {
	for fn := range c.Enclosing((*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)) {
		return fn.Node()
	}

	return nil
}

// killingWrite returns the first plain assignment to a local variable in the
// same block as the call's assignment and after it. Whenever a later read
// executes after the call, this assignment has run in between, so the read
// never sees the call's value. It is nil when a loop, switch or select holds the
// call or the function has a goto, where a break, continue or jump could skip it.
func killingWrite(call inspector.Cursor, uses []analysis.ValueUse) *ast.AssignStmt {
	block := call.Parent().Parent().Node()
	if _, ok := block.(*ast.BlockStmt); !ok {
		return nil
	}

	for c := range call.Enclosing((*ast.FuncDecl)(nil), (*ast.FuncLit)(nil), (*ast.ForStmt)(nil), (*ast.RangeStmt)(nil), (*ast.SwitchStmt)(nil), (*ast.TypeSwitchStmt)(nil), (*ast.SelectStmt)(nil)) {
		switch c.Node().(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			if stmtfacts.HasGoto(funcBody(c.Node()).List) {
				return nil
			}
		default:
			return nil
		}

		break
	}

	var best *ast.AssignStmt

	for _, u := range uses {
		if !isAssignTarget(u.Cursor) {
			continue
		}

		stmt := u.Cursor.Parent()
		if stmt.Parent().Node() != block || stmt.Node().Pos() <= call.Parent().Node().End() {
			continue
		}

		if best == nil || stmt.Node().Pos() < best.Pos() {
			best = stmt.Node().(*ast.AssignStmt)
		}
	}

	return best
}

// funcBody returns the body of a function declaration or literal.
func funcBody(fn ast.Node) *ast.BlockStmt {
	if decl, ok := fn.(*ast.FuncDecl); ok {
		return decl.Body
	}

	return fn.(*ast.FuncLit).Body
}

// bareReturnOfResult returns a bare return in a function enclosing at that
// has obj as a named result, or nil.
func bareReturnOfResult(info *gotypes.Info, at inspector.Cursor, obj gotypes.Object) *ast.ReturnStmt {
	for fn := range at.Enclosing((*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)) {
		var ft *ast.FuncType
		var body *ast.BlockStmt

		switch f := fn.Node().(type) {
		case *ast.FuncDecl:
			ft, body = f.Type, f.Body
		case *ast.FuncLit:
			ft, body = f.Type, f.Body
		}

		named := false

		for _, field := range listOf(ft.Results) {
			for _, n := range field.Names {
				named = named || info.Defs[n] == obj
			}
		}

		if !named || body == nil {
			continue
		}
		var bare *ast.ReturnStmt

		walkBodyForReturnStmts(body, func(r *ast.ReturnStmt) {
			if len(r.Results) == 0 && bare == nil {
				bare = r
			}
		})

		return bare
	}

	return nil
}

// isAssignTarget reports whether the identifier at id is only written: a
// variable on the left side of = or :=.
func isAssignTarget(id inspector.Cursor) bool {
	if id.ParentEdgeKind() != edge.AssignStmt_Lhs {
		return false
	}

	tok := id.Parent().Node().(*ast.AssignStmt).Tok

	return tok == token.ASSIGN || tok == token.DEFINE
}
