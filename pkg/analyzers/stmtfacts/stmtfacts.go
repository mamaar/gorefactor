// Package stmtfacts answers the two questions every flattening analyzer has to
// ask before it may drop an `else`: does this branch hand control away for
// good, and would dedenting that block redeclare a name the enclosing scope
// already uses.
//
// Both questions used to be answered privately inside package earlyreturn, with
// a terminator check that accepted only five statement kinds — return, break,
// continue, goto, and a call to panic — as a *last* statement. That is far
// narrower than Go's own definition, and the gap had teeth: a branch ending in
// an if-else whose arms all return is terminating by the spec, but read as
// non-terminating here, so the `else` after it looked undroppable and the chain
// it belonged to was reported unfixable. Terminates below implements the spec
// definition instead (Go spec, "Terminating statements"), which is what lets
// deepifelse flatten real code rather than only the textbook shape.
//
// One deliberate deviation from the spec: a bare break or continue counts as
// terminating at the top level. The spec excludes them because it is answering
// "can this function fall off its end", whereas callers here are asking "can
// control reach the statement after this one in *this* block" — and a break
// does leave the block, whatever it does afterwards. Where the distinction
// matters the recursion tracks it explicitly: inside a switch or select clause
// a break lands just past the switch, so it is not terminating there, and the
// breakExits parameter carries that context down.
package stmtfacts

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
)

// Terminates reports whether control cannot continue to the statement
// following stmt in the block that contains it.
//
// info is used only to confirm that a call to `panic` really is the builtin —
// a local declaration named panic would shadow it, and treating a call to that
// as terminating would drop an else branch that still needs to run. It may be
// nil, in which case the name alone decides (see IsPanicCall).
func Terminates(info *types.Info, stmt ast.Stmt) bool {
	return terminates(info, stmt, "", true)
}

// terminates is Terminates with the two pieces of context the recursion needs.
//
// label is the label attached to stmt, if any, so `for` and `switch` can tell
// whether a `break L` somewhere inside them is aimed at them. breakExits says
// whether a bare `break` at this position takes control out of the region the
// caller cares about: true inside plain blocks and if-else arms, false inside a
// switch or select clause, where a break only ends the switch and execution
// carries on directly after it.
func terminates(info *types.Info, stmt ast.Stmt, label string, breakExits bool) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true

	case *ast.BranchStmt:
		switch s.Tok {
		case token.GOTO:
			return true
		case token.CONTINUE:
			// A continue always leaves the current block, including a switch
			// clause: it resumes at the enclosing loop's post statement.
			return true
		case token.FALLTHROUGH:
			// Control moves to the next clause rather than past the switch.
			// Callers require every clause to terminate, so the switch as a
			// whole is still judged on the clause that fallthrough reaches.
			return true
		case token.BREAK:
			return breakExits
		}
		return false

	case *ast.ExprStmt:
		return IsPanicCall(info, s.X)

	case *ast.LabeledStmt:
		return terminates(info, s.Stmt, s.Label.Name, breakExits)

	case *ast.BlockStmt:
		return lastTerminates(info, s.List, breakExits)

	case *ast.IfStmt:
		// Without an else there is always a path around the if.
		if s.Else == nil {
			return false
		}
		return lastTerminates(info, s.Body.List, breakExits) &&
			terminates(info, s.Else, "", breakExits)

	case *ast.ForStmt:
		// `for {}` never finishes on its own, so only a break aimed at it can
		// let control past. A `for` with a condition can always fail it.
		return s.Cond == nil && !breaksOutList(s.Body.List, label)

	case *ast.RangeStmt:
		// A range loop ends when the operand is exhausted.
		return false

	case *ast.SwitchStmt:
		return clausesTerminate(info, s.Body, label, true)

	case *ast.TypeSwitchStmt:
		return clausesTerminate(info, s.Body, label, true)

	case *ast.SelectStmt:
		// A select has no "no case matched" path to fall out of, so unlike a
		// switch it needs no default to be terminating.
		return clausesTerminate(info, s.Body, label, false)
	}

	return false
}

// lastTerminates reports whether a statement list ends in a terminating
// statement. An empty list falls straight through.
func lastTerminates(info *types.Info, list []ast.Stmt, breakExits bool) bool {
	if len(list) == 0 {
		return false
	}
	return terminates(info, list[len(list)-1], "", breakExits)
}

// clausesTerminate reports whether a switch or select is terminating: every
// clause body ends in a terminating statement, no break anywhere inside is
// aimed at it, and — for a switch, which can otherwise match nothing — there is
// a default clause.
//
// The clause bodies are checked with breakExits false: a break in a clause
// lands immediately after the switch, so a clause ending in one does not hand
// control away. The separate breaksOutList scan is still needed because a break
// in any position, not just the last, gives control that same way out.
func clausesTerminate(info *types.Info, body *ast.BlockStmt, label string, needDefault bool) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	if breaksOutList(body.List, label) {
		return false
	}

	hasDefault := false
	for _, clause := range body.List {
		switch c := clause.(type) {
		case *ast.CaseClause:
			if c.List == nil {
				hasDefault = true
			}
			if !lastTerminates(info, c.Body, false) {
				return false
			}
		case *ast.CommClause:
			if c.Comm == nil {
				hasDefault = true
			}
			if !lastTerminates(info, c.Body, false) {
				return false
			}
		default:
			return false
		}
	}

	return hasDefault || !needDefault
}

// breaksOutList reports whether any break inside a statement list targets the
// construct enclosing that list — a bare break that no nested breakable
// construct has already captured, or a break naming label.
func breaksOutList(list []ast.Stmt, label string) bool {
	for _, stmt := range list {
		if breaksOut(stmt, label) {
			return true
		}
	}
	return false
}

func breaksOut(stmt ast.Stmt, label string) bool {
	switch s := stmt.(type) {
	case *ast.BranchStmt:
		if s.Tok != token.BREAK {
			return false
		}
		if s.Label == nil {
			return true
		}
		return label != "" && s.Label.Name == label

	case *ast.BlockStmt:
		return breaksOutList(s.List, label)

	case *ast.IfStmt:
		if breaksOutList(s.Body.List, label) {
			return true
		}
		if s.Else != nil {
			return breaksOut(s.Else, label)
		}
		return false

	case *ast.LabeledStmt:
		return breaksOut(s.Stmt, label)

	case *ast.CaseClause:
		return breaksOutList(s.Body, label)

	case *ast.CommClause:
		return breaksOutList(s.Body, label)

	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		// A nested breakable construct swallows every bare break beneath it.
		// Only a break naming our own label still reaches us.
		return hasLabeledBreak(s, label)
	}

	return false
}

// hasLabeledBreak reports whether n contains `break label`. It is only ever
// asked about nested breakable constructs, where a bare break belongs to the
// nested construct and a labelled one may not.
func hasLabeledBreak(n ast.Node, label string) bool {
	if label == "" {
		return false
	}
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found {
			return false
		}
		br, ok := node.(*ast.BranchStmt)
		if ok && br.Tok == token.BREAK && br.Label != nil && br.Label.Name == label {
			found = true
			return false
		}
		return true
	})
	return found
}

// IsPanicCall reports whether expr is a call to the builtin panic.
//
// Type information decides when it is available: a local declaration named
// panic shadows the builtin, and treating a call to that as terminating would
// drop an else branch that still needs to run. The name alone is the fallback
// for packages that were not type-checked.
func IsPanicCall(info *types.Info, expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "panic" {
		return false
	}

	if info != nil && info.Uses != nil {
		if obj, found := info.Uses[ident]; found {
			_, isBuiltin := obj.(*types.Builtin)
			return isBuiltin
		}
	}
	return true
}

// DeclaredNames collects the identifiers a statement list declares directly in
// its own scope. Declarations inside nested blocks are excluded: they travel
// with their block and cannot collide.
func DeclaredNames(list []ast.Stmt) map[string]bool {
	names := make(map[string]bool)

	for _, stmt := range list {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok != token.DEFINE {
				continue
			}
			for _, lhs := range s.Lhs {
				AddName(names, lhs)
			}
		case *ast.DeclStmt:
			decl, ok := s.Decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range decl.Specs {
				switch sp := spec.(type) {
				case *ast.ValueSpec:
					for _, ident := range sp.Names {
						AddName(names, ident)
					}
				case *ast.TypeSpec:
					AddName(names, sp.Name)
				}
			}
		}
	}

	return names
}

// SignatureNames collects receiver, parameter, and result names, which share
// the function body's scope and so can collide with a dedented declaration.
func SignatureNames(recv *ast.FieldList, fnType *ast.FuncType) map[string]bool {
	names := make(map[string]bool)

	lists := []*ast.FieldList{recv}
	if fnType != nil {
		lists = append(lists, fnType.Params, fnType.Results)
	}

	for _, list := range lists {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			for _, ident := range field.Names {
				AddName(names, ident)
			}
		}
	}

	return names
}

// AddName records expr's identifier, ignoring the blank identifier, which
// declares nothing and can never collide.
func AddName(names map[string]bool, expr ast.Expr) {
	if ident, ok := expr.(*ast.Ident); ok && ident.Name != "_" {
		names[ident.Name] = true
	}
}

// FirstClash returns a name declared in both maps, or "" when they are
// disjoint. Candidates are sorted so the reported reason is stable across runs.
func FirstClash(moved, destination map[string]bool) string {
	var clashes []string
	for name := range moved {
		if destination[name] {
			clashes = append(clashes, name)
		}
	}
	if len(clashes) == 0 {
		return ""
	}
	sort.Strings(clashes)
	return clashes[0]
}

// ReferencesAny reports whether n mentions any of the given names.
//
// The match is by name rather than by resolved object, which over-approximates:
// an identifier that happens to share a name with a shadowed declaration counts
// as a reference. Callers use this to decide whether a rewrite is safe, so an
// over-approximation can only cost them a refusal, never produce a wrong
// rewrite. Selector fields are skipped — the `Y` in `x.Y` names a field, not
// the variable a caller is asking about.
func ReferencesAny(n ast.Node, names map[string]bool) bool {
	if n == nil || len(names) == 0 {
		return false
	}

	found := false
	var visit func(ast.Node) bool
	visit = func(node ast.Node) bool {
		if found || node == nil {
			return false
		}
		switch v := node.(type) {
		case *ast.SelectorExpr:
			// Descend into the operand under the same visitor, so a selector
			// nested inside it is treated the same way, but never look at the
			// field name itself.
			ast.Inspect(v.X, visit)
			return false
		case *ast.Ident:
			if names[v.Name] {
				found = true
			}
			return false
		}
		return true
	}
	ast.Inspect(n, visit)

	return found
}
