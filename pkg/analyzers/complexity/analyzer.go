package complexity

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	Function            string `json:"function"`
	File                string `json:"file"`
	Line                int    `json:"line"`
	CyclomaticComplexity int   `json:"cyclomatic_complexity"`
	CognitiveComplexity  int   `json:"cognitive_complexity"`
	LinesOfCode          int   `json:"lines_of_code"`
	Parameters           int   `json:"parameters"`
	MaxNestingDepth      int   `json:"max_nesting_depth"`
	Level                string `json:"level"`
}

// Metrics holds raw complexity numbers.
type Metrics struct {
	CyclomaticComplexity int
	CognitiveComplexity  int
	LinesOfCode          int
	Parameters           int
	LocalVariables       int
	NestedBlocks         int
	MaxNestingDepth      int
}

type config struct {
	minComplexity int
}

// Option configures the analyzer.
type Option func(*config)

// WithMinComplexity sets the minimum cyclomatic complexity threshold.
func WithMinComplexity(n int) Option {
	return func(c *config) { c.minComplexity = n }
}

var Analyzer = &analysis.Analyzer{
	Name:     "complexity",
	Doc:      "analyzes cyclomatic and cognitive complexity of functions",
	Run:      makeRun(config{minComplexity: 10}),
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

// NewAnalyzer creates a configured complexity analyzer.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := config{minComplexity: 10}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &analysis.Analyzer{
		Name:     "complexity",
		Doc:      "analyzes cyclomatic and cognitive complexity of functions",
		Run:      makeRun(cfg),
		Requires: []*analysis.Analyzer{inspect.Analyzer},
	}
}

func makeRun(cfg config) func(*analysis.Pass) (any, error) {
	return func(pass *analysis.Pass) (any, error) {
		insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
		var results []*Result

		for cur := range insp.Root().Preorder((*ast.FuncDecl)(nil)) {
			funcDecl := cur.Node().(*ast.FuncDecl)
			if funcDecl.Body == nil {
				continue
			}

			metrics := calculateMetrics(pass.Fset, funcDecl)
			if metrics.CyclomaticComplexity < cfg.minComplexity {
				continue
			}

			pos := pass.Fset.Position(funcDecl.Pos())
			level := classifyComplexity(metrics.CyclomaticComplexity)

			pass.Report(analysis.Diagnostic{
				Pos:     funcDecl.Pos(),
				End:     funcDecl.End(),
				Message: fmt.Sprintf("function %s has cyclomatic complexity %d (%s)", funcDecl.Name.Name, metrics.CyclomaticComplexity, level),
			})

			results = append(results, &Result{
				Function:             funcDecl.Name.Name,
				File:                 pos.Filename,
				Line:                 pos.Line,
				CyclomaticComplexity: metrics.CyclomaticComplexity,
				CognitiveComplexity:  metrics.CognitiveComplexity,
				LinesOfCode:          metrics.LinesOfCode,
				Parameters:           metrics.Parameters,
				MaxNestingDepth:      metrics.MaxNestingDepth,
				Level:                level,
			})
		}

		sort.Slice(results, func(i, j int) bool {
			return results[i].CyclomaticComplexity > results[j].CyclomaticComplexity
		})

		return results, nil
	}
}

func calculateMetrics(fset *token.FileSet, funcDecl *ast.FuncDecl) *Metrics {
	m := &Metrics{CyclomaticComplexity: 1}

	if funcDecl.Type.Params != nil {
		for _, field := range funcDecl.Type.Params.List {
			m.Parameters += len(field.Names)
		}
	}

	if funcDecl.Body != nil {
		startPos := fset.Position(funcDecl.Body.Lbrace)
		endPos := fset.Position(funcDecl.Body.Rbrace)
		m.LinesOfCode = endPos.Line - startPos.Line + 1
		walkWithDepth(funcDecl.Body, m, 0)
	}

	return m
}

func walkWithDepth(node ast.Node, m *Metrics, depth int) {
	if node == nil {
		return
	}

	switch stmt := node.(type) {
	case *ast.IfStmt:
		m.CyclomaticComplexity++
		m.CognitiveComplexity += cognitiveWeight(depth)
		if stmt.Else != nil {
			if _, ok := stmt.Else.(*ast.IfStmt); !ok {
				m.CyclomaticComplexity++
			}
		}
		if stmt.Init != nil {
			walkWithDepth(stmt.Init, m, depth+1)
		}
		walkWithDepth(stmt.Body, m, depth+1)
		if stmt.Else != nil {
			if _, ok := stmt.Else.(*ast.IfStmt); ok {
				walkWithDepth(stmt.Else, m, depth)
			} else {
				walkWithDepth(stmt.Else, m, depth+1)
			}
		}

	case *ast.ForStmt:
		m.CyclomaticComplexity++
		m.CognitiveComplexity += cognitiveWeight(depth)
		walkWithDepth(stmt.Body, m, depth+1)

	case *ast.RangeStmt:
		m.CyclomaticComplexity++
		m.CognitiveComplexity += cognitiveWeight(depth)
		walkWithDepth(stmt.Body, m, depth+1)

	case *ast.SwitchStmt:
		if stmt.Body != nil {
			caseCount := countSwitchCases(stmt.Body)
			m.CyclomaticComplexity += caseCount
			m.CognitiveComplexity += cognitiveWeight(depth)
			for _, s := range stmt.Body.List {
				if cc, ok := s.(*ast.CaseClause); ok {
					for _, child := range cc.Body {
						walkWithDepth(child, m, depth+1)
					}
				}
			}
		}

	case *ast.TypeSwitchStmt:
		if stmt.Body != nil {
			caseCount := countSwitchCases(stmt.Body)
			m.CyclomaticComplexity += caseCount
			m.CognitiveComplexity += cognitiveWeight(depth)
			for _, s := range stmt.Body.List {
				if cc, ok := s.(*ast.CaseClause); ok {
					for _, child := range cc.Body {
						walkWithDepth(child, m, depth+1)
					}
				}
			}
		}

	case *ast.SelectStmt:
		if stmt.Body != nil {
			caseCount := countSelectCases(stmt.Body)
			m.CyclomaticComplexity += caseCount
			m.CognitiveComplexity += cognitiveWeight(depth)
			for _, s := range stmt.Body.List {
				if cc, ok := s.(*ast.CommClause); ok {
					for _, child := range cc.Body {
						walkWithDepth(child, m, depth+1)
					}
				}
			}
		}

	case *ast.FuncLit:
		m.CognitiveComplexity += cognitiveWeight(depth)

	case *ast.GoStmt:
		m.CognitiveComplexity += cognitiveWeight(depth)
		walkWithDepth(stmt.Call, m, depth)

	case *ast.DeferStmt:
		m.CognitiveComplexity += cognitiveWeight(depth)
		walkWithDepth(stmt.Call, m, depth)

	case *ast.BlockStmt:
		if depth > 0 {
			m.NestedBlocks++
		}
		if depth > m.MaxNestingDepth {
			m.MaxNestingDepth = depth
		}
		for _, child := range stmt.List {
			walkWithDepth(child, m, depth)
		}

	case *ast.AssignStmt:
		if stmt.Tok == token.DEFINE {
			for _, expr := range stmt.Lhs {
				if ident, ok := expr.(*ast.Ident); ok && ident.Name != "_" {
					m.LocalVariables++
				}
			}
		}

	case *ast.DeclStmt:
		walkWithDepth(stmt.Decl, m, depth)

	case *ast.GenDecl:
		if stmt.Tok == token.VAR {
			for _, spec := range stmt.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					m.LocalVariables += len(valueSpec.Names)
				}
			}
		}

	case *ast.ExprStmt:
		walkWithDepth(stmt.X, m, depth)

	case *ast.ReturnStmt:
		// nothing special

	case *ast.LabeledStmt:
		walkWithDepth(stmt.Stmt, m, depth)
	}
}

func cognitiveWeight(nestingLevel int) int {
	if nestingLevel == 0 {
		return 1
	}
	return 1 + nestingLevel
}

func countSwitchCases(body *ast.BlockStmt) int {
	count := 0
	hasDefault := false

	for _, stmt := range body.List {
		if caseClause, ok := stmt.(*ast.CaseClause); ok {
			if caseClause.List == nil {
				hasDefault = true
			} else {
				count++
			}
		}
	}

	if !hasDefault {
		count++
	}

	return count
}

func countSelectCases(body *ast.BlockStmt) int {
	count := 0
	for _, stmt := range body.List {
		if _, ok := stmt.(*ast.CommClause); ok {
			count++
		}
	}
	return count
}

// ClassifyComplexity classifies a cyclomatic complexity value into a level string.
func ClassifyComplexity(complexity int) string {
	return classifyComplexity(complexity)
}

func classifyComplexity(complexity int) string {
	switch {
	case complexity >= 20:
		return "extreme"
	case complexity >= 15:
		return "very_high"
	case complexity >= 10:
		return "high"
	case complexity >= 5:
		return "moderate"
	default:
		return "low"
	}
}
