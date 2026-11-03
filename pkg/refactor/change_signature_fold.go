package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"maps"
	"slices"
	"strings"

	"golang.org/x/tools/go/ast/edge"
	"golang.org/x/tools/go/ast/inspector"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// foldEdit replaces [Start, End) with Text, by token position.
type foldEdit struct {
	Start, End token.Pos
	Text       string
	Lines      bool // a deletion that also takes the blank-only lines around it
	LineAfter  bool // Text starts with a line break, left out when one already follows End
	LineBefore bool // Text starts with a line break, left out when one already precedes Start
}

// foldPlan is how a call site loses its error result: the edits that fold the
// nil checks of the variable, the source they delete, and the reasons it cannot.
type foldPlan struct {
	Edits    []foldEdit
	Dropped  [][2]token.Pos
	Refusals []returnRefusal
}

const foldDescription = "Fold nil check of removed error result"

// errFold plans the removal of one error variable from a call's left side by
// treating the variable as nil: its nil checks in if conditions fold, and the
// branches they make dead go. A read it cannot fold is a refusal.
type errFold struct {
	info     *gotypes.Info
	obj      gotypes.Object
	assign   *ast.AssignStmt // the call's assignment
	idx      int             // position of the variable in assign.Lhs
	edits    []foldEdit
	dropped  [][2]token.Pos
	refuse   []returnRefusal
	promoted map[*ast.IfStmt]bool // else-ifs that took the place of an if that folded away
	gone     map[*ast.IfStmt]bool // ifs whose header folded away
}

func (f *errFold) inDropped(p token.Pos) bool {
	return slices.ContainsFunc(f.dropped, func(r [2]token.Pos) bool { return r[0] <= p && p < r[1] })
}

func (f *errFold) refusal(pos token.Pos, why string) {
	f.refuse = append(f.refuse, returnRefusal{Pos: pos, Reason: why})
}

// foldErrorReads folds the uses of obj, the error variable that assign binds
// at idx, by treating it as nil. A use with Read set folds when it is a nil
// check; every other use is refused unless a dead branch of the fold deletes it.
func foldErrorReads(info *gotypes.Info, site analysis.CallSite, assign *ast.AssignStmt, idx int, obj gotypes.Object, uses []returnRefusal) foldPlan {
	if !gotypes.Identical(obj.Type(), gotypes.Universe.Lookup("error").Type()) {
		return foldPlan{Refusals: uses}
	}

	f := &errFold{info: info, obj: obj, assign: assign, idx: idx, promoted: map[*ast.IfStmt]bool{}, gone: map[*ast.IfStmt]bool{}}
	ifs := map[token.Pos]inspector.Cursor{}
	var stray []returnRefusal

	for _, r := range uses {
		if r.Read != nil {
			c, ok := nilCheckIf(info, r.Read.Cursor)
			if ok {
				ifs[c.Node().Pos()] = c
				continue
			}
		}

		stray = append(stray, r)
	}

	for _, pos := range slices.Sorted(maps.Keys(ifs)) {
		if !f.inDropped(pos) {
			f.foldIf(ifs[pos])
		}
	}

	for _, r := range stray {
		if !f.inDropped(r.Pos) {
			f.refuse = append(f.refuse, r)
		}
	}

	f.checkLastUses(site)

	return foldPlan{Edits: mergeTouching(f.edits), Dropped: f.dropped, Refusals: f.refuse}
}

// nilCheckIf returns the if whose condition holds the nil comparison that
// contains the identifier at id, reached only through ||, && and parentheses.
func nilCheckIf(info *gotypes.Info, id inspector.Cursor) (inspector.Cursor, bool) {
	cmpNode := id.Parent()

	cmpExpr, ok := cmpNode.Node().(ast.Expr)
	if !ok || nilComparison(info, info.Uses[id.Node().(*ast.Ident)], cmpExpr) == token.ILLEGAL {
		return inspector.Cursor{}, false
	}

	for c := cmpNode; ; c = c.Parent() {
		switch p := c.Parent().Node().(type) {
		case *ast.ParenExpr:
		case *ast.BinaryExpr:
			if p.Op != token.LAND && p.Op != token.LOR {
				return inspector.Cursor{}, false
			}
		case *ast.IfStmt:
			return c.Parent(), p.Cond == c.Node()
		default:
			return inspector.Cursor{}, false
		}
	}
}

type truth int

const (
	unknown truth = iota
	isTrue
	isFalse
)

// evalNil evaluates cond with obj as nil. For an undecided condition it
// returns the deletions that remove the operands that decided to nothing; why
// is set when dropping an operand would skip something that can panic or act.
func (f *errFold) evalNil(cond ast.Expr) (v truth, dels [][2]token.Pos, why string) {
	switch e := cond.(type) {
	case *ast.ParenExpr:
		return f.evalNil(e.X)
	case *ast.BinaryExpr:
		if e.Op == token.LAND || e.Op == token.LOR {
			return f.evalChain(e)
		}

		switch nilComparison(f.info, f.obj, e) {
		case token.EQL:
			return isTrue, nil, ""
		case token.NEQ:
			return isFalse, nil, ""
		}
	}

	return unknown, nil, ""
}

// evalChain evaluates the || or && chain n: an operand that decides the chain
// ends it, one that cannot change it is dropped.
func (f *errFold) evalChain(n *ast.BinaryExpr) (truth, [][2]token.Pos, string) {
	ops := flattenChain(n)

	decides, neutral := isTrue, isFalse
	if n.Op == token.LAND {
		decides, neutral = isFalse, isTrue
	}
	var dels [][2]token.Pos
	remove := make([]bool, len(ops))
	sawEffect, kept := false, false

	for i, op := range ops {
		v, d, why := f.evalNil(op)
		switch {
		case why != "":
			return unknown, nil, why
		case v == decides && sawEffect:
			return unknown, nil, "folding the check would skip an operand that can panic or have effects"
		case v == decides:
			return decides, nil, ""
		case v == neutral:
			remove[i] = true
			continue
		}

		kept = true

		dels = append(dels, d...)
		sawEffect = sawEffect || !f.pure(op)
	}

	if !kept {
		return neutral, nil, ""
	}

	return unknown, append(dels, chainDeletions(ops, remove)...), ""
}

// pure reports whether evaluating e can neither panic nor act: it is built
// from identifiers, literals and constants with comparisons and logical operators.
func (f *errFold) pure(e ast.Expr) bool {
	tv, ok := f.info.Types[e]
	if ok && tv.Value != nil {
		return true
	}

	switch e := e.(type) {
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.ParenExpr:
		return f.pure(e.X)
	case *ast.UnaryExpr:
		return e.Op == token.NOT && f.pure(e.X)
	case *ast.BinaryExpr:
		switch e.Op {
		case token.LAND, token.LOR, token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return f.pure(e.X) && f.pure(e.Y)
		}
	}

	return false
}

// delete removes the source in [from, to) and records it as dropped.
func (f *errFold) delete(from, to token.Pos) {
	f.edits = append(f.edits, foldEdit{Start: from, End: to})
	f.dropped = append(f.dropped, [2]token.Pos{from, to})
}

// foldIf folds the nil checks in the condition of the if at c.
func (f *errFold) foldIf(c inspector.Cursor) {
	ifs := c.Node().(*ast.IfStmt)

	v, dels, why := f.evalNil(ifs.Cond)
	if why != "" {
		f.refusal(ifs.Cond.Pos(), why)
		return
	}

	if v == unknown {
		for _, d := range dels {
			f.delete(d[0], d[1])
		}

		return
	}

	parent, isElseIf := c.Parent().Node().(*ast.IfStmt)

	isElseIf = isElseIf && parent.Else == ifs && !f.promoted[ifs]
	if isElseIf && ifs.Init != nil {
		f.refusal(ifs.Pos(), "the nil check is in an else-if with an init statement, which cannot be folded")
		return
	}

	keep := ifs.Else

	gone := ast.Node(ifs.Body)
	if v == isTrue {
		keep, gone = ifs.Body, ifs.Else
	}

	if keep != nil && f.terminatesChain(c, keep) {
		f.refusal(ifs.Pos(), "folding the nil check would leave the code after it unreachable")
		return
	}

	if gone != nil {
		f.checkBreaks(c, gone)
	}

	f.gone[ifs] = true

	f.dropped = append(f.dropped, [2]token.Pos{ifs.Cond.Pos(), ifs.Cond.End()})
	if gone != nil {
		f.dropped = append(f.dropped, [2]token.Pos{gone.Pos(), gone.End()})
	}

	if keep == nil {
		if isElseIf && f.gone[parent] {
			f.refusal(ifs.Pos(), "the nil checks of an else-if chain fold into each other, which cannot be folded")
			return
		}

		f.dropIf(ifs, parent, isElseIf)

		return
	}

	inner, ok := keep.(*ast.IfStmt)
	if ok {
		f.promoted[inner] = !isElseIf
	}

	f.unwrapIf(ifs, keep, isElseIf)
}

// terminatesChain reports whether, with the if at c replaced by keep, the
// if chain it belongs to ends in a terminating statement that is followed by
// more statements, which vet reports as unreachable.
func (f *errFold) terminatesChain(c inspector.Cursor, keep ast.Stmt) bool {
	if !stmtfacts.Terminates(f.info, keep) {
		return false
	}

	for {
		parent, ok := c.Parent().Node().(*ast.IfStmt)
		if !ok || parent.Else != c.Node() {
			break
		}

		if !stmtfacts.Terminates(f.info, parent.Body) {
			return false
		}

		c = c.Parent()
	}

	return hasFollowingStmt(c)
}

// checkBreaks refuses when gone holds the only break of an enclosing for loop
// without a condition: without it the loop would never end and the code after
// it would be unreachable.
func (f *errFold) checkBreaks(c inspector.Cursor, gone ast.Node) {
	for _, b := range leavingBreaks(gone) {
		loop, label := f.breakTarget(c, b)
		if loop == nil || loop.Cond != nil {
			continue
		}

		other := slices.ContainsFunc(leavingBreaks(loop.Body), func(o *ast.BranchStmt) bool {
			return (o.Label == nil || o.Label.Name == label) && (gone.Pos() > o.Pos() || o.Pos() >= gone.End())
		})
		if !other {
			f.refusal(b.Pos(), "folding the nil check would remove the only break of an endless loop, leaving the code after it unreachable")
		}
	}
}

// breakTarget returns the for loop that the break b, inside the if at c,
// leaves, and its label.
func (f *errFold) breakTarget(c inspector.Cursor, b *ast.BranchStmt) (*ast.ForStmt, string) {
	for enc := range c.Enclosing((*ast.ForStmt)(nil), (*ast.RangeStmt)(nil), (*ast.SwitchStmt)(nil), (*ast.TypeSwitchStmt)(nil), (*ast.SelectStmt)(nil), (*ast.FuncLit)(nil)) {
		label := ""

		l, ok := enc.Parent().Node().(*ast.LabeledStmt)
		if ok {
			label = l.Label.Name
		}

		if b.Label != nil && b.Label.Name != label {
			continue
		}

		loop, _ := enc.Node().(*ast.ForStmt)

		return loop, label
	}

	return nil, ""
}

// leavingBreaks returns the breaks in n that leave n: labeled ones and
// unlabeled ones not inside a nested loop, switch, select or function literal.
func leavingBreaks(n ast.Node) []*ast.BranchStmt {
	var out []*ast.BranchStmt
	var stack []ast.Node
	nested := 0
	breakable := func(x ast.Node) bool {
		switch x.(type) {
		case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
			return true
		}

		return false
	}
	ast.Inspect(n, func(x ast.Node) bool {
		if x == nil {
			if breakable(stack[len(stack)-1]) {
				nested--
			}

			stack = stack[:len(stack)-1]

			return true
		}

		stack = append(stack, x)
		if breakable(x) {
			nested++
		}

		b, ok := x.(*ast.BranchStmt)
		if ok && b.Tok == token.BREAK && (b.Label != nil || nested == 0) {
			out = append(out, b)
		}

		return true
	})

	return out
}

// dropIf removes an if with nothing to keep, leaving its init statement.
func (f *errFold) dropIf(ifs, parent *ast.IfStmt, isElseIf bool) {
	switch {
	case isElseIf:
		f.edits = append(f.edits, foldEdit{Start: parent.Body.End(), End: ifs.End()})
	case ifs.Init != nil:
		f.edits = append(f.edits, foldEdit{Start: ifs.Pos(), End: ifs.Init.Pos()}, foldEdit{Start: ifs.Init.End(), End: ifs.End()})
	default:
		f.edits = append(f.edits, foldEdit{Start: ifs.Pos(), End: ifs.End(), Lines: true})
	}
}

// unwrapIf replaces ifs by keep, one of its branches. The branch keeps its
// braces when stripping them would change which names share a scope. With an
// init statement that still declares names, the result is wrapped in a block.
func (f *errFold) unwrapIf(ifs *ast.IfStmt, keep ast.Stmt, isElseIf bool) {
	wrap := ifs.Init != nil && f.initDefines(ifs.Init)
	from, to := keep.Pos(), keep.End() // retained source

	block, ok := keep.(*ast.BlockStmt)
	if ok && !isElseIf && !declaresNames(block) {
		from, to = block.Lbrace+1, block.Rbrace
	}

	strip := from != keep.Pos()

	if ifs.Init == nil {
		f.edits = append(f.edits, foldEdit{Start: ifs.Pos(), End: from, Lines: strip})
	} else {
		open := ""
		if wrap {
			open = "{\n"
		}

		f.edits = append(f.edits,
			foldEdit{Start: ifs.Pos(), End: ifs.Init.Pos(), Text: open},
			foldEdit{Start: ifs.Init.End(), End: from, Text: "\n", LineAfter: true})
	}

	closer := ""
	if wrap {
		closer = "\n}"
	}

	if to != ifs.End() || closer != "" {
		f.edits = append(f.edits, foldEdit{Start: to, End: ifs.End(), Text: closer, Lines: strip && closer == "", LineBefore: true})
	}
}

// initDefines reports whether init, once the removed variable is gone, still
// declares a name.
func (f *errFold) initDefines(init ast.Stmt) bool {
	as, ok := init.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE {
		return false
	}

	for i, l := range as.Lhs {
		id, ok := l.(*ast.Ident)
		if ok && id.Name != "_" && (as != f.assign || i != f.idx) {
			return true
		}
	}

	return false
}

// declaresNames reports whether block declares a name in its own scope.
func declaresNames(block *ast.BlockStmt) bool {
	for _, s := range block.List {
		switch s := s.(type) {
		case *ast.DeclStmt, *ast.LabeledStmt:
			return true
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				return true
			}
		}
	}

	return false
}

// hasFollowingStmt reports whether a statement follows the one at c, or the
// bare block that ends with it, in its list.
func hasFollowingStmt(c inspector.Cursor) bool {
	for {
		_, i := c.ParentEdge()
		switch p := c.Parent().Node().(type) {
		case *ast.BlockStmt:
			if i < len(p.List)-1 {
				return true
			}
		case *ast.CaseClause:
			return i < len(p.Body)-1
		case *ast.CommClause:
			return i < len(p.Body)-1
		default:
			return false
		}

		block := c.Parent()

		k := block.ParentEdgeKind()
		if k != edge.BlockStmt_List && k != edge.CaseClause_Body && k != edge.CommClause_Body {
			return false
		}

		c = block
	}
}

// mergeTouching joins deletions that touch, so the whole-line widening sees
// them as one.
func mergeTouching(edits []foldEdit) []foldEdit {
	slices.SortFunc(edits, func(a, b foldEdit) int { return int(a.Start) - int(b.Start) })

	var out []foldEdit
	for _, e := range edits {
		n := len(out)
		if n > 0 && out[n-1].End == e.Start && out[n-1].Text == "" && e.Text == "" {
			out[n-1].End = e.End
			out[n-1].Lines = out[n-1].Lines || e.Lines

			continue
		}

		out = append(out, e)
	}

	return out
}

// checkLastUses refuses when the dropped source holds the last use of a local
// variable or label declared outside it, which would leave it unused.
func (f *errFold) checkLastUses(site analysis.CallSite) {
	if len(f.dropped) == 0 {
		return
	}

	var fn ast.Node
	for enc := range site.Cursor.Enclosing((*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)) {
		fn = enc.Node()
	}

	if fn == nil {
		return
	}
	var params [][2]token.Pos
	written := map[*ast.Ident]bool{}
	type count struct {
		dropped token.Pos
		outside int
	}
	counts := map[gotypes.Object]*count{}

	ast.Inspect(fn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FieldList:
			params = append(params, [2]token.Pos{n.Pos(), n.End()})
		case *ast.AssignStmt:
			if n.Tok == token.ASSIGN {
				for _, l := range n.Lhs {
					id, ok := l.(*ast.Ident)
					if ok {
						written[id] = true
					}
				}
			}
		case *ast.IncDecStmt:
			id, ok := n.X.(*ast.Ident)
			if ok {
				written[id] = true
			}
		case *ast.Ident:
			obj := f.info.Uses[n]
			switch v := obj.(type) {
			case *gotypes.Var:
				if v.IsField() || v == f.obj || written[n] {
					return true
				}
			case *gotypes.Label:
			default:
				return true
			}

			if obj.Pos() < fn.Pos() || obj.Pos() >= fn.End() || f.inDropped(obj.Pos()) {
				return true
			}

			c := counts[obj]
			if c == nil {
				c = &count{}
				counts[obj] = c
			}

			if f.inDropped(n.Pos()) {
				c.dropped = n.Pos()
			} else {
				c.outside++
			}
		}

		return true
	})

	for obj, c := range counts {
		inParams := slices.ContainsFunc(params, func(r [2]token.Pos) bool { return r[0] <= obj.Pos() && obj.Pos() < r[1] })
		if c.dropped.IsValid() && c.outside == 0 && !inParams {
			f.refusal(c.dropped, fmt.Sprintf("folding the nil check removes the last use of %s, which would be left unused", obj.Name()))
		}
	}
}

// foldChange converts e to a plan change in file.
func foldChange(ws *pkgtypes.Workspace, file *pkgtypes.File, e foldEdit) pkgtypes.Change {
	content := file.OriginalContent

	start, end := ws.FileSet.Position(e.Start).Offset, ws.FileSet.Position(e.End).Offset
	if e.Lines {
		start, end = wholeLines(content, start, end)
	}

	text := e.Text
	if e.LineAfter && end < len(content) && content[end] == '\n' {
		text = ""
	}

	if e.LineBefore && text != "" {
		i := start
		for i > 0 && (content[i-1] == ' ' || content[i-1] == '\t') {
			i--
		}

		if i > 0 && content[i-1] == '\n' {
			text = strings.TrimPrefix(text, "\n")
		}
	}
	// "Wrap" and "Replace" exempt a fragment from the validator's syntax check.
	desc := foldDescription

	switch {
	case strings.TrimSpace(text) == "" && text != "":
		desc = "Replace folded if header with a line break"
	case text != "":
		desc = "Wrap init statement of folded if in a block"
	}

	return pkgtypes.Change{
		File:        file.Path,
		Start:       start,
		End:         end,
		OldText:     string(content[start:end]),
		NewText:     text,
		Description: desc,
	}
}
