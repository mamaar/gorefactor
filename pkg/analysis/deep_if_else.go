package analysis

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/mamaar/gorefactor/pkg/types"
)

// DeepIfElseViolation represents a detected deep if-else chain that should
// use early returns (guard clauses) instead.
type DeepIfElseViolation struct {
	File                       string
	Line                       int
	Column                     int
	Function                   string
	NestingDepth               int
	HappyPathDepth             int
	ErrorBranches              int
	ComplexityReductionPercent int
	Suggestion                 string
}

// DeepIfElseAnalyzer detects nested if-else chains that should use early returns.
type DeepIfElseAnalyzer struct {
	workspace      *types.Workspace
	fileSet        *token.FileSet
	maxNesting     int
	minElseLines   int
}

// NewDeepIfElseAnalyzer creates a new deep if-else analyzer.
// maxNesting is the maximum acceptable nesting depth before reporting (default 2).
// minElseLines is the minimum number of lines in an else branch to trigger detection (default 3).
func NewDeepIfElseAnalyzer(ws *types.Workspace, maxNesting, minElseLines int) *DeepIfElseAnalyzer {
	if maxNesting < 0 {
		maxNesting = 2
	}
	if minElseLines <= 0 {
		minElseLines = 3
	}
	return &DeepIfElseAnalyzer{
		workspace:    ws,
		fileSet:      ws.FileSet,
		maxNesting:   maxNesting,
		minElseLines: minElseLines,
	}
}

// AnalyzeWorkspace analyzes all packages in the workspace.
func (a *DeepIfElseAnalyzer) AnalyzeWorkspace() []*DeepIfElseViolation {
	var results []*DeepIfElseViolation
	for _, pkg := range a.workspace.Packages {
		results = append(results, a.AnalyzePackage(pkg)...)
	}
	return results
}

// AnalyzePackage analyzes all files in a package.
func (a *DeepIfElseAnalyzer) AnalyzePackage(pkg *types.Package) []*DeepIfElseViolation {
	var results []*DeepIfElseViolation
	for _, file := range pkg.Files {
		results = append(results, a.AnalyzeFile(file)...)
	}
	return results
}

// AnalyzeFile analyzes a single file for deep if-else violations.
func (a *DeepIfElseAnalyzer) AnalyzeFile(file *types.File) []*DeepIfElseViolation {
	var results []*DeepIfElseViolation

	for _, decl := range file.AST.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Body == nil {
			continue
		}
		results = append(results, a.analyzeFunction(file, funcDecl)...)
	}

	return results
}

// analyzeFunction checks a function body for deep if-else chains.
func (a *DeepIfElseAnalyzer) analyzeFunction(file *types.File, fn *ast.FuncDecl) []*DeepIfElseViolation {
	var results []*DeepIfElseViolation

	// Walk the function body looking for if-else chains
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}

		// Only analyze if statements that have else branches
		if ifStmt.Else == nil {
			return true
		}

		// Measure the nesting depth of this if-else chain
		depth := a.measureIfElseDepth(ifStmt, 1)
		if depth <= a.maxNesting {
			return true
		}

		// Check if the else branch has significant code
		elseLines := a.countElseLines(ifStmt)
		if elseLines < a.minElseLines {
			return true
		}

		// Count error branches (else clauses that could be early returns)
		errorBranches := a.countErrorBranches(ifStmt)

		// Calculate happy path depth (how deep is the main logic buried)
		happyDepth := a.measureHappyPathDepth(ifStmt, 1)

		// Estimate complexity reduction
		reductionPercent := 0
		if depth > 0 {
			reductionPercent = ((depth - 1) * 100) / depth
		}

		pos := a.fileSet.Position(ifStmt.Pos())
		results = append(results, &DeepIfElseViolation{
			File:                       file.Path,
			Line:                       pos.Line,
			Column:                     pos.Column,
			Function:                   fn.Name.Name,
			NestingDepth:               depth,
			HappyPathDepth:             happyDepth,
			ErrorBranches:              errorBranches,
			ComplexityReductionPercent: reductionPercent,
			Suggestion:                 fmt.Sprintf("Invert conditions and use early returns for error cases (depth %d -> 0)", depth),
		})

		// Don't recurse into this if-else — we've already measured it
		return false
	})

	return results
}

// measureIfElseDepth measures the nesting depth of nested if-else chains.
// It counts depth by following the "then" branch of if statements that have else clauses,
// since the pattern `if x { if y { ... } else { ... } } else { ... }` produces deep nesting.
func (a *DeepIfElseAnalyzer) measureIfElseDepth(ifStmt *ast.IfStmt, currentDepth int) int {
	maxDepth := currentDepth

	// Check for nested if-else in the body (then branch)
	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			if innerIf.Else != nil {
				d := a.measureIfElseDepth(innerIf, currentDepth+1)
				if d > maxDepth {
					maxDepth = d
				}
			}
		}
	}

	// Check the else branch for else-if chains
	if elseIf, ok := ifStmt.Else.(*ast.IfStmt); ok {
		if elseIf.Else != nil {
			d := a.measureIfElseDepth(elseIf, currentDepth+1)
			if d > maxDepth {
				maxDepth = d
			}
		}
	} else if elseBlock, ok := ifStmt.Else.(*ast.BlockStmt); ok {
		for _, stmt := range elseBlock.List {
			if innerIf, ok := stmt.(*ast.IfStmt); ok {
				if innerIf.Else != nil {
					d := a.measureIfElseDepth(innerIf, currentDepth+1)
					if d > maxDepth {
						maxDepth = d
					}
				}
			}
		}
	}

	return maxDepth
}

// countElseLines counts the total lines in else branches of an if-else chain.
func (a *DeepIfElseAnalyzer) countElseLines(ifStmt *ast.IfStmt) int {
	if ifStmt.Else == nil {
		return 0
	}

	total := 0

	switch e := ifStmt.Else.(type) {
	case *ast.BlockStmt:
		startLine := a.fileSet.Position(e.Lbrace).Line
		endLine := a.fileSet.Position(e.Rbrace).Line
		total += endLine - startLine + 1
	case *ast.IfStmt:
		// else-if: count the if body + recurse
		startLine := a.fileSet.Position(e.Body.Lbrace).Line
		endLine := a.fileSet.Position(e.Body.Rbrace).Line
		total += endLine - startLine + 1
		total += a.countElseLines(e)
	}

	// Also count else lines from nested if-else in the then-body
	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			total += a.countElseLines(innerIf)
		}
	}

	return total
}

// countErrorBranches counts else branches that look like error returns.
func (a *DeepIfElseAnalyzer) countErrorBranches(ifStmt *ast.IfStmt) int {
	count := 0

	if ifStmt.Else != nil {
		switch e := ifStmt.Else.(type) {
		case *ast.BlockStmt:
			if a.blockLooksLikeErrorReturn(e) {
				count++
			}
		case *ast.IfStmt:
			count += a.countErrorBranches(e)
		}
	}

	// Check nested if-else in the then body
	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			count += a.countErrorBranches(innerIf)
		}
	}

	// Also check if the then-body itself looks like an error return
	// (for the inverted pattern: `if err == nil { ... } else { return err }`)
	if ifStmt.Else != nil {
		if block, ok := ifStmt.Else.(*ast.BlockStmt); ok {
			if a.blockLooksLikeErrorReturn(block) {
				// Already counted above
			}
		}
	}

	return count
}

// blockLooksLikeErrorReturn checks if a block contains a return statement
// (suggesting it's an error-handling branch).
func (a *DeepIfElseAnalyzer) blockLooksLikeErrorReturn(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

// measureHappyPathDepth measures how deep the "happy path" (main logic) is buried.
// It follows the then-branch of if-else chains to find the deepest non-error path.
func (a *DeepIfElseAnalyzer) measureHappyPathDepth(ifStmt *ast.IfStmt, currentDepth int) int {
	maxDepth := currentDepth

	// The happy path is usually in the then-branch when the else is an error return
	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			if innerIf.Else != nil {
				d := a.measureHappyPathDepth(innerIf, currentDepth+1)
				if d > maxDepth {
					maxDepth = d
				}
			}
		}
	}

	return maxDepth
}
