package extractsig

import (
	"go/ast"
	"go/token"
	gotypes "go/types"
)

type objSet map[gotypes.Object]bool

func (s objSet) clone() objSet {
	out := make(objSet, len(s))
	for k := range s {
		out[k] = true
	}
	return out
}

// intersect returns the objects present in every set; with no sets it
// returns fallback.
func intersect(sets []objSet, fallback objSet) objSet {
	if len(sets) == 0 {
		return fallback
	}
	out := sets[0].clone()
	for _, s := range sets[1:] {
		for k := range out {
			if !s[k] {
				delete(out, k)
			}
		}
	}
	return out
}

// flowFacts records how the selection uses variables declared outside it.
type flowFacts struct {
	// readBeforeWrite: some path reads the variable before the block has
	// assigned it.
	readBeforeWrite objSet
	// read: the block reads the variable somewhere.
	read objSet
	// written: the block assigns the variable by a plain `x = ...`.
	written objSet
	// definite: every path that leaves the block normally has assigned it.
	definite objSet
	// unreliable: the block uses goto, so none of the above is trustworthy.
	unreliable bool
}

// flow is a conservative definite-assignment walk over the selection. Where
// it cannot tell, it reports a read, which keeps the variable an input.
type flow struct {
	a     *analyzer
	facts flowFacts
}

func (a *analyzer) analyzeFlow() *flowFacts {
	f := &flow{a: a, facts: flowFacts{
		readBeforeWrite: objSet{}, read: objSet{}, written: objSet{},
	}}
	f.facts.definite = f.list(a.stmts, objSet{})
	return &f.facts
}

func (f *flow) list(stmts []ast.Stmt, w objSet) objSet {
	for _, s := range stmts {
		w = f.stmt(s, w)
	}
	return w
}

// reads records every tracked variable mentioned in n as a read. w is the set
// assigned on every path to n.
func (f *flow) reads(n ast.Node, w objSet) {
	if n == nil {
		return
	}
	ast.Inspect(n, func(c ast.Node) bool {
		id, ok := c.(*ast.Ident)
		if !ok {
			return true
		}
		obj := f.a.objectOf(id)
		if !f.a.isCapturedVar(obj) {
			return true
		}
		f.facts.read[obj] = true
		if !w[obj] {
			f.facts.readBeforeWrite[obj] = true
		}
		return true
	})
}

// write marks e as assigned when it is a plain tracked variable, and reads it
// otherwise (`x.f = v` and `x[i] = v` need the incoming x).
func (f *flow) write(e ast.Expr, w objSet) {
	id, ok := e.(*ast.Ident)
	if !ok {
		f.reads(e, w)
		return
	}
	obj := f.a.info.Uses[id]
	if !f.a.isCapturedVar(obj) {
		return
	}
	f.facts.written[obj] = true
	w[obj] = true
}

func (f *flow) stmt(s ast.Stmt, w objSet) objSet {
	switch x := s.(type) {
	case *ast.AssignStmt:
		for _, r := range x.Rhs {
			f.reads(r, w)
		}
		plain := x.Tok == token.ASSIGN || x.Tok == token.DEFINE
		for _, l := range x.Lhs {
			if !plain {
				f.reads(l, w)
			}
			f.write(l, w)
		}
	case *ast.BlockStmt:
		return f.list(x.List, w)
	case *ast.LabeledStmt:
		return f.stmt(x.Stmt, w)
	case *ast.BranchStmt:
		if x.Tok == token.GOTO {
			f.facts.unreliable = true
		}
	case *ast.IfStmt:
		return f.ifStmt(x, w)
	case *ast.ForStmt:
		w = f.optStmt(x.Init, w)
		f.reads(x.Cond, w)
		body := f.list(x.Body.List, w.clone())
		f.optStmt(x.Post, body)
	case *ast.RangeStmt:
		f.reads(x.X, w)
		body := w.clone()
		if x.Tok == token.ASSIGN {
			if x.Key != nil {
				f.write(x.Key, body)
			}
			if x.Value != nil {
				f.write(x.Value, body)
			}
		}
		f.list(x.Body.List, body)
	case *ast.SwitchStmt:
		w = f.optStmt(x.Init, w)
		f.reads(x.Tag, w)
		return f.clauses(x.Body, w, true)
	case *ast.TypeSwitchStmt:
		w = f.optStmt(x.Init, w)
		f.reads(x.Assign, w)
		return f.clauses(x.Body, w, true)
	case *ast.SelectStmt:
		return f.clauses(x.Body, w, false)
	case *ast.EmptyStmt:
	default:
		f.reads(s, w)
	}
	return w
}

func (f *flow) optStmt(s ast.Stmt, w objSet) objSet {
	if s == nil {
		return w
	}
	return f.stmt(s, w)
}

func (f *flow) ifStmt(x *ast.IfStmt, w objSet) objSet {
	w = f.optStmt(x.Init, w)
	f.reads(x.Cond, w)
	var joined []objSet
	body := f.list(x.Body.List, w.clone())
	if !terminates(x.Body) {
		joined = append(joined, body)
	}
	if x.Else == nil {
		joined = append(joined, w)
	} else {
		other := f.stmt(x.Else, w.clone())
		if !terminates(x.Else) {
			joined = append(joined, other)
		}
	}
	return intersect(joined, w)
}

// clauses walks the case or comm clauses of a switch or select. A clause
// containing a break contributes the pre-switch set, because the break can
// leave before the clause's later assignments.
func (f *flow) clauses(body *ast.BlockStmt, w objSet, needDefault bool) objSet {
	var joined []objSet
	hasDefault := false
	for _, cl := range body.List {
		var list []ast.Stmt
		set := w.clone()
		switch c := cl.(type) {
		case *ast.CaseClause:
			hasDefault = hasDefault || c.List == nil
			for _, e := range c.List {
				f.reads(e, w)
			}
			list = c.Body
		case *ast.CommClause:
			hasDefault = hasDefault || c.Comm == nil
			set = f.optStmt(c.Comm, set)
			list = c.Body
		}
		set = f.list(list, set)
		switch {
		case hasAnyBreak(list):
			joined = append(joined, w)
		case !alwaysTerminates(list):
			joined = append(joined, set)
		}
	}
	if needDefault && !hasDefault {
		joined = append(joined, w)
	}
	return intersect(joined, w)
}

// hasAnyBreak reports whether list contains a break outside a function
// literal, whichever statement it targets.
func hasAnyBreak(list []ast.Stmt) bool {
	found := false
	for _, s := range list {
		ast.Inspect(s, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.BranchStmt:
				found = found || x.Tok == token.BREAK
			}
			return !found
		})
	}
	return found
}

// writeFirst reports whether the block always assigns obj before reading it
// and the value then either leaves the block or is read in it, so the
// extracted function needs no incoming value.
func (a *analyzer) writeFirst(obj gotypes.Object) bool {
	f := a.flow
	if f.unreliable || !f.written[obj] || f.readBeforeWrite[obj] || !f.definite[obj] {
		return false
	}
	return f.read[obj] || a.liveOut(obj)
}

// liveOut reports whether code outside the block can observe obj's value
// after the block: a later reference, a reference in a loop or closure that
// runs again, a closure defined elsewhere, or an address taken elsewhere.
func (a *analyzer) liveOut(obj gotypes.Object) bool {
	if a.usedAfterBlock(obj) {
		return true
	}
	region := a.repeatRegion()
	var stack []ast.Node
	found := false
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		id, ok := n.(*ast.Ident)
		if !ok || a.info.Uses[id] != obj || (id.Pos() >= a.blockStart && id.Pos() < a.blockEnd) {
			return !found
		}
		inRegion := region != nil && region.Pos() <= id.Pos() && id.Pos() < region.End() && obj.Pos() < region.Pos()
		found = found || inRegion || a.inForeignClosure(stack) || takesAddress(stack)
		return !found
	})
	return found
}

// repeatRegion returns the outermost loop or function literal around the
// block, whose body can run again after the block did.
func (a *analyzer) repeatRegion() ast.Node {
	var region ast.Node
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.ForStmt, *ast.RangeStmt, *ast.FuncLit:
			if region == nil && n.Pos() <= a.blockStart && a.blockEnd <= n.End() {
				region = n
			}
		}
		return region == nil
	})
	return region
}

// inForeignClosure reports whether the ancestors include a function literal
// that does not contain the block.
func (a *analyzer) inForeignClosure(stack []ast.Node) bool {
	for _, n := range stack {
		lit, ok := n.(*ast.FuncLit)
		if ok && !(lit.Pos() <= a.blockStart && a.blockEnd <= lit.End()) {
			return true
		}
	}
	return false
}

// takesAddress reports whether the identifier at the top of stack is the
// operand of an address-of expression, possibly through selectors or indexes.
func takesAddress(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		switch p := stack[i].(type) {
		case *ast.UnaryExpr:
			return p.Op == token.AND
		case *ast.SelectorExpr, *ast.IndexExpr, *ast.ParenExpr, *ast.SliceExpr:
		default:
			return false
		}
	}
	return false
}
