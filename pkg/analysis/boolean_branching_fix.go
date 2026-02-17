package analysis

import (
	"bytes"
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// boolAssignDetail stores the full detail needed for fixing.
type boolAssignDetail struct {
	varName    string
	sourceExpr string // e.g. "accept"
	cmpValue   string // e.g. `"application/x-shapefile"`
	negated    bool   // true if !=
	assignNode *ast.AssignStmt
}

// switchCase maps a boolean variable to its switch case content.
type switchCase struct {
	cmpValue string
	negated  bool
	body     *ast.BlockStmt
}

// BooleanBranchingFixer fixes boolean branching violations by converting
// if/else-if chains to switch statements.
type BooleanBranchingFixer struct {
	workspace   *types.Workspace
	fileSet     *token.FileSet
	minBranches int
}

// NewBooleanBranchingFixer creates a new boolean branching fixer.
func NewBooleanBranchingFixer(ws *types.Workspace, minBranches int) *BooleanBranchingFixer {
	if minBranches <= 0 {
		minBranches = 2
	}
	return &BooleanBranchingFixer{
		workspace:   ws,
		fileSet:     ws.FileSet,
		minBranches: minBranches,
	}
}

// BooleanBranchingFixResult holds information about what was fixed.
type BooleanBranchingFixResult struct {
	BooleansRemoved []string
	SwitchCases     int
}

// Fix analyzes the workspace (or a specific package) and returns a plan to fix all violations.
func (f *BooleanBranchingFixer) Fix(pkg string) (*types.RefactoringPlan, []*BooleanBranchingFixResult, error) {
	var packages []*types.Package
	if pkg != "" {
		resolved := types.ResolvePackagePath(f.workspace, pkg)
		p, ok := f.workspace.Packages[resolved]
		if !ok {
			return &types.RefactoringPlan{}, nil, nil
		}
		packages = []*types.Package{p}
	} else {
		for _, p := range f.workspace.Packages {
			packages = append(packages, p)
		}
	}

	var allChanges []types.Change
	var allResults []*BooleanBranchingFixResult
	affectedSet := make(map[string]bool)

	for _, p := range packages {
		for _, file := range p.Files {
			changes, results := f.fixFile(file)
			allChanges = append(allChanges, changes...)
			allResults = append(allResults, results...)
			for _, c := range changes {
				affectedSet[c.File] = true
			}
		}
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

// fixFile processes a single file for boolean branching fixes.
func (f *BooleanBranchingFixer) fixFile(file *types.File) ([]types.Change, []*BooleanBranchingFixResult) {
	var changes []types.Change
	var results []*BooleanBranchingFixResult

	for _, decl := range file.AST.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Body == nil {
			continue
		}
		c, r := f.fixFunction(file, funcDecl)
		changes = append(changes, c...)
		results = append(results, r...)
	}

	return changes, results
}

// fixFunction processes a single function for boolean branching fixes.
func (f *BooleanBranchingFixer) fixFunction(file *types.File, fn *ast.FuncDecl) ([]types.Change, []*BooleanBranchingFixResult) {
	assigns := f.collectDetailedAssigns(file, fn.Body)

	// Group by source expression
	groups := make(map[string][]boolAssignDetail)
	for _, ba := range assigns {
		groups[ba.sourceExpr] = append(groups[ba.sourceExpr], ba)
	}

	var changes []types.Change
	var results []*BooleanBranchingFixResult

	for sourceExpr, group := range groups {
		if len(group) < f.minBranches {
			continue
		}

		varSet := make(map[string]bool, len(group))
		varToDetail := make(map[string]boolAssignDetail, len(group))
		for _, ba := range group {
			varSet[ba.varName] = true
			varToDetail[ba.varName] = ba
		}

		// Find the if/else-if chain using these booleans
		ifStmt := f.findIfChain(fn.Body, varSet)
		if ifStmt == nil {
			continue
		}

		// Extract switch cases from the if-chain
		cases := f.extractCases(ifStmt, varToDetail)
		if len(cases) == 0 {
			continue
		}

		// Build the changes
		c, ok := f.buildChanges(file, sourceExpr, group, ifStmt, cases)
		if !ok {
			continue
		}

		var boolNames []string
		for _, ba := range group {
			boolNames = append(boolNames, ba.varName)
		}

		changes = append(changes, c...)
		results = append(results, &BooleanBranchingFixResult{
			BooleansRemoved: boolNames,
			SwitchCases:     len(cases),
		})
	}

	return changes, results
}

// collectDetailedAssigns finds all boolean comparison assignments with full detail.
func (f *BooleanBranchingFixer) collectDetailedAssigns(file *types.File, body *ast.BlockStmt) []boolAssignDetail {
	var results []boolAssignDetail

	// Only look at top-level statements in the function body to avoid nested scopes
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

		sourceExpr := f.exprText(file, binExpr.X)
		cmpValue := f.exprText(file, binExpr.Y)

		results = append(results, boolAssignDetail{
			varName:    ident.Name,
			sourceExpr: sourceExpr,
			cmpValue:   cmpValue,
			negated:    binExpr.Op == token.NEQ,
			assignNode: assign,
		})
	}

	return results
}

// findIfChain finds the if/else-if chain in the block that uses the given boolean variables.
func (f *BooleanBranchingFixer) findIfChain(body *ast.BlockStmt, varSet map[string]bool) *ast.IfStmt {
	for _, stmt := range body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		if f.condDirectlyUsesVar(ifStmt.Cond, varSet) {
			return ifStmt
		}
	}
	return nil
}

// condDirectlyUsesVar checks if a condition is directly a boolean variable reference.
func (f *BooleanBranchingFixer) condDirectlyUsesVar(expr ast.Expr, varSet map[string]bool) bool {
	if expr == nil {
		return false
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return varSet[ident.Name]
	}
	return false
}

// extractCases walks the if/else-if chain and maps each branch to a switch case.
func (f *BooleanBranchingFixer) extractCases(ifStmt *ast.IfStmt, varToDetail map[string]boolAssignDetail) []switchCase {
	var cases []switchCase

	current := ifStmt
	for current != nil {
		// Get the boolean variable from the condition
		ident, ok := current.Cond.(*ast.Ident)
		if !ok {
			return nil // condition not a simple ident — bail out
		}

		detail, ok := varToDetail[ident.Name]
		if !ok {
			return nil // condition uses a var we don't track — bail out
		}

		cases = append(cases, switchCase{
			cmpValue: detail.cmpValue,
			negated:  detail.negated,
			body:     current.Body,
		})

		// Follow the else-if chain
		if current.Else == nil {
			break
		}
		if nextIf, ok := current.Else.(*ast.IfStmt); ok {
			current = nextIf
		} else if block, ok := current.Else.(*ast.BlockStmt); ok {
			// Final else block becomes default case
			cases = append(cases, switchCase{
				cmpValue: "", // empty means default
				body:     block,
			})
			break
		} else {
			break
		}
	}

	return cases
}

// buildChanges creates the Change entries for a single violation.
func (f *BooleanBranchingFixer) buildChanges(
	file *types.File,
	sourceExpr string,
	assigns []boolAssignDetail,
	ifStmt *ast.IfStmt,
	cases []switchCase,
) ([]types.Change, bool) {
	content := file.OriginalContent
	if len(content) == 0 {
		return nil, false
	}

	var changes []types.Change

	// 1. Remove each boolean assignment line
	for _, ba := range assigns {
		startOffset := f.fileSet.Position(ba.assignNode.Pos()).Offset
		endOffset := f.fileSet.Position(ba.assignNode.End()).Offset

		// Extend to cover the full line (including leading indent and trailing newline)
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

		changes = append(changes, types.Change{
			File:        file.Path,
			Start:       lineStart,
			End:         lineEnd,
			OldText:     string(content[lineStart:lineEnd]),
			NewText:     "",
			Description: "Remove boolean assignment: " + ba.varName,
		})
	}

	// 2. Replace the if/else-if chain with a switch statement
	ifStart := f.fileSet.Position(ifStmt.Pos()).Offset
	ifEnd := f.endOfIfChain(ifStmt)
	ifEndOffset := f.fileSet.Position(ifEnd).Offset

	// Detect indentation from the if statement line
	indent := extractIndentation(content, ifStart)

	// Build the switch statement text
	switchText := f.buildSwitchText(file, sourceExpr, cases, indent)

	changes = append(changes, types.Change{
		File:        file.Path,
		Start:       ifStart,
		End:         ifEndOffset,
		OldText:     string(content[ifStart:ifEndOffset]),
		NewText:     switchText,
		Description: "Replace if/else-if chain with switch " + sourceExpr,
	})

	return changes, true
}

// endOfIfChain returns the End position of the entire if/else-if chain.
func (f *BooleanBranchingFixer) endOfIfChain(ifStmt *ast.IfStmt) token.Pos {
	if ifStmt.Else == nil {
		return ifStmt.End()
	}
	switch e := ifStmt.Else.(type) {
	case *ast.IfStmt:
		return f.endOfIfChain(e)
	case *ast.BlockStmt:
		return e.End()
	}
	return ifStmt.End()
}

// buildSwitchText generates the switch statement source text.
func (f *BooleanBranchingFixer) buildSwitchText(file *types.File, sourceExpr string, cases []switchCase, indent string) string {
	var sb strings.Builder
	sb.WriteString("switch ")
	sb.WriteString(sourceExpr)
	sb.WriteString(" {\n")

	for _, c := range cases {
		if c.cmpValue == "" {
			// default case
			sb.WriteString(indent)
			sb.WriteString("default:\n")
		} else {
			sb.WriteString(indent)
			sb.WriteString("case ")
			sb.WriteString(c.cmpValue)
			sb.WriteString(":\n")
		}

		// Extract the body statements (without the braces)
		bodyText := f.blockBodyText(file, c.body, indent)
		sb.WriteString(bodyText)
	}

	sb.WriteString(indent)
	sb.WriteString("}")

	return sb.String()
}

// blockBodyText extracts the statements inside a block, preserving their indentation.
func (f *BooleanBranchingFixer) blockBodyText(file *types.File, block *ast.BlockStmt, baseIndent string) string {
	content := file.OriginalContent
	if len(content) == 0 || len(block.List) == 0 {
		return ""
	}

	// Get the text from the first statement to the last statement
	firstPos := f.fileSet.Position(block.List[0].Pos()).Offset
	lastEnd := f.fileSet.Position(block.List[len(block.List)-1].End()).Offset

	if firstPos < 0 || lastEnd > len(content) || firstPos >= lastEnd {
		return ""
	}

	// Extract lines between the first and last statement
	bodyBytes := content[firstPos:lastEnd]
	lines := bytes.Split(bodyBytes, []byte("\n"))

	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(baseIndent)
		sb.WriteByte('\t')
		sb.WriteString(string(line))
		if i < len(lines)-1 {
			sb.WriteByte('\n')
		}
	}
	sb.WriteByte('\n')

	return sb.String()
}

// exprText extracts the source text for an expression.
func (f *BooleanBranchingFixer) exprText(file *types.File, expr ast.Expr) string {
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
func (f *BooleanBranchingFixer) findFile(path string) *types.File {
	for _, pkg := range f.workspace.Packages {
		if file, ok := pkg.Files[path]; ok {
			return file
		}
	}
	return nil
}
