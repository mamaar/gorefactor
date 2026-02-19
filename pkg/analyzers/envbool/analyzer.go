package envbool

import (
	"go/ast"

	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File             string   `json:"file"`
	Line             int      `json:"line"`
	Column           int      `json:"column"`
	Function         string   `json:"function_name"`
	ParameterName    string   `json:"parameter_name"`
	ParameterType    string   `json:"parameter_type"`
	PropagationDepth int      `json:"propagation_depth"`
	CallChain        []string `json:"call_chain"`
	SuggestedPattern string   `json:"suggested_pattern"`
	Suggestion       string   `json:"suggestion"`
}

type config struct {
	maxDepth int
}

// Option configures the analyzer.
type Option func(*config)

// WithMaxDepth sets the propagation depth threshold.
func WithMaxDepth(n int) Option {
	return func(c *config) { c.maxDepth = n }
}

var Analyzer = &analysis.Analyzer{
	Name: "envbool",
	Doc:  "detects environment boolean parameters passed down call stacks",
	Run:  makeRun(config{maxDepth: 1}),
}

// NewAnalyzer creates a configured analyzer.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := config{maxDepth: 1}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &analysis.Analyzer{
		Name: "envbool",
		Doc:  "detects environment boolean parameters passed down call stacks",
		Run:  makeRun(cfg),
	}
}

func makeRun(cfg config) func(*analysis.Pass) (any, error) {
	return func(pass *analysis.Pass) (any, error) {
		var results []*Result

		for _, file := range pass.Files {
			results = append(results, analyzeFile(pass, file, cfg.maxDepth)...)
		}

		return results, nil
	}
}

var envBoolPatterns = []string{
	"istest", "isprod", "isproduction", "isdev", "isdevelopment",
	"islocal", "isstaging", "isdebug", "testmode", "devmode",
	"debugmode", "prodmode", "production", "debug", "testing",
}

func analyzeFile(pass *analysis.Pass, file *ast.File, maxDepth int) []*Result {
	var results []*Result

	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Type.Params == nil {
			continue
		}
		results = append(results, analyzeFunction(pass, file, funcDecl, maxDepth)...)
	}

	return results
}

func analyzeFunction(pass *analysis.Pass, file *ast.File, fn *ast.FuncDecl, maxDepth int) []*Result {
	var results []*Result

	for _, field := range fn.Type.Params.List {
		if !isBoolType(field.Type) {
			continue
		}

		for _, name := range field.Names {
			if !isEnvBoolName(name.Name) {
				continue
			}

			chain, depth := tracePropagation(fn, name.Name)
			if depth < maxDepth {
				continue
			}

			pos := pass.Fset.Position(name.Pos())
			pattern := suggestEnvPattern(name.Name)

			pass.Report(analysis.Diagnostic{
				Pos:     name.Pos(),
				End:     name.End(),
				Message: "environment boolean parameter '" + name.Name + "' should be replaced with " + pattern,
			})

			results = append(results, &Result{
				File:             pos.Filename,
				Line:             pos.Line,
				Column:           pos.Column,
				Function:         fn.Name.Name,
				ParameterName:    name.Name,
				ParameterType:    "bool",
				PropagationDepth: depth,
				CallChain:        chain,
				SuggestedPattern: pattern,
				Suggestion:       buildEnvSuggestion(name.Name, pattern),
			})
		}
	}

	return results
}

func isBoolType(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "bool"
}

func tracePropagation(fn *ast.FuncDecl, paramName string) ([]string, int) {
	if fn.Body == nil {
		return nil, 0
	}

	chain := []string{fn.Name.Name}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		for _, arg := range call.Args {
			ident, ok := arg.(*ast.Ident)
			if !ok || ident.Name != paramName {
				continue
			}
			calleeName := callName(call)
			if calleeName != "" {
				chain = append(chain, calleeName)
			}
		}
		return true
	})

	depth := max(len(chain)-1, 0)
	return chain, depth
}

func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	}
	return ""
}

func isEnvBoolName(name string) bool {
	lower := strings.ToLower(name)
	return slices.Contains(envBoolPatterns, lower)
}

func suggestEnvPattern(paramName string) string {
	lower := strings.ToLower(paramName)

	switch {
	case strings.Contains(lower, "mode") ||
		strings.Contains(lower, "prod") ||
		strings.Contains(lower, "test") ||
		strings.Contains(lower, "dev") ||
		strings.Contains(lower, "staging") ||
		strings.Contains(lower, "local"):
		return "interface_implementation"
	case strings.Contains(lower, "debug"):
		return "concrete_value"
	}

	return "interface_implementation"
}

func buildEnvSuggestion(paramName, pattern string) string {
	switch pattern {
	case "interface_implementation":
		return "Replace '" + paramName + "' parameter with an interface. Define separate implementations for each environment (e.g., ProdService, TestService)"
	case "concrete_value":
		return "Replace '" + paramName + "' parameter with the concrete value it controls. Resolve the value at initialization time"
	}
	return "Replace '" + paramName + "' with an interface or concrete value"
}

