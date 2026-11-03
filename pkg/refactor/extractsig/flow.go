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

func (a *analyzer) newFlow() *flow {
	return &flow{a: a, facts: flowFacts{
		readBeforeWrite: objSet{}, read: objSet{}, written: objSet{},
	}}
}

func (a *analyzer) analyzeFlow() *flowFacts {
	f := a.newFlow()
	f.facts.definite = f.list(a.stmts, objSet{})
	return &f.facts
}

// readAfterBlock reports whether code that runs after the block in its
// function can read obj's value as the block left it. Loops and closures
// are liveOut's concern.
func (a *analyzer) readAfterBlock(obj gotypes.Object) bool {
	f := a.newFlow()
	w := objSet{}
	for _, s := range a.followingStmts() {
		w = f.stmt(s, w)
		if terminates(s) {
			break
		}
	}
	return f.facts.unreliable || f.facts.readBeforeWrite[obj]
}

// followingStmts lists, innermost first, the statements that run after the
// selection in each enclosing statement list up to the end of its function.
func (a *analyzer) followingStmts() []ast.Stmt {
	path := a.pathTo(a.stmts[len(a.stmts)-1])
	var out []ast.Stmt
	for i := len(path) - 2; i >= 0; i-- {
		var list []ast.Stmt
		switch p := path[i].(type) {
		case *ast.BlockStmt:
			list = p.List
		case *ast.CaseClause:
			list = p.Body
		case *ast.CommClause:
			list = p.Body
		}
		for j, s := range list {
			if ast.Node(s) == path[i+1] {
				out = append(out, list[j+1:]...)
			}
		}
		if path[i] == ast.Node(a.innerBody) {
			break
		}
	}
	return out
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
		f.read(obj, w)
		return true
	})
}

func (f *flow) read(obj gotypes.Object, w objSet) {
	f.facts.read[obj] = true
	if !w[obj] {
		f.facts.readBeforeWrite[obj] = true
	}
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
	case *ast.ReturnStmt:
		f.returnStmt(x, w)
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

// returnStmt reads the results it lists; a bare return reads every named
// result of the function.
func (f *flow) returnStmt(x *ast.ReturnStmt, w objSet) {
	if len(x.Results) > 0 {
		f.reads(x, w)
		return
	}
	for v := range f.a.innerSig.Results().Variables() {
		if f.a.isCapturedVar(v) {
			f.read(v, w)
		}
	}
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

// writeFirst reports whether the block never reads obj before assigning it
// and needs no incoming value: the block assigns it on every path and the
// value leaves the block or is read in it, or the block assigns it on some
// paths only and nothing after the block observes it.
func (a *analyzer) writeFirst(obj gotypes.Object) bool {
	f := a.flow
	if f.unreliable || !f.written[obj] || f.readBeforeWrite[obj] {
		return false
	}
	if f.definite[obj] {
		return f.read[obj] || a.liveOut(obj)
	}
	return f.read[obj] && !a.liveOut(obj)
}

// liveOut reports whether code outside the block can observe the value the
// block leaves in obj: a later read that no write intervenes before, a
// reference in a loop or closure that runs again, a closure defined
// elsewhere, or an address taken elsewhere.
func (a *analyzer) liveOut(obj gotypes.Object) bool {
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
	return found || a.readAfterBlock(obj)
}

// knownZero reports whether obj provably holds its zero value when the block
// starts: it is declared by a `var x T` without a value, nothing before the
// block mentions it, no loop or goto can bring the block round again, and the
// block neither takes its address nor captures it in a closure.
func (a *analyzer) knownZero(obj gotypes.Object) bool {
	if a.zeroDecl(obj) == nil {
		return false
	}
	if region := a.repeatRegion(); region != nil && obj.Pos() < region.Pos() {
		return false
	}
	var stack []ast.Node
	ok := true
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.BranchStmt:
			ok = ok && x.Tok != token.GOTO
		case *ast.Ident:
			if a.info.Uses[x] != obj {
				break
			}
			switch {
			case x.Pos() < a.blockStart:
				ok = false
			case x.Pos() < a.blockEnd:
				ok = ok && !a.inBlockClosure(stack) && !takesAddress(stack)
			}
		}
		return ok
	})
	return ok
}

// inBlockClosure reports whether the ancestors include a function literal
// inside the selection.
func (a *analyzer) inBlockClosure(stack []ast.Node) bool {
	for _, n := range stack {
		if _, ok := n.(*ast.FuncLit); ok && n.Pos() >= a.blockStart {
			return true
		}
	}
	return false
}

// zeroDecl returns the `var x T` statement that declares obj with no value.
func (a *analyzer) zeroDecl(obj gotypes.Object) *ast.DeclStmt {
	var found *ast.DeclStmt
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		ds, ok := n.(*ast.DeclStmt)
		if !ok || found != nil {
			return found == nil
		}
		gd, ok := ds.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			return false
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for _, name := range vs.Names {
				if a.info.Defs[name] == obj && len(vs.Values) == 0 {
					found = ds
				}
			}
		}
		return false
	})
	return found
}

// droppableDecl returns the standalone `var x T` statement for a known-zero
// obj that sits in the same statement list as the block, so the call site can
// declare the variable in its place.
func (a *analyzer) droppableDecl(obj gotypes.Object) *ast.DeclStmt {
	ds := a.zeroDecl(obj)
	if ds == nil || ds.Pos() >= a.blockStart {
		return nil
	}
	gd := ds.Decl.(*ast.GenDecl)
	if gd.Lparen.IsValid() || len(gd.Specs[0].(*ast.ValueSpec).Names) != 1 {
		return nil
	}
	path := a.pathTo(a.stmts[0])
	parent := path[len(path)-2]
	for _, c := range children(parent) {
		if c == ast.Node(ds) {
			return ds
		}
	}
	return nil
}

// repeatRegion returns the outermost loop or function literal around the
// block, whose body can run again after the block did. A loop that is the
// whole selection does not repeat it.
func (a *analyzer) repeatRegion() ast.Node {
	var region ast.Node
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.ForStmt, *ast.RangeStmt, *ast.FuncLit:
			if region == nil && n.Pos() <= a.blockStart && a.blockEnd <= n.End() && (n.Pos() != a.blockStart || n.End() != a.blockEnd) {
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
