package analysis

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/mamaar/gorefactor/pkg/types"
)

// BooleanBranchingViolation represents a detected boolean branching pattern
// that could be replaced with a switch statement.
type BooleanBranchingViolation struct {
	File             string
	Line             int
	Column           int
	Function         string
	SourceVariable   string
	BooleanVariables []string
	BranchCount      int
	Suggestion       string
}

// boolAssign tracks a boolean comparison assignment like `x := expr == "val"`
type boolAssign struct {
	varName    string
	sourceExpr string // text of the LHS of == or !=
	line       int
	column     int
}

// BooleanBranchingAnalyzer detects intermediate boolean variables used for
// branching that should be switch statements instead.
type BooleanBranchingAnalyzer struct {
	workspace   *types.Workspace
	fileSet     *token.FileSet
	minBranches int
}

// NewBooleanBranchingAnalyzer creates a new boolean branching analyzer.
// minBranches is the minimum number of boolean branches from the same source
// expression to trigger a violation (default 2).
func NewBooleanBranchingAnalyzer(ws *types.Workspace, minBranches int) *BooleanBranchingAnalyzer {
	if minBranches <= 0 {
		minBranches = 2
	}
	return &BooleanBranchingAnalyzer{
		workspace:   ws,
		fileSet:     ws.FileSet,
		minBranches: minBranches,
	}
}

// AnalyzeWorkspace analyzes all packages in the workspace.
func (a *BooleanBranchingAnalyzer) AnalyzeWorkspace() []*BooleanBranchingViolation {
	var results []*BooleanBranchingViolation
	for _, pkg := range a.workspace.Packages {
		results = append(results, a.AnalyzePackage(pkg)...)
	}
	return results
}

// AnalyzePackage analyzes all files in a package.
func (a *BooleanBranchingAnalyzer) AnalyzePackage(pkg *types.Package) []*BooleanBranchingViolation {
	var results []*BooleanBranchingViolation
	for _, file := range pkg.Files {
		results = append(results, a.AnalyzeFile(file)...)
	}
	return results
}

// AnalyzeFile analyzes a single file for boolean branching violations.
func (a *BooleanBranchingAnalyzer) AnalyzeFile(file *types.File) []*BooleanBranchingViolation {
	var results []*BooleanBranchingViolation

	for _, decl := range file.AST.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Body == nil {
			continue
		}
		results = append(results, a.analyzeFunction(file, funcDecl)...)
	}

	return results
}

// analyzeFunction inspects a single function body for boolean branching patterns.
func (a *BooleanBranchingAnalyzer) analyzeFunction(file *types.File, fn *ast.FuncDecl) []*BooleanBranchingViolation {
	// Collect boolean comparison assignments: `x := expr == "val"` or `x := expr != "val"`
	assigns := a.collectBoolAssigns(file, fn.Body)

	// Group by source expression
	groups := make(map[string][]boolAssign)
	for _, ba := range assigns {
		groups[ba.sourceExpr] = append(groups[ba.sourceExpr], ba)
	}

	var results []*BooleanBranchingViolation

	for sourceExpr, group := range groups {
		if len(group) < a.minBranches {
			continue
		}

		// Collect the boolean variable names
		varNames := make([]string, len(group))
		varSet := make(map[string]bool, len(group))
		for i, ba := range group {
			varNames[i] = ba.varName
			varSet[ba.varName] = true
		}

		// Check if these booleans are used in if/else-if chains
		if !a.usedInBranching(fn.Body, varSet) {
			continue
		}

		pos := a.fileSet.Position(fn.Pos())
		results = append(results, &BooleanBranchingViolation{
			File:             file.Path,
			Line:             group[0].line,
			Column:           group[0].column,
			Function:         fn.Name.Name,
			SourceVariable:   sourceExpr,
			BooleanVariables: varNames,
			BranchCount:      len(group),
			Suggestion:       fmt.Sprintf("Replace boolean variables with switch %s { ... }", sourceExpr),
		})
		_ = pos
	}

	return results
}

// collectBoolAssigns finds all `v := expr == "val"` or `v := expr != "val"` assignments
// in a block statement (non-recursively into nested blocks, but walks the full function).
func (a *BooleanBranchingAnalyzer) collectBoolAssigns(file *types.File, body *ast.BlockStmt) []boolAssign {
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

		// Extract the source expression (the non-literal side)
		sourceExpr := a.exprText(file, binExpr.X)

		pos := a.fileSet.Position(assign.Pos())
		results = append(results, boolAssign{
			varName:    ident.Name,
			sourceExpr: sourceExpr,
			line:       pos.Line,
			column:     pos.Column,
		})

		return true
	})

	return results
}

// usedInBranching checks whether any of the given variable names appear as
// conditions in if/else-if chains within the block.
func (a *BooleanBranchingAnalyzer) usedInBranching(body *ast.BlockStmt, varSet map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if a.condUsesVar(ifStmt.Cond, varSet) {
			found = true
			return false
		}
		return true
	})
	return found
}

// condUsesVar checks if a condition expression references any variable in varSet.
func (a *BooleanBranchingAnalyzer) condUsesVar(expr ast.Expr, varSet map[string]bool) bool {
	if expr == nil {
		return false
	}

	switch e := expr.(type) {
	case *ast.Ident:
		return varSet[e.Name]
	case *ast.UnaryExpr:
		return a.condUsesVar(e.X, varSet)
	case *ast.BinaryExpr:
		return a.condUsesVar(e.X, varSet) || a.condUsesVar(e.Y, varSet)
	case *ast.ParenExpr:
		return a.condUsesVar(e.X, varSet)
	}
	return false
}

// exprText extracts the source text for an expression.
func (a *BooleanBranchingAnalyzer) exprText(file *types.File, expr ast.Expr) string {
	content := file.OriginalContent
	if len(content) == 0 {
		return ""
	}
	start := a.fileSet.Position(expr.Pos()).Offset
	end := a.fileSet.Position(expr.End()).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}
