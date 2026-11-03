package booleanbranch

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	"github.com/mamaar/gorefactor/pkg/analyzers/srctext"
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
	booleansRemoved  []string
	switchCases      int
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
			BooleansRemoved: r.booleansRemoved,
			SwitchCases:     r.switchCases,
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
		results = append(results, analyzeFunction(pass, funcDecl, content, cfg)...)
	}

	return results
}

func analyzeFunction(pass *analysis.Pass, fn *ast.FuncDecl, content []byte, cfg *config) []*Result {
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

		var removed []string
		var switchCases int
		// Attempt to build SuggestedFixes using detailed assign info.
		if len(content) > 0 {
			detailedAssigns := collectDetailedAssigns(pass.Fset, fn.Body, content)
			detailedGroups := make(map[string][]boolAssignDetail)
			for _, da := range detailedAssigns {
				detailedGroups[da.sourceExpr] = append(detailedGroups[da.sourceExpr], da)
			}

			detailedGroup, ok := detailedGroups[sourceExpr]
			if ok && len(detailedGroup) >= cfg.minBranches {
				varToDetail := make(map[string]boolAssignDetail, len(detailedGroup))
				detailVarSet := make(map[string]bool, len(detailedGroup))
				for _, da := range detailedGroup {
					varToDetail[da.varName] = da
					detailVarSet[da.varName] = true
				}

				chains := findIfChains(fn.Body, detailVarSet)
				diag.SuggestedFixes, removed, switchCases = buildSuggestedFixes(
					pass.Fset, content, sourceExpr, detailedGroup, chains, fn.Body,
				)
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
			booleansRemoved:  removed,
			switchCases:      switchCases,
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

// collectDetailedAssigns finds boolean comparison assignments throughout the
// function body, capturing the comparison value for fix generation. Detection
// has always walked nested blocks, so fix generation must see the same sites.
func collectDetailedAssigns(fset *token.FileSet, body *ast.BlockStmt, content []byte) []boolAssignDetail {
	var results []boolAssignDetail

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
		cmp := exprText(fset, content, binExpr.Y)

		results = append(results, boolAssignDetail{
			varName:    ident.Name,
			sourceExpr: src,
			cmpValue:   cmp,
			negated:    binExpr.Op == token.NEQ,
			assignNode: assign,
		})
		return true
	})

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

// findIfChains finds every root if/else-if chain that directly branches on
// one of the derived booleans. Separate if statements remain separate switches;
// this preserves intervening statements and comments.
func findIfChains(body *ast.BlockStmt, varSet map[string]bool) []*ast.IfStmt {
	children := make(map[*ast.IfStmt]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if condDirectlyUsesVar(ifStmt.Cond, varSet) {
			child, ok := ifStmt.Else.(*ast.IfStmt)
			if ok && condDirectlyUsesVar(child.Cond, varSet) {
				children[child] = true
			}
		}
		return true
	})

	var roots []*ast.IfStmt
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if condDirectlyUsesVar(ifStmt.Cond, varSet) && !children[ifStmt] {
			roots = append(roots, ifStmt)
		}
		return true
	})
	return roots
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

// buildSuggestedFixes creates edits for every directly supported branch. A
// derived boolean is removed only when all of its remaining uses are the
// conditions being replaced; values retained for later data flow stay intact.
func buildSuggestedFixes(
	fset *token.FileSet,
	content []byte,
	sourceExpr string,
	assigns []boolAssignDetail,
	chains []*ast.IfStmt,
	body *ast.BlockStmt,
) ([]analysis.SuggestedFix, []string, int) {
	if len(content) == 0 {
		return nil, nil, 0
	}

	var edits []analysis.TextEdit
	convertedConditions := make(map[token.Pos]bool)
	totalCases := 0
	for _, chain := range chains {
		cases := extractCases(chain, detailsByName(assigns))
		if len(cases) == 0 {
			continue
		}
		for current := chain; current != nil; {
			convertedConditions[current.Cond.Pos()] = true
			next, _ := current.Else.(*ast.IfStmt)
			current = next
		}

		start := fset.Position(chain.Pos()).Offset
		endPos := endOfIfChain(chain)
		end := fset.Position(endPos).Offset
		if start < 0 || end > len(content) || start >= end {
			return nil, nil, 0
		}

		indent := srctext.Indentation(content, start)
		edits = append(edits, analysis.TextEdit{
			Pos:     chain.Pos(),
			End:     endPos,
			NewText: []byte(buildSwitchText(fset, content, sourceExpr, cases, indent)),
		})
		totalCases += len(cases)
	}

	// Refuse a partial rewrite: leaving any derived boolean in an unsupported
	// branch condition would make the detector/fixer disagreement persist.
	branchUses := 0
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if ok && condUsesVar(ifStmt.Cond, namesOf(assigns)) {
			branchUses++
		}
		return true
	})
	if branchUses != len(convertedConditions) || totalCases == 0 {
		return nil, nil, 0
	}

	var removed []string
	for _, ba := range assigns {
		if usedOutsideConditions(body, ba, convertedConditions) {
			continue
		}
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
			return nil, nil, 0
		}

		edits = append(edits, analysis.TextEdit{
			Pos:     fset.File(ba.assignNode.Pos()).Pos(lineStart),
			End:     fset.File(ba.assignNode.End()).Pos(lineEnd),
			NewText: nil,
		})
		removed = append(removed, ba.varName)
	}

	return []analysis.SuggestedFix{
		{
			Message:   fmt.Sprintf("Replace boolean branching on %q with switch statement", sourceExpr),
			TextEdits: edits,
		},
	}, removed, totalCases
}

func detailsByName(assigns []boolAssignDetail) map[string]boolAssignDetail {
	out := make(map[string]boolAssignDetail, len(assigns))
	for _, assign := range assigns {
		out[assign.varName] = assign
	}
	return out
}

func namesOf(assigns []boolAssignDetail) map[string]bool {
	out := make(map[string]bool, len(assigns))
	for _, assign := range assigns {
		out[assign.varName] = true
	}
	return out
}

func usedOutsideConditions(body *ast.BlockStmt, assign boolAssignDetail, converted map[token.Pos]bool) bool {
	used := false
	ast.Inspect(body, func(n ast.Node) bool {
		if used {
			return false
		}
		ident, ok := n.(*ast.Ident)
		if !ok || ident.Name != assign.varName || ident.Pos() == assign.assignNode.Lhs[0].Pos() {
			return true
		}
		for condPos := range converted {
			ifStmt := findIfAt(body, condPos)
			if ifStmt != nil && ident.Pos() >= ifStmt.Cond.Pos() && ident.Pos() < ifStmt.Cond.End() {
				return true
			}
		}
		used = true
		return false
	})
	return used
}

func findIfAt(body *ast.BlockStmt, condPos token.Pos) *ast.IfStmt {
	var found *ast.IfStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if ok && ifStmt.Cond.Pos() == condPos {
			found = ifStmt
			return false
		}
		return true
	})
	return found
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
			sb.WriteString("default:\n")
		} else {
			sb.WriteString("case ")
			sb.WriteString(c.cmpValue)
			sb.WriteString(":\n")
		}

		bodyText := srctext.Normalize(srctext.BlockText(fset, content, c.body))
		if bodyText != "" {
			sb.WriteString(srctext.IndentLines(bodyText, "\t"))
			sb.WriteByte('\n')
		}
	}

	sb.WriteString("}")

	return srctext.ApplyIndent(sb.String(), indent)
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
