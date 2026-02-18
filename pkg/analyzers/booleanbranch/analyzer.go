package booleanbranch

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File             string   `json:"file"`
	Line             int      `json:"line"`
	Column           int      `json:"column"`
	Function         string   `json:"function"`
	SourceVariable   string   `json:"source_variable"`
	BooleanVariables []string `json:"boolean_variables"`
	BranchCount      int      `json:"branch_count"`
	Suggestion       string   `json:"suggestion"`
}

// FixResult holds metadata about a boolean branching fix.
type FixResult struct {
	BooleansRemoved []string `json:"booleans_removed"`
	SwitchCases     int      `json:"switch_cases"`
}

// CollectFixResults extracts fix metadata from results for MCP handlers.
func CollectFixResults(results []*Result) []*FixResult {
	var out []*FixResult
	for _, r := range results {
		out = append(out, &FixResult{
			BooleansRemoved: r.BooleanVariables,
			SwitchCases:     r.BranchCount,
		})
	}
	return out
}

// config holds analyzer configuration.
type config struct {
	minBranches int
}

// Option is a functional option for NewAnalyzer.
type Option func(*config)

// WithMinBranches sets the minimum number of boolean branches from the same
// source expression required to trigger a violation.
func WithMinBranches(n int) Option {
	return func(c *config) {
		c.minBranches = n
	}
}

// Analyzer is the default boolean branching analyzer with minBranches=2.
var Analyzer = NewAnalyzer()

// NewAnalyzer creates a new boolean branching analyzer with the given options.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := &config{minBranches: 2}
	for _, o := range opts {
		o(cfg)
	}

	return &analysis.Analyzer{
		Name:     "booleanbranch",
		Doc:      "detects intermediate boolean variables used for branching that should be switch statements instead",
		Requires: []*analysis.Analyzer{filedata.Analyzer},
		Run: func(pass *analysis.Pass) (any, error) {
			return run(pass, cfg)
		},
	}
}

func run(pass *analysis.Pass, cfg *config) ([]*Result, error) {
	fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
	var results []*Result

	for _, file := range pass.Files {
		pos := pass.Fset.Position(file.Pos())
		content := fd.Content[pos.Filename]
		results = append(results, analyzeFile(pass, file, content, cfg)...)
	}

	return results, nil
}

// boolAssign tracks a boolean comparison assignment like `x := expr == "val"`.
type boolAssign struct {
	varName    string
	sourceExpr string // text of the LHS of == or !=
	line       int
	column     int
}

// boolAssignDetail stores full detail needed for fix generation.
type boolAssignDetail struct {
	varName    string
	sourceExpr string
	cmpValue   string
	negated    bool
	assignNode *ast.AssignStmt
}

// switchCase maps a boolean variable to its switch case content.
type switchCase struct {
	cmpValue string
	negated  bool
	body     *ast.BlockStmt
}

func analyzeFile(pass *analysis.Pass, file *ast.File, content []byte, cfg *config) []*Result {
	var results []*Result

	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Body == nil {
			continue
		}
		results = append(results, analyzeFunction(pass, file, funcDecl, content, cfg)...)
	}

	return results
}

func analyzeFunction(pass *analysis.Pass, file *ast.File, fn *ast.FuncDecl, content []byte, cfg *config) []*Result {
	assigns := collectBoolAssigns(pass.Fset, fn.Body, content)

	// Group by source expression.
	groups := make(map[string][]boolAssign)
	for _, ba := range assigns {
		groups[ba.sourceExpr] = append(groups[ba.sourceExpr], ba)
	}

	var results []*Result

	for sourceExpr, group := range groups {
		if len(group) < cfg.minBranches {
			continue
		}

		varNames := make([]string, len(group))
		varSet := make(map[string]bool, len(group))
		for i, ba := range group {
			varNames[i] = ba.varName
			varSet[ba.varName] = true
		}

		if !usedInBranching(fn.Body, varSet) {
			continue
		}

		pos := pass.Fset.Position(fn.Pos())
		suggestion := fmt.Sprintf("Replace boolean variables with switch %s { ... }", sourceExpr)

		diag := analysis.Diagnostic{
			Pos:     fn.Pos(),
			End:     fn.End(),
			Message: fmt.Sprintf("boolean branching on %q should use a switch statement (%d branches)", sourceExpr, len(group)),
		}

		// Attempt to build SuggestedFixes using detailed assign info.
		if len(content) > 0 {
			detailedAssigns := collectDetailedAssigns(pass.Fset, fn.Body, content)
			detailedGroups := make(map[string][]boolAssignDetail)
			for _, da := range detailedAssigns {
				detailedGroups[da.sourceExpr] = append(detailedGroups[da.sourceExpr], da)
			}

			if detailedGroup, ok := detailedGroups[sourceExpr]; ok && len(detailedGroup) >= cfg.minBranches {
				varToDetail := make(map[string]boolAssignDetail, len(detailedGroup))
				detailVarSet := make(map[string]bool, len(detailedGroup))
				for _, da := range detailedGroup {
					varToDetail[da.varName] = da
					detailVarSet[da.varName] = true
				}

				ifStmt := findIfChain(fn.Body, detailVarSet)
				if ifStmt != nil {
					cases := extractCases(ifStmt, varToDetail)
					if len(cases) > 0 {
						fixes := buildSuggestedFixes(pass.Fset, content, sourceExpr, detailedGroup, ifStmt, cases)
						diag.SuggestedFixes = fixes
					}
				}
			}
		}

		pass.Report(diag)

		results = append(results, &Result{
			File:             pos.Filename,
			Line:             group[0].line,
			Column:           group[0].column,
			Function:         fn.Name.Name,
			SourceVariable:   sourceExpr,
			BooleanVariables: varNames,
			BranchCount:      len(group),
			Suggestion:       suggestion,
		})
	}

	return results
}

// collectBoolAssigns finds all `v := expr == val` or `v := expr != val`
// assignments in the function body.
func collectBoolAssigns(fset *token.FileSet, body *ast.BlockStmt, content []byte) []boolAssign {
	var results []boolAssign

	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
			return true
		}
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}

		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}

		binExpr, ok := assign.Rhs[0].(*ast.BinaryExpr)
		if !ok {
			return true
		}
		if binExpr.Op != token.EQL && binExpr.Op != token.NEQ {
			return true
		}

		src := exprText(fset, content, binExpr.X)
		pos := fset.Position(assign.Pos())
		results = append(results, boolAssign{
			varName:    ident.Name,
			sourceExpr: src,
			line:       pos.Line,
			column:     pos.Column,
		})

		return true
	})

	return results
}

// collectDetailedAssigns finds all boolean comparison assignments at the top
// level of the function body, capturing the comparison value for fix generation.
func collectDetailedAssigns(fset *token.FileSet, body *ast.BlockStmt, content []byte) []boolAssignDetail {
	var results []boolAssignDetail

	for _, stmt := range body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
			continue
		}
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			continue
		}

		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			continue
		}

		binExpr, ok := assign.Rhs[0].(*ast.BinaryExpr)
		if !ok {
			continue
		}
		if binExpr.Op != token.EQL && binExpr.Op != token.NEQ {
			continue
		}

		src := exprText(fset, content, binExpr.X)
		cmp := exprText(fset, content, binExpr.Y)

		results = append(results, boolAssignDetail{
			varName:    ident.Name,
			sourceExpr: src,
			cmpValue:   cmp,
			negated:    binExpr.Op == token.NEQ,
			assignNode: assign,
		})
	}

	return results
}

// usedInBranching checks whether any of the given variable names appear as
// conditions in if statements within the block.
func usedInBranching(body *ast.BlockStmt, varSet map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if condUsesVar(ifStmt.Cond, varSet) {
			found = true
			return false
		}
		return true
	})
	return found
}

// condUsesVar checks if a condition expression references any variable in varSet.
func condUsesVar(expr ast.Expr, varSet map[string]bool) bool {
	if expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return varSet[e.Name]
	case *ast.UnaryExpr:
		return condUsesVar(e.X, varSet)
	case *ast.BinaryExpr:
		return condUsesVar(e.X, varSet) || condUsesVar(e.Y, varSet)
	case *ast.ParenExpr:
		return condUsesVar(e.X, varSet)
	}
	return false
}

// findIfChain finds the if/else-if chain in the block that directly uses the
// given boolean variable names as its condition.
func findIfChain(body *ast.BlockStmt, varSet map[string]bool) *ast.IfStmt {
	for _, stmt := range body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		if condDirectlyUsesVar(ifStmt.Cond, varSet) {
			return ifStmt
		}
	}
	return nil
}

// condDirectlyUsesVar checks if a condition is directly a boolean variable reference.
func condDirectlyUsesVar(expr ast.Expr, varSet map[string]bool) bool {
	if expr == nil {
		return false
	}
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	return varSet[ident.Name]
}

// extractCases walks the if/else-if chain and maps each branch to a switch case.
func extractCases(ifStmt *ast.IfStmt, varToDetail map[string]boolAssignDetail) []switchCase {
	var cases []switchCase

	current := ifStmt
	for current != nil {
		ident, ok := current.Cond.(*ast.Ident)
		if !ok {
			return nil
		}

		detail, ok := varToDetail[ident.Name]
		if !ok {
			return nil
		}

		cases = append(cases, switchCase{
			cmpValue: detail.cmpValue,
			negated:  detail.negated,
			body:     current.Body,
		})

		if current.Else == nil {
			break
		}
		switch e := current.Else.(type) {
		case *ast.IfStmt:
			current = e
		case *ast.BlockStmt:
			cases = append(cases, switchCase{
				cmpValue: "",
				body:     e,
			})
			current = nil
		default:
			current = nil
		}
	}

	return cases
}

// buildSuggestedFixes creates the TextEdits for removing bool assignments and
// replacing the if/else-if chain with a switch statement.
func buildSuggestedFixes(
	fset *token.FileSet,
	content []byte,
	sourceExpr string,
	assigns []boolAssignDetail,
	ifStmt *ast.IfStmt,
	cases []switchCase,
) []analysis.SuggestedFix {
	if len(content) == 0 {
		return nil
	}

	var edits []analysis.TextEdit

	// 1. Remove each boolean assignment line.
	for _, ba := range assigns {
		startOffset := fset.Position(ba.assignNode.Pos()).Offset
		endOffset := fset.Position(ba.assignNode.End()).Offset

		lineStart := startOffset
		for lineStart > 0 && content[lineStart-1] != '\n' {
			lineStart--
		}
		lineEnd := endOffset
		for lineEnd < len(content) && content[lineEnd] != '\n' {
			lineEnd++
		}
		if lineEnd < len(content) {
			lineEnd++ // include the newline
		}

		if lineStart < 0 || lineEnd > len(content) || lineStart >= lineEnd {
			continue
		}

		edits = append(edits, analysis.TextEdit{
			Pos:     fset.File(ba.assignNode.Pos()).Pos(lineStart),
			End:     fset.File(ba.assignNode.End()).Pos(lineEnd),
			NewText: nil,
		})
	}

	// 2. Replace the if/else-if chain with a switch statement.
	ifStartOffset := fset.Position(ifStmt.Pos()).Offset
	ifEndPos := endOfIfChain(ifStmt)
	ifEndOffset := fset.Position(ifEndPos).Offset

	if ifStartOffset < 0 || ifEndOffset > len(content) || ifStartOffset >= ifEndOffset {
		return nil
	}

	indent := extractIndentation(content, ifStartOffset)
	switchText := buildSwitchText(fset, content, sourceExpr, cases, indent)

	edits = append(edits, analysis.TextEdit{
		Pos:     ifStmt.Pos(),
		End:     ifEndPos,
		NewText: []byte(switchText),
	})

	return []analysis.SuggestedFix{
		{
			Message:   fmt.Sprintf("Replace boolean branching on %q with switch statement", sourceExpr),
			TextEdits: edits,
		},
	}
}

// endOfIfChain returns the End position of the entire if/else-if chain.
func endOfIfChain(ifStmt *ast.IfStmt) token.Pos {
	if ifStmt.Else == nil {
		return ifStmt.End()
	}
	switch e := ifStmt.Else.(type) {
	case *ast.IfStmt:
		return endOfIfChain(e)
	case *ast.BlockStmt:
		return e.End()
	}
	return ifStmt.End()
}

// buildSwitchText generates the switch statement source text.
func buildSwitchText(fset *token.FileSet, content []byte, sourceExpr string, cases []switchCase, indent string) string {
	var sb strings.Builder
	sb.WriteString("switch ")
	sb.WriteString(sourceExpr)
	sb.WriteString(" {\n")

	for _, c := range cases {
		if c.cmpValue == "" {
			sb.WriteString(indent)
			sb.WriteString("default:\n")
		} else {
			sb.WriteString(indent)
			sb.WriteString("case ")
			sb.WriteString(c.cmpValue)
			sb.WriteString(":\n")
		}

		bodyText := blockBodyText(fset, content, c.body, indent)
		sb.WriteString(bodyText)
	}

	sb.WriteString(indent)
	sb.WriteString("}")

	return sb.String()
}

// blockBodyText extracts the statements inside a block, preserving indentation.
func blockBodyText(fset *token.FileSet, content []byte, block *ast.BlockStmt, baseIndent string) string {
	if len(content) == 0 || len(block.List) == 0 {
		return ""
	}

	firstPos := fset.Position(block.List[0].Pos()).Offset
	lastEnd := fset.Position(block.List[len(block.List)-1].End()).Offset

	if firstPos < 0 || lastEnd > len(content) || firstPos >= lastEnd {
		return ""
	}

	bodyBytes := content[firstPos:lastEnd]
	lines := bytes.Split(bodyBytes, []byte("\n"))

	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(baseIndent)
		sb.WriteByte('\t')
		sb.Write(line)
		if i < len(lines)-1 {
			sb.WriteByte('\n')
		}
	}
	sb.WriteByte('\n')

	return sb.String()
}

// exprText extracts the source text for an expression using the file set and
// raw file content.
func exprText(fset *token.FileSet, content []byte, expr ast.Expr) string {
	if len(content) == 0 {
		return ""
	}
	start := fset.Position(expr.Pos()).Offset
	end := fset.Position(expr.End()).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

// extractIndentation returns the leading whitespace of the line containing
// the given byte offset.
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
