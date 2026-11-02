// Package srctext provides helpers for lifting verbatim source text out of an
// AST node, and for the small syntactic rewrites (condition inversion) that the
// flattening analyzers share.
//
// Working from original source text rather than re-printing the AST is
// deliberate: go/printer drops or misplaces comments when printing a modified
// tree, and these analyzers rewrite code that is full of explanatory comments.
// Slicing the original bytes keeps every comment, blank line, and raw string
// literal exactly as the author wrote it.
//
// PushNeg is the shared negation-pushing engine: it applies De Morgan's laws
// and operator flips to compute the logical negation of an expression as
// source text, with the NaN-safety guard on ordering operators and the
// bookkeeping (negation count, operator precedence) a splicing caller needs
// to parenthesise its result correctly. InvertCondition is a thin wrapper
// over it for callers - the flattening analyzers earlyreturn and deepifelse -
// that only ever splice the result in as a whole `if` condition and so don't
// need that bookkeeping. Package boolsimplify builds on PushNeg too: it walks
// existing `!` operators in already-written code and decides, per negation,
// whether pushing it inward actually reduces the operator count enough to be
// worth reporting - a policy question PushNeg itself has no opinion on.
package srctext

import (
	"bytes"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// BlockText returns the source text between a block's braces, with
// whitespace-only leading and trailing lines removed.
//
// It deliberately slices from Lbrace+1 to Rbrace rather than from the first
// statement's Pos to the last statement's End: a comment before the first
// statement, a comment after the last one, and a trailing line comment on the
// final statement all live outside the statement range and would otherwise be
// silently deleted when the block is hoisted somewhere else.
//
// The returned text keeps its original indentation. Callers do not need to
// re-indent it — Serializer.ApplyChanges runs the result through
// format.Source, which regenerates all leading whitespace from the parsed
// structure.
func BlockText(fset *token.FileSet, content []byte, block *ast.BlockStmt) string {
	if len(content) == 0 || block == nil {
		return ""
	}

	start := fset.Position(block.Lbrace).Offset + 1
	end := fset.Position(block.Rbrace).Offset
	if start < 0 || end > len(content) || start >= end {
		return ""
	}

	return trimBlankEdgeLines(string(content[start:end]))
}

// NodeText returns the verbatim source text spanned by a node.
func NodeText(fset *token.FileSet, content []byte, node ast.Node) string {
	if len(content) == 0 || node == nil {
		return ""
	}
	start := fset.Position(node.Pos()).Offset
	end := fset.Position(node.End()).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

// ExprText returns the verbatim source text of an expression.
func ExprText(fset *token.FileSet, content []byte, expr ast.Expr) string {
	return NodeText(fset, content, expr)
}

// InvertCondition returns source text for the logical negation of expr. It is
// a thin wrapper over PushNeg that discards the negation-count and precedence
// bookkeeping callers of PushNeg itself (boolsimplify) need but the
// flattening analyzers do not - they always splice the result in as a whole
// `if` condition, never mid-expression, so there is nothing for them to
// parenthesise against. It returns "" when the expression's text cannot be
// recovered.
func InvertCondition(fset *token.FileSet, content []byte, info *types.Info, expr ast.Expr) string {
	text, _, _, ok := PushNeg(fset, content, info, expr)
	if !ok {
		return ""
	}
	return text
}

// PushNeg returns source text for the logical negation of e, pushing the `!`
// inward with De Morgan's laws (`!(a && b)` -> `!a || !b`) and canceling
// double negations and comparison operators along the way, rather than
// emitting a blanket `!(...)` wrapper. `!(check(a))`-style redundant
// parentheses never appear: the one case that does fall back to a bare `!`
// wrap - an operand PushNeg cannot see through, such as a call - only adds
// parentheses when the operand is itself a *ast.BinaryExpr, since that is the
// only shape where `!` would otherwise bind wrong.
//
// It also returns the number of `!` operators the resulting text contains and
// the precedence of its top-level operator (token.UnaryPrec when the result
// binds at least as tightly as a negation would - a bare identifier, a call,
// or the outcome of an operator flip). Callers that splice the result into an
// existing expression, rather than using it as a whole condition, use prec to
// decide whether the splice site needs parentheses.
//
// ok is false when the source text of some sub-expression could not be
// recovered from content. Callers must then treat the whole rewrite as
// unavailable - splicing a working expression together with a broken
// fragment is worse than leaving it alone.
//
// The ordering operators (<, >, <=, >=) are not flipped when either operand
// is a floating-point type, since flipping changes behavior for NaN operands
// (`!(x < y)` is not equivalent to `x >= y` when x or y is NaN). info drives
// this guard via IsFloatComparison; when info is nil, or the package was not
// type-checked so TypeOf returns nil for the operands, the guard cannot see
// the float type and conservatively keeps the blanket wrap rather than
// risking a NaN-unsafe flip.
func PushNeg(fset *token.FileSet, content []byte, info *types.Info, e ast.Expr) (text string, negations, prec int, ok bool) {
	switch e := e.(type) {
	case *ast.ParenExpr:
		// Parentheses are dropped here and re-added by precedence wherever the
		// result actually needs them, so pushing through them is safe.
		return PushNeg(fset, content, info, e.X)

	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			// The two negations cancel, leaving the operand as it stands.
			return negVerbatim(fset, content, ast.Unparen(e.X))
		}

	case *ast.BinaryExpr:
		switch e.Op {
		case token.LAND:
			return negBinary(fset, content, info, e, token.LOR)
		case token.LOR:
			return negBinary(fset, content, info, e, token.LAND)
		case token.EQL:
			return negFlip(fset, content, e, "!=")
		case token.NEQ:
			return negFlip(fset, content, e, "==")
		case token.LSS:
			if !IsFloatComparison(info, e) {
				return negFlip(fset, content, e, ">=")
			}
		case token.GTR:
			if !IsFloatComparison(info, e) {
				return negFlip(fset, content, e, "<=")
			}
		case token.LEQ:
			if !IsFloatComparison(info, e) {
				return negFlip(fset, content, e, ">")
			}
		case token.GEQ:
			if !IsFloatComparison(info, e) {
				return negFlip(fset, content, e, "<")
			}
		}
	}

	// Anything else - including a float ordering comparison, which falls
	// through the switch above - keeps its `!`. That leaves the negation count
	// unchanged for this operand, which is exactly what should stop a rewrite
	// that buys nothing.
	return negWrapNot(fset, content, e)
}

// negBinary negates `X op Y` by negating both operands and joining them with
// op's De Morgan dual.
func negBinary(fset *token.FileSet, content []byte, info *types.Info, e *ast.BinaryExpr, op token.Token) (string, int, int, bool) {
	lhs, lhsNeg, lhsPrec, ok := PushNeg(fset, content, info, e.X)
	if !ok {
		return "", 0, 0, false
	}
	rhs, rhsNeg, rhsPrec, ok := PushNeg(fset, content, info, e.Y)
	if !ok {
		return "", 0, 0, false
	}

	prec := op.Precedence()

	// A negated operand can come back looser than the operator now joining it:
	// negating `(!a && !b) || c` turns the left operand into `a || b` under an
	// `&&`. Equal precedence needs no parentheses - `&&` and `||` are both
	// associative and Go parses them left to right, so re-associating changes
	// neither the value nor the short-circuit order.
	if lhsPrec < prec {
		lhs = "(" + lhs + ")"
	}
	if rhsPrec < prec {
		rhs = "(" + rhs + ")"
	}

	return lhs + " " + op.String() + " " + rhs, lhsNeg + rhsNeg, prec, true
}

// negFlip negates a comparison by replacing its operator, carrying both
// operands over verbatim so their own structure is untouched. The flipped
// operator has the same precedence as the original, so the result slots into
// e's old place.
func negFlip(fset *token.FileSet, content []byte, e *ast.BinaryExpr, op string) (string, int, int, bool) {
	lhs := ExprText(fset, content, e.X)
	rhs := ExprText(fset, content, e.Y)
	if lhs == "" || rhs == "" {
		return "", 0, 0, false
	}
	return lhs + " " + op + " " + rhs, CountNegations(e), e.Op.Precedence(), true
}

// negWrapNot negates e the blunt way, by putting a `!` in front of it.
func negWrapNot(fset *token.FileSet, content []byte, e ast.Expr) (string, int, int, bool) {
	text, negations, _, ok := negVerbatim(fset, content, e)
	if !ok {
		return "", 0, 0, false
	}
	if _, isBinary := e.(*ast.BinaryExpr); isBinary {
		// `!` binds tighter than every binary operator.
		text = "(" + text + ")"
	}
	return "!" + text, negations + 1, token.UnaryPrec, true
}

// negVerbatim lifts e's source text unchanged, reporting the negations it
// already contains and how tightly its top-level operator binds.
func negVerbatim(fset *token.FileSet, content []byte, e ast.Expr) (string, int, int, bool) {
	text := ExprText(fset, content, e)
	if text == "" {
		return "", 0, 0, false
	}
	return text, CountNegations(e), exprPrec(e), true
}

// exprPrec reports how tightly e's top-level operator binds. Everything that
// is not a binary expression - an identifier, a call, an index, a
// parenthesised group - binds at least as tightly as a unary operator.
func exprPrec(e ast.Expr) int {
	if binary, ok := e.(*ast.BinaryExpr); ok {
		return binary.Op.Precedence()
	}
	return token.UnaryPrec
}

// CountNegations counts the `!` operators anywhere under e, including those
// inside operands that a rewrite would carry over verbatim. Counting the
// whole subtree on both sides is what makes a before/after comparison honest.
func CountNegations(e ast.Expr) int {
	count := 0
	ast.Inspect(e, func(n ast.Node) bool {
		if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
			count++
		}
		return true
	})
	return count
}

// IsFloatComparison reports whether either operand of e has a floating-point
// type. It returns false when info or e is nil, or when the type of an
// operand cannot be determined (info.TypeOf returns nil for packages that
// were not type-checked).
func IsFloatComparison(info *types.Info, e *ast.BinaryExpr) bool {
	if info == nil || e == nil {
		return false
	}
	return isFloatType(info.TypeOf(e.X)) || isFloatType(info.TypeOf(e.Y))
}

// isFloatType reports whether t's underlying type is a floating-point basic
// type, which covers float32, float64, and untyped float constants.
func isFloatType(t types.Type) bool {
	if t == nil {
		return false
	}
	basic, ok := t.Underlying().(*types.Basic)
	if !ok {
		return false
	}
	return basic.Info()&types.IsFloat != 0
}

// Indentation returns the leading whitespace of the line containing offset.
func Indentation(content []byte, offset int) string {
	if offset < 0 || offset > len(content) {
		return ""
	}

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

// trimBlankEdgeLines drops whitespace-only lines from both ends of text,
// leaving interior blank lines (which carry the author's grouping) intact.
func trimBlankEdgeLines(text string) string {
	lines := strings.Split(text, "\n")

	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}

	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}

	if start >= end {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// SpansRawString reports whether n contains a raw string literal that runs over
// more than one line.
//
// Flattening moves statements between nesting levels, and every level costs a
// tab. Inside a back-quoted literal that leading whitespace is not layout, it is
// the value: re-indenting `foo\n\tbar` changes what the program computes, and
// gofmt will not put it back afterwards because gofmt never touches string
// contents either. There is no correct amount of indentation to apply to such a
// chain, so it is left exactly as written.
func SpansRawString(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found {
			return false
		}
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.HasPrefix(lit.Value, "`") && strings.Contains(lit.Value, "\n") {
			found = true
			return false
		}
		return true
	})
	return found
}

// Normalize drops whitespace-only lines from both ends of text and removes the
// indentation every remaining line shares, leaving the text at column zero with
// its internal structure intact. Interior blank lines are kept: they carry the
// author's grouping.
func Normalize(text string) string {
	lines := strings.Split(text, "\n")

	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start >= end {
		return ""
	}
	lines = lines[start:end]

	prefix := ""
	first := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if first {
			prefix = indent
			first = false
			continue
		}
		prefix = commonPrefix(prefix, indent)
	}

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = strings.TrimPrefix(line, prefix)
	}

	return strings.Join(lines, "\n")
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// IndentLines prefixes every non-blank line, which is what nesting a rendered
// block one level deeper amounts to.
func IndentLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// ApplyIndent prepares rendered text for splicing into a file at a position
// that already carries indent. Every line but the first needs it added; the
// first is written where the old statement began.
func ApplyIndent(text, indent string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i == 0 {
			continue
		}
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}
