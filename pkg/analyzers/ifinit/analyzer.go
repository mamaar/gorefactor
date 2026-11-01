package ifinit

import (
	"bytes"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Column     int      `json:"column"`
	Variables  []string `json:"variables"`
	Expression string   `json:"expression"`
	Snippet    string   `json:"snippet"`
	Function   string   `json:"function"`
	Kind       string   `json:"kind"` // "if" | "switch" | "type_switch"
	IsElseIf   bool     `json:"is_else_if"`
	Skipped    bool     `json:"skipped"`
	SkipReason string   `json:"skip_reason,omitempty"`
}

var Analyzer = &analysis.Analyzer{
	Name:     "ifinit",
	Doc:      "detects if/switch-init assignments that should be split into separate assignment and if/switch-check",
	Run:      run,
	Requires: []*analysis.Analyzer{filedata.Analyzer},
}

// fixMessage MUST stay byte-identical: it flows into types.Change.Description,
// and this project's validator and import logic string-match on it.
const fixMessage = "Split if-init assignment into separate assignment and if-check"

func run(pass *analysis.Pass) (any, error) {
	fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
	var results []*Result

	for _, file := range pass.Files {
		pos := pass.Fset.Position(file.Pos())
		content := fd.Content[pos.Filename]
		results = append(results, analyzeFile(pass, file, content)...)
	}

	return results, nil
}

// declaredName is what a "declared" scope map stores for a name already in
// scope at some point in a statement list. Usually it is just the declaring
// ident, so canDowngrade can look its object up via pass.TypesInfo.Defs/Uses.
// For names that go/types does not record that way — the implicit per-clause
// variable of a type-switch case clause — obj is pre-resolved instead (see
// pass.TypesInfo.Implicits) and takes priority when set.
type declaredName struct {
	ident *ast.Ident
	obj   types.Object
}

// scope maps names in scope at some point in a statement list to the
// declaration that put them there.
type scope map[string]declaredName

// walker performs a recursive descent over statement lists (BlockStmt.List,
// CaseClause.Body, CommClause.Body). Each list carries its own "declared"
// scope: the set of names already in scope at each point in that list. Most
// lists start empty (Go's := may legally shadow an outer scope), except a
// function/func-literal body (seeded from receiver/params/named results) and
// a type-switch case clause or select comm clause, whose clause-scoped guard
// variable Go treats as declared in the clause's own implicit block.
type walker struct {
	pass        *analysis.Pass
	content     []byte
	results     []*Result
	currentFunc string
}

func analyzeFile(pass *analysis.Pass, file *ast.File, content []byte) []*Result {
	w := &walker{pass: pass, content: content}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			w.currentFunc = d.Name.Name
			w.walkList(d.Body.List, functionScopeNames(d))
		case *ast.GenDecl:
			// Package-level var/const initializers may embed func literals
			// (e.g. `var f = func() { ... }`); scan those for violations too.
			w.currentFunc = ""
			w.processFuncLitsIn(d)
		}
	}

	return w.results
}

// walkList processes a statement list strictly in order, mutating declared
// as it goes so that later statements see the effect of earlier hoists.
//
// cascaded is scoped to this one list: it tracks later plain ':=' siblings
// that an earlier hoist in this same list has already claimed and rewritten
// to '=' (see forwardCascade), so a second hoist that would independently
// also reach the same statement does not emit a second, overlapping edit
// for it.
func (w *walker) walkList(list []ast.Stmt, declared scope) {
	cascaded := make(map[*ast.AssignStmt]bool)
	for i, stmt := range list {
		w.walkStmt(stmt, list, i, declared, cascaded)
	}
}

func (w *walker) walkStmt(stmt ast.Stmt, list []ast.Stmt, idx int, declared scope, cascaded map[*ast.AssignStmt]bool) {
	switch s := stmt.(type) {
	case *ast.DeclStmt:
		w.recordDeclStmt(s, declared)
		w.processFuncLitsIn(s)
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE {
			for _, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					declared[id.Name] = declaredName{ident: id}
				}
			}
		}
		w.processFuncLitsIn(s)
	case *ast.IfStmt:
		w.handleIfChain(s, declared, false, false, list, idx, cascaded)
	case *ast.SwitchStmt:
		w.handleSwitch(s, declared, false, list, idx, cascaded)
	case *ast.TypeSwitchStmt:
		w.handleTypeSwitch(s, declared, false, list, idx, cascaded)
	case *ast.ForStmt:
		// Explicitly not matched: for-loop inits must never be hoisted.
		w.processFuncLitsIn(s.Init)
		w.processFuncLitsIn(s.Cond)
		w.processFuncLitsIn(s.Post)
		if s.Body != nil {
			w.walkList(s.Body.List, make(scope))
		}
	case *ast.RangeStmt:
		// Explicitly not matched.
		w.processFuncLitsIn(s.X)
		if s.Body != nil {
			w.walkList(s.Body.List, make(scope))
		}
	case *ast.BlockStmt:
		w.walkList(s.List, make(scope))
	case *ast.SelectStmt:
		if s.Body != nil {
			for _, c := range s.Body.List {
				if cc, ok := c.(*ast.CommClause); ok {
					w.processFuncLitsIn(cc.Comm)
					w.walkList(cc.Body, commClauseScope(cc))
				}
			}
		}
	case *ast.LabeledStmt:
		w.walkLabeled(s, list, idx, declared, cascaded)
	default:
		w.processFuncLitsIn(stmt)
	}
}

// walkLabeled handles the guard: an if/switch/type-switch that is the direct
// Stmt of a LabeledStmt must not have its own Init hoisted (that would
// retarget the label onto the assignment). Nested content is still walked.
func (w *walker) walkLabeled(s *ast.LabeledStmt, list []ast.Stmt, idx int, declared scope, cascaded map[*ast.AssignStmt]bool) {
	switch inner := s.Stmt.(type) {
	case *ast.IfStmt:
		w.handleIfChain(inner, declared, false, true, list, idx, cascaded)
	case *ast.SwitchStmt:
		w.handleSwitch(inner, declared, true, list, idx, cascaded)
	case *ast.TypeSwitchStmt:
		w.handleTypeSwitch(inner, declared, true, list, idx, cascaded)
	default:
		w.walkStmt(s.Stmt, list, idx, declared, cascaded)
	}
}

// handleIfChain processes one *ast.IfStmt. isElseIf is true when this node
// was reached via a parent IfStmt's Else field; guarded is true when this
// node is the direct Stmt of a LabeledStmt (guarded implies !isElseIf). list
// and idx locate node within its enclosing statement list, for the forward
// cascade scan (see forwardCascade); an else-if node has no such list (its
// own new names never escape the brand-new block it hoists into), so the
// recursive else-if call below passes nil/-1/nil.
func (w *walker) handleIfChain(node *ast.IfStmt, declared scope, isElseIf, guarded bool, list []ast.Stmt, idx int, cascaded map[*ast.AssignStmt]bool) {
	w.processFuncLitsIn(node.Init)
	w.processFuncLitsIn(node.Cond)

	if !guarded {
		if assign, ok := matchInitAssign(node.Init); ok {
			w.emitIfFix(node, assign, declared, isElseIf, list, idx, cascaded)
		}
	}

	if node.Body != nil {
		w.walkList(node.Body.List, make(scope))
	}

	switch e := node.Else.(type) {
	case *ast.IfStmt:
		// A chained else-if is never guarded, even under a labeled head if:
		// hoisting it does not retarget the label.
		w.handleIfChain(e, declared, true, false, nil, -1, nil)
	case *ast.BlockStmt:
		w.walkList(e.List, make(scope))
	}
}

func (w *walker) handleSwitch(node *ast.SwitchStmt, declared scope, guarded bool, list []ast.Stmt, idx int, cascaded map[*ast.AssignStmt]bool) {
	w.processFuncLitsIn(node.Init)
	w.processFuncLitsIn(node.Tag)

	if !guarded {
		if assign, ok := matchInitAssign(node.Init); ok {
			w.emitSwitchFix(node, assign, declared, list, idx, cascaded)
		}
	}

	if node.Body != nil {
		for _, c := range node.Body.List {
			if cc, ok := c.(*ast.CaseClause); ok {
				for _, e := range cc.List {
					w.processFuncLitsIn(e)
				}
				w.walkList(cc.Body, make(scope))
			}
		}
	}
}

func (w *walker) handleTypeSwitch(node *ast.TypeSwitchStmt, declared scope, guarded bool, list []ast.Stmt, idx int, cascaded map[*ast.AssignStmt]bool) {
	w.processFuncLitsIn(node.Init)
	w.processFuncLitsIn(node.Assign)

	if !guarded && node.Init != nil {
		w.emitTypeSwitchFix(node, declared, list, idx, cascaded)
	}

	if node.Body != nil {
		for _, c := range node.Body.List {
			if cc, ok := c.(*ast.CaseClause); ok {
				w.walkList(cc.Body, w.typeSwitchClauseScope(node, cc))
			}
		}
	}
}

// typeSwitchClauseScope seeds a fresh scope for one case clause's body with
// the type-switch guard variable, if any. Per the Go spec, that variable is
// declared in the implicit block of *each* clause, not in an enclosing
// block, so a hoist landing in the clause body must see it as already
// declared. Each clause gets its own map — they are sibling scopes and must
// not share a mutable one.
//
// go/types does not record the guard ident's own occurrence via Defs/Uses
// (each clause has a differently-typed copy); it records the per-clause
// object in pass.TypesInfo.Implicits[clause] instead. When available, that
// object is used directly so canDowngrade can type-check against it; when
// unavailable (untyped workspace), the guard ident is stored anyway so the
// name still reads as colliding, and canDowngrade will conservatively skip.
func (w *walker) typeSwitchClauseScope(node *ast.TypeSwitchStmt, cc *ast.CaseClause) scope {
	seed := make(scope)
	assign, ok := node.Assign.(*ast.AssignStmt)
	if !ok {
		return seed
	}
	for _, lhs := range assign.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		dn := declaredName{ident: id}
		if info := w.pass.TypesInfo; info != nil && info.Implicits != nil {
			if obj := info.Implicits[cc]; obj != nil {
				dn.obj = obj
			}
		}
		seed[id.Name] = dn
	}
	return seed
}

// commClauseScope seeds a fresh scope for one select comm clause's body with
// the clause's own receive variable(s) (`case v := <-ch:`), if any. Per the
// Go spec that variable is declared in the clause's own implicit block, so a
// hoist landing in the clause body must see it as already declared. Unlike
// the type-switch guard, go/types records this ident normally via Defs, so
// no Implicits lookup is needed.
func commClauseScope(cc *ast.CommClause) scope {
	seed := make(scope)
	assign, ok := cc.Comm.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return seed
	}
	for _, lhs := range assign.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		seed[id.Name] = declaredName{ident: id}
	}
	return seed
}

// matchInitAssign reports whether init is an *ast.AssignStmt with Tok ==
// DEFINE or ASSIGN — the only shapes *ast.IfStmt / *ast.SwitchStmt Inits
// that this analyzer hoists.
func matchInitAssign(init ast.Stmt) (*ast.AssignStmt, bool) {
	if init == nil {
		return nil, false
	}
	assign, ok := init.(*ast.AssignStmt)
	if !ok {
		return nil, false
	}
	if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
		return nil, false
	}
	return assign, true
}

// emission is the outcome of deciding how (or whether) to hoist an Init
// assignment.
type emission struct {
	text     string       // hoisted init statement text (unchanged, or := downgraded to =)
	skip     bool         // true if the site should be reported but not fixed
	reason   string       // set when skip is true
	newNames []*ast.Ident // non-blank names newly declared in the landing block by this emission
}

// decideEmission decides how to hoist assign. When checkCollisions is false
// (plain '=' assigns, or an else-if's brand-new wrapping block) the text is
// always emitted unchanged. When true, it performs the collision analysis
// described in the package design: unchanged ':=' unless every non-blank
// LHS name already exists in declared, in which case it attempts a
// type-checked downgrade to '=', or skips if that cannot be verified safe.
func (w *walker) decideEmission(assign *ast.AssignStmt, declared scope, checkCollisions bool) emission {
	fullText := w.text(assign.Pos(), assign.End())

	if assign.Tok != token.DEFINE || !checkCollisions {
		var newNames []*ast.Ident
		if assign.Tok == token.DEFINE {
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					newNames = append(newNames, id)
				}
			}
		}
		return emission{text: fullText, newNames: newNames}
	}

	var colliding, newOnes []*ast.Ident
	for _, lhs := range assign.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		if _, exists := declared[id.Name]; exists {
			colliding = append(colliding, id)
		} else {
			newOnes = append(newOnes, id)
		}
	}

	if len(colliding) == 0 || len(newOnes) > 0 {
		// No collision, or a partial collision: ':=' is legal unchanged.
		return emission{text: fullText, newNames: newOnes}
	}

	// Every non-blank name collides: try to downgrade to '='.
	ok, reason := w.canDowngrade(assign, colliding, declared)
	if !ok {
		return emission{skip: true, reason: reason}
	}
	return emission{text: w.rewriteDefineToAssign(assign)}
}

// canDowngrade verifies, via pass.TypesInfo, that writing through each
// colliding name with '=' cannot change its type. If type information is
// unavailable or any lookup fails, it refuses conservatively.
func (w *walker) canDowngrade(assign *ast.AssignStmt, colliding []*ast.Ident, declared scope) (bool, string) {
	info := w.pass.TypesInfo
	if info == nil || info.Types == nil {
		return false, "type information unavailable"
	}

	for _, id := range colliding {
		idx := lhsIndex(assign, id)
		rhsType := w.rhsTypeAt(assign, idx)
		if rhsType == nil {
			return false, "type information unavailable"
		}

		dn, ok := declared[id.Name]
		if !ok {
			return false, "type information unavailable"
		}

		obj := dn.obj
		if obj == nil && dn.ident != nil {
			obj = info.Defs[dn.ident]
			if obj == nil {
				obj = info.Uses[dn.ident]
			}
		}
		if obj == nil || obj.Type() == nil {
			return false, "type information unavailable"
		}

		if !types.AssignableTo(rhsType, obj.Type()) {
			return false, "types not assignable"
		}
	}

	return true, ""
}

// resolveCascade is the entry point emitIfFix/emitSwitchFix/emitTypeSwitchFix
// call once they have decided to hoist with newNames newly introduced into
// declared. If newNames is empty, or there is no enclosing list to scan
// (an else-if's brand-new block), there is nothing to cascade. Otherwise it
// forward-scans for later plain ':=' siblings that newNames would break; see
// forwardCascade.
func (w *walker) resolveCascade(list []ast.Stmt, idx int, declared scope, newNames []*ast.Ident, cascaded map[*ast.AssignStmt]bool) (edits []analysis.TextEdit, targets []*ast.AssignStmt, ok bool, reason string) {
	if len(newNames) == 0 || list == nil {
		return nil, nil, true, ""
	}
	augmented := augmentScope(declared, newNames)
	return w.forwardCascade(list, idx, augmented, cascaded)
}

// augmentScope returns a copy of declared with newNames added, each mapped
// to its own declaring ident (an ordinary if/switch-init LHS ident always
// has a real Defs entry, unlike a type-switch clause guard). declared itself
// is left untouched — the hoist introducing newNames has not been committed
// yet when this is used to evaluate whether it is even safe to commit.
func augmentScope(declared scope, newNames []*ast.Ident) scope {
	augmented := make(scope, len(declared)+len(newNames))
	maps.Copy(augmented, declared)
	for _, id := range newNames {
		augmented[id.Name] = declaredName{ident: id}
	}
	return augmented
}

// forwardCascade scans list[i+1:] — direct siblings only; it never descends
// into nested blocks, which are new scopes where ':=' remains legal
// regardless of what this hoist introduces — for a later plain
// *ast.AssignStmt with Tok == DEFINE whose non-blank LHS names are now *all*
// already declared given augmented (declared plus this hoist's own newNames,
// not yet committed to declared when this runs).
//
// Hoisting introduces a name into the block earlier than it existed before,
// and that alone can turn such a statement from a legal ':=' into "no new
// variables on left side of :=". Such a statement is not itself an
// if/switch-init smell — it is not reported as a Result/Diagnostic — it is
// only kept compiling by folding its downgrade into the initiating hoist's
// own SuggestedFix as an additional TextEdit.
//
// cascaded records statements already claimed by an earlier hoist's cascade
// in the same list, so a second, independent hoist that would also reach
// the same statement does not emit a second, overlapping edit for it.
//
// If every affected statement can be safely downgraded (per canDowngrade),
// it returns their edits and the claimed statements with ok=true. If any
// cannot be verified safe, it returns ok=false and no edits — the caller
// must not hoist at all, since doing so would leave that statement broken.
func (w *walker) forwardCascade(list []ast.Stmt, i int, augmented scope, cascaded map[*ast.AssignStmt]bool) (edits []analysis.TextEdit, targets []*ast.AssignStmt, ok bool, reason string) {
	for j := i + 1; j < len(list); j++ {
		assign, isAssign := list[j].(*ast.AssignStmt)
		if !isAssign || assign.Tok != token.DEFINE || cascaded[assign] {
			continue
		}

		var names []*ast.Ident
		allDeclared := true
		for _, lhs := range assign.Lhs {
			id, idOk := lhs.(*ast.Ident)
			if !idOk || id.Name == "_" {
				continue
			}
			names = append(names, id)
			if _, exists := augmented[id.Name]; !exists {
				allDeclared = false
			}
		}
		if len(names) == 0 || !allDeclared {
			// No names, or a partial collision (at least one name is still
			// new): ':=' remains legal here unchanged, nothing to cascade.
			continue
		}

		startOff := w.pass.Fset.Position(assign.Pos()).Offset
		endOff := w.pass.Fset.Position(assign.End()).Offset
		if startOff < 0 || endOff > len(w.content) || startOff >= endOff {
			return nil, nil, false, "hoisting would invalidate a later := that cannot be downgraded: unable to compute a safe edit"
		}

		if downOK, downReason := w.canDowngrade(assign, names, augmented); !downOK {
			return nil, nil, false, "hoisting would invalidate a later := that cannot be downgraded: " + downReason
		}

		edits = append(edits, analysis.TextEdit{
			Pos:     assign.Pos(),
			End:     assign.End(),
			NewText: []byte(w.rewriteDefineToAssign(assign)),
		})
		targets = append(targets, assign)
	}

	return edits, targets, true, ""
}

func lhsIndex(assign *ast.AssignStmt, id *ast.Ident) int {
	for i, lhs := range assign.Lhs {
		if lhs == id {
			return i
		}
	}
	return -1
}

// rhsTypeAt returns the type that would be assigned to assign.Lhs[idx],
// handling both the 1:1 case (a, b := f(), g()) and the tuple case
// (a, b := f()) where f returns multiple values.
func (w *walker) rhsTypeAt(assign *ast.AssignStmt, idx int) types.Type {
	if idx < 0 {
		return nil
	}
	info := w.pass.TypesInfo

	if len(assign.Rhs) == len(assign.Lhs) {
		if idx >= len(assign.Rhs) {
			return nil
		}
		return info.TypeOf(assign.Rhs[idx])
	}

	if len(assign.Rhs) == 1 {
		t := info.TypeOf(assign.Rhs[0])
		if t == nil {
			return nil
		}
		if tuple, ok := t.Underlying().(*types.Tuple); ok {
			if idx >= tuple.Len() {
				return nil
			}
			return tuple.At(idx).Type()
		}
		if tuple, ok := t.(*types.Tuple); ok {
			if idx >= tuple.Len() {
				return nil
			}
			return tuple.At(idx).Type()
		}
		if idx == 0 {
			return t
		}
		return nil
	}

	return nil
}

// rewriteDefineToAssign rewrites a ':=' AssignStmt to '=' positionally,
// never by string-replacing ":=" (a func-literal RHS can legally contain
// ":=").
func (w *walker) rewriteDefineToAssign(assign *ast.AssignStmt) string {
	before := w.text(assign.Pos(), assign.TokPos)
	after := w.text(assign.TokPos+2, assign.End())
	return before + "=" + after
}

// --- if ---

func (w *walker) emitIfFix(node *ast.IfStmt, assign *ast.AssignStmt, declared scope, isElseIf bool, list []ast.Stmt, idx int, cascaded map[*ast.AssignStmt]bool) {
	pos := w.pass.Fset.Position(node.Pos())
	vars := lhsNames(assign)
	expr := w.rhsText(assign)
	snippet := sourceLine(w.content, pos.Line)
	condText := w.text(node.Cond.Pos(), node.Cond.End())

	checkCollisions := assign.Tok == token.DEFINE && !isElseIf
	em := w.decideEmission(assign, declared, checkCollisions)

	var fixes []analysis.SuggestedFix
	if !em.skip {
		if isElseIf {
			fixes = w.buildElseIfFixes(node, em.text, condText)
		} else {
			cascadeEdits, cascadeTargets, cascadeOK, cascadeReason := w.resolveCascade(list, idx, declared, em.newNames, cascaded)
			if !cascadeOK {
				em.skip = true
				em.reason = cascadeReason
			} else {
				fixes = w.buildHeadIfFix(node, em.text, condText, cascadeEdits)
				// Only commit newly-declared names / claimed cascade targets
				// once a fix was actually produced — buildHeadIfFix returns
				// nil if its own offset bounds check fails, in which case
				// nothing changes in the source and declared/cascaded must
				// not be mutated either.
				if len(fixes) > 0 {
					for _, id := range em.newNames {
						declared[id.Name] = declaredName{ident: id}
					}
					for _, t := range cascadeTargets {
						cascaded[t] = true
					}
				}
			}
		}
	}

	w.pass.Report(analysis.Diagnostic{
		Pos:            node.Pos(),
		End:            node.End(),
		Message:        "if-init assignment should be split into separate assignment and if-check",
		SuggestedFixes: fixes,
	})

	w.results = append(w.results, &Result{
		File:       pos.Filename,
		Line:       pos.Line,
		Column:     pos.Column,
		Variables:  vars,
		Expression: expr,
		Snippet:    snippet,
		Function:   w.currentFunc,
		Kind:       "if",
		IsElseIf:   isElseIf,
		Skipped:    em.skip,
		SkipReason: em.reason,
	})
}

// buildHeadIfFix builds the hoist edit plus any cascade edits (see
// forwardCascade) into a single SuggestedFix.
func (w *walker) buildHeadIfFix(node *ast.IfStmt, initText, condText string, cascadeEdits []analysis.TextEdit) []analysis.SuggestedFix {
	startOffset := w.pass.Fset.Position(node.Pos()).Offset
	lbraceOffset := w.pass.Fset.Position(node.Body.Lbrace).Offset
	endOffset := lbraceOffset + 1
	if startOffset < 0 || endOffset > len(w.content) || startOffset >= endOffset {
		return nil
	}

	indent := extractIndentation(w.content, startOffset)

	var sb strings.Builder
	sb.WriteString(initText)
	sb.WriteByte('\n')
	sb.WriteString(indent)
	sb.WriteString("if ")
	sb.WriteString(condText)
	sb.WriteString(" {")

	edits := []analysis.TextEdit{{Pos: node.Pos(), End: node.Body.Lbrace + 1, NewText: []byte(sb.String())}}
	edits = append(edits, cascadeEdits...)

	return []analysis.SuggestedFix{{
		Message:   fixMessage,
		TextEdits: edits,
	}}
}

// buildElseIfFixes de-sugars `} else if init; cond { ... }` into a nested
// block via two TextEdits in one SuggestedFix:
//  1. Replace [elseIf.Pos(), elseIf.Body.Lbrace+1) with the wrapping "{",
//     the hoisted init, and the new "if cond {" header. elseIf.Pos() is the
//     `if` token, so the preceding "} else " text survives untouched.
//  2. A zero-width insertion of "\n}" at elseIf.End(), which spans the
//     entire remaining else-if chain and is exactly where the wrapping
//     block must close.
func (w *walker) buildElseIfFixes(node *ast.IfStmt, initText, condText string) []analysis.SuggestedFix {
	startOffset := w.pass.Fset.Position(node.Pos()).Offset
	lbraceOffset := w.pass.Fset.Position(node.Body.Lbrace).Offset
	headerEnd := lbraceOffset + 1
	if startOffset < 0 || headerEnd > len(w.content) || startOffset >= headerEnd {
		return nil
	}

	tailOffset := w.pass.Fset.Position(node.End()).Offset
	if tailOffset < 0 || tailOffset > len(w.content) {
		return nil
	}
	indent := extractIndentation(w.content, startOffset)
	innerIndent := indent + "\t"

	var sb strings.Builder
	sb.WriteString("{\n")
	sb.WriteString(innerIndent)
	sb.WriteString(initText)
	sb.WriteByte('\n')
	sb.WriteString(innerIndent)
	sb.WriteString("if ")
	sb.WriteString(condText)
	sb.WriteString(" {")

	return []analysis.SuggestedFix{{
		Message: fixMessage,
		TextEdits: []analysis.TextEdit{
			{Pos: node.Pos(), End: node.Body.Lbrace + 1, NewText: []byte(sb.String())},
			{Pos: node.End(), End: node.End(), NewText: []byte("\n" + indent + "}")},
		},
	}}
}

// --- switch ---

func (w *walker) emitSwitchFix(node *ast.SwitchStmt, assign *ast.AssignStmt, declared scope, list []ast.Stmt, idx int, cascaded map[*ast.AssignStmt]bool) {
	pos := w.pass.Fset.Position(node.Pos())
	vars := lhsNames(assign)
	expr := w.rhsText(assign)
	snippet := sourceLine(w.content, pos.Line)

	checkCollisions := assign.Tok == token.DEFINE
	em := w.decideEmission(assign, declared, checkCollisions)

	var fixes []analysis.SuggestedFix
	if !em.skip {
		cascadeEdits, cascadeTargets, cascadeOK, cascadeReason := w.resolveCascade(list, idx, declared, em.newNames, cascaded)
		if !cascadeOK {
			em.skip = true
			em.reason = cascadeReason
		} else {
			var tagText string
			if node.Tag != nil {
				tagText = w.text(node.Tag.Pos(), node.Tag.End())
			}
			fixes = w.buildSwitchFix(node, em.text, tagText, cascadeEdits)
			// See buildHeadIfFix: only commit if a fix was actually produced.
			if len(fixes) > 0 {
				for _, id := range em.newNames {
					declared[id.Name] = declaredName{ident: id}
				}
				for _, t := range cascadeTargets {
					cascaded[t] = true
				}
			}
		}
	}

	w.pass.Report(analysis.Diagnostic{
		Pos:            node.Pos(),
		End:            node.End(),
		Message:        "switch-init assignment should be split into separate assignment and switch",
		SuggestedFixes: fixes,
	})

	w.results = append(w.results, &Result{
		File:       pos.Filename,
		Line:       pos.Line,
		Column:     pos.Column,
		Variables:  vars,
		Expression: expr,
		Snippet:    snippet,
		Function:   w.currentFunc,
		Kind:       "switch",
		Skipped:    em.skip,
		SkipReason: em.reason,
	})
}

func (w *walker) buildSwitchFix(node *ast.SwitchStmt, initText, tagText string, cascadeEdits []analysis.TextEdit) []analysis.SuggestedFix {
	startOffset := w.pass.Fset.Position(node.Pos()).Offset
	lbraceOffset := w.pass.Fset.Position(node.Body.Lbrace).Offset
	endOffset := lbraceOffset + 1
	if startOffset < 0 || endOffset > len(w.content) || startOffset >= endOffset {
		return nil
	}

	indent := extractIndentation(w.content, startOffset)

	var sb strings.Builder
	sb.WriteString(initText)
	sb.WriteByte('\n')
	sb.WriteString(indent)
	sb.WriteString("switch ")
	if tagText != "" {
		sb.WriteString(tagText)
		sb.WriteString(" ")
	}
	sb.WriteString("{")

	edits := []analysis.TextEdit{{Pos: node.Pos(), End: node.Body.Lbrace + 1, NewText: []byte(sb.String())}}
	edits = append(edits, cascadeEdits...)

	return []analysis.SuggestedFix{{
		Message:   fixMessage,
		TextEdits: edits,
	}}
}

// --- type switch ---

func (w *walker) emitTypeSwitchFix(node *ast.TypeSwitchStmt, declared scope, list []ast.Stmt, idx int, cascaded map[*ast.AssignStmt]bool) {
	pos := w.pass.Fset.Position(node.Pos())

	assign, isAssign := node.Init.(*ast.AssignStmt)

	var em emission
	var vars []string
	var expr string
	if isAssign {
		vars = lhsNames(assign)
		expr = w.rhsText(assign)
		checkCollisions := assign.Tok == token.DEFINE
		em = w.decideEmission(assign, declared, checkCollisions)
	} else {
		expr = w.text(node.Init.Pos(), node.Init.End())
		em = emission{text: expr}
	}

	snippet := sourceLine(w.content, pos.Line)

	var fixes []analysis.SuggestedFix
	if !em.skip {
		cascadeEdits, cascadeTargets, cascadeOK, cascadeReason := w.resolveCascade(list, idx, declared, em.newNames, cascaded)
		if !cascadeOK {
			em.skip = true
			em.reason = cascadeReason
		} else {
			// Never touch Assign: `v := x.(type)` is not a valid standalone
			// statement, so it is re-emitted verbatim as the new switch's guard.
			assignText := w.text(node.Assign.Pos(), node.Assign.End())
			fixes = w.buildTypeSwitchFix(node, em.text, assignText, cascadeEdits)
			// See buildHeadIfFix: only commit if a fix was actually produced.
			if len(fixes) > 0 {
				for _, id := range em.newNames {
					declared[id.Name] = declaredName{ident: id}
				}
				for _, t := range cascadeTargets {
					cascaded[t] = true
				}
			}
		}
	}

	w.pass.Report(analysis.Diagnostic{
		Pos:            node.Pos(),
		End:            node.End(),
		Message:        "type-switch-init assignment should be split into separate assignment and type-switch",
		SuggestedFixes: fixes,
	})

	w.results = append(w.results, &Result{
		File:       pos.Filename,
		Line:       pos.Line,
		Column:     pos.Column,
		Variables:  vars,
		Expression: expr,
		Snippet:    snippet,
		Function:   w.currentFunc,
		Kind:       "type_switch",
		Skipped:    em.skip,
		SkipReason: em.reason,
	})
}

func (w *walker) buildTypeSwitchFix(node *ast.TypeSwitchStmt, initText, assignText string, cascadeEdits []analysis.TextEdit) []analysis.SuggestedFix {
	startOffset := w.pass.Fset.Position(node.Pos()).Offset
	lbraceOffset := w.pass.Fset.Position(node.Body.Lbrace).Offset
	endOffset := lbraceOffset + 1
	if startOffset < 0 || endOffset > len(w.content) || startOffset >= endOffset {
		return nil
	}

	indent := extractIndentation(w.content, startOffset)

	var sb strings.Builder
	sb.WriteString(initText)
	sb.WriteByte('\n')
	sb.WriteString(indent)
	sb.WriteString("switch ")
	sb.WriteString(assignText)
	sb.WriteString(" {")

	edits := []analysis.TextEdit{{Pos: node.Pos(), End: node.Body.Lbrace + 1, NewText: []byte(sb.String())}}
	edits = append(edits, cascadeEdits...)

	return []analysis.SuggestedFix{{
		Message:   fixMessage,
		TextEdits: edits,
	}}
}

// --- func literal descent ---

// processFuncLitsIn scans node for *ast.FuncLit occurrences, without
// descending past a FuncLit boundary (its body is handled separately by
// walkFuncLit, so double-visiting is avoided). Statements can only embed a
// nested block via a func literal, so this is sufficient to find every func
// literal reachable from a non-block-bearing expression.
func (w *walker) processFuncLitsIn(node ast.Node) {
	if node == nil {
		return
	}
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if fl, ok := n.(*ast.FuncLit); ok {
			w.walkFuncLit(fl)
			return false
		}
		return true
	})
}

func (w *walker) walkFuncLit(fl *ast.FuncLit) {
	if fl.Body == nil {
		return
	}
	var params, results *ast.FieldList
	if fl.Type != nil {
		params, results = fl.Type.Params, fl.Type.Results
	}
	w.walkList(fl.Body.List, scopeNamesFromFields(params, results))
}

// --- shared helpers ---

func functionScopeNames(fn *ast.FuncDecl) scope {
	var params, results *ast.FieldList
	if fn.Type != nil {
		params, results = fn.Type.Params, fn.Type.Results
	}
	return scopeNamesFromFields(fn.Recv, params, results)
}

func scopeNamesFromFields(fieldLists ...*ast.FieldList) scope {
	declared := make(scope)
	for _, fl := range fieldLists {
		if fl == nil {
			continue
		}
		for _, field := range fl.List {
			for _, name := range field.Names {
				if name.Name != "_" {
					declared[name.Name] = declaredName{ident: name}
				}
			}
		}
	}
	return declared
}

func (w *walker) recordDeclStmt(s *ast.DeclStmt, declared scope) {
	gd, ok := s.Decl.(*ast.GenDecl)
	if !ok {
		return
	}
	if gd.Tok != token.VAR && gd.Tok != token.CONST {
		return
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range vs.Names {
			if name.Name != "_" {
				declared[name.Name] = declaredName{ident: name}
			}
		}
	}
}

func lhsNames(assign *ast.AssignStmt) []string {
	var vars []string
	for _, lhs := range assign.Lhs {
		if ident, ok := lhs.(*ast.Ident); ok {
			vars = append(vars, ident.Name)
		}
	}
	return vars
}

func (w *walker) rhsText(assign *ast.AssignStmt) string {
	if len(assign.Rhs) == 0 {
		return ""
	}
	return w.text(assign.Rhs[0].Pos(), assign.Rhs[len(assign.Rhs)-1].End())
}

func (w *walker) text(from, to token.Pos) string {
	return sourceText(w.pass.Fset, w.content, from, to)
}

func sourceText(fset *token.FileSet, content []byte, from, to token.Pos) string {
	if len(content) == 0 {
		return ""
	}
	start := fset.Position(from).Offset
	end := fset.Position(to).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

func sourceLine(content []byte, line int) string {
	if len(content) == 0 {
		return ""
	}
	lines := bytes.Split(content, []byte("\n"))
	if line < 1 || line > len(lines) {
		return ""
	}
	return string(bytes.TrimSpace(lines[line-1]))
}

func extractIndentation(content []byte, offset int) string {
	lineStart := offset
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}
	var buf bytes.Buffer
	for i := lineStart; i < len(content); i++ {
		if content[i] == ' ' || content[i] == '\t' {
			buf.WriteByte(content[i])
		} else {
			break
		}
	}
	return buf.String()
}
