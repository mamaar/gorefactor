// Package deepifelse detects nested if-else chains that should be flattened
// into guard clauses, and rewrites the ones it can.
//
// Detection and rewriting used to be wildly out of step. The detector fires on
// any chain past a depth threshold, while the rewriter accepted exactly one
// shape: every else a block ending in return, and every body a single nested
// if-else. Anything using `else if`, or with a body that was a plain return,
// was reported and then declined — which ruled out most real code, including
// chains where every branch already returned and flattening was obviously
// safe.
//
// The rewriter now works on the chain as a whole. A chain is the links
// `if C0 {B0} else if C1 {B1} … [else {F}]`, and each link is rewritten by one
// of two moves:
//
//   - Elide. If Bi hands control away, the else after it is redundant: emit
//     `if Ci {Bi}` and carry on with the rest of the chain at the same level.
//     No condition is negated, so the author's polarity survives.
//   - Invert. At the last link, when F hands control away, emit `if !Ci {F}`
//     and dedent Bi. This is the classic guard clause, and it is what actually
//     removes a level of nesting.
//
// Inversion is confined to the last link on purpose: inverting an earlier one
// would move the remaining `else if` links *inside* the new guard, which nests
// rather than flattens. Between the two moves, inversion is chosen only when
// it pays — when Bi does not hand control away (elision is not available), or
// when F is strictly shorter than Bi, so the short branch is the one that ends
// up in the guard. On a tie elision wins, because negating a condition that
// already reads forwards buys nothing.
//
// What the rewriter will not do is hoist an `if init; cond` header. That is
// fix_if_init_assignments' job (package ifinit), and it does it properly,
// including downgrading a colliding `:=` to `=`. A link that declares
// variables in its header keeps them and is never inverted; the reason
// reported says which tool to run first. Running fix_if_init_assignments and
// then this fixer is what takes such a chain all the way down to depth 1.
package deepifelse

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	"github.com/mamaar/gorefactor/pkg/analyzers/srctext"
	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File                       string `json:"file"`
	Line                       int    `json:"line"`
	Column                     int    `json:"column"`
	Function                   string `json:"function_name"`
	NestingDepth               int    `json:"nesting_depth"`
	HappyPathDepth             int    `json:"happy_path_depth"`
	ErrorBranches              int    `json:"error_branches"`
	ComplexityReductionPercent int    `json:"complexity_reduction_estimate"`
	Suggestion                 string `json:"suggestion"`

	// Fixable reports whether a rewrite was attached to this finding. The
	// detector fires on any chain past the depth threshold, and a chain can be
	// flattened only partway, so this means "some rewrite was produced", not
	// "the chain came out flat".
	Fixable bool `json:"fixable"`
	// Reason explains why the rewrite stopped where it did. It is empty only
	// when the whole chain was flattened, so a Fixable finding carrying a
	// Reason is one that was flattened as far as it safely could be.
	Reason string `json:"reason,omitempty"`
	// EarlyReturns is how many branches the rewrite lifted out of the nesting
	// — the real count from the rewritten chain, not an estimate from nesting
	// depth. Zero when Fixable is false.
	EarlyReturns int `json:"early_returns"`
}

type config struct {
	maxNesting   int
	minElseLines int
}

// Option configures the analyzer.
type Option func(*config)

// WithMaxNesting sets the maximum acceptable nesting depth.
func WithMaxNesting(n int) Option {
	return func(c *config) { c.maxNesting = n }
}

// WithMinElseLines sets the minimum else branch lines to trigger detection.
func WithMinElseLines(n int) Option {
	return func(c *config) { c.minElseLines = n }
}

var Analyzer = &analysis.Analyzer{
	Name:     "deepifelse",
	Doc:      "detects nested if-else chains that should use early returns (guard clauses)",
	Run:      makeRun(config{maxNesting: 2, minElseLines: 3}),
	Requires: []*analysis.Analyzer{filedata.Analyzer},
}

// NewAnalyzer creates a configured analyzer.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := config{maxNesting: 2, minElseLines: 3}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &analysis.Analyzer{
		Name:     "deepifelse",
		Doc:      "detects nested if-else chains that should use early returns (guard clauses)",
		Run:      makeRun(cfg),
		Requires: []*analysis.Analyzer{filedata.Analyzer},
	}
}

// fixMessage becomes types.Change.Description, which Validator.validateCompilation
// string-matches to decide whether the new text is a standalone Go fragment
// worth parsing before it is applied. This wording deliberately avoids every
// substring on that list ("parameter", "import", "call to", "argument",
// "Split", "Replace", "Wrap", …) so the emitted text really does get parsed.
const fixMessage = "Flatten deep if-else chain with early returns"

// reasonNoText is reported when a piece of the chain's source text could not be
// recovered from the file content. Splicing a rewrite together out of a
// partially recovered chain would be worse than leaving it alone.
const reasonNoText = "source text for the chain is unavailable"

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

type walker struct {
	pass    *analysis.Pass
	cfg     config
	content []byte
	results []*Result

	// fnBody is the body of the function currently being walked, which is the
	// region a dedent has to be safe within.
	fnBody *ast.BlockStmt

	// reported holds the source span of every chain already reported, so a
	// chain nested inside one is never reported again. Without this a function
	// literal inside a reported chain could produce a second text edit
	// overlapping the first, which Serializer.validateChangePositions rejects
	// outright.
	reported []span
}

type span struct{ start, end token.Pos }

// dest describes where a rendered block's statements will end up: the names
// declared directly in that statement list, and every name visible there. A
// dedent has to answer to both — the first decides whether the result still
// compiles, the second whether it still means the same thing.
type dest struct {
	list  map[string]bool
	scope map[string]bool
}

// nested returns the destination for a block that keeps its own braces.
func (d dest) nested(block *ast.BlockStmt) dest {
	own := stmtfacts.DeclaredNames(block.List)
	return dest{list: own, scope: union(d.scope, own)}
}

// dedented returns the destination for a block whose braces are about to be
// removed, merging its declarations into the surrounding list.
func (d dest) dedented(block *ast.BlockStmt) dest {
	own := stmtfacts.DeclaredNames(block.List)
	return dest{list: union(d.list, own), scope: union(d.scope, own)}
}

func (w *walker) walkFile(file *ast.File) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		w.fnBody = fn.Body
		w.walkStmts(fn.Body.List, fn.Name.Name, stmtfacts.SignatureNames(fn.Recv, fn.Type))

		// Function literals open their own function scope, so their bodies are
		// roots in their own right. The structural walk never descends into
		// expressions, so this is the only visit each literal body gets.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok || lit.Body == nil {
				return true
			}
			w.fnBody = lit.Body
			w.walkStmts(lit.Body.List, fn.Name.Name+".func", stmtfacts.SignatureNames(nil, lit.Type))
			return true
		})
	}
}

// walkStmts inspects one statement list. scope holds every identifier already
// visible where this list sits, which is what decides whether a nested block
// can safely be dedented into it.
func (w *walker) walkStmts(list []ast.Stmt, fnName string, scope map[string]bool) {
	declared := stmtfacts.DeclaredNames(list)
	names := union(scope, declared)

	for _, stmt := range list {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if ok && ifStmt.Else != nil {
			if w.tryChain(ifStmt, fnName, dest{list: declared, scope: names}) {
				// The finding covers this whole chain, and so does its edit.
				// Descending would report chains inside it a second time.
				continue
			}
		}
		w.descend(stmt, fnName, names)
	}
}

// descend walks the statement lists nested inside stmt, carrying the enclosing
// scope down with it. Unlike the equivalent walk in package earlyreturn, the
// scope is propagated rather than reset at each nested block: a block dedented
// into an inner scope can shadow a name declared further out, which changes
// what the statements after it refer to.
func (w *walker) descend(stmt ast.Stmt, fnName string, scope map[string]bool) {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		w.walkStmts(s.Body.List, fnName, scope)
		switch e := s.Else.(type) {
		case *ast.BlockStmt:
			w.walkStmts(e.List, fnName, scope)
		case *ast.IfStmt:
			w.descend(e, fnName, scope)
		}
	case *ast.ForStmt:
		w.walkStmts(s.Body.List, fnName, scope)
	case *ast.RangeStmt:
		w.walkStmts(s.Body.List, fnName, scope)
	case *ast.BlockStmt:
		w.walkStmts(s.List, fnName, scope)
	case *ast.LabeledStmt:
		w.descend(s.Stmt, fnName, scope)
	case *ast.SwitchStmt:
		// An init statement (`switch v := f(); v {`) declares v in the switch's
		// own implicit block, which encloses each clause. A clause body may
		// therefore shadow it legally, so it is not a collision candidate.
		w.walkClauses(s.Body, fnName, scope)
	case *ast.TypeSwitchStmt:
		// The type switch guard is different: `switch v := x.(type)` declares v
		// in the implicit block of *each clause* - the same scope as that
		// clause's body statements. A block dedented into the clause body
		// therefore does collide with it.
		w.walkClauses(s.Body, fnName, union(scope, assignedNames(s.Assign)))
	case *ast.SelectStmt:
		w.walkClauses(s.Body, fnName, scope)
	}
}

func (w *walker) walkClauses(body *ast.BlockStmt, fnName string, scope map[string]bool) {
	if body == nil {
		return
	}
	for _, clause := range body.List {
		switch c := clause.(type) {
		case *ast.CaseClause:
			w.walkStmts(c.Body, fnName, scope)
		case *ast.CommClause:
			// `case v := <-ch:` declares v in this clause's implicit block,
			// alongside the clause body statements.
			w.walkStmts(c.Body, fnName, union(scope, assignedNames(c.Comm)))
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

// tryChain reports a chain past the thresholds and attaches whatever rewrite
// could be built for it. It returns whether the chain was reported, which is
// also whether the caller should stop descending into it.
func (w *walker) tryChain(ifStmt *ast.IfStmt, fnName string, d dest) bool {
	depth := measureIfElseDepth(ifStmt, 1)
	if depth <= w.cfg.maxNesting {
		return false
	}
	if countElseLines(w.pass.Fset, ifStmt) < w.cfg.minElseLines {
		return false
	}
	if w.alreadyReported(ifStmt) {
		return false
	}

	res := w.flattenChain(ifStmt, d)
	if res.lifted > 0 && srctext.SpansRawString(ifStmt) {
		res = flattened{reason: "the chain contains a multi-line raw string literal, whose indentation is part of its value"}
	}

	var fixes []analysis.SuggestedFix
	if res.lifted > 0 {
		startOffset := w.offset(ifStmt.Pos())
		indent := srctext.Indentation(w.content, startOffset)
		fixes = append(fixes, analysis.SuggestedFix{
			Message: fixMessage,
			TextEdits: []analysis.TextEdit{
				{Pos: ifStmt.Pos(), End: ifStmt.End(), NewText: []byte(srctext.ApplyIndent(res.text, indent))},
			},
		})
	}

	w.pass.Report(analysis.Diagnostic{
		Pos:            ifStmt.Pos(),
		End:            ifStmt.End(),
		Message:        fmt.Sprintf("deep if-else chain (depth %d) should use early returns", depth),
		SuggestedFixes: fixes,
	})

	pos := w.pass.Fset.Position(ifStmt.Pos())
	reductionPercent := 0
	if depth > 0 {
		reductionPercent = ((depth - 1) * 100) / depth
	}

	w.results = append(w.results, &Result{
		File:                       pos.Filename,
		Line:                       pos.Line,
		Column:                     pos.Column,
		Function:                   fnName,
		NestingDepth:               depth,
		HappyPathDepth:             measureHappyPathDepth(ifStmt, 1),
		ErrorBranches:              countErrorBranches(ifStmt),
		ComplexityReductionPercent: reductionPercent,
		Suggestion:                 fmt.Sprintf("Invert conditions and use early returns for error cases (depth %d -> 0)", depth),
		Fixable:                    res.lifted > 0,
		Reason:                     res.reason,
		EarlyReturns:               res.lifted,
	})

	w.reported = append(w.reported, span{ifStmt.Pos(), ifStmt.End()})
	return true
}

func (w *walker) alreadyReported(n ast.Node) bool {
	for _, s := range w.reported {
		if n.Pos() >= s.start && n.End() <= s.end {
			return true
		}
	}
	return false
}

// --- detection measurements ---

func measureIfElseDepth(ifStmt *ast.IfStmt, currentDepth int) int {
	maxDepth := currentDepth

	for _, stmt := range ifStmt.Body.List {
		innerIf, ok := stmt.(*ast.IfStmt)
		if ok {
			if innerIf.Else != nil {
				d := measureIfElseDepth(innerIf, currentDepth+1)
				if d > maxDepth {
					maxDepth = d
				}
			}
		}
	}

	elseIf, ok := ifStmt.Else.(*ast.IfStmt)
	if ok {
		if elseIf.Else != nil {
			d := measureIfElseDepth(elseIf, currentDepth+1)
			if d > maxDepth {
				maxDepth = d
			}
		}
	} else {
		elseBlock, ok := ifStmt.Else.(*ast.BlockStmt)
		if ok {
			for _, stmt := range elseBlock.List {
				innerIf, ok := stmt.(*ast.IfStmt)
				if ok {
					if innerIf.Else != nil {
						d := measureIfElseDepth(innerIf, currentDepth+1)
						if d > maxDepth {
							maxDepth = d
						}
					}
				}
			}
		}
	}

	return maxDepth
}

func countElseLines(fset *token.FileSet, ifStmt *ast.IfStmt) int {
	if ifStmt.Else == nil {
		return 0
	}

	total := 0

	switch e := ifStmt.Else.(type) {
	case *ast.BlockStmt:
		startLine := fset.Position(e.Lbrace).Line
		endLine := fset.Position(e.Rbrace).Line
		total += endLine - startLine + 1
	case *ast.IfStmt:
		startLine := fset.Position(e.Body.Lbrace).Line
		endLine := fset.Position(e.Body.Rbrace).Line
		total += endLine - startLine + 1
		total += countElseLines(fset, e)
	}

	for _, stmt := range ifStmt.Body.List {
		innerIf, ok := stmt.(*ast.IfStmt)
		if ok {
			total += countElseLines(fset, innerIf)
		}
	}

	return total
}

func countErrorBranches(ifStmt *ast.IfStmt) int {
	count := 0

	if ifStmt.Else != nil {
		switch e := ifStmt.Else.(type) {
		case *ast.BlockStmt:
			if blockLooksLikeErrorReturn(e) {
				count++
			}
		case *ast.IfStmt:
			count += countErrorBranches(e)
		}
	}

	for _, stmt := range ifStmt.Body.List {
		innerIf, ok := stmt.(*ast.IfStmt)
		if ok {
			count += countErrorBranches(innerIf)
		}
	}

	return count
}

func blockLooksLikeErrorReturn(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		_, ok := stmt.(*ast.ReturnStmt)
		if ok {
			return true
		}
	}
	return false
}

func measureHappyPathDepth(ifStmt *ast.IfStmt, currentDepth int) int {
	maxDepth := currentDepth

	for _, stmt := range ifStmt.Body.List {
		innerIf, ok := stmt.(*ast.IfStmt)
		if ok {
			if innerIf.Else != nil {
				d := measureHappyPathDepth(innerIf, currentDepth+1)
				if d > maxDepth {
					maxDepth = d
				}
			}
		}
	}

	return maxDepth
}

// --- the rewriter ---

// flattened is the outcome of rendering a chain or a block. text is the
// statements it produced, normalised so the first line sits at column zero and
// everything else is indented relative to it; the caller re-indents once, at
// the point where the text is spliced in. lifted counts the branches taken out
// of the nesting, and reason says why the rewrite stopped where it did.
type flattened struct {
	text   string
	lifted int
	reason string
}

// flattenChain renders the chain rooted at root as a sequence of statements
// belonging to the destination that currently holds it.
func (w *walker) flattenChain(root *ast.IfStmt, d dest) flattened {
	links, final := chainOf(root)

	// Names the surrounding code still reads. Computed per chain, not once per
	// function: a nested chain is judged against everything outside *it*,
	// including the parts of the enclosing chain that are not inside it.
	read := w.namesReadOutside(root)

	// The last link can only be elided if the final else can be dedented after
	// it, so this verdict is needed before the loop reaches that link.
	finalClash := ""
	if final != nil {
		finalClash = w.dedentBlocked(final, d, read)
	}

	var stmts []string
	lifted := 0
	reason := ""

	stop := func(from *ast.IfStmt, why string) flattened {
		text := w.stmtText(from)
		if text == "" {
			return flattened{strings.Join(stmts, "\n"), lifted, reasonNoText}
		}
		stmts = append(stmts, text)
		// why describes this chain; reason came from a nested one. The outer
		// explanation is the one that says where flattening actually stopped.
		return flattened{strings.Join(stmts, "\n"), lifted, firstReason(why, reason)}
	}

	for i, link := range links {
		isLast := i == len(links)-1

		if isLast && final != nil {
			res, ok, why := w.invert(link, final, d, read)
			switch {
			case ok:
				stmts = append(stmts, res.text)
				return flattened{strings.Join(stmts, "\n"), lifted + res.lifted, firstReason(reason, res.reason)}
			case finalClash != "":
				// Neither move is available: inversion was refused and eliding
				// would leave the final else to be dedented, which collides.
				return stop(link, firstReason(why, finalClash))
			default:
				// Elision will still flatten this link, but a level of nesting
				// stays that inverting would have removed. That is worth
				// reporting: it is usually the "run fix_if_init_assignments
				// first" case, where a second tool gets the caller the rest of
				// the way.
				reason = firstReason(why, reason)
			}
		}

		why := w.elidable(link)
		if why != "" {
			return stop(link, why)
		}

		header, ok := w.linkHeader(link)
		if !ok {
			return stop(link, reasonNoText)
		}
		body := w.flattenBlock(link.Body, d.nested(link.Body))
		if body.text == "" {
			return stop(link, reasonNoText)
		}

		stmts = append(stmts, header+" {\n"+srctext.IndentLines(body.text, "\t")+"\n}")
		lifted += 1 + body.lifted
		reason = firstReason(reason, body.reason)
	}

	if final != nil {
		res := w.flattenBlock(final, d.dedented(final))
		if res.text == "" {
			return flattened{strings.Join(stmts, "\n"), lifted, reasonNoText}
		}
		stmts = append(stmts, res.text)
		lifted += res.lifted
		reason = firstReason(reason, res.reason)
	}

	return flattened{strings.Join(stmts, "\n"), lifted, reason}
}

// invert renders `if C {B} else {F}` as `if !C {F}` followed by B, which is the
// move that actually removes a level of nesting. ok is false when the move is
// unavailable or not worth making; why is empty in the latter case, since
// preferring elision is a choice rather than a refusal.
func (w *walker) invert(link *ast.IfStmt, final *ast.BlockStmt, d dest, read map[string]bool) (flattened, bool, string) {
	// Inverting the condition leaves the header's declarations with nowhere to
	// go: they cannot stay (the branch they belong to is now the else) and
	// hoisting them is another tool's job.
	if link.Init != nil {
		return flattened{}, false, fmt.Sprintf(
			"the if header declares %s, so its condition cannot be inverted; run fix_if_init_assignments first",
			nameList(stmtfacts.DeclaredNames([]ast.Stmt{link.Init})))
	}

	// If the else can fall out of its own block, the statements dedented after
	// it would start running when it finished.
	if !stmtfacts.Terminates(w.pass.TypesInfo, final) {
		return flattened{}, false, "the final else branch does not hand control away, so the branches cannot be swapped"
	}

	why := w.dedentBlocked(link.Body, d, read)
	if why != "" {
		return flattened{}, false, why
	}

	// When the body already hands control away, elision is available too, and
	// it keeps the author's condition as written. Invert only when it buys
	// something: the shorter branch belongs in the guard.
	if stmtfacts.Terminates(w.pass.TypesInfo, link.Body) && w.blockLines(final) >= w.blockLines(link.Body) {
		return flattened{}, false, ""
	}

	inverted := srctext.InvertCondition(w.pass.Fset, w.content, w.pass.TypesInfo, link.Cond)
	if inverted == "" {
		return flattened{}, false, reasonNoText
	}

	// The else keeps its own block, so it is rendered in a scope of its own.
	// The body is dedented into the enclosing one.
	guard := w.flattenBlock(final, d.nested(final))
	body := w.flattenBlock(link.Body, d.dedented(link.Body))
	if guard.text == "" || body.text == "" {
		return flattened{}, false, reasonNoText
	}

	text := "if " + inverted + " {\n" + srctext.IndentLines(guard.text, "\t") + "\n}\n" + body.text
	return flattened{
		text:   text,
		lifted: 1 + guard.lifted + body.lifted,
		reason: firstReason(guard.reason, body.reason),
	}, true, ""
}

// elidable reports why the else after this link cannot simply be dropped, or
// "" when it can.
func (w *walker) elidable(link *ast.IfStmt) string {
	if !stmtfacts.Terminates(w.pass.TypesInfo, link.Body) {
		return "a branch does not hand control away, so the else after it cannot be dropped"
	}

	// The header's declarations are scoped to the whole if statement, so the
	// links after this one can see them. Once the else is dropped they are
	// confined to this branch.
	if link.Init != nil && link.Else != nil {
		names := stmtfacts.DeclaredNames([]ast.Stmt{link.Init})
		if stmtfacts.ReferencesAny(link.Else, names) {
			return fmt.Sprintf(
				"the if header declares %s, which a later branch uses; run fix_if_init_assignments first",
				nameList(names))
		}
	}

	return ""
}

// flattenBlock renders a block's contents, flattening any chain among its own
// statements. Everything else is carried over verbatim, so comments, blank
// lines and raw string literals survive exactly as written.
//
// Chains nested further down — inside a loop or a switch within this block —
// are left alone here. They become visible to the walker once this chain is
// gone, on a later run.
func (w *walker) flattenBlock(block *ast.BlockStmt, d dest) flattened {
	if block == nil {
		return flattened{}
	}

	start := w.offset(block.Lbrace) + 1
	end := w.offset(block.Rbrace)
	if start < 1 || end > len(w.content) || start >= end {
		return flattened{}
	}
	base := string(w.content[start:end])

	var edits []splice
	lifted := 0
	reason := ""

	for _, stmt := range block.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok || ifStmt.Else == nil {
			continue
		}

		res := w.flattenChain(ifStmt, d)
		reason = firstReason(reason, res.reason)
		if res.lifted == 0 {
			continue
		}

		indent := srctext.Indentation(w.content, w.offset(ifStmt.Pos()))
		edits = append(edits, splice{
			start: w.offset(ifStmt.Pos()),
			end:   w.offset(ifStmt.End()),
			text:  srctext.ApplyIndent(res.text, indent),
		})
		lifted += res.lifted
	}

	return flattened{srctext.Normalize(applySplices(base, start, edits)), lifted, reason}
}

// dedentBlocked reports why block's own declarations cannot be merged into the
// statement list at d, or "" when they can.
//
// Two distinct hazards live here, and conflating them is what made the old
// blanket name-collision test refuse rewrites that were perfectly safe.
//
// The first is a compile error: a short variable declaration needs at least one
// new name on its left, so `x := 1` dedented next to another `x` in the same
// list does not build. A var, const or type declaration is stricter still —
// any repeat in the same list is a redeclaration.
//
// The second is a silent change of meaning. When a dedented `:=` reuses a name
// that is merely *visible* — declared further out, or earlier in this list
// alongside a genuinely new name — it assigns to that existing variable instead
// of shadowing it. That is only observable if something outside the chain still
// reads the name, which is what read records. A name read nowhere but inside
// the chain being rewritten can be reused freely, and refusing those is what
// kept `typeValue, ok := …` from ever being flattened next to a `typeKey, ok :=
// …` that nothing else looks at.
func (w *walker) dedentBlocked(block *ast.BlockStmt, d dest, read map[string]bool) string {
	reuse := func(name string) string {
		if d.scope[name] && read[name] {
			return fmt.Sprintf(
				"dedenting the branch would reuse %q, which the surrounding code still reads", name)
		}
		return ""
	}

	for _, stmt := range block.List {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok != token.DEFINE {
				continue
			}
			names := make(map[string]bool)
			for _, lhs := range s.Lhs {
				stmtfacts.AddName(names, lhs)
			}
			if len(names) > 0 && stmtfacts.FirstClash(names, d.list) != "" && subset(names, d.list) {
				return fmt.Sprintf(
					"dedenting the branch would redeclare %q with no new variable on the left",
					stmtfacts.FirstClash(names, d.list))
			}
			for name := range names {
				why := reuse(name)
				if why != "" {
					return why
				}
			}

		case *ast.DeclStmt:
			names := stmtfacts.DeclaredNames([]ast.Stmt{s})
			clash := stmtfacts.FirstClash(names, d.list)
			if clash != "" {
				return fmt.Sprintf(
					"dedenting the branch would redeclare %q in the enclosing scope", clash)
			}
			for name := range names {
				why := reuse(name)
				if why != "" {
					return why
				}
			}
		}
	}

	return ""
}

// namesReadOutside collects the identifiers the enclosing function reads
// outside the span of n.
//
// Declarations are deliberately not reads. The name on the left of a `:=`, in a
// var or type spec, in a parameter list, or on a label, introduces a binding
// rather than consulting one, and counting those was what made this test
// useless: a dedented `typeValue, ok := …` was refused because the `ok` in the
// `typeKey, ok := …` above it looked like a use, when nothing ever read it.
//
// Reads *before* the chain count alongside reads after it. Textual order is not
// execution order — a chain inside a loop runs after the statements below it on
// the next iteration, and a closure defined earlier can be called later — so
// anything short of "read nowhere else at all" would need flow analysis to be
// sound.
func (w *walker) namesReadOutside(n ast.Node) map[string]bool {
	read := make(map[string]bool)
	if w.fnBody == nil {
		return read
	}

	var visit func(ast.Node) bool
	visit = func(node ast.Node) bool {
		if node == nil {
			return false
		}
		// The chain is being rewritten as a whole; what it says about its own
		// names is not evidence about the code around it.
		if node.Pos() >= n.Pos() && node.End() <= n.End() {
			return false
		}

		switch v := node.(type) {
		case *ast.AssignStmt:
			if v.Tok == token.DEFINE {
				for _, rhs := range v.Rhs {
					ast.Inspect(rhs, visit)
				}
				return false
			}
		case *ast.ValueSpec:
			if v.Type != nil {
				ast.Inspect(v.Type, visit)
			}
			for _, value := range v.Values {
				ast.Inspect(value, visit)
			}
			return false
		case *ast.TypeSpec:
			ast.Inspect(v.Type, visit)
			return false
		case *ast.Field:
			ast.Inspect(v.Type, visit)
			return false
		case *ast.LabeledStmt:
			ast.Inspect(v.Stmt, visit)
			return false
		case *ast.BranchStmt:
			// `break L` names a label, not a variable.
			return false
		case *ast.SelectorExpr:
			// The `Y` in `x.Y` is a field, not the variable in question.
			ast.Inspect(v.X, visit)
			return false
		case *ast.Ident:
			read[v.Name] = true
			return false
		}

		return true
	}
	ast.Inspect(w.fnBody, visit)

	return read
}

// chainOf unpacks an if statement into its links and its final else block, if
// it has one. Every link but the last is followed by an `else if`.
func chainOf(root *ast.IfStmt) ([]*ast.IfStmt, *ast.BlockStmt) {
	var links []*ast.IfStmt

	current := root
	for {
		links = append(links, current)
		switch e := current.Else.(type) {
		case *ast.IfStmt:
			current = e
		case *ast.BlockStmt:
			return links, e
		default:
			return links, nil
		}
	}
}

// linkHeader renders the `if` and everything up to the opening brace, keeping
// any init statement attached to the condition that needs it.
func (w *walker) linkHeader(link *ast.IfStmt) (string, bool) {
	cond := srctext.ExprText(w.pass.Fset, w.content, link.Cond)
	if cond == "" {
		return "", false
	}
	if link.Init == nil {
		return "if " + cond, true
	}

	init := srctext.NodeText(w.pass.Fset, w.content, link.Init)
	if init == "" {
		return "", false
	}
	return "if " + init + "; " + cond, true
}

// stmtText lifts a statement's source verbatim, normalised to start at column
// zero. The statement's own indentation is prepended first so that the shared
// prefix normalize strips is the same one every following line carries.
func (w *walker) stmtText(n ast.Node) string {
	text := srctext.NodeText(w.pass.Fset, w.content, n)
	if text == "" {
		return ""
	}
	return srctext.Normalize(srctext.Indentation(w.content, w.offset(n.Pos())) + text)
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

func (w *walker) offset(pos token.Pos) int {
	return w.pass.Fset.Position(pos).Offset
}

// --- text assembly ---

// splice replaces the file bytes in [start, end) with text.
type splice struct {
	start, end int
	text       string
}

// applySplices rewrites base, whose first byte is at file offset base0, by
// applying edits given in file offsets.
func applySplices(base string, base0 int, edits []splice) string {
	if len(edits) == 0 {
		return base
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })

	var sb strings.Builder
	cursor := 0
	for _, e := range edits {
		start, end := e.start-base0, e.end-base0
		if start < cursor || end > len(base) || start > end {
			continue
		}
		sb.WriteString(base[cursor:start])
		sb.WriteString(e.text)
		cursor = end
	}
	sb.WriteString(base[cursor:])

	return sb.String()
}

// --- small helpers ---

// subset reports whether every name in a is already in b.
func subset(a, b map[string]bool) bool {
	for name := range a {
		if !b[name] {
			return false
		}
	}
	return true
}

func union(a, b map[string]bool) map[string]bool {
	names := make(map[string]bool, len(a)+len(b))
	for n := range a {
		names[n] = true
	}
	for n := range b {
		names[n] = true
	}
	return names
}

// firstReason keeps the reason closest to the outside of the rewrite, which is
// the one describing where flattening actually stopped.
func firstReason(reasons ...string) string {
	for _, r := range reasons {
		if r != "" {
			return r
		}
	}
	return ""
}

// nameList renders a set of identifiers for a reason string, sorted so the
// message does not change between runs.
func nameList(names map[string]bool) string {
	var sorted []string
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	quoted := make([]string, len(sorted))
	for i, name := range sorted {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(quoted, ", ")
}
