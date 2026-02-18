package analysis

import (
	"bytes"
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// guardClause represents one extracted guard: `if <invertedCond> { <body> }`.
type guardClause struct {
	invertedCond string
	body         string // body text (the else-branch content)
}

// DeepIfElseFixResult holds information about what was fixed per violation.
type DeepIfElseFixResult struct {
	Function           string
	NestingDepthBefore int
	NestingDepthAfter  int
	EarlyReturnsAdded  int
}

// DeepIfElseFixer fixes deep if-else chains by converting them to early returns.
type DeepIfElseFixer struct {
	workspace    *types.Workspace
	fileSet      *token.FileSet
	maxNesting   int
	minElseLines int
}

// NewDeepIfElseFixer creates a new deep if-else fixer.
func NewDeepIfElseFixer(ws *types.Workspace, maxNesting, minElseLines int) *DeepIfElseFixer {
	if maxNesting < 0 {
		maxNesting = 2
	}
	if minElseLines <= 0 {
		minElseLines = 3
	}
	return &DeepIfElseFixer{
		workspace:    ws,
		fileSet:      ws.FileSet,
		maxNesting:   maxNesting,
		minElseLines: minElseLines,
	}
}

// Fix analyzes and returns a plan to fix all deep if-else violations.
func (f *DeepIfElseFixer) Fix(pkg string) (*types.RefactoringPlan, []*DeepIfElseFixResult, error) {
	// Use the analyzer to find violations
	analyzer := NewDeepIfElseAnalyzer(f.workspace, f.maxNesting, f.minElseLines)

	var violations []*DeepIfElseViolation
	if pkg != "" {
		resolved := types.ResolvePackagePath(f.workspace, pkg)
		p, ok := f.workspace.Packages[resolved]
		if !ok {
			return &types.RefactoringPlan{}, nil, nil
		}
		violations = analyzer.AnalyzePackage(p)
	} else {
		violations = analyzer.AnalyzeWorkspace()
	}

	if len(violations) == 0 {
		return &types.RefactoringPlan{}, nil, nil
	}

	var allChanges []types.Change
	var allResults []*DeepIfElseFixResult
	affectedSet := make(map[string]bool)

	for _, v := range violations {
		file := f.findFile(v.File)
		if file == nil {
			continue
		}

		change, result, ok := f.buildFix(file, v)
		if !ok {
			continue
		}

		allChanges = append(allChanges, change)
		allResults = append(allResults, result)
		affectedSet[v.File] = true
	}

	var affected []string
	for path := range affectedSet {
		affected = append(affected, path)
	}

	return &types.RefactoringPlan{
		Changes:       allChanges,
		AffectedFiles: affected,
	}, allResults, nil
}

// buildFix creates a Change for a single violation.
func (f *DeepIfElseFixer) buildFix(file *types.File, v *DeepIfElseViolation) (types.Change, *DeepIfElseFixResult, bool) {
	// Find the if statement AST node at the violation location
	ifStmt := f.findIfStmtAt(file, v.Line, v.Column)
	if ifStmt == nil {
		return types.Change{}, nil, false
	}

	// Extract the chain of guard clauses and the happy path
	guards, happyPath, ok := f.extractChain(file, ifStmt)
	if !ok {
		return types.Change{}, nil, false
	}

	content := file.OriginalContent
	startOffset := f.fileSet.Position(ifStmt.Pos()).Offset
	endOffset := f.fileSet.Position(ifStmt.End()).Offset

	if startOffset < 0 || endOffset > len(content) || startOffset >= endOffset {
		return types.Change{}, nil, false
	}

	indent := extractIndentation(content, startOffset)

	// Build the replacement text
	newText := f.buildReplacementText(guards, happyPath, indent)

	return types.Change{
			File:        file.Path,
			Start:       startOffset,
			End:         endOffset,
			OldText:     string(content[startOffset:endOffset]),
			NewText:     newText,
			Description: "Flatten deep if-else chain with early returns in " + v.Function,
		}, &DeepIfElseFixResult{
			Function:           v.Function,
			NestingDepthBefore: v.NestingDepth,
			NestingDepthAfter:  0,
			EarlyReturnsAdded:  len(guards),
		}, true
}

// findIfStmtAt finds the if statement at the given line/column.
func (f *DeepIfElseFixer) findIfStmtAt(file *types.File, line, col int) *ast.IfStmt {
	var result *ast.IfStmt
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if result != nil {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		pos := f.fileSet.Position(ifStmt.Pos())
		if pos.Line == line && pos.Column == col {
			result = ifStmt
			return false
		}
		return true
	})
	return result
}

// extractChain walks the nested if-else and extracts guard clauses + happy path.
// Returns (guards, happyPathText, ok).
// Conservative: only handles simple chains where each then-body is either:
//   - A single nested if-else (continue the chain)
//   - The happy path (leaf statements)
//
// And each else branch contains a return (making it safe for early return).
func (f *DeepIfElseFixer) extractChain(file *types.File, ifStmt *ast.IfStmt) ([]guardClause, string, bool) {
	var guards []guardClause

	current := ifStmt
	for {
		if current.Else == nil {
			// No else — not the pattern we fix
			return nil, "", false
		}

		// The else branch must contain a return to be safe for early return
		elseBlock, ok := current.Else.(*ast.BlockStmt)
		if !ok {
			// else-if: not the simple nested pattern we handle conservatively
			return nil, "", false
		}
		if !f.blockHasReturn(elseBlock) {
			return nil, "", false
		}

		// Invert the condition and capture the else body as a guard
		inverted := f.invertCondition(file, current.Cond)
		if inverted == "" {
			return nil, "", false
		}

		elseBody := f.blockStatementsText(file, elseBlock)
		guards = append(guards, guardClause{
			invertedCond: inverted,
			body:         elseBody,
		})

		// Check the then-body: is it a single nested if-else, or the happy path?
		innerIf := f.singleNestedIfElse(current.Body)
		if innerIf != nil {
			// Continue unwinding the chain
			current = innerIf
			continue
		}

		// The then-body is the happy path
		happyPath := f.blockStatementsText(file, current.Body)
		return guards, happyPath, true
	}
}

// singleNestedIfElse returns the nested if-else if the block contains exactly
// one statement which is an if-else. Otherwise returns nil.
func (f *DeepIfElseFixer) singleNestedIfElse(block *ast.BlockStmt) *ast.IfStmt {
	if len(block.List) != 1 {
		return nil
	}
	ifStmt, ok := block.List[0].(*ast.IfStmt)
	if !ok || ifStmt.Else == nil {
		return nil
	}
	return ifStmt
}

// blockHasReturn checks if the block contains a return statement.
func (f *DeepIfElseFixer) blockHasReturn(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

// invertCondition returns the textual inversion of a condition expression.
func (f *DeepIfElseFixer) invertCondition(file *types.File, expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		lhs := f.exprText(file, e.X)
		rhs := f.exprText(file, e.Y)
		if lhs == "" || rhs == "" {
			return ""
		}
		switch e.Op {
		case token.EQL:
			return lhs + " != " + rhs
		case token.NEQ:
			return lhs + " == " + rhs
		case token.LSS:
			return lhs + " >= " + rhs
		case token.GTR:
			return lhs + " <= " + rhs
		case token.LEQ:
			return lhs + " > " + rhs
		case token.GEQ:
			return lhs + " < " + rhs
		case token.LAND:
			// !(a && b) = !a || !b — too complex, wrap with !()
			return "!(" + f.exprText(file, expr) + ")"
		case token.LOR:
			return "!(" + f.exprText(file, expr) + ")"
		}
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			// !x → x
			return f.exprText(file, e.X)
		}
	case *ast.Ident:
		// x → !x
		return "!" + e.Name
	case *ast.ParenExpr:
		inner := f.invertCondition(file, e.X)
		if inner == "" {
			return ""
		}
		return inner
	}

	// Fallback: negate the whole expression
	text := f.exprText(file, expr)
	if text == "" {
		return ""
	}
	return "!(" + text + ")"
}

// buildReplacementText constructs the flattened code: guard clauses + happy path.
func (f *DeepIfElseFixer) buildReplacementText(guards []guardClause, happyPath, indent string) string {
	var sb strings.Builder

	for _, g := range guards {
		sb.WriteString("if ")
		sb.WriteString(g.invertedCond)
		sb.WriteString(" {\n")

		// Write each line of the body with proper indent
		lines := strings.SplitSeq(g.body, "\n")
		for line := range lines {
			if strings.TrimSpace(line) == "" {
				sb.WriteByte('\n')
				continue
			}
			sb.WriteString(indent)
			sb.WriteByte('\t')
			sb.WriteString(strings.TrimSpace(line))
			sb.WriteByte('\n')
		}

		sb.WriteString(indent)
		sb.WriteString("}\n")
		sb.WriteString(indent)
	}

	// Append happy path
	lines := strings.Split(happyPath, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if i < len(lines)-1 {
				sb.WriteByte('\n')
			}
			continue
		}
		sb.WriteString(trimmed)
		if i < len(lines)-1 {
			sb.WriteByte('\n')
			sb.WriteString(indent)
		}
	}

	return sb.String()
}

// blockStatementsText extracts the text of all statements inside a block.
func (f *DeepIfElseFixer) blockStatementsText(file *types.File, block *ast.BlockStmt) string {
	content := file.OriginalContent
	if len(content) == 0 || len(block.List) == 0 {
		return ""
	}

	firstPos := f.fileSet.Position(block.List[0].Pos()).Offset
	lastEnd := f.fileSet.Position(block.List[len(block.List)-1].End()).Offset

	if firstPos < 0 || lastEnd > len(content) || firstPos >= lastEnd {
		return ""
	}

	return string(content[firstPos:lastEnd])
}

// exprText extracts the source text for an expression.
func (f *DeepIfElseFixer) exprText(file *types.File, expr ast.Expr) string {
	content := file.OriginalContent
	if len(content) == 0 {
		return ""
	}
	start := f.fileSet.Position(expr.Pos()).Offset
	end := f.fileSet.Position(expr.End()).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

// findFile locates a file by path across all packages.
func (f *DeepIfElseFixer) findFile(path string) *types.File {
	for _, pkg := range f.workspace.Packages {
		if file, ok := pkg.Files[path]; ok {
			return file
		}
	}
	return nil
}

// extractIndentationFromContent returns the leading whitespace of the line containing offset.
// This is a package-level helper reusing the same logic as extractIndentation.
func extractIndentationFromContent(content []byte, offset int) string {
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
