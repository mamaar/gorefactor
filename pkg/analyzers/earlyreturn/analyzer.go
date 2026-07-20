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
// Every fix is a whole-statement replacement of the if statement, emitted as
// verbatim source text. Layout is not this package's job: the serializer runs
// format.Source over the file after applying changes, which regenerates all
// indentation from the parsed structure.
package earlyreturn

import (
	"fmt"
	"go/ast"
	"go/token"

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
	Name:     "earlyreturn",
	Doc:      "detects conditionals that should be inverted into early-return guard clauses",
	Run:      makeRun(defaultConfig()),
	Requires: []*analysis.Analyzer{filedata.Analyzer},
}

// NewAnalyzer creates a configured analyzer.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &analysis.Analyzer{
		Name:     "earlyreturn",
		Doc:      "detects conditionals that should be inverted into early-return guard clauses",
		Run:      makeRun(cfg),
		Requires: []*analysis.Analyzer{filedata.Analyzer},
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
		isLast := i == len(list)-1

		if ifStmt, ok := stmt.(*ast.IfStmt); ok {
			if w.tryMatch(ifStmt, kind, isLast, fnName, fnType, names) {
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
func (w *walker) tryMatch(ifStmt *ast.IfStmt, kind blockKind, isLast bool, fnName string, fnType *ast.FuncType, scopeNames map[string]bool) bool {
	if ifStmt.Else != nil {
		return w.tryElseAfterReturn(ifStmt, fnName, scopeNames)
	}

	switch {
	case kind == blockFuncBody && isLast:
		return w.tryGuard(ifStmt, PatternGuardClause, fnName, fnType, scopeNames)
	case kind == blockLoopBody && isLast:
		return w.tryGuard(ifStmt, PatternLoopContinue, fnName, fnType, scopeNames)
	}

	return false
}

// tryGuard handles the two no-else shapes, which differ only in the early exit
// they emit.
func (w *walker) tryGuard(ifStmt *ast.IfStmt, pattern Pattern, fnName string, fnType *ast.FuncType, scopeNames map[string]bool) bool {
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

	if clash := stmtfacts.FirstClash(stmtfacts.DeclaredNames(ifStmt.Body.List), scopeNames); clash != "" {
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

	newText := "if " + inverted + " {\n" + exit + "\n}\n" + bodyText
	w.record(ifStmt, pattern, fnName, bodyLines, true, "")
	w.report(ifStmt, pattern, newText)
	return true
}

func (w *walker) tryElseAfterReturn(ifStmt *ast.IfStmt, fnName string, scopeNames map[string]bool) bool {
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

	if clash := stmtfacts.FirstClash(stmtfacts.DeclaredNames(elseBlock.List), scopeNames); clash != "" {
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

	newText := "if " + condText + " {\n" + ifBodyText + "\n}\n" + elseBodyText
	w.record(ifStmt, PatternElseAfterReturn, fnName, bodyLines, true, "")
	w.report(ifStmt, PatternElseAfterReturn, newText)
	return true
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
	if lines := end - start - 1; lines > 0 {
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
