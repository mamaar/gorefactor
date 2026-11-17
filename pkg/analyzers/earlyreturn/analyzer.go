// Package earlyreturn detects conditionals that keep the happy path indented
// and rewrites them as guard clauses, so the main flow sits at the left margin.
//
// It covers three shapes that the deepifelse analyzer does not, because that
// one only fires when an else branch is present and nesting already exceeds a
// depth threshold:
//
//   - guard_clause:      if cond { body }   as the last statement of a function
//   - loop_continue:     if cond { body }   as the last statement of a loop body
//   - else_after_return: if cond { ...; return } else { body }
//
// Every fix is a whole-statement replacement of the if statement. The
// replacement is rendered at column zero and then indented for its splice
// point, so dry-run previews show the same layout that apply-time gofmt keeps.
package earlyreturn

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	"github.com/mamaar/gorefactor/pkg/analyzers/srctext"
	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
)

// Pattern names the shape of conditional a finding describes.
type Pattern string

const (
	// PatternGuardClause is `if cond { body }` as the final statement of a
	// function body, rewritten to `if !cond { return }` plus the body.
	PatternGuardClause Pattern = "guard_clause"
	// PatternLoopContinue is `if cond { body }` as the final statement of a
	// loop body, rewritten to `if !cond { continue }` plus the body.
	PatternLoopContinue Pattern = "loop_continue"
	// PatternElseAfterReturn is `if cond { ...; return } else { body }`,
	// rewritten by dropping the else and dedenting its body.
	PatternElseAfterReturn Pattern = "else_after_return"
)

// AllPatterns lists every pattern this analyzer knows about.
var AllPatterns = []Pattern{PatternGuardClause, PatternLoopContinue, PatternElseAfterReturn}

// ParsePattern maps a pattern name to its constant, reporting whether the name
// is known.
func ParsePattern(s string) (Pattern, bool) {
	for _, p := range AllPatterns {
		if string(p) == s {
			return p, true
		}
	}

	return "", false
}

// Result is the typed result returned for MCP consumption.
type Result struct {
	File      string  `json:"file"`
	Line      int     `json:"line"`
	Column    int     `json:"column"`
	Function  string  `json:"function_name"`
	Pattern   Pattern `json:"pattern"`
	BodyLines int     `json:"body_lines"`
	// Fixable reports whether a rewrite was attached to this finding. When
	// false, Reason explains what blocked it and no change is produced.
	Fixable    bool   `json:"fixable"`
	Reason     string `json:"reason,omitempty"`
	Suggestion string `json:"suggestion"`
}

type config struct {
	minBodyLines int
	patterns     map[Pattern]bool
}

func defaultConfig() config {
	patterns := make(map[Pattern]bool, len(AllPatterns))
	for _, p := range AllPatterns {
		patterns[p] = true
	}

	return config{minBodyLines: 3, patterns: patterns}
}

// Option configures the analyzer.
type Option func(*config)

// WithMinBodyLines sets how many lines a body must span before inverting the
// condition is considered worthwhile. It applies to guard_clause and
// loop_continue only; else_after_return is always worth collapsing, however
// short the else branch.
func WithMinBodyLines(n int) Option {
	return func(c *config) { c.minBodyLines = n }
}

// WithPatterns restricts detection to the given patterns. Unknown or empty
// input leaves the default (all patterns) in place.
func WithPatterns(patterns ...Pattern) Option {
	return func(c *config) {
		if len(patterns) == 0 {
			return
		}

		selected := make(map[Pattern]bool, len(patterns))
		for _, p := range patterns {
			selected[p] = true
		}

		c.patterns = selected
	}
}

// Analyzer is the default-configured analyzer.
var Analyzer = &analysis.Analyzer{
	Name:             "earlyreturn",
	RunDespiteErrors: true,
	Doc:              "detects conditionals that should be inverted into early-return guard clauses",
	Run:              makeRun(defaultConfig()),
	Requires:         []*analysis.Analyzer{filedata.Analyzer},
}

// NewAnalyzer creates a configured analyzer.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	return &analysis.Analyzer{
		Name:             "earlyreturn",
		RunDespiteErrors: true,
		Doc:              "detects conditionals that should be inverted into early-return guard clauses",
		Run:              makeRun(cfg),
		Requires:         []*analysis.Analyzer{filedata.Analyzer},
	}
}

func makeRun(cfg config) func(*analysis.Pass) (any, error) {
	return func(pass *analysis.Pass) (any, error) {
		fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
		var results []*Result

		for _, file := range pass.Files {
			pos := pass.Fset.Position(file.Pos())
			w := &walker{pass: pass, cfg: cfg, content: fd.Content[pos.Filename]}
			w.walkFile(file)
			results = append(results, w.results...)
		}

		return results, nil
	}
}

// blockKind describes what encloses a statement list, which decides whether an
// early exit can be synthesised for it and what that exit looks like.
type blockKind int

const (
	// blockOther is any list from which control cannot be short-circuited by a
	// bare return or continue: an if body, a case clause, a plain block.
	blockOther blockKind = iota
	blockFuncBody
	blockLoopBody
)

type walker struct {
	pass    *analysis.Pass
	cfg     config
	content []byte
	results []*Result
}

func (w *walker) walkFile(file *ast.File) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		w.walkStmts(fn.Body.List, blockFuncBody, fn.Name.Name, fn.Type, stmtfacts.SignatureNames(fn.Recv, fn.Type))

		// Function literals open their own function scope, so their bodies are
		// roots in their own right. The structural walk never descends into
		// expressions, so this is the only visit each literal body gets.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok || lit.Body == nil {
				return true
			}

			name := fn.Name.Name + ".func"
			w.walkStmts(lit.Body.List, blockFuncBody, name, lit.Type, stmtfacts.SignatureNames(nil, lit.Type))

			return true
		})
	}
}

// walkStmts inspects one statement list. scopeNames holds every identifier
// already declared in the scope this list belongs to, which is what decides
// whether a nested block can be safely dedented into it.
func (w *walker) walkStmts(list []ast.Stmt, kind blockKind, fnName string, fnType *ast.FuncType, scopeNames map[string]bool) {
	names := stmtfacts.DeclaredNames(list)
	for n := range scopeNames {
		names[n] = true
	}

	for i, stmt := range list {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if ok {
			if w.tryMatch(ifStmt, kind, fnName, fnType, enclosing{list: list, idx: i, names: names}) {
				// A fix was attached covering this whole if statement. Do not
				// descend: a nested fix would produce an overlapping text edit,
				// which Serializer.validateChangePositions rejects outright.
				continue
			}
		}

		w.descend(stmt, fnName, fnType)
	}
}

// descend walks the statement lists nested inside stmt, carrying the enclosing
// function context down with it.
func (w *walker) descend(stmt ast.Stmt, fnName string, fnType *ast.FuncType) {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		w.walkStmts(s.Body.List, blockOther, fnName, fnType, nil)

		switch e := s.Else.(type) {
		case *ast.BlockStmt:
			w.walkStmts(e.List, blockOther, fnName, fnType, nil)
		case *ast.IfStmt:
			w.descend(e, fnName, fnType)
		}
	case *ast.ForStmt:
		w.walkStmts(s.Body.List, blockLoopBody, fnName, fnType, nil)
	case *ast.RangeStmt:
		w.walkStmts(s.Body.List, blockLoopBody, fnName, fnType, nil)
	case *ast.BlockStmt:
		w.walkStmts(s.List, blockOther, fnName, fnType, nil)
	case *ast.LabeledStmt:
		w.descend(s.Stmt, fnName, fnType)
	case *ast.SwitchStmt:
		// An init statement (`switch v := f(); v {`) declares v in the switch's
		// own implicit block, which encloses each clause. A clause body may
		// therefore shadow it legally, so it is not a collision candidate.
		w.walkClauses(s.Body, fnName, fnType, nil)
	case *ast.TypeSwitchStmt:
		// The type switch guard is different: `switch v := x.(type)` declares v
		// in the implicit block of *each clause* - the same scope as that
		// clause's body statements. A block dedented into the clause body
		// therefore does collide with it.
		w.walkClauses(s.Body, fnName, fnType, assignedNames(s.Assign))
	case *ast.SelectStmt:
		w.walkClauses(s.Body, fnName, fnType, nil)
	}
}

// walkClauses walks each clause body of a switch or select. guardNames holds
// identifiers that share the clause body's scope rather than enclosing it.
func (w *walker) walkClauses(body *ast.BlockStmt, fnName string, fnType *ast.FuncType, guardNames map[string]bool) {
	if body == nil {
		return
	}

	for _, clause := range body.List {
		switch c := clause.(type) {
		case *ast.CaseClause:
			w.walkStmts(c.Body, blockOther, fnName, fnType, guardNames)
		case *ast.CommClause:
			// `case v := <-ch:` declares v in this clause's implicit block,
			// alongside the clause body statements.
			w.walkStmts(c.Body, blockOther, fnName, fnType, assignedNames(c.Comm))
		}
	}
}

// assignedNames returns the identifiers a short variable declaration defines,
// or nil for any other statement.
func assignedNames(stmt ast.Stmt) map[string]bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return nil
	}

	names := make(map[string]bool)
	for _, lhs := range assign.Lhs {
		stmtfacts.AddName(names, lhs)
	}

	return names
}

// tryMatch reports whether ifStmt matched a pattern *and* a fix was attached.
// A finding recorded without a fix returns false so the walk still descends
// into it — an unfixable finding emits no text edit, so nothing can overlap.
func (w *walker) tryMatch(ifStmt *ast.IfStmt, kind blockKind, fnName string, fnType *ast.FuncType, enc enclosing) bool {
	if ifStmt.Else != nil {
		return w.tryElseAfterReturn(ifStmt, fnName, enc)
	}

	isLast := enc.idx == len(enc.list)-1

	switch {
	case kind == blockFuncBody && isLast:
		return w.tryGuard(ifStmt, PatternGuardClause, fnName, fnType, enc)
	case kind == blockLoopBody && isLast:
		return w.tryGuard(ifStmt, PatternLoopContinue, fnName, fnType, enc)
	}

	return false
}

// tryGuard handles the two no-else shapes, which differ only in the early exit
// they emit.
func (w *walker) tryGuard(ifStmt *ast.IfStmt, pattern Pattern, fnName string, fnType *ast.FuncType, enc enclosing) bool {
	if !w.cfg.patterns[pattern] || ifStmt.Init != nil || len(ifStmt.Body.List) == 0 {
		return false
	}

	bodyLines := w.blockLines(ifStmt.Body)
	if bodyLines < w.cfg.minBodyLines {
		return false
	}

	exit := "continue"

	if pattern == PatternGuardClause {
		var ok bool

		exit, ok = bareReturn(fnType)
		if !ok {
			w.record(ifStmt, pattern, fnName, bodyLines, false,
				"function has unnamed results, so the guard would have to invent return values")

			return false
		}
	}

	clash := w.firstClash(ifStmt.Body.List, enc)
	if clash != "" {
		w.record(ifStmt, pattern, fnName, bodyLines, false,
			fmt.Sprintf("dedenting the body would redeclare %q in the enclosing scope", clash))

		return false
	}

	inverted := srctext.InvertCondition(w.pass.Fset, w.content, w.pass.TypesInfo, ifStmt.Cond)

	bodyText := srctext.BlockText(w.pass.Fset, w.content, ifStmt.Body)
	if inverted == "" || bodyText == "" {
		w.record(ifStmt, pattern, fnName, bodyLines, false, "source text for the condition or body is unavailable")
		return false
	}

	indent := srctext.Indentation(w.content, w.pass.Fset.Position(ifStmt.Pos()).Offset)
	newText := "if " + inverted + " {\n\t" + exit + "\n}\n\n" + srctext.Normalize(bodyText)
	newText = srctext.ApplyIndent(newText, indent)

	w.record(ifStmt, pattern, fnName, bodyLines, true, "")
	w.report(ifStmt, pattern, newText)

	return true
}

func (w *walker) tryElseAfterReturn(ifStmt *ast.IfStmt, fnName string, enc enclosing) bool {
	if !w.cfg.patterns[PatternElseAfterReturn] {
		return false
	}

	elseBlock, ok := ifStmt.Else.(*ast.BlockStmt)
	if !ok || len(elseBlock.List) == 0 || len(ifStmt.Body.List) == 0 {
		return false
	}

	// An init statement declares names scoped to the whole if statement.
	// Dedenting the else body would move references to those names out of the
	// scope that declares them.
	if ifStmt.Init != nil {
		return false
	}

	if !stmtfacts.Terminates(w.pass.TypesInfo, ifStmt.Body.List[len(ifStmt.Body.List)-1]) {
		return false
	}

	bodyLines := w.blockLines(elseBlock)

	clash := w.firstClash(elseBlock.List, enc)
	if clash != "" {
		w.record(ifStmt, PatternElseAfterReturn, fnName, bodyLines, false,
			fmt.Sprintf("dedenting the else branch would redeclare %q in the enclosing scope", clash))

		return false
	}

	condText := srctext.ExprText(w.pass.Fset, w.content, ifStmt.Cond)
	ifBodyText := srctext.BlockText(w.pass.Fset, w.content, ifStmt.Body)

	elseBodyText := srctext.BlockText(w.pass.Fset, w.content, elseBlock)
	if condText == "" || ifBodyText == "" || elseBodyText == "" {
		w.record(ifStmt, PatternElseAfterReturn, fnName, bodyLines, false, "source text for the branches is unavailable")
		return false
	}

	indent := srctext.Indentation(w.content, w.pass.Fset.Position(ifStmt.Pos()).Offset)
	newText := "if " + condText + " {\n" +
		srctext.IndentLines(srctext.Normalize(ifBodyText), "\t") + "\n}\n\n" +
		srctext.Normalize(elseBodyText)
	newText = srctext.ApplyIndent(newText, indent)

	w.record(ifStmt, PatternElseAfterReturn, fnName, bodyLines, true, "")
	w.report(ifStmt, PatternElseAfterReturn, newText)

	return true
}

// enclosing locates an if statement in its statement list. names holds every
// identifier the list's scope already declares.
type enclosing struct {
	list  []ast.Stmt
	idx   int
	names map[string]bool
}

// firstClash returns a name that dedenting moved into enc's scope would
// redeclare, or "" when none does. A name is not a clash when the moved `:=`
// can reuse the outer variable instead, see reusable.
func (w *walker) firstClash(moved []ast.Stmt, enc enclosing) string {
	declared := stmtfacts.DeclaredNames(moved)

	for name := range declared {
		if enc.names[name] && w.reusable(name, moved, enc) {
			delete(declared, name)
		}
	}

	return stmtfacts.FirstClash(declared, enc.names)
}

// reusable reports whether, once moved is dedented into enc's scope, its
// `:=` that declares name still compiles and keeps its meaning by assigning
// the outer variable of the same name instead of declaring a new one. That
// holds when the statement declares another new name, both variables have the
// same type, nothing after the if reads the outer variable, and no named
// result, goto, closure or pointer can observe the overwrite.
func (w *walker) reusable(name string, moved []ast.Stmt, enc enclosing) bool {
	outer := w.outerVar(name, enc.list[:enc.idx])
	inner := w.firstDefinition(name, moved, enc.names)

	if outer == nil || inner == nil || !gotypes.Identical(outer.Type(), inner.Type()) {
		return false
	}

	v, isVar := outer.(*gotypes.Var)
	if !isVar || v.Kind() == gotypes.ResultVar || stmtfacts.HasGoto(enc.list) {
		return false
	}

	if refersTo(enc.list[enc.idx+1:], map[string]bool{name: true}) {
		return false
	}

	info := w.pass.TypesInfo

	// The if counts too: a closure or pointer made in its header, or in the moved
	// block before the `:=`, holds the outer variable.
	return !slices.ContainsFunc(enc.list[:enc.idx+1], func(s ast.Stmt) bool { return stmtfacts.Captures(info, s, outer) }) &&
		!slices.ContainsFunc(moved, func(s ast.Stmt) bool { return stmtfacts.Captures(info, s, inner) })
}

// outerVar returns the variable name that a `:=` in prior declares or assigns.
func (w *walker) outerVar(name string, prior []ast.Stmt) gotypes.Object {
	for _, stmt := range prior {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE {
			continue
		}

		for _, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if ok && ident.Name == name {
				return w.pass.TypesInfo.ObjectOf(ident)
			}
		}
	}

	return nil
}

// firstDefinition returns the variable that the first statement in moved
// declaring name creates, or nil when that statement is not a `:=` that would
// still declare a new name after dedenting.
func (w *walker) firstDefinition(name string, moved []ast.Stmt, destination map[string]bool) gotypes.Object {
	for i, stmt := range moved {
		if !stmtfacts.DeclaredNames([]ast.Stmt{stmt})[name] {
			continue
		}

		assign, ok := stmt.(*ast.AssignStmt)
		if !ok {
			return nil
		}

		earlier := stmtfacts.DeclaredNames(moved[:i])

		var obj gotypes.Object

		hasNew := false

		for _, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || ident.Name == "_" {
				continue
			}

			if ident.Name == name {
				obj = w.pass.TypesInfo.Defs[ident]
			} else if !destination[ident.Name] && !earlier[ident.Name] {
				hasNew = true
			}
		}

		if !hasNew {
			return nil
		}

		return obj
	}

	return nil
}

// refersTo reports whether any of stmts mentions one of names.
func refersTo(stmts []ast.Stmt, names map[string]bool) bool {
	for _, stmt := range stmts {
		if stmtfacts.ReferencesAny(stmt, names) {
			return true
		}
	}

	return false
}

func (w *walker) record(ifStmt *ast.IfStmt, pattern Pattern, fnName string, bodyLines int, fixable bool, reason string) {
	pos := w.pass.Fset.Position(ifStmt.Pos())
	w.results = append(w.results, &Result{
		File:       pos.Filename,
		Line:       pos.Line,
		Column:     pos.Column,
		Function:   fnName,
		Pattern:    pattern,
		BodyLines:  bodyLines,
		Fixable:    fixable,
		Reason:     reason,
		Suggestion: suggestion(pattern),
	})
}

func (w *walker) report(ifStmt *ast.IfStmt, pattern Pattern, newText string) {
	message := fixMessage(pattern)
	w.pass.Report(analysis.Diagnostic{
		Pos:     ifStmt.Pos(),
		End:     ifStmt.End(),
		Message: message,
		SuggestedFixes: []analysis.SuggestedFix{{
			Message: message,
			TextEdits: []analysis.TextEdit{
				{Pos: ifStmt.Pos(), End: ifStmt.End(), NewText: []byte(newText)},
			},
		}},
	})
}

// fixMessage becomes the Change.Description carried through to the validator,
// which skips syntax checking for descriptions that look like code fragments
// (see Validator.validateCompilation). These wordings deliberately avoid those
// substrings so the emitted text does get parsed before it is written.
func fixMessage(pattern Pattern) string {
	switch pattern {
	case PatternGuardClause:
		return "Invert condition into an early return"
	case PatternLoopContinue:
		return "Invert condition into an early continue"
	default:
		return "Drop else branch that follows a terminating if"
	}
}

func suggestion(pattern Pattern) string {
	switch pattern {
	case PatternGuardClause:
		return "Invert the condition and return early, so the body is no longer nested"
	case PatternLoopContinue:
		return "Invert the condition and continue early, so the body is no longer nested"
	default:
		return "The if branch already terminates; drop the else and dedent its body"
	}
}

func (w *walker) blockLines(block *ast.BlockStmt) int {
	start := w.pass.Fset.Position(block.Lbrace).Line
	end := w.pass.Fset.Position(block.Rbrace).Line

	lines := end - start - 1
	if lines > 0 {
		return lines
	}

	return 0
}

// bareReturn returns the early-exit statement for a function with the given
// signature, and whether one can be written without inventing values.
//
// In code that compiles, a function declaring results must end in a terminating
// statement, so a trailing `if cond { ... }` only ever occurs in a result-less
// function. The named-results case is handled anyway, and the unnamed case is
// refused rather than guessed at.
func bareReturn(fnType *ast.FuncType) (string, bool) {
	if fnType == nil || fnType.Results == nil || len(fnType.Results.List) == 0 {
		return "return", true
	}

	for _, field := range fnType.Results.List {
		if len(field.Names) == 0 {
			return "", false
		}
	}

	return "return", true
}
