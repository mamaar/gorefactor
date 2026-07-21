// Package switchchain detects if/else-if/else chains that should be switch
// statements, and rewrites the ones it can.
//
// It is the deliberate complement of package deepifelse, which flattens a chain
// into guard clauses. Flattening is the right move only when every branch hands
// control away: that is what makes the `else` after it redundant. deepifelse
// therefore refuses a chain whose branches fall through ("a branch does not hand
// control away, so the else after it cannot be dropped"), and refuses one whose
// `if` headers declare variables ("run fix_if_init_assignments first"). Those
// two refusals cover most of the chains that were never guard clauses in the
// first place: a dispatch on one value, or a run of type assertions. Those are
// switch statements, and this analyzer converts them. A chain the flattener can
// already rewrite is not reported here at all, so the two fixers never compete
// for the same code.
//
// Converting is structurally safer than flattening, for two reasons that between
// them delete most of the machinery deepifelse needs:
//
//   - A case clause is its own implicit block, so every branch body keeps the
//     scope it already had. Nothing is dedented, so there is no name collision
//     to check, no "does the surrounding code still read this" analysis, and no
//     scope bookkeeping threaded through the walk.
//   - gofmt puts `case` labels at the switch's own indentation, so a branch body
//     ends up at exactly the depth it started at. No branch is re-indented.
//
// What is left is a question about meaning rather than layout: does the switch
// evaluate the same things, in the same order, as the chain did. Cases are tried
// top to bottom and their values left to right, which is precisely the chain's
// own short-circuit order, so the answer is yes for everything the conversion
// carries over verbatim. The only expression at risk is one the conversion
// *hoists* — the tag of a tagged switch, read once instead of once per
// comparison, and the operand of a type switch, asserted once instead of once
// per link. Those two are checked where they are known, in planTagged and
// planTypeSwitch. A tagless switch hoists nothing and needs no such check at
// all.
//
// A convertible chain nested inside a branch body is left alone: the body is
// carried over verbatim, so the inner chain survives untouched and becomes
// visible to the walker on a later run, once the outer chain is a switch. That
// keeps every conversion to a single, non-overlapping text edit.
package switchchain

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	"github.com/mamaar/gorefactor/pkg/analyzers/srctext"
	"github.com/mamaar/gorefactor/pkg/analyzers/stmtfacts"
)

// The switch forms a chain can map onto.
const (
	// KindTagged is `switch x { case k: }` — every link compares one expression
	// against a value.
	KindTagged = "tagged"
	// KindTagless is `switch { case cond: }` — the general form, which needs
	// nothing of the conditions but that they are booleans.
	KindTagless = "tagless"
	// KindTypeSwitch is `switch v := x.(type) { case T: }` — every link is a
	// comma-ok type assertion tested for success.
	KindTypeSwitch = "type_switch"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Function string `json:"function_name"`

	// Kind is the switch form this chain maps onto: "tagged", "tagless" or
	// "type_switch". It is set even when Fixable is false, so a caller can see
	// what was refused.
	Kind string `json:"kind"`
	// Branches counts the links plus the final else, i.e. how many case
	// clauses the switch would have.
	Branches   int      `json:"branches"`
	Tag        string   `json:"tag,omitempty"`
	Cases      []string `json:"cases,omitempty"`
	HasDefault bool     `json:"has_default"`

	Fixable bool   `json:"fixable"`
	Reason  string `json:"reason,omitempty"`
}

type config struct {
	minBranches int
	kinds       map[string]bool
}

// Option configures the analyzer.
type Option func(*config)

// WithMinBranches sets how many case clauses the resulting switch must have
// before a chain is worth reporting.
func WithMinBranches(n int) Option {
	return func(c *config) { c.minBranches = n }
}

// WithKinds restricts reporting to a subset of the switch forms. Passing no
// kinds leaves all three enabled.
func WithKinds(kinds ...string) Option {
	return func(c *config) {
		if len(kinds) == 0 {
			return
		}
		set := make(map[string]bool, len(kinds))
		for _, kind := range kinds {
			set[kind] = true
		}
		c.kinds = set
	}
}

// defaultConfig is a minimum of three branches, which is gocritic's ifElseChain
// threshold: `if/else if/else` and `if/else if/else if` qualify, a plain
// `if/else` does not. Two branches is an if statement that happens to be
// written with an else; turning it into a switch buys nothing.
func defaultConfig() config {
	return config{
		minBranches: 3,
		kinds: map[string]bool{
			KindTagged:     true,
			KindTagless:    true,
			KindTypeSwitch: true,
		},
	}
}

var Analyzer = &analysis.Analyzer{
	Name:     "switchchain",
	Doc:      "detects if-else chains that should be switch statements",
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
		Name:     "switchchain",
		Doc:      "detects if-else chains that should be switch statements",
		Run:      makeRun(cfg),
		Requires: []*analysis.Analyzer{filedata.Analyzer},
	}
}

// fixMessage becomes types.Change.Description, which Validator.validateCompilation
// string-matches to decide whether the new text is a standalone Go fragment
// worth parsing before it is applied. This wording deliberately avoids every
// substring on that list ("parameter", "import", "call to", "argument",
// "interface method", "qualified references", "Split", "Replace", "Wrap") so the
// emitted text really does get parsed.
const fixMessage = "Convert if-else chain to a switch statement"

// reasonNoText is reported when a piece of the chain's source text could not be
// recovered from the file content. Splicing a switch together out of a partially
// recovered chain would be worse than leaving it alone.
const reasonNoText = "source text for the chain is unavailable"

// makeRun reports one diagnostic and appends one Result per chain, in that
// order, and attaches a SuggestedFix only when the Result is Fixable.
//
// That pairing is an invariant the MCP layer depends on: it filters the results
// to their Fixable subsequence and pairs fixableResults[i] with changes[i],
// refusing to apply anything if the two counts disagree. Reporting a diagnostic
// without appending its Result, appending in a different order, or attaching a
// fix to an unfixable finding all break the alignment silently.
func makeRun(cfg config) func(*analysis.Pass) (any, error) {
	return func(pass *analysis.Pass) (any, error) {
		fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
		var results []*Result

		for _, file := range pass.Files {
			pos := pass.Fset.Position(file.Pos())
			w := &walker{pass: pass, cfg: cfg, content: fd.Content[pos.Filename], file: file}
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
	// file is the file being walked, held for its comment list: a comment
	// between the branches of a chain has nowhere to go in a switch, and the
	// only way to find one is to scan the file's comments for the chain's span.
	file    *ast.File
	results []*Result

	// reported holds the source span of every chain already reported, so a chain
	// nested inside one is never reported again. Without this a function literal
	// inside a reported chain could produce a second text edit overlapping the
	// first, which Serializer.validateChangePositions rejects outright.
	reported []span
}

type span struct{ start, end token.Pos }

func (w *walker) walkFile(file *ast.File) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		w.walkStmts(fn.Body.List, fn.Name.Name)

		// Function literals open their own function scope, so their bodies are
		// roots in their own right. The structural walk never descends into
		// expressions, so this is the only visit each literal body gets.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok || lit.Body == nil {
				return true
			}
			w.walkStmts(lit.Body.List, fn.Name.Name+".func")
			return true
		})
	}
}

// walkStmts inspects one statement list. Unlike the equivalent walk in package
// deepifelse it carries no scope: a case clause keeps its branch's braces, in
// effect, so nothing this analyzer moves changes which names are visible where.
func (w *walker) walkStmts(list []ast.Stmt, fnName string) {
	for _, stmt := range list {
		if ifStmt, ok := stmt.(*ast.IfStmt); ok && ifStmt.Else != nil {
			if w.tryChain(ifStmt, fnName) {
				// The finding covers this whole chain, and so does its edit.
				// Descending would report chains inside it a second time.
				continue
			}
		}
		w.descend(stmt, fnName)
	}
}

// descend walks the statement lists nested inside stmt. Expressions are not
// followed: a chain inside a function literal is reached by walkFile's
// ast.Inspect instead, so that it is walked as the root of its own function.
func (w *walker) descend(stmt ast.Stmt, fnName string) {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		w.walkStmts(s.Body.List, fnName)
		switch e := s.Else.(type) {
		case *ast.BlockStmt:
			w.walkStmts(e.List, fnName)
		case *ast.IfStmt:
			w.descend(e, fnName)
		}
	case *ast.ForStmt:
		w.walkStmts(s.Body.List, fnName)
	case *ast.RangeStmt:
		w.walkStmts(s.Body.List, fnName)
	case *ast.BlockStmt:
		w.walkStmts(s.List, fnName)
	case *ast.LabeledStmt:
		w.descend(s.Stmt, fnName)
	case *ast.SwitchStmt:
		w.walkClauses(s.Body, fnName)
	case *ast.TypeSwitchStmt:
		w.walkClauses(s.Body, fnName)
	case *ast.SelectStmt:
		w.walkClauses(s.Body, fnName)
	}
}

func (w *walker) walkClauses(body *ast.BlockStmt, fnName string) {
	if body == nil {
		return
	}
	for _, clause := range body.List {
		switch c := clause.(type) {
		case *ast.CaseClause:
			w.walkStmts(c.Body, fnName)
		case *ast.CommClause:
			w.walkStmts(c.Body, fnName)
		}
	}
}

// tryChain reports a chain worth converting and attaches the rewrite if one
// could be built. It returns whether the chain was reported, which is also
// whether the caller should stop descending into it.
func (w *walker) tryChain(root *ast.IfStmt, fnName string) bool {
	if w.alreadyReported(root) {
		return false
	}

	links, final := chainOf(root)
	branches := len(links)
	if final != nil {
		branches++
	}
	if branches < w.cfg.minBranches {
		return false
	}
	if w.flattenerHandlesIt(links, final) {
		return false
	}

	// Classification runs even when a hazard has already ruled the rewrite out,
	// because Kind is reported either way: a caller that sees a refused
	// type_switch learns something a bare "unfixable" would not tell it.
	kind, p, classReason := w.classify(links, final)
	if !w.cfg.kinds[kind] {
		return false
	}

	// The hazard checks describe the chain itself rather than the shape it maps
	// onto, so their explanation is the more useful one when both fire.
	reason := firstReason(w.hazards(root, links, final), classReason)

	text := ""
	if reason == "" {
		text, reason = w.render(root, links, final, p)
	}

	var fixes []analysis.SuggestedFix
	if reason == "" {
		fixes = append(fixes, analysis.SuggestedFix{
			Message: fixMessage,
			TextEdits: []analysis.TextEdit{
				{Pos: root.Pos(), End: root.End(), NewText: []byte(text)},
			},
		})
	}

	w.pass.Report(analysis.Diagnostic{
		Pos:            root.Pos(),
		End:            root.End(),
		Message:        fmt.Sprintf("if-else chain with %d branches should be a %s", branches, switchName(kind)),
		SuggestedFixes: fixes,
	})

	pos := w.pass.Fset.Position(root.Pos())
	w.results = append(w.results, &Result{
		File:       pos.Filename,
		Line:       pos.Line,
		Column:     pos.Column,
		Function:   fnName,
		Kind:       kind,
		Branches:   branches,
		Tag:        p.tag,
		Cases:      p.cases,
		HasDefault: final != nil,
		Fixable:    reason == "",
		Reason:     reason,
	})

	w.reported = append(w.reported, span{root.Pos(), root.End()})
	return true
}

func switchName(kind string) string {
	if kind == KindTypeSwitch {
		return "type switch"
	}
	return "switch statement"
}

func (w *walker) alreadyReported(n ast.Node) bool {
	for _, s := range w.reported {
		if n.Pos() >= s.start && n.End() <= s.end {
			return true
		}
	}
	return false
}

// flattenerHandlesIt reports whether fix_deep_if_else_chains (package
// deepifelse) can already rewrite this chain, in which case it is not reported
// here at all.
//
// This is the division of labour between the two fixers, and it is the single
// most important decision in this file. deepifelse elides the `else` after any
// branch that hands control away, so it rewrites exactly the chains where every
// link body terminates, the final else terminates too, and no link declares
// variables in its `if` header — its `elidable` and `invert` refuse the other
// shapes. Reporting one of those here would put two fixers on the same code,
// each producing a whole-chain edit, and whichever ran second would be rewriting
// text that no longer exists.
//
// A chain with a header declaration goes the other way: the flattener refuses it
// outright and tells the caller to run fix_if_init_assignments first, whereas a
// switch carries a first-link `init` perfectly well. So termination is not
// consulted at all once a link has an Init — that chain is ours regardless.
func (w *walker) flattenerHandlesIt(links []*ast.IfStmt, final *ast.BlockStmt) bool {
	for _, link := range links {
		if link.Init != nil {
			return false
		}
		if !stmtfacts.Terminates(w.pass.TypesInfo, link.Body) {
			return false
		}
	}
	if final != nil && !stmtfacts.Terminates(w.pass.TypesInfo, final) {
		return false
	}
	return true
}

// --- hazards ---

// hazards returns why this chain cannot become a switch whatever shape it has,
// or "" when nothing is in the way. These are the checks whose failure mode is
// silent: nothing downstream would catch a break that changed target or a
// comment that was deleted.
func (w *walker) hazards(root *ast.IfStmt, links []*ast.IfStmt, final *ast.BlockStmt) string {
	// A bare break in a branch currently leaves the enclosing for or switch.
	// Wrapping the branches in a new switch makes that switch intercept it, so
	// control resumes right after the converted chain instead. Nothing about the
	// result fails to compile, which is exactly what makes this the most
	// dangerous check in the file.
	for _, link := range links {
		if stmtfacts.BreaksOut(link.Body.List, "") {
			return "a branch breaks out of an enclosing loop or switch, which the new switch would intercept"
		}
	}
	if final != nil && stmtfacts.BreaksOut(final.List, "") {
		return "the else branch breaks out of an enclosing loop or switch, which the new switch would intercept"
	}

	// Rendering runs every branch body through srctext.Normalize, which strips
	// the indentation the lines share. An interior line of a multi-line raw
	// string sits at whatever column its value says, commonly zero, so it drags
	// that shared prefix to nothing and the rest of the body loses its layout.
	if srctext.SpansRawString(root) {
		return "the chain contains a multi-line raw string literal, whose indentation is part of its value"
	}

	if why := w.strayComment(root, links, final); why != "" {
		return why
	}

	// Deliberately absent: any rule about calls or channel receives in the
	// conditions. Evaluation count is only at stake for an expression the
	// conversion *hoists* out of the chain, and each form hoists something
	// different — the tag for a tagged switch, the asserted operand for a type
	// switch, nothing at all for a tagless one. Each of those is checked where
	// it is known, in planTagged and planTypeSwitch. A blanket scan of the
	// conditions here would refuse the two thirds of real chains whose
	// conditions merely call something they are entitled to call.
	return ""
}

// strayComment reports a comment that sits inside the chain but outside every
// branch block — the `// why` in `} else /* why */ if c {`, or a comment on an
// `if` header line. The rewrite keeps branch bodies verbatim and rebuilds
// everything around them, so such a comment would be silently deleted.
func (w *walker) strayComment(root *ast.IfStmt, links []*ast.IfStmt, final *ast.BlockStmt) string {
	if w.file == nil {
		return ""
	}

	blocks := make([]*ast.BlockStmt, 0, len(links)+1)
	for _, link := range links {
		blocks = append(blocks, link.Body)
	}
	if final != nil {
		blocks = append(blocks, final)
	}

	for _, group := range w.file.Comments {
		for _, c := range group.List {
			if c.Pos() < root.Pos() || c.End() > root.End() {
				continue
			}
			inside := false
			for _, block := range blocks {
				if c.Pos() > block.Lbrace && c.End() <= block.Rbrace {
					inside = true
					break
				}
			}
			if !inside {
				return "a comment sits between the chain's branches, where a switch has nowhere to keep it"
			}
		}
	}

	return ""
}

// hasCallOrReceive reports whether any of the nodes contains a call or a channel
// receive. A type assertion `x.(T)` is neither: it reads a value that is already
// there, so repeating or not repeating it is unobservable.
func hasCallOrReceive(nodes []ast.Node) bool {
	found := false
	for _, n := range nodes {
		if n == nil {
			continue
		}
		ast.Inspect(n, func(node ast.Node) bool {
			if found {
				return false
			}
			switch v := node.(type) {
			case *ast.CallExpr:
				found = true
			case *ast.UnaryExpr:
				if v.Op == token.ARROW {
					found = true
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// --- classification ---

// plan is everything the renderer needs: the text between `switch` and the
// opening brace, and one case expression per link.
type plan struct {
	// header is the whole tag clause, including a first-link init and its
	// semicolon. Empty for a bare `switch {`.
	header string
	// tag is just the expression being switched on, for reporting. Empty for a
	// tagless switch.
	tag   string
	cases []string
}

// classify decides which switch form the chain maps onto and builds the plan
// for it, or explains why that form's own rules rule the conversion out. The
// kind is returned even alongside a refusal.
//
// THE ORDER HERE IS LOAD-BEARING. The type switch is tried before anything else,
// and in particular before the "only the first link may have an init" refusal,
// because a type-switch chain has an init on *every* link — that is its shape.
// Applying the init rule first would refuse every type switch in existence
// before its own matcher ever ran.
func (w *walker) classify(links []*ast.IfStmt, final *ast.BlockStmt) (string, plan, string) {
	if ts, ok := w.matchTypeSwitch(links); ok {
		p, reason := w.planTypeSwitch(ts, links, final)
		return KindTypeSwitch, p, reason
	}

	tagged, isTagged := w.matchTagged(links)
	kind := KindTagless
	if isTagged {
		kind = KindTagged
	}

	// `switch init; x {` carries exactly one init statement, the chain's first.
	// A later link's init has nowhere to go — it is scoped to the rest of the
	// chain, which is now a set of sibling clauses.
	for _, link := range links[1:] {
		if link.Init != nil {
			return kind, plan{}, "a later link declares variables in its if header; run fix_if_init_assignments first"
		}
	}

	init, ok := w.initText(links[0])
	if !ok {
		return kind, plan{}, reasonNoText
	}

	if isTagged {
		p, reason := w.planTagged(tagged, init)
		return KindTagged, p, reason
	}

	p, reason := w.planTagless(links, init)
	return KindTagless, p, reason
}

// initText renders a link's init statement, without its trailing semicolon. ok
// is false only when the text could not be recovered; a link with no init
// returns "" with ok true.
func (w *walker) initText(link *ast.IfStmt) (string, bool) {
	if link.Init == nil {
		return "", true
	}
	text := srctext.NodeText(w.pass.Fset, w.content, link.Init)
	return text, text != ""
}

// --- type switch ---

// typeSwitchChain is a chain of `if v, ok := X.(T); ok` links, before any of the
// rules that decide whether they can share one guard have been applied.
type typeSwitchChain struct {
	// operands, operandNodes, bindings and asserted are parallel to the chain's
	// links.
	operands     []string
	operandNodes []ast.Node
	bindings     []string
	asserted     []ast.Expr
	okNames      map[string]bool
}

// matchTypeSwitch recognises the shape only. Everything that could still make
// the conversion wrong is decided in planTypeSwitch, so that those cases are
// reported as refusals with an explanation rather than silently falling through
// to the tagless form, which would render `case ok:` and not compile.
func (w *walker) matchTypeSwitch(links []*ast.IfStmt) (typeSwitchChain, bool) {
	chain := typeSwitchChain{okNames: make(map[string]bool)}

	for _, link := range links {
		assign, ok := link.Init.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
			return typeSwitchChain{}, false
		}
		value, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return typeSwitchChain{}, false
		}
		okIdent, ok := assign.Lhs[1].(*ast.Ident)
		if !ok {
			return typeSwitchChain{}, false
		}
		assertion, ok := assign.Rhs[0].(*ast.TypeAssertExpr)
		if !ok || assertion.Type == nil {
			return typeSwitchChain{}, false
		}
		// The condition has to be that very `ok` and nothing else. `ok && more`
		// carries a test the case clause has no room for.
		cond, ok := ast.Unparen(link.Cond).(*ast.Ident)
		if !ok || cond.Name != okIdent.Name || okIdent.Name == "_" {
			return typeSwitchChain{}, false
		}

		chain.operands = append(chain.operands, srctext.ExprText(w.pass.Fset, w.content, assertion.X))
		chain.operandNodes = append(chain.operandNodes, assertion.X)
		chain.bindings = append(chain.bindings, value.Name)
		chain.asserted = append(chain.asserted, assertion.Type)
		chain.okNames[okIdent.Name] = true
	}

	return chain, true
}

func (w *walker) planTypeSwitch(chain typeSwitchChain, links []*ast.IfStmt, final *ast.BlockStmt) (plan, string) {
	var p plan

	for _, operand := range chain.operands {
		if operand == "" {
			return p, reasonNoText
		}
		if operand != chain.operands[0] {
			return p, "the branches assert on different expressions, which a single type switch cannot do"
		}
	}
	p.tag = chain.operands[0]

	// The operand is the one expression a type switch really does hoist: the
	// chain asserted on it once per link, the guard asserts on it once. Calling
	// a function or draining a channel that many fewer times is observable, and
	// unlike the tagged form there is no pure-path requirement standing in the
	// way, since the operand can be any expression at all.
	if hasCallOrReceive(chain.operandNodes) {
		return p, "the asserted expression calls a function or receives from a channel, so a type switch would not evaluate it as often as the chain did"
	}

	// One guard binds one name. `_` in some links is fine — Go's "declared and
	// not used" rule for a type switch guard is satisfied when any one clause
	// uses the variable — but two different real names are two different
	// variables, and the clauses that used each would now read the other.
	binding := ""
	for _, name := range chain.bindings {
		if name == "_" {
			continue
		}
		if binding != "" && name != binding {
			return p, fmt.Sprintf("the branches bind the asserted value to different names (%q and %q), which a single type switch guard cannot do", binding, name)
		}
		binding = name
	}

	for i, typeExpr := range chain.asserted {
		text := srctext.ExprText(w.pass.Fset, w.content, typeExpr)
		if text == "" {
			return p, reasonNoText
		}
		for j := range i {
			if w.sameType(chain.asserted[j], typeExpr) {
				// The chain merely had a dead branch; a type switch repeating a
				// type does not compile at all.
				return p, fmt.Sprintf("two branches assert the same type %s, which a type switch cannot repeat; remove the dead branch first", text)
			}
		}
		p.cases = append(p.cases, text)
	}

	// `ok` exists only because the assertion was written as a statement. A type
	// switch has no such variable, so a body that reads one cannot be carried
	// over.
	for _, link := range links {
		if stmtfacts.ReferencesAny(link.Body, chain.okNames) {
			return p, "a branch reads the assertion's ok variable, which a type switch does not bind"
		}
	}
	if final != nil && stmtfacts.ReferencesAny(final, chain.okNames) {
		return p, "the else branch reads the assertion's ok variable, which a type switch does not bind"
	}

	if binding != "" {
		names := map[string]bool{binding: true}
		for i, link := range links {
			if chain.bindings[i] == binding {
				// The guard variable is declared in the clause's own implicit
				// block, the same one the body statements live in — not in an
				// enclosing block the way the `if` header was. A body that
				// redeclares the name compiles today and stops compiling as a
				// clause.
				if stmtfacts.DeclaredNames(link.Body.List)[binding] {
					return p, fmt.Sprintf("a branch redeclares %q, which the type switch guard already binds in that clause", binding)
				}
				continue
			}
			// This branch does not bind the name today, so whatever it reads by
			// that name comes from an enclosing scope. In a type switch every
			// clause binds it, and the read would silently change meaning.
			if stmtfacts.ReferencesAny(link.Body, names) {
				return p, fmt.Sprintf("a branch reads an outer %q, which the type switch would rebind in every clause", binding)
			}
		}
		if final != nil && stmtfacts.ReferencesAny(final, names) {
			return p, fmt.Sprintf("the else branch reads an outer %q, which the new default clause would shadow", binding)
		}
	}

	if binding == "" {
		p.header = p.tag + ".(type)"
	} else {
		p.header = binding + " := " + p.tag + ".(type)"
	}

	return p, ""
}

// sameType reports whether two asserted types are the same type. Identity comes
// from go/types where the package was type-checked, so an alias and the type it
// names are recognised as the duplicate they are; source text is the fallback
// for a package that was not.
func (w *walker) sameType(a, b ast.Expr) bool {
	info := w.pass.TypesInfo
	if info != nil && info.Types != nil {
		ta, aok := info.Types[a]
		tb, bok := info.Types[b]
		if aok && bok && ta.Type != nil && tb.Type != nil {
			return types.Identical(ta.Type, tb.Type)
		}
	}

	textA := srctext.ExprText(w.pass.Fset, w.content, a)
	textB := srctext.ExprText(w.pass.Fset, w.content, b)
	return textA != "" && textA == textB
}

// --- tagged switch ---

// taggedChain is a chain of `X == K` links sharing one X, with the case values
// each link tests, in the order the link's `||` operands wrote them.
type taggedChain struct {
	tag     string
	tagExpr ast.Expr
	values  [][]ast.Expr
}

// matchTagged recognises `X == K [|| X == K2 …]` links sharing one X.
//
// Which operand is the tag is decided by trying the first comparison's operands
// in turn, since `a == b` says nothing on its own about which side is the
// dispatch value. Only a pure path expression is considered, so the tag the
// conversion hoists is one that can be re-read without effect.
func (w *walker) matchTagged(links []*ast.IfStmt) (taggedChain, bool) {
	first := disjuncts(links[0].Cond)
	eq, ok := first[0].(*ast.BinaryExpr)
	if !ok || eq.Op != token.EQL {
		return taggedChain{}, false
	}

	for _, candidate := range []ast.Expr{eq.X, eq.Y} {
		if !isPathExpr(candidate) {
			continue
		}
		text := srctext.ExprText(w.pass.Fset, w.content, candidate)
		if text == "" {
			continue
		}
		if chain, ok := w.matchTaggedWith(links, text, candidate); ok {
			return chain, true
		}
	}

	return taggedChain{}, false
}

func (w *walker) matchTaggedWith(links []*ast.IfStmt, tag string, tagExpr ast.Expr) (taggedChain, bool) {
	chain := taggedChain{tag: tag, tagExpr: tagExpr}

	for _, link := range links {
		var values []ast.Expr
		for _, term := range disjuncts(link.Cond) {
			eq, ok := term.(*ast.BinaryExpr)
			if !ok || eq.Op != token.EQL {
				return taggedChain{}, false
			}
			switch {
			case srctext.ExprText(w.pass.Fset, w.content, eq.X) == tag:
				values = append(values, eq.Y)
			case srctext.ExprText(w.pass.Fset, w.content, eq.Y) == tag:
				values = append(values, eq.X)
			default:
				return taggedChain{}, false
			}
		}
		chain.values = append(chain.values, values)
	}

	return chain, true
}

func (w *walker) planTagged(chain taggedChain, init string) (plan, string) {
	p := plan{tag: chain.tag, header: chain.tag}
	if init != "" {
		// The init's declarations were visible across the whole chain, and in a
		// switch they are visible in every clause and in the default, which is
		// the same set of statements.
		p.header = init + "; " + chain.tag
	}

	// The tag is the one thing a tagged switch hoists: the chain read it once
	// per comparison it tried, and the switch reads it once. isPathExpr has
	// already ruled out the obvious ways that could matter, but a call can still
	// hide inside a path — `m[f()] == 1` indexes with one — so the whole tag is
	// scanned.
	//
	// The case values are pointedly not scanned. `case a, b:` then `case c:`
	// evaluates a, then b if a did not match, then c, which is exactly the order
	// and the short-circuiting of `x == a || x == b` followed by `x == c`. A
	// call among them is evaluated as often as it was before.
	if hasCallOrReceive([]ast.Node{chain.tagExpr}) {
		return p, "the switch tag calls a function or receives from a channel, so a switch would evaluate it once where the chain evaluated it once per branch"
	}

	// A switch may not list the same constant twice, where the chain was merely
	// carrying a branch that could never run. go/types resolves the value behind
	// a name, so `case "a"` next to `case b` with `const b = "a"` is caught here
	// rather than at compile time.
	//
	// Nothing can be said this way about non-constant cases or about nil: they
	// have no constant value to compare, and two of them may or may not be equal
	// at run time — which is legal for a switch anyway. The MCP fixer's go/types
	// verification pass is the backstop for whatever slips through.
	var seen []constant.Value

	for _, values := range chain.values {
		texts := make([]string, 0, len(values))
		for _, value := range values {
			text := srctext.ExprText(w.pass.Fset, w.content, value)
			if text == "" {
				return p, reasonNoText
			}
			texts = append(texts, text)

			val := w.constValue(value)
			if val == nil {
				continue
			}
			for _, prev := range seen {
				// Kinds are compared first because go/constant.Compare panics on
				// operands of different kinds.
				if prev.Kind() == val.Kind() && constant.Compare(prev, token.EQL, val) {
					return p, fmt.Sprintf("two branches test the tag against the same value %s, which a switch cannot repeat; remove the dead branch first", text)
				}
			}
			seen = append(seen, val)
		}
		p.cases = append(p.cases, strings.Join(texts, ", "))
	}

	return p, ""
}

func (w *walker) constValue(e ast.Expr) constant.Value {
	info := w.pass.TypesInfo
	if info == nil || info.Types == nil {
		return nil
	}
	tv, ok := info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() == constant.Unknown {
		return nil
	}
	return tv.Value
}

// isPathExpr reports whether e only reads a variable, a field, an element or a
// pointee — an expression whose value is what it is, with nothing happening on
// the way to it.
//
// There is deliberately no types.Comparable gate on the tag. It looks like there
// should be one, since a case expression has to be comparable with the tag, but
// the input already compiles: every case value came from an `==` against this
// very tag, so each comparison is legal by assumption, and a switch of them is
// legal for the same reason. `switch s { case nil: }` compiles for a slice, map,
// func, chan or interface tag exactly as the `s == nil` it came from does. A
// comparability check would only refuse conversions that were already sound.
func isPathExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		// `switch nil {` has no type to switch on, so nil is never a tag even
		// though `x == nil` reads like one.
		return v.Name != "nil" && v.Name != "_"
	case *ast.SelectorExpr:
		return isPathExpr(v.X)
	case *ast.IndexExpr:
		return isPathExpr(v.X)
	case *ast.StarExpr:
		return isPathExpr(v.X)
	case *ast.ParenExpr:
		return isPathExpr(v.X)
	}
	return false
}

// disjuncts flattens an `||` tree into the terms it joins, in source order,
// which is the order a switch will try them in.
func disjuncts(e ast.Expr) []ast.Expr {
	inner := ast.Unparen(e)
	if binary, ok := inner.(*ast.BinaryExpr); ok && binary.Op == token.LOR {
		return append(disjuncts(binary.X), disjuncts(binary.Y)...)
	}
	return []ast.Expr{inner}
}

// --- tagless switch ---

// planTagless is the fallback: every condition becomes a case expression of its
// own, verbatim.
//
// It has no purity rule, and that is a correctness argument rather than an
// oversight — please do not add one. A tagless switch hoists nothing. Its case
// expressions are evaluated top to bottom, one at a time, stopping at the first
// that is true, which is precisely what the chain's conditions did; the
// conversion moves no expression across a branch boundary and evaluates none of
// them a different number of times. A condition may therefore call whatever it
// likes, block on a channel, or mutate what the next condition reads, and the
// switch will do the same things in the same order. A gate here is pure loss:
// measured over x/tools it refused roughly two thirds of all convertible
// chains, every one of them soundly convertible.
func (w *walker) planTagless(links []*ast.IfStmt, init string) (plan, string) {
	var p plan
	if init != "" {
		p.header = init + ";"
	}

	for _, link := range links {
		text := srctext.ExprText(w.pass.Fset, w.content, link.Cond)
		if text == "" {
			return p, reasonNoText
		}
		p.cases = append(p.cases, text)
	}

	return p, ""
}

// --- rendering ---

// render assembles the switch and prepares it for splicing over the chain.
//
// Case labels are written at column zero and each body is normalised to column
// zero and indented one level, which is the layout gofmt produces: a `case` sits
// at the switch's own indentation, so a body one tab deeper than the label lands
// at precisely the depth it had inside the `if`. ApplyIndent then adds the
// chain's own indentation to every line but the first, which is written where
// the `if` keyword already sits.
func (w *walker) render(root *ast.IfStmt, links []*ast.IfStmt, final *ast.BlockStmt, p plan) (string, string) {
	// Establishes that the file content is really available before an empty
	// body below is read as an empty branch rather than as missing text.
	if srctext.NodeText(w.pass.Fset, w.content, root) == "" {
		return "", reasonNoText
	}
	if len(p.cases) != len(links) {
		return "", reasonNoText
	}

	var sb strings.Builder
	sb.WriteString("switch ")
	if p.header != "" {
		sb.WriteString(wrapIndent(p.header) + " ")
	}
	sb.WriteString("{\n")

	for i, link := range links {
		sb.WriteString("case " + wrapIndent(p.cases[i]) + ":\n")
		body, ok := w.branchText(link.Body)
		if !ok {
			return "", reasonNoText
		}
		if body != "" {
			sb.WriteString(body + "\n")
		}
	}

	if final != nil {
		sb.WriteString("default:\n")
		body, ok := w.branchText(final)
		if !ok {
			return "", reasonNoText
		}
		if body != "" {
			sb.WriteString(body + "\n")
		}
	}

	sb.WriteString("}")

	indent := srctext.Indentation(w.content, w.offset(root.Pos()))
	return srctext.ApplyIndent(sb.String(), indent), ""
}

// wrapIndent re-indents the continuation lines of an expression that was
// written across several lines.
//
// A condition like
//
//	if strings.Contains(msg, "a") ||
//		strings.Contains(msg, "b") {
//
// carries its second line at one tab past the `if`. Lifted verbatim onto a
// `case` label, which sits at the switch's own column rather than one level in,
// that tab is measured from the wrong place, and ApplyIndent then adds the
// chain's indentation on top — leaving the continuation two levels too deep.
// gofmt puts it exactly one tab past its case, so that is where it goes. The
// lines' shared prefix is stripped before the tab is added, which preserves the
// relative shape of a condition wrapped more than once.
func wrapIndent(text string) string {
	first, rest, wrapped := strings.Cut(text, "\n")
	if !wrapped {
		return text
	}
	return first + "\n" + srctext.IndentLines(srctext.Normalize(rest), "\t")
}

// branchText renders one branch body as the contents of a case clause. ok is
// false when text was expected and could not be recovered; an empty string with
// ok true is an empty branch, which renders as a bare `case x:`.
func (w *walker) branchText(block *ast.BlockStmt) (string, bool) {
	raw := srctext.BlockText(w.pass.Fset, w.content, block)

	// Whatever follows the opening brace on its own line — `{ // why` — starts
	// at the column the brace happened to leave it at, not at the body's
	// indentation. Normalize strips the prefix its lines share, so that one
	// stray column drags the shared prefix to nothing and the entire body keeps
	// indentation it should have lost. Putting that first line where the body
	// sits, one level in from the closing brace, is what the rest of the lines
	// already say.
	if tail := w.braceLineTail(block); strings.TrimSpace(tail) != "" {
		lines := strings.SplitN(raw, "\n", 2)
		indent := srctext.Indentation(w.content, w.offset(block.Rbrace)) + "\t"
		lines[0] = indent + strings.TrimLeft(lines[0], " \t")
		raw = strings.Join(lines, "\n")
	}

	text := srctext.Normalize(raw)
	if text == "" {
		return "", len(block.List) == 0
	}
	return srctext.IndentLines(text, "\t"), true
}

// braceLineTail returns whatever sits between a block's opening brace and the
// end of that line.
func (w *walker) braceLineTail(block *ast.BlockStmt) string {
	start := w.offset(block.Lbrace) + 1
	if start < 1 || start > len(w.content) {
		return ""
	}
	rest := w.content[start:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	return string(rest)
}

func (w *walker) offset(pos token.Pos) int {
	return w.pass.Fset.Position(pos).Offset
}

// --- small helpers ---

// chainOf unpacks an if statement into its links and its final else block, if it
// has one. Every link but the last is followed by an `else if`.
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

// firstReason returns the first non-empty reason, which is the explanation the
// caller is meant to act on.
func firstReason(reasons ...string) string {
	for _, r := range reasons {
		if r != "" {
			return r
		}
	}
	return ""
}
