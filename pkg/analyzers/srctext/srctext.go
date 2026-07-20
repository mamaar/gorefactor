// Package srctext provides helpers for lifting verbatim source text out of an
// AST node, and for the small syntactic rewrites (condition inversion) that the
// flattening analyzers share.
//
// Working from original source text rather than re-printing the AST is
// deliberate: go/printer drops or misplaces comments when printing a modified
// tree, and these analyzers rewrite code that is full of explanatory comments.
// Slicing the original bytes keeps every comment, blank line, and raw string
// literal exactly as the author wrote it.
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
// re-indent it — Serializer.applyChangesToFile runs the result through
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

// InvertCondition returns source text for the logical negation of expr,
// preferring an operator flip (`a == b` -> `a != b`) over a blanket `!(...)`
// wrapper. It returns "" when the expression's text cannot be recovered.
//
// The ordering operators (<, >, <=, >=) are not flipped when either operand
// is a floating-point type, since flipping changes behavior for NaN operands
// (`!(x < y)` is not equivalent to `x >= y` when x or y is NaN). info is used
// to detect this; when info is nil, or the package was not type-checked so
// TypeOf returns nil for the operands, the guard cannot see the float type
// and this function falls back to flipping the operator exactly as before -
// callers relying on the NaN guard must supply type information.
func InvertCondition(fset *token.FileSet, content []byte, info *types.Info, expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		lhs := ExprText(fset, content, e.X)
		rhs := ExprText(fset, content, e.Y)
		if lhs == "" || rhs == "" {
			return ""
		}
		switch e.Op {
		case token.EQL:
			return lhs + " != " + rhs
		case token.NEQ:
			return lhs + " == " + rhs
		case token.LSS:
			if IsFloatComparison(info, e) {
				return "!(" + ExprText(fset, content, expr) + ")"
			}
			return lhs + " >= " + rhs
		case token.GTR:
			if IsFloatComparison(info, e) {
				return "!(" + ExprText(fset, content, expr) + ")"
			}
			return lhs + " <= " + rhs
		case token.LEQ:
			if IsFloatComparison(info, e) {
				return "!(" + ExprText(fset, content, expr) + ")"
			}
			return lhs + " > " + rhs
		case token.GEQ:
			if IsFloatComparison(info, e) {
				return "!(" + ExprText(fset, content, expr) + ")"
			}
			return lhs + " < " + rhs
		case token.LAND, token.LOR:
			return "!(" + ExprText(fset, content, expr) + ")"
		}
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return ExprText(fset, content, e.X)
		}
	case *ast.Ident:
		return "!" + e.Name
	case *ast.ParenExpr:
		inner := InvertCondition(fset, content, info, e.X)
		if inner == "" {
			return ""
		}
		return inner
	}

	text := ExprText(fset, content, expr)
	if text == "" {
		return ""
	}
	return "!(" + text + ")"
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
