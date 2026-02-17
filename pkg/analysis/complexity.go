package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"

	"github.com/mamaar/gorefactor/pkg/types"
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
	workspace   *types.Workspace
	fileSet     *token.FileSet
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

	ast.Inspect(file.AST, func(n ast.Node) bool {
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			if funcDecl.Body != nil { // Skip function declarations without body
				result := ca.AnalyzeFunction(funcDecl, file)
				// Only include functions above complexity threshold
				if result.Metrics.CyclomaticComplexity >= ca.minComplexity {
					results = append(results, result)
				}
			}
		}
		return true
	})

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

// analyzeBlockComplexity recursively analyzes complexity within a block
func (ca *ComplexityAnalyzer) analyzeBlockComplexity(node ast.Node, metrics *ComplexityMetrics, nestingLevel int) {
	if nestingLevel > metrics.MaxNestingDepth {
		metrics.MaxNestingDepth = nestingLevel
	}

	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		switch stmt := n.(type) {
		case *ast.IfStmt:
			metrics.CyclomaticComplexity++
			metrics.CognitiveComplexity += ca.calculateCognitiveWeight(nestingLevel)
			if stmt.Else != nil {
				// Don't count "else if" as separate complexity
				if _, ok := stmt.Else.(*ast.IfStmt); !ok {
					metrics.CyclomaticComplexity++
				}
			}

		case *ast.ForStmt, *ast.RangeStmt:
			metrics.CyclomaticComplexity++
			metrics.CognitiveComplexity += ca.calculateCognitiveWeight(nestingLevel)

		case *ast.SwitchStmt, *ast.TypeSwitchStmt:
			// Each case adds to complexity
			if switchStmt, ok := stmt.(*ast.SwitchStmt); ok && switchStmt.Body != nil {
				caseCount := ca.countSwitchCases(switchStmt.Body)
				metrics.CyclomaticComplexity += caseCount
				metrics.CognitiveComplexity += ca.calculateCognitiveWeight(nestingLevel)
			}
			if typeSwitchStmt, ok := stmt.(*ast.TypeSwitchStmt); ok && typeSwitchStmt.Body != nil {
				caseCount := ca.countSwitchCases(typeSwitchStmt.Body)
				metrics.CyclomaticComplexity += caseCount
				metrics.CognitiveComplexity += ca.calculateCognitiveWeight(nestingLevel)
			}

		case *ast.SelectStmt:
			if stmt.Body != nil {
				caseCount := ca.countSelectCases(stmt.Body)
				metrics.CyclomaticComplexity += caseCount
				metrics.CognitiveComplexity += ca.calculateCognitiveWeight(nestingLevel)
			}

		case *ast.CaseClause, *ast.CommClause:
			// Individual cases are handled in switch/select analysis above
			return false

		case *ast.FuncLit:
			// Anonymous functions add cognitive complexity
			metrics.CognitiveComplexity += ca.calculateCognitiveWeight(nestingLevel)
			return false // Don't recurse into anonymous functions

		case *ast.GoStmt, *ast.DeferStmt:
			metrics.CognitiveComplexity += ca.calculateCognitiveWeight(nestingLevel)

		case *ast.BlockStmt:
			// Count nested blocks
			if nestingLevel > 0 {
				metrics.NestedBlocks++
			}

		case *ast.AssignStmt:
			// Count local variable declarations
			if stmt.Tok == token.DEFINE {
				for _, expr := range stmt.Lhs {
					if ident, ok := expr.(*ast.Ident); ok && ident.Name != "_" {
						metrics.LocalVariables++
					}
				}
			}

		case *ast.GenDecl:
			// Count local variable declarations in var blocks
			if stmt.Tok == token.VAR {
				for _, spec := range stmt.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						metrics.LocalVariables += len(valueSpec.Names)
					}
				}
			}
		}

		// Continue recursion for nested structures with increased nesting level
		switch n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, 
			 *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.BlockStmt:
			// These increase nesting level for their children
			return true
		default:
			return true
		}
	})
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

	report := fmt.Sprintf("Found %d complex functions:\n\n", len(results))
	
	for i, result := range results {
		if i >= 20 { // Limit to top 20 most complex functions
			report += fmt.Sprintf("... and %d more functions\n", len(results)-i)
			break
		}
		
		level := ClassifyComplexity(result.Metrics.CyclomaticComplexity)
		report += fmt.Sprintf("%d. %s (%s:%d)\n", i+1, 
			result.Function.Name, 
			result.Function.File, 
			result.Position.Line)
		report += fmt.Sprintf("   Cyclomatic Complexity: %d (%s)\n", 
			result.Metrics.CyclomaticComplexity, level)
		report += fmt.Sprintf("   Cognitive Complexity: %d\n", 
			result.Metrics.CognitiveComplexity)
		report += fmt.Sprintf("   Lines of Code: %d\n", 
			result.Metrics.LinesOfCode)
		report += fmt.Sprintf("   Parameters: %d, Local Variables: %d\n", 
			result.Metrics.Parameters, result.Metrics.LocalVariables)
		report += fmt.Sprintf("   Max Nesting Depth: %d\n\n", 
			result.Metrics.MaxNestingDepth)
	}
	
	return report
}