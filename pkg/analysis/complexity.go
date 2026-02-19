package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
	"golang.org/x/tools/go/ast/inspector"
)

// ComplexityMetrics holds complexity analysis results
type ComplexityMetrics struct {
	CyclomaticComplexity int
	CognitiveComplexity  int
	LinesOfCode          int
	Parameters           int
	LocalVariables       int
	NestedBlocks         int
	MaxNestingDepth      int
}

// ComplexityResult represents complexity analysis for a function
type ComplexityResult struct {
	Function *types.Symbol
	Metrics  *ComplexityMetrics
	Position token.Position
}

// ComplexityAnalyzer analyzes code complexity
type ComplexityAnalyzer struct {
	workspace     *types.Workspace
	fileSet       *token.FileSet
	minComplexity int // Only report functions above this threshold
}

func NewComplexityAnalyzer(ws *types.Workspace, minComplexity int) *ComplexityAnalyzer {
	if minComplexity <= 0 {
		minComplexity = 10 // Default threshold
	}

	return &ComplexityAnalyzer{
		workspace:     ws,
		fileSet:       ws.FileSet,
		minComplexity: minComplexity,
	}
}

// AnalyzeWorkspace analyzes complexity for all functions in the workspace
func (ca *ComplexityAnalyzer) AnalyzeWorkspace() ([]*ComplexityResult, error) {
	var results []*ComplexityResult

	for _, pkg := range ca.workspace.Packages {
		pkgResults, err := ca.AnalyzePackage(pkg)
		if err != nil {
			return nil, err
		}
		results = append(results, pkgResults...)
	}

	// Sort by complexity (highest first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Metrics.CyclomaticComplexity > results[j].Metrics.CyclomaticComplexity
	})

	return results, nil
}

// AnalyzePackage analyzes complexity for all functions in a package
func (ca *ComplexityAnalyzer) AnalyzePackage(pkg *types.Package) ([]*ComplexityResult, error) {
	var results []*ComplexityResult

	for _, file := range pkg.Files {
		fileResults, err := ca.AnalyzeFile(file)
		if err != nil {
			return nil, err
		}
		results = append(results, fileResults...)
	}

	return results, nil
}

// AnalyzeFile analyzes complexity for all functions in a file
func (ca *ComplexityAnalyzer) AnalyzeFile(file *types.File) ([]*ComplexityResult, error) {
	var results []*ComplexityResult

	ins := inspector.New([]*ast.File{file.AST})
	for cur := range ins.Root().Preorder((*ast.FuncDecl)(nil)) {
		funcDecl := cur.Node().(*ast.FuncDecl)
		if funcDecl.Body != nil {
			result := ca.AnalyzeFunction(funcDecl, file)
			if result.Metrics.CyclomaticComplexity >= ca.minComplexity {
				results = append(results, result)
			}
		}
	}

	return results, nil
}

// AnalyzeFunction analyzes complexity of a single function
func (ca *ComplexityAnalyzer) AnalyzeFunction(funcDecl *ast.FuncDecl, file *types.File) *ComplexityResult {
	pos := ca.fileSet.Position(funcDecl.Pos())

	// Create symbol for the function
	symbol := &types.Symbol{
		Name:     funcDecl.Name.Name,
		Package:  getPackageIdentifier(file.Package),
		File:     file.Path,
		Position: funcDecl.Name.Pos(),
		End:      funcDecl.End(),
		Line:     pos.Line,
		Column:   pos.Column,
		Kind:     types.FunctionSymbol,
	}

	if funcDecl.Recv != nil {
		symbol.Kind = types.MethodSymbol
	}

	metrics := ca.calculateMetrics(funcDecl)

	return &ComplexityResult{
		Function: symbol,
		Metrics:  metrics,
		Position: pos,
	}
}

// calculateMetrics calculates all complexity metrics for a function
func (ca *ComplexityAnalyzer) calculateMetrics(funcDecl *ast.FuncDecl) *ComplexityMetrics {
	metrics := &ComplexityMetrics{
		CyclomaticComplexity: 1, // Base complexity
		CognitiveComplexity:  0,
		LinesOfCode:          0,
		Parameters:           0,
		LocalVariables:       0,
		NestedBlocks:         0,
		MaxNestingDepth:      0,
	}

	// Count parameters
	if funcDecl.Type.Params != nil {
		for _, field := range funcDecl.Type.Params.List {
			metrics.Parameters += len(field.Names)
		}
	}

	if funcDecl.Body != nil {
		// Calculate line count
		startPos := ca.fileSet.Position(funcDecl.Body.Lbrace)
		endPos := ca.fileSet.Position(funcDecl.Body.Rbrace)
		metrics.LinesOfCode = endPos.Line - startPos.Line + 1

		// Calculate complexity metrics
		ca.analyzeBlockComplexity(funcDecl.Body, metrics, 0)
	}

	return metrics
}

// analyzeBlockComplexity recursively analyzes complexity within a block,
// properly tracking nesting depth by recursing into scope-introducing nodes
// with an incremented depth.
func (ca *ComplexityAnalyzer) analyzeBlockComplexity(node ast.Node, metrics *ComplexityMetrics, nestingLevel int) {
	if nestingLevel > metrics.MaxNestingDepth {
		metrics.MaxNestingDepth = nestingLevel
	}

	ca.walkWithDepth(node, metrics, nestingLevel)
}

// walkWithDepth walks the AST rooted at node, tracking nesting depth explicitly.
// Scope-introducing nodes (if, for, range, switch, typeswitch, select) recurse
// into their children with depth+1.
func (ca *ComplexityAnalyzer) walkWithDepth(node ast.Node, metrics *ComplexityMetrics, depth int) {
	if node == nil {
		return
	}

	switch stmt := node.(type) {
	case *ast.IfStmt:
		metrics.CyclomaticComplexity++
		metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
		if stmt.Else != nil {
			if _, ok := stmt.Else.(*ast.IfStmt); !ok {
				metrics.CyclomaticComplexity++
			}
		}
		// Recurse into init, cond (expression children are handled by default walk),
		// body and else at depth+1
		if stmt.Init != nil {
			ca.walkWithDepth(stmt.Init, metrics, depth+1)
		}
		ca.walkWithDepth(stmt.Body, metrics, depth+1)
		if stmt.Else != nil {
			// "else if" stays at same depth, plain "else" at depth+1
			if _, ok := stmt.Else.(*ast.IfStmt); ok {
				ca.walkWithDepth(stmt.Else, metrics, depth)
			} else {
				ca.walkWithDepth(stmt.Else, metrics, depth+1)
			}
		}

	case *ast.ForStmt:
		metrics.CyclomaticComplexity++
		metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
		ca.walkWithDepth(stmt.Body, metrics, depth+1)

	case *ast.RangeStmt:
		metrics.CyclomaticComplexity++
		metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
		ca.walkWithDepth(stmt.Body, metrics, depth+1)

	case *ast.SwitchStmt:
		if stmt.Body != nil {
			caseCount := ca.countSwitchCases(stmt.Body)
			metrics.CyclomaticComplexity += caseCount
			metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
			// Walk case clauses at depth+1
			for _, s := range stmt.Body.List {
				if cc, ok := s.(*ast.CaseClause); ok {
					for _, child := range cc.Body {
						ca.walkWithDepth(child, metrics, depth+1)
					}
				}
			}
		}

	case *ast.TypeSwitchStmt:
		if stmt.Body != nil {
			caseCount := ca.countSwitchCases(stmt.Body)
			metrics.CyclomaticComplexity += caseCount
			metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
			for _, s := range stmt.Body.List {
				if cc, ok := s.(*ast.CaseClause); ok {
					for _, child := range cc.Body {
						ca.walkWithDepth(child, metrics, depth+1)
					}
				}
			}
		}

	case *ast.SelectStmt:
		if stmt.Body != nil {
			caseCount := ca.countSelectCases(stmt.Body)
			metrics.CyclomaticComplexity += caseCount
			metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
			for _, s := range stmt.Body.List {
				if cc, ok := s.(*ast.CommClause); ok {
					for _, child := range cc.Body {
						ca.walkWithDepth(child, metrics, depth+1)
					}
				}
			}
		}

	case *ast.FuncLit:
		metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
		// Don't recurse into anonymous functions

	case *ast.GoStmt:
		metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
		// Walk the call expression but not as a deeper nesting scope
		ca.walkWithDepth(stmt.Call, metrics, depth)

	case *ast.DeferStmt:
		metrics.CognitiveComplexity += ca.calculateCognitiveWeight(depth)
		ca.walkWithDepth(stmt.Call, metrics, depth)

	case *ast.BlockStmt:
		if depth > 0 {
			metrics.NestedBlocks++
		}
		if depth > metrics.MaxNestingDepth {
			metrics.MaxNestingDepth = depth
		}
		for _, child := range stmt.List {
			ca.walkWithDepth(child, metrics, depth)
		}

	case *ast.AssignStmt:
		if stmt.Tok == token.DEFINE {
			for _, expr := range stmt.Lhs {
				if ident, ok := expr.(*ast.Ident); ok && ident.Name != "_" {
					metrics.LocalVariables++
				}
			}
		}

	case *ast.DeclStmt:
		ca.walkWithDepth(stmt.Decl, metrics, depth)

	case *ast.GenDecl:
		if stmt.Tok == token.VAR {
			for _, spec := range stmt.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					metrics.LocalVariables += len(valueSpec.Names)
				}
			}
		}

	case *ast.ExprStmt:
		ca.walkWithDepth(stmt.X, metrics, depth)

	case *ast.ReturnStmt:
		// nothing special

	case *ast.LabeledStmt:
		ca.walkWithDepth(stmt.Stmt, metrics, depth)
	}
}

// calculateCognitiveWeight calculates cognitive complexity weight based on nesting
func (ca *ComplexityAnalyzer) calculateCognitiveWeight(nestingLevel int) int {
	if nestingLevel == 0 {
		return 1
	}
	// Cognitive complexity increases with nesting
	return 1 + nestingLevel
}

// countSwitchCases counts the number of case clauses in a switch statement
func (ca *ComplexityAnalyzer) countSwitchCases(body *ast.BlockStmt) int {
	count := 0
	hasDefault := false

	for _, stmt := range body.List {
		if caseClause, ok := stmt.(*ast.CaseClause); ok {
			if caseClause.List == nil {
				// Default case
				hasDefault = true
			} else {
				// Regular case
				count++
			}
		}
	}

	// If there's no default case, add 1 for the implicit default path
	if !hasDefault {
		count++
	}

	return count
}

// countSelectCases counts the number of case clauses in a select statement
func (ca *ComplexityAnalyzer) countSelectCases(body *ast.BlockStmt) int {
	count := 0

	for _, stmt := range body.List {
		if _, ok := stmt.(*ast.CommClause); ok {
			count++
		}
	}

	return count
}

// GetComplexityThresholds returns standard complexity thresholds
func GetComplexityThresholds() map[string]int {
	return map[string]int{
		"low":       1,
		"moderate":  5,
		"high":      10,
		"very_high": 15,
		"extreme":   20,
	}
}

// ClassifyComplexity classifies complexity level
func ClassifyComplexity(complexity int) string {
	thresholds := GetComplexityThresholds()

	switch {
	case complexity >= thresholds["extreme"]:
		return "extreme"
	case complexity >= thresholds["very_high"]:
		return "very_high"
	case complexity >= thresholds["high"]:
		return "high"
	case complexity >= thresholds["moderate"]:
		return "moderate"
	default:
		return "low"
	}
}

// FormatComplexityReport formats a complexity analysis report
func FormatComplexityReport(results []*ComplexityResult) string {
	if len(results) == 0 {
		return "No complex functions found."
	}

	var report strings.Builder
	report.WriteString(fmt.Sprintf("Found %d complex functions:\n\n", len(results)))

	for i, result := range results {
		if i >= 20 { // Limit to top 20 most complex functions
			report.WriteString(fmt.Sprintf("... and %d more functions\n", len(results)-i))
			break
		}

		level := ClassifyComplexity(result.Metrics.CyclomaticComplexity)
		report.WriteString(fmt.Sprintf("%d. %s (%s:%d)\n", i+1,
			result.Function.Name,
			result.Function.File,
			result.Position.Line))
		report.WriteString(fmt.Sprintf("   Cyclomatic Complexity: %d (%s)\n",
			result.Metrics.CyclomaticComplexity, level))
		report.WriteString(fmt.Sprintf("   Cognitive Complexity: %d\n",
			result.Metrics.CognitiveComplexity))
		report.WriteString(fmt.Sprintf("   Lines of Code: %d\n",
			result.Metrics.LinesOfCode))
		report.WriteString(fmt.Sprintf("   Parameters: %d, Local Variables: %d\n",
			result.Metrics.Parameters, result.Metrics.LocalVariables))
		report.WriteString(fmt.Sprintf("   Max Nesting Depth: %d\n\n",
			result.Metrics.MaxNestingDepth))
	}

	return report.String()
}
