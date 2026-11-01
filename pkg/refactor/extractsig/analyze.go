// Package extractsig computes the signature of an "extract function/method"
// refactoring from go/types information.
//
// The historical extract implementation was purely syntactic: it stripped
// string literals with regexps, scraped identifiers out of the remaining text
// and guessed which of them were variables. That approach cannot tell a
// package-level function from a local variable, a method name from a field,
// or a builtin from a user identifier, so it produced signatures that did not
// compile. This package replaces that guesswork with go/types object
// resolution, and refuses (rather than guesses) whenever the answer is not
// determinable.
//
// The one hard requirement on callers: the *ast.Ident keys in
// pkg.TypesInfo come from the AST parsed with ws.FileSet and stored on
// file.AST. Analysis must use exactly those nodes — re-parsing the file with a
// fresh FileSet yields structurally identical but pointer-distinct idents, and
// every Uses/Defs lookup then silently returns nil, which is precisely the
// class of bug this package exists to remove.
package extractsig

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"sort"
	"strconv"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Param is a value the extracted function must receive.
type Param struct {
	Name string // identifier as it appears in source
	Type string // rendered type, qualified for the source file's imports
}

// Result is a value the extracted function must return.
type Result struct {
	Name string
	Type string
}

// Analysis is the computed signature of an extraction.
type Analysis struct {
	Params  []Param
	Results []Result

	// TailReturns is true when the block contains return statements bound to
	// the enclosing function. The call site then becomes `return name(...)`.
	TailReturns bool
	// EnclosingResults holds the enclosing function's result types; set only
	// when TailReturns is true.
	EnclosingResults []Result

	Stmts    []ast.Stmt // selected statements, source order
	BodyText string     // block source, dedented to zero base indentation
	// Imports maps import path -> package name for packages referenced by a
	// rendered type that the source file does not already import.
	Imports map[string]string
	// EnclosingFunc is the FuncDecl the block was taken from.
	EnclosingFunc *ast.FuncDecl
}

// Refusal reports that extraction cannot be performed soundly. It is returned
// as an error so callers can `errors.As` it and surface the reason verbatim;
// no part of this package ever falls back to a guess.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, args ...any) *Refusal {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// analyzer carries the per-call state. It exists so the many small helpers
// below do not each need the workspace, the type info and the block bounds
// threaded through their parameter lists.
type analyzer struct {
	ws   *types.Workspace
	pkg  *types.Package
	file *types.File
	info *gotypes.Info
	fset *token.FileSet

	fn    *ast.FuncDecl // enclosing declared function
	stmts []ast.Stmt    // the selected run

	// innerBody is the body of the innermost function (FuncDecl or FuncLit)
	// that contains the selection. Return statements in the selection bind to
	// that function, not necessarily to fn.
	innerBody *ast.BlockStmt
	// innerSig is the signature of that innermost function.
	innerSig *gotypes.Signature

	blockStart token.Pos
	blockEnd   token.Pos

	// paramObjs holds the objects behind Analysis.Params, index for index.
	// Results are decided by object identity, and a name alone cannot
	// distinguish two shadowed variables that happen to share one.
	paramObjs []gotypes.Object

	// fileImports maps import path -> local alias for the imports the source
	// file already has. An empty alias means "imported under its own package
	// name".
	fileImports map[string]string
	// newImports collects packages a rendered type referenced that the file
	// does not import yet: import path -> package name.
	newImports map[string]string
}

// Analyze computes the signature for extracting [startLine,endLine] from file.
// Returns a *Refusal error when extraction would be unsound.
// The caller must have type-checked pkg already.
func Analyze(ws *types.Workspace, pkg *types.Package, file *types.File, startLine, endLine int) (*Analysis, error) {
	if ws == nil || pkg == nil || file == nil {
		return nil, refuse("workspace, package and file are all required")
	}
	if ws.FileSet == nil {
		return nil, refuse("workspace has no FileSet; positions cannot be resolved")
	}
	// Type-checking in this project is lazy: a package that nobody asked
	// about carries no TypesInfo at all, and every Uses/Defs lookup below
	// would come back nil — producing an empty, plausible-looking and
	// completely wrong signature. Refusing is the only safe answer.
	if pkg.TypesInfo == nil || pkg.TypesPkg == nil {
		return nil, refuse("type information is unavailable for package %s; type-check it before extracting", pkg.ImportPath)
	}
	if file.AST == nil {
		return nil, refuse("file %s has no parsed AST", file.Path)
	}
	if startLine <= 0 || endLine < startLine {
		return nil, refuse("invalid line range %d-%d", startLine, endLine)
	}

	a := &analyzer{
		ws:          ws,
		pkg:         pkg,
		file:        file,
		info:        pkg.TypesInfo,
		fset:        ws.FileSet,
		fileImports: fileImportAliases(file.AST),
		newImports:  make(map[string]string),
	}

	if err := a.locate(startLine, endLine); err != nil {
		return nil, err
	}
	if err := a.checkControlFlow(); err != nil {
		return nil, err
	}

	params, err := a.collectParams()
	if err != nil {
		return nil, err
	}
	results, err := a.collectResults(params)
	if err != nil {
		return nil, err
	}

	res := &Analysis{
		Params:        params,
		Results:       results,
		Stmts:         a.stmts,
		EnclosingFunc: a.fn,
	}

	if err := a.analyzeReturns(res); err != nil {
		return nil, err
	}

	body, err := a.bodyText()
	if err != nil {
		return nil, err
	}
	res.BodyText = body
	res.Imports = a.newImports

	return res, nil
}

// ---------------------------------------------------------------------------
// 1. Locating the block
// ---------------------------------------------------------------------------

func (a *analyzer) line(p token.Pos) int { return a.fset.Position(p).Line }

// locate finds the enclosing FuncDecl and the contiguous run of statements
// that exactly spans [startLine,endLine].
func (a *analyzer) locate(startLine, endLine int) error {
	var fn *ast.FuncDecl
	for _, decl := range a.file.AST.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		fs, fe := a.line(fd.Pos()), a.line(fd.End())
		switch {
		case startLine >= fs && endLine <= fe:
			fn = fd
		case (startLine >= fs && startLine <= fe) || (endLine >= fs && endLine <= fe):
			// Overlaps a function without being contained by it: the range
			// runs off one end of the function body.
			return refuse("line range %d-%d crosses the boundary of function %s; a block must lie inside a single function", startLine, endLine, fd.Name.Name)
		}
	}
	if fn == nil {
		return refuse("line range %d-%d is not inside a function body", startLine, endLine)
	}
	a.fn = fn

	// Statement lists live in three shapes: block bodies, and the (un-braced)
	// bodies of case and comm clauses. All three are valid extraction sites.
	var best []ast.Stmt
	bestSpan := token.Pos(-1)
	consider := func(list []ast.Stmt) {
		for i := range list {
			if a.line(list[i].Pos()) != startLine {
				continue
			}
			for j := i; j < len(list); j++ {
				if a.line(list[j].End()) != endLine {
					continue
				}
				// Prefer the tightest match, which is the innermost block:
				// an enclosing block can only match by covering strictly
				// more source.
				if span := list[j].End() - list[i].Pos(); bestSpan < 0 || span < bestSpan {
					best, bestSpan = list[i:j+1], span
				}
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BlockStmt:
			consider(x.List)
		case *ast.CaseClause:
			consider(x.Body)
		case *ast.CommClause:
			consider(x.Body)
		}
		return true
	})
	if best == nil {
		return refuse("line range %d-%d does not align to statement boundaries; a block must start at the first line of a statement and end at the last line of a statement", startLine, endLine)
	}

	a.stmts = best
	a.blockStart = best[0].Pos()
	a.blockEnd = best[len(best)-1].End()

	return a.locateInnerFunc()
}

// locateInnerFunc records the innermost function containing the selection.
// Returns bind to that function, which is not fn when the selection sits
// inside a closure.
func (a *analyzer) locateInnerFunc() error {
	a.innerBody = a.fn.Body
	obj := a.info.Defs[a.fn.Name]
	if obj == nil {
		return refuse("function %s has no type information; type-checking of package %s is incomplete", a.fn.Name.Name, a.pkg.ImportPath)
	}
	sig, ok := obj.Type().(*gotypes.Signature)
	if !ok {
		return refuse("function %s does not have a signature type", a.fn.Name.Name)
	}
	a.innerSig = sig

	// Innermost wins: nested literals are visited outside-in, so the last
	// containing literal seen is the deepest one.
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok || lit.Body == nil {
			return true
		}
		if lit.Body.Pos() <= a.blockStart && a.blockEnd <= lit.Body.End() {
			if litSig, ok := a.info.TypeOf(lit).(*gotypes.Signature); ok {
				a.innerBody = lit.Body
				a.innerSig = litSig
			}
		}
		return true
	})
	return nil
}

// ---------------------------------------------------------------------------
// 2. Control-flow sanity
// ---------------------------------------------------------------------------

// checkControlFlow rejects selections whose control flow cannot survive being
// moved into a separate function: a break/continue/goto whose target lies
// outside the selection, or a type declaration (whose name would escape the
// extracted function's scope while still being referenced by the signature).
func (a *analyzer) checkControlFlow() error {
	labels := map[string]bool{}
	for _, s := range a.stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			if ls, ok := n.(*ast.LabeledStmt); ok {
				labels[ls.Label.Name] = true
			}
			return true
		})
	}

	var walkErr error
	// inLoop/inSwitch track whether an unlabeled break or continue would find
	// a target *within* the selection. The outermost level is neither, even
	// when the selection happens to sit inside a loop in the original
	// function — that loop is not coming along.
	var walk func(n ast.Node, inLoop, inSwitch bool)
	walk = func(n ast.Node, inLoop, inSwitch bool) {
		if n == nil || walkErr != nil {
			return
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			// A closure has its own control flow; nothing inside it can
			// branch out into the selection's context.
			return
		case *ast.DeclStmt:
			if gd, ok := x.Decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				walkErr = refuse("block declares a named type; extracting it would move the type out of scope of the remaining code")
				return
			}
		case *ast.BranchStmt:
			switch x.Tok {
			case token.BREAK:
				if x.Label != nil {
					if !labels[x.Label.Name] {
						walkErr = refuse("block contains `break %s` whose label is outside the selection", x.Label.Name)
					}
				} else if !inLoop && !inSwitch {
					walkErr = refuse("block contains a `break` that would escape the extracted function")
				}
			case token.CONTINUE:
				if x.Label != nil {
					if !labels[x.Label.Name] {
						walkErr = refuse("block contains `continue %s` whose label is outside the selection", x.Label.Name)
					}
				} else if !inLoop {
					walkErr = refuse("block contains a `continue` that would escape the extracted function")
				}
			case token.GOTO:
				if x.Label == nil || !labels[x.Label.Name] {
					walkErr = refuse("block contains a `goto` whose target label is outside the selection")
				}
			}
			return
		case *ast.ForStmt:
			walk(x.Body, true, false)
			return
		case *ast.RangeStmt:
			walk(x.Body, true, false)
			return
		case *ast.SwitchStmt:
			walk(x.Body, inLoop, true)
			return
		case *ast.TypeSwitchStmt:
			walk(x.Body, inLoop, true)
			return
		case *ast.SelectStmt:
			walk(x.Body, inLoop, true)
			return
		}
		for _, child := range children(n) {
			walk(child, inLoop, inSwitch)
		}
	}
	for _, s := range a.stmts {
		walk(s, false, false)
	}
	return walkErr
}

// children returns the direct child nodes of n. ast.Inspect cannot be used
// where the traversal needs to carry state down (as checkControlFlow's
// inLoop/inSwitch do), so the walk is manual and this reconstructs the child
// list by running a one-level Inspect.
func children(n ast.Node) []ast.Node {
	var out []ast.Node
	first := true
	ast.Inspect(n, func(c ast.Node) bool {
		if first {
			first = false
			return true
		}
		if c == nil {
			return false
		}
		out = append(out, c)
		return false
	})
	return out
}

// ---------------------------------------------------------------------------
// 3. Identifier collection and filtering
// ---------------------------------------------------------------------------

// collectIdents gathers every identifier in the selection that could denote a
// value, in source order. Identifiers that syntactically cannot be a variable
// reference are dropped here rather than after object resolution, because
// several of them (struct field names, method names) *do* resolve to objects
// and would otherwise be mistaken for captured variables.
func (a *analyzer) collectIdents() []*ast.Ident {
	var out []*ast.Ident
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case nil:
			return false

		case *ast.SelectorExpr:
			// Sel is a field or method name, resolved against the type of X,
			// never a variable in the enclosing function. Capturing it is how
			// method names such as `x.hasRequiredFilterKeys()` used to end up
			// as parameters.
			ast.Inspect(x.X, visit)
			return false

		case *ast.CompositeLit:
			// Keys of a struct literal are field names; keys of a map or
			// array literal are ordinary expressions and must be visited.
			structLit := false
			if t := a.info.TypeOf(x); t != nil {
				_, structLit = deref(t).Underlying().(*gotypes.Struct)
			}
			if x.Type != nil {
				ast.Inspect(x.Type, visit)
			}
			for _, elt := range x.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					ast.Inspect(elt, visit)
					continue
				}
				if !structLit {
					ast.Inspect(kv.Key, visit)
				}
				ast.Inspect(kv.Value, visit)
			}
			return false

		case *ast.LabeledStmt:
			ast.Inspect(x.Stmt, visit)
			return false

		case *ast.BranchStmt:
			// The only identifier here is a label.
			return false

		case *ast.Field:
			// Field/parameter/result names are declarations, not references.
			if x.Type != nil {
				ast.Inspect(x.Type, visit)
			}
			return false

		case *ast.Ident:
			out = append(out, x)
			return false
		}
		return true
	}
	for _, s := range a.stmts {
		ast.Inspect(s, visit)
	}
	// The traversal above is pre-order and mostly source-ordered, but a few
	// node kinds list children out of source order. Parameter order is
	// load-bearing for the generated signature, so sort explicitly.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pos() < out[j].Pos() })
	return out
}

// objectOf resolves an identifier to its object, preferring a use over a
// definition. Info.Scopes is not populated by this project's type-checker, so
// Uses/Defs are the only resolution mechanism available.
func (a *analyzer) objectOf(id *ast.Ident) gotypes.Object {
	if obj := a.info.Uses[id]; obj != nil {
		return obj
	}
	return a.info.Defs[id]
}

// isCapturedVar reports whether obj is a variable the extracted function would
// have to receive as a parameter: a local of the enclosing function declared
// outside the selected block.
func (a *analyzer) isCapturedVar(obj gotypes.Object) bool {
	if obj == nil || obj.Name() == "_" {
		return false
	}
	// Package-level declarations (functions such as `orDefault`, vars, consts,
	// types) stay reachable from the extracted function, so they are not
	// parameters.
	if obj.Parent() == a.pkg.TypesPkg.Scope() {
		return false
	}
	// Builtins: append, len, nil, true, ...
	if obj.Parent() == gotypes.Universe {
		return false
	}
	switch v := obj.(type) {
	case *gotypes.PkgName, *gotypes.TypeName, *gotypes.Builtin, *gotypes.Nil, *gotypes.Const, *gotypes.Label:
		return false
	case *gotypes.Func:
		// Methods reach their receiver through the value they are called on.
		return false
	case *gotypes.Var:
		if v.IsField() {
			return false
		}
	default:
		return false
	}

	// Declared inside the enclosing function (this covers the receiver,
	// parameters, named results and any local, including locals of an
	// intervening closure) but outside the selection itself.
	pos := obj.Pos()
	if pos < a.fn.Pos() || pos >= a.fn.End() {
		return false
	}
	if pos >= a.blockStart && pos < a.blockEnd {
		return false
	}
	return true
}

// collectParams returns the captured variables in order of first appearance in
// the block. Source order — not alphabetical order — is what makes the
// generated call site line up with how a human would have written it.
func (a *analyzer) collectParams() ([]Param, error) {
	var params []Param
	a.paramObjs = nil
	seen := map[gotypes.Object]bool{}
	for _, id := range a.collectIdents() {
		obj := a.objectOf(id)
		if !a.isCapturedVar(obj) || seen[obj] {
			continue
		}
		seen[obj] = true
		typeStr, err := a.renderType(obj.Type(), fmt.Sprintf("variable %s", obj.Name()))
		if err != nil {
			return nil, err
		}
		params = append(params, Param{Name: obj.Name(), Type: typeStr})
		a.paramObjs = append(a.paramObjs, obj)
	}
	return params, nil
}

// ---------------------------------------------------------------------------
// 4. Results
// ---------------------------------------------------------------------------

// collectResults determines which values must travel back out of the extracted
// function: captured variables the block mutates in a way that value-passing
// would lose, followed by variables the block declares and the remainder of
// the function still uses.
func (a *analyzer) collectResults(params []Param) ([]Result, error) {
	mutated := a.mutatedObjects()

	var results []Result
	for i, p := range params {
		if mutated[a.paramObjs[i]] {
			results = append(results, Result{Name: p.Name, Type: p.Type})
		}
	}

	for _, obj := range a.blockDeclaredVars() {
		if !a.usedAfterBlock(obj) {
			continue
		}
		typeStr, err := a.renderType(obj.Type(), fmt.Sprintf("variable %s", obj.Name()))
		if err != nil {
			return nil, err
		}
		results = append(results, Result{Name: obj.Name(), Type: typeStr})
	}
	return results, nil
}

// mutatedObjects returns the set of objects the block writes to in a way the
// caller could not otherwise observe.
func (a *analyzer) mutatedObjects() map[gotypes.Object]bool {
	out := map[gotypes.Object]bool{}

	note := func(lhs ast.Expr) {
		base := baseIdent(lhs)
		if base == nil {
			return
		}
		obj := a.objectOf(base)
		if obj == nil {
			return
		}
		// A mutation reaching memory the caller already shares with us —
		// `m[k] = v` on a map, `p.F = v` through a pointer, `*p = v` — is
		// visible without returning anything. Only a write that replaces the
		// callee's own copy needs to be handed back.
		if a.mutationVisibleToCaller(lhs) {
			return
		}
		out[obj] = true
	}

	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			// `:=` is included deliberately: when the redeclared name already
			// exists in the same scope go/types records it in Uses, so it is
			// a genuine write to the outer variable rather than a new one.
			for _, lhs := range x.Lhs {
				note(lhs)
			}
		case *ast.IncDecStmt:
			note(x.X)
		case *ast.RangeStmt:
			if x.Tok == token.ASSIGN {
				if x.Key != nil {
					note(x.Key)
				}
				if x.Value != nil {
					note(x.Value)
				}
			}
		case *ast.CallExpr:
			a.notePointerReceiverCall(x, out)
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				// The address escapes into something we cannot see; assume
				// whatever receives it writes through the pointer.
				if base := baseIdent(x.X); base != nil {
					if obj := a.objectOf(base); obj != nil && !a.mutationVisibleToCaller(x.X) {
						out[obj] = true
					}
				}
			}
		}
		return true
	}
	for _, s := range a.stmts {
		// Closures are walked too: a literal that assigns to a captured
		// variable mutates it whenever it runs.
		ast.Inspect(s, visit)
	}
	return out
}

// notePointerReceiverCall records the implicit address-of in a pointer-receiver
// method call on an addressable value: `b.WriteString(s)` means
// `(&b).WriteString(s)` and mutates b exactly as `&b` would, so b has to
// travel back out of the extracted function.
//
// Info.Selections is not populated by this project's type-checker, so the
// method is resolved through Uses on the selector's Sel instead — which works
// for concrete methods (the only ones that can have a pointer receiver).
func (a *analyzer) notePointerReceiverCall(call *ast.CallExpr, out map[gotypes.Object]bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	fn, ok := a.info.Uses[sel.Sel].(*gotypes.Func)
	if !ok {
		return
	}
	sig, ok := fn.Type().(*gotypes.Signature)
	if !ok || sig.Recv() == nil {
		return
	}
	if _, ok := sig.Recv().Type().(*gotypes.Pointer); !ok {
		// Value receiver (or an interface method): the callee gets a copy.
		return
	}
	recvType := a.info.TypeOf(sel.X)
	if recvType == nil {
		return
	}
	if _, ok := recvType.Underlying().(*gotypes.Pointer); ok {
		// Already a pointer, so no address is taken and the mutation is
		// visible to the caller regardless.
		return
	}
	base := baseIdent(sel.X)
	if base == nil {
		return
	}
	if obj := a.objectOf(base); obj != nil && !a.mutationVisibleToCaller(sel.X) {
		out[obj] = true
	}
}

// mutationVisibleToCaller reports whether writing to lhs reaches memory that
// the caller of the extracted function already shares.
//
// This is the fix for the case that used to be silently dropped:
// `res.Checks = append(res.Checks, ...)` where res is a struct *value* is NOT
// visible to the caller, so res has to be returned. The same statement with a
// *Result is visible and must not be.
func (a *analyzer) mutationVisibleToCaller(lhs ast.Expr) bool {
	switch e := lhs.(type) {
	case *ast.ParenExpr:
		return a.mutationVisibleToCaller(e.X)
	case *ast.StarExpr:
		return true
	case *ast.IndexExpr:
		switch deref(a.info.TypeOf(e.X)).Underlying().(type) {
		case *gotypes.Map, *gotypes.Slice, *gotypes.Pointer:
			return true
		}
		// Arrays are values; indexing one is only visible if the array
		// itself is reached through something shared.
		return a.mutationVisibleToCaller(e.X)
	case *ast.SelectorExpr:
		if t := a.info.TypeOf(e.X); t != nil {
			if _, ok := t.Underlying().(*gotypes.Pointer); ok {
				return true
			}
		}
		return a.mutationVisibleToCaller(e.X)
	}
	// A bare identifier: whole-value assignment, never visible.
	return false
}

// baseIdent walks an assignment target down to the identifier it is rooted at,
// so that `res.Checks[i].Name` reports `res`.
func baseIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.ParenExpr:
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SliceExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return nil
		}
	}
}

// blockDeclaredVars returns variables declared inside the selection, in
// declaration order.
func (a *analyzer) blockDeclaredVars() []gotypes.Object {
	var ids []*ast.Ident
	for _, s := range a.stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				ids = append(ids, id)
			}
			return true
		})
	}
	sort.SliceStable(ids, func(i, j int) bool { return ids[i].Pos() < ids[j].Pos() })

	var out []gotypes.Object
	seen := map[gotypes.Object]bool{}
	for _, id := range ids {
		obj := a.info.Defs[id]
		if obj == nil || obj.Name() == "_" || seen[obj] {
			continue
		}
		v, ok := obj.(*gotypes.Var)
		if !ok || v.IsField() {
			continue
		}
		seen[obj] = true
		out = append(out, obj)
	}
	return out
}

// usedAfterBlock reports whether obj is still referenced after the selection.
// The comparison is object identity, not name: a later shadowing declaration
// of the same name is a different object and must not count.
//
// Any reference counts, not only reads. A variable declared inside the block
// stops existing once the block moves away, so an assignment to it after the
// block would no longer compile either.
func (a *analyzer) usedAfterBlock(obj gotypes.Object) bool {
	found := false
	ast.Inspect(a.fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Pos() < a.blockEnd {
			return true
		}
		if a.info.Uses[id] == obj || a.info.Defs[id] == obj {
			found = true
		}
		return true
	})
	return found
}

// ---------------------------------------------------------------------------
// 5. Return statements
// ---------------------------------------------------------------------------

func (a *analyzer) analyzeReturns(res *Analysis) error {
	if !a.hasBoundReturn() {
		return nil
	}
	if a.innerBody != a.fn.Body {
		// The selection lives in a closure, so its returns bind to the
		// closure. Turning them into returns of a new top-level function
		// would change which function they exit.
		return refuse("block sits inside a function literal and contains `return`; its returns bind to the literal and cannot be extracted")
	}
	if !alwaysTerminates(a.stmts) {
		return refuse("block mixes early returns with fall-through: some paths return from the enclosing function and others continue past the block, which cannot be expressed by a single extracted call")
	}

	res.TailReturns = true
	results := a.innerSig.Results()
	for v := range results.Variables() {
		v := v
		typeStr, err := a.renderType(v.Type(), "enclosing function result")
		if err != nil {
			return err
		}
		res.EnclosingResults = append(res.EnclosingResults, Result{Name: v.Name(), Type: typeStr})
	}
	return nil
}

// hasBoundReturn reports whether the selection contains a return that exits
// the function the selection belongs to (as opposed to a nested closure).
func (a *analyzer) hasBoundReturn() bool {
	found := false
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		if found {
			return false
		}
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
			return false
		}
		return true
	}
	for _, s := range a.stmts {
		ast.Inspect(s, visit)
	}
	return found
}

// alwaysTerminates reports whether every path through the statement list ends
// in a return or an equivalent terminal call. It mirrors the "terminating
// statement" rules of the Go spec (the same analysis go/types uses to decide
// whether a function is missing a return), which is exactly the property
// needed here: only a block that always terminates can be replaced by
// `return name(...)` at the call site.
func alwaysTerminates(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	return terminates(stmts[len(stmts)-1])
}

func terminates(s ast.Stmt) bool {
	switch x := s.(type) {
	case *ast.ReturnStmt:
		return true

	case *ast.ExprStmt:
		return isTerminalCall(x.X)

	case *ast.BranchStmt:
		// goto and fallthrough transfer control unconditionally; break and
		// continue do not end the function. checkControlFlow has already
		// rejected any of these whose target is outside the selection.
		return x.Tok == token.GOTO || x.Tok == token.FALLTHROUGH

	case *ast.LabeledStmt:
		return terminates(x.Stmt)

	case *ast.BlockStmt:
		return alwaysTerminates(x.List)

	case *ast.IfStmt:
		return x.Else != nil && terminates(x.Body) && terminates(x.Else)

	case *ast.ForStmt:
		// `for {}` with no condition and no break out of it never completes.
		return x.Cond == nil && !hasBreak(x.Body, "", true)

	case *ast.SwitchStmt:
		return terminatingClauses(x.Body, true)

	case *ast.TypeSwitchStmt:
		return terminatingClauses(x.Body, true)

	case *ast.SelectStmt:
		// A select with no ready case blocks forever, so unlike a switch it
		// does not need a default clause to be terminating.
		return terminatingClauses(x.Body, false)
	}
	return false
}

// terminatingClauses reports whether every clause of a switch or select body
// terminates. needDefault distinguishes switch (which falls through when no
// case matches unless there is a default) from select (which blocks instead).
func terminatingClauses(body *ast.BlockStmt, needDefault bool) bool {
	if body == nil {
		return false
	}
	hasDefault := false
	for _, cl := range body.List {
		var list []ast.Stmt
		switch c := cl.(type) {
		case *ast.CaseClause:
			if c.List == nil {
				hasDefault = true
			}
			list = c.Body
		case *ast.CommClause:
			if c.Comm == nil {
				hasDefault = true
			}
			list = c.Body
		default:
			return false
		}
		if !alwaysTerminates(list) {
			return false
		}
		for _, s := range list {
			if hasBreak(s, "", true) {
				return false
			}
		}
	}
	return hasDefault || !needDefault
}

// hasBreak reports whether s contains a break that targets the statement the
// caller is asking about: an unlabeled break when implicit is true, or a
// labeled break naming label.
func hasBreak(s ast.Stmt, label string, implicit bool) bool {
	switch x := s.(type) {
	case *ast.BranchStmt:
		if x.Tok == token.BREAK {
			if x.Label == nil {
				return implicit
			}
			return x.Label.Name == label
		}
	case *ast.BlockStmt:
		return hasBreakList(x.List, label, implicit)
	case *ast.IfStmt:
		if hasBreak(x.Body, label, implicit) {
			return true
		}
		return x.Else != nil && hasBreak(x.Else, label, implicit)
	case *ast.CaseClause:
		return hasBreakList(x.Body, label, false)
	case *ast.CommClause:
		return hasBreakList(x.Body, label, false)
	case *ast.LabeledStmt:
		return hasBreak(x.Stmt, label, implicit)
	case *ast.SwitchStmt:
		// An unlabeled break inside a nested switch binds to that switch, so
		// only a labeled break can escape it.
		return label != "" && hasBreak(x.Body, label, false)
	case *ast.TypeSwitchStmt:
		return label != "" && hasBreak(x.Body, label, false)
	case *ast.SelectStmt:
		return label != "" && hasBreak(x.Body, label, false)
	case *ast.ForStmt:
		return label != "" && hasBreak(x.Body, label, false)
	case *ast.RangeStmt:
		return label != "" && hasBreak(x.Body, label, false)
	}
	return false
}

func hasBreakList(list []ast.Stmt, label string, implicit bool) bool {
	for _, s := range list {
		if hasBreak(s, label, implicit) {
			return true
		}
	}
	return false
}

// isTerminalCall recognises calls that never return: panic, os.Exit,
// runtime.Goexit and the log.Fatal family.
func isTerminalCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == "panic"
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		if !ok {
			return false
		}
		switch pkg.Name + "." + fn.Sel.Name {
		case "os.Exit", "runtime.Goexit", "log.Fatal", "log.Fatalf", "log.Fatalln", "log.Panic", "log.Panicf", "log.Panicln":
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 6. Type rendering
// ---------------------------------------------------------------------------

func fileImportAliases(f *ast.File) map[string]string {
	out := make(map[string]string)
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		alias := ""
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				// Neither can be used to qualify a type name, so treat the
				// package as not imported at all.
				continue
			}
			alias = imp.Name.Name
		}
		out[path] = alias
	}
	return out
}

// qualifier renders package qualifiers the way the *source file* writes them,
// which is what makes a rendered type paste-able into that file.
//
// The package being extracted from must render bare: a local type has to come
// out as `scanReport`, not `pkg.scanReport`. Any other package
// uses the alias the file already has for it, or its own name — and in the
// latter case the import has to be added, so it is recorded.
func (a *analyzer) qualifier(p *gotypes.Package) string {
	if p == nil || p == a.pkg.TypesPkg {
		return ""
	}
	if alias, ok := a.fileImports[p.Path()]; ok {
		if alias != "" {
			return alias
		}
		return p.Name()
	}
	a.newImports[p.Path()] = p.Name()
	return p.Name()
}

// renderType renders t as it should appear in the extracted function's
// signature. There is deliberately no fallback: emitting `interface{}` for a
// type that could not be worked out produces code that compiles but is wrong,
// which is far worse than refusing.
func (a *analyzer) renderType(t gotypes.Type, what string) (string, error) {
	if t == nil {
		return "", refuse("the type of %s could not be determined", what)
	}
	if b, ok := t.(*gotypes.Basic); ok && b.Kind() == gotypes.Invalid {
		return "", refuse("the type of %s is invalid; the package does not type-check cleanly", what)
	}
	s := gotypes.TypeString(t, a.qualifier)
	if s == "" || strings.Contains(s, "invalid type") {
		return "", refuse("the type of %s could not be rendered (%q)", what, s)
	}
	return s, nil
}

func deref(t gotypes.Type) gotypes.Type {
	if t == nil {
		return gotypes.Typ[gotypes.Invalid]
	}
	if p, ok := t.Underlying().(*gotypes.Pointer); ok {
		return p.Elem()
	}
	return t
}

// ---------------------------------------------------------------------------
// 7. Body text
// ---------------------------------------------------------------------------

// bodyText returns the exact source of the selected statements, dedented to
// zero base indentation. Without the dedent the extracted body keeps the
// nesting depth it had at its old site, which gofmt does not repair because
// the result is still syntactically valid — just wrongly indented.
func (a *analyzer) bodyText() (string, error) {
	src := a.file.OriginalContent
	if len(src) == 0 {
		return "", refuse("source content for %s is unavailable", a.file.Path)
	}
	start := a.fset.Position(a.blockStart).Offset
	end := a.fset.Position(a.blockEnd).Offset
	if start < 0 || end > len(src) || start >= end {
		return "", refuse("could not map the selected statements back to source offsets")
	}

	// Extend the start back to the beginning of the line so the first line
	// carries its indentation; otherwise it looks like the least-indented
	// line and the dedent below becomes a no-op.
	lineStart := start
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}
	if strings.TrimLeft(string(src[lineStart:start]), " \t") == "" {
		start = lineStart
	}

	return dedent(string(src[start:end])), nil
}

// dedent strips the longest whitespace prefix common to all non-blank lines.
//
// Lines inside a raw string literal are rewritten along with everything else.
// Detecting them would mean re-lexing the region, and a raw literal whose
// continuation lines are indented is already unusual enough that the trade is
// worth making explicit here rather than silently.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	prefix := ""
	havePrefix := false
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		ws := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		if !havePrefix {
			prefix, havePrefix = ws, true
			continue
		}
		prefix = commonPrefix(prefix, ws)
		if prefix == "" {
			break
		}
	}
	if prefix == "" {
		return s
	}
	for i, ln := range lines {
		lines[i] = strings.TrimPrefix(ln, prefix)
	}
	return strings.Join(lines, "\n")
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}
