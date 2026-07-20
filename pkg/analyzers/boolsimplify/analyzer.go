// Package boolsimplify walks the `!` operators already written in source and
// removes the ones srctext.PushNeg can push inward with De Morgan's laws,
// where doing so strictly reduces the number of `!` operators.
//
// The negation-pushing engine itself - De Morgan distribution, operator
// flips, double-negation cancellation, and the NaN-safety guard on ordering
// operators - lives in srctext.PushNeg, shared with the flattening analyzers
// that invert conditions (earlyreturn, deepifelse, via
// srctext.InvertCondition). This package supplies what PushNeg deliberately
// leaves out: the walk over existing negations, the "strictly fewer
// negations" policy gate, splice-site parenthesisation for a result landing
// mid-expression rather than as a whole condition, and the diagnostic/
// SuggestedFix plumbing.
//
// The "strictly fewer negations" rule is what makes the pass objective: there
// is no taste call about whether `!(a && b)` reads better as `!a || !b`, only
// an operator count that either drops or does not. It also bounds repetition —
// every fix lowers the file's total negation count, so repeated runs converge.
// A single pass is not always a fixed point: a reducible negation buried in an
// operand carried over verbatim (a call argument, say) is only reached on the
// next run, because rewriting it in the same pass would mean emitting a text
// edit nested inside another one.
//
// De Morgan preserves evaluation order, so no side-effect analysis is needed:
// `!(a && b)` becomes `!a || !b`, which still evaluates a first and b only
// when a is true. NaN is the one semantic trap, and it is handled by refusing
// to flip ordering operators on floating-point operands.
package boolsimplify

import (
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	"github.com/mamaar/gorefactor/pkg/analyzers/srctext"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Function string `json:"function_name"`
	// Before is the verbatim source of the negation that was matched; After is
	// the text that replaces it, parenthesised for its surrounding context.
	Before          string `json:"before"`
	After           string `json:"after"`
	NegationsBefore int    `json:"negations_before"`
	NegationsAfter  int    `json:"negations_after"`
}

type config struct {
	minReduction int
}

func defaultConfig() config {
	return config{minReduction: 1}
}

// Option configures the analyzer.
type Option func(*config)

// WithMinReduction sets how many `!` operators a rewrite must remove before it
// is worth reporting. Values below 1 are clamped: a rewrite that removes no
// negation would leave the pass with work to do on its own output.
func WithMinReduction(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.minReduction = n
	}
}

const doc = "removes negations that De Morgan's laws can push inwards, where that leaves fewer negations"

// Analyzer is the default-configured analyzer.
var Analyzer = &analysis.Analyzer{
	Name:     "boolsimplify",
	Doc:      doc,
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
		Name:     "boolsimplify",
		Doc:      doc,
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

type walker struct {
	pass    *analysis.Pass
	cfg     config
	content []byte
	results []*Result
}

func (w *walker) walkFile(file *ast.File) {
	for _, decl := range file.Decls {
		name := ""
		if fn, ok := decl.(*ast.FuncDecl); ok {
			name = fn.Name.Name
		}
		w.walkNode(decl, name)
	}
}

// walkNode visits every negation under root, outermost first, tracking the
// chain of ancestors so a rewrite can be parenthesised for the context it
// lands in.
func (w *walker) walkNode(root ast.Node, fnName string) {
	var stack []ast.Node

	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)

		unary, ok := n.(*ast.UnaryExpr)
		if !ok || unary.Op != token.NOT {
			return true
		}
		if !w.trySimplify(unary, stack, fnName) {
			return true
		}

		// A fix covering this whole negation was emitted, and its rewrite
		// already accounts for every negation inside it. Descending would
		// produce a text edit nested in this one, which the change-overlap
		// validator rejects outright. ast.Inspect does not call back with nil
		// for a node whose children are skipped, so pop the stack by hand.
		stack = stack[:len(stack)-1]
		return false
	})
}

// trySimplify reports whether a fix was emitted for unary.
func (w *walker) trySimplify(unary *ast.UnaryExpr, stack []ast.Node, fnName string) bool {
	before := srctext.CountNegations(unary)

	text, after, prec, ok := srctext.PushNeg(w.pass.Fset, w.content, w.pass.TypesInfo, unary.X)
	if !ok || before-after < w.cfg.minReduction {
		return false
	}

	original := srctext.ExprText(w.pass.Fset, w.content, unary)
	if original == "" {
		return false
	}

	// Parentheses cost nothing in the negation count, so adding them here
	// cannot invalidate the reduction test above.
	if needsParens(prec, stack) {
		text = "(" + text + ")"
	}

	pos := w.pass.Fset.Position(unary.Pos())
	w.results = append(w.results, &Result{
		File:            pos.Filename,
		Line:            pos.Line,
		Column:          pos.Column,
		Function:        fnName,
		Before:          original,
		After:           text,
		NegationsBefore: before,
		NegationsAfter:  after,
	})

	w.pass.Report(analysis.Diagnostic{
		Pos:     unary.Pos(),
		End:     unary.End(),
		Message: fixMessage,
		SuggestedFixes: []analysis.SuggestedFix{{
			Message: fixMessage,
			TextEdits: []analysis.TextEdit{
				{Pos: unary.Pos(), End: unary.End(), NewText: []byte(text)},
			},
		}},
	})
	return true
}

// fixMessage becomes the Change.Description carried through to the validator.
// The replacement is a bare expression rather than a standalone statement, so
// the wording deliberately contains "Replace", which is one of the substrings
// Validator.validateCompilation treats as marking a code fragment and skips
// syntax checking for.
const fixMessage = "Replace negation with an equivalent expression using fewer negations"

// needsParens reports whether a replacement whose top-level operator has the
// given precedence must be parenthesised to keep its meaning where the node on
// top of stack currently sits.
//
// The replacement text is spliced in place of a `!x`, which binds as tightly as
// anything can, so a looser result silently rebinds without this: splicing
// `a || b` bare into `c && !(...)` yields `c && a || b`, which parses as
// `(c && a) || b`. Contexts bounded by a delimiter — a call argument, a return
// value, an if condition, an assignment right-hand side — take the default and
// get no parentheses, since gofmt never removes redundant ones.
func needsParens(prec int, stack []ast.Node) bool {
	if len(stack) < 2 {
		return true
	}

	child := stack[len(stack)-1]

	switch parent := stack[len(stack)-2].(type) {
	case *ast.BinaryExpr:
		parentPrec := parent.Op.Precedence()
		if prec != parentPrec {
			return prec < parentPrec
		}
		// `&&` and `||` each own a precedence level outright, so equal
		// precedence here means the same operator on both sides. Both are
		// associative and Go parses them left to right, so re-associating
		// changes neither the value nor the evaluation order.
		if prec == token.LAND.Precedence() || prec == token.LOR.Precedence() {
			return false
		}
		// The comparison operators share a level with each other, and are
		// left-associative: an equally binding left operand needs no
		// parentheses, but a right one does, since `x == a >= b` means
		// `(x == a) >= b` rather than `x == (a >= b)`.
		return parent.Y == child

	case *ast.UnaryExpr, *ast.StarExpr:
		return prec < token.UnaryPrec

	case *ast.SelectorExpr, *ast.TypeAssertExpr:
		return true

	case *ast.CallExpr:
		return parent.Fun == child

	case *ast.IndexExpr:
		return parent.X == child

	case *ast.IndexListExpr:
		return parent.X == child

	case *ast.SliceExpr:
		return parent.X == child
	}

	return false
}
