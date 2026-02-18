package analysis

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// envBoolPatterns are common environment boolean parameter names.
var envBoolPatterns = []string{
	"istest", "isprod", "isproduction", "isdev", "isdevelopment",
	"islocal", "isstaging", "isdebug", "testmode", "devmode",
	"debugmode", "prodmode", "production", "debug", "testing",
}

// EnvBooleanViolation represents a detected environment boolean parameter.
type EnvBooleanViolation struct {
	File             string
	Line             int
	Column           int
	Function         string
	ParameterName    string
	ParameterType    string
	PropagationDepth int
	CallChain        []string
	SuggestedPattern string
	Suggestion       string
}

// EnvBooleanAnalyzer detects environment boolean parameters passed through call stacks.
type EnvBooleanAnalyzer struct {
	workspace *types.Workspace
	fileSet   *token.FileSet
	maxDepth  int
}

// NewEnvBooleanAnalyzer creates a new environment boolean analyzer.
// maxDepth is the propagation depth threshold before flagging (default 1,
// meaning any env boolean parameter is flagged).
func NewEnvBooleanAnalyzer(ws *types.Workspace, maxDepth int) *EnvBooleanAnalyzer {
	if maxDepth < 0 {
		maxDepth = 1
	}
	return &EnvBooleanAnalyzer{
		workspace: ws,
		fileSet:   ws.FileSet,
		maxDepth:  maxDepth,
	}
}

// AnalyzeWorkspace analyzes all packages in the workspace.
func (a *EnvBooleanAnalyzer) AnalyzeWorkspace() []*EnvBooleanViolation {
	var results []*EnvBooleanViolation
	for _, pkg := range a.workspace.Packages {
		results = append(results, a.AnalyzePackage(pkg)...)
	}
	return results
}

// AnalyzePackage analyzes all files in a package.
func (a *EnvBooleanAnalyzer) AnalyzePackage(pkg *types.Package) []*EnvBooleanViolation {
	var results []*EnvBooleanViolation
	for _, file := range pkg.Files {
		results = append(results, a.AnalyzeFile(file)...)
	}
	return results
}

// AnalyzeFile analyzes a single file for environment boolean violations.
func (a *EnvBooleanAnalyzer) AnalyzeFile(file *types.File) []*EnvBooleanViolation {
	var results []*EnvBooleanViolation

	for _, decl := range file.AST.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Type.Params == nil {
			continue
		}
		results = append(results, a.analyzeFunction(file, funcDecl)...)
	}

	return results
}

// analyzeFunction checks a function's parameters for environment booleans.
func (a *EnvBooleanAnalyzer) analyzeFunction(file *types.File, fn *ast.FuncDecl) []*EnvBooleanViolation {
	var results []*EnvBooleanViolation

	for _, field := range fn.Type.Params.List {
		// Check if the parameter type is bool
		if !a.isBoolType(field.Type) {
			continue
		}

		for _, name := range field.Names {
			if !isEnvBoolName(name.Name) {
				continue
			}

			// Trace propagation: find calls in the body that pass this parameter
			chain, depth := a.tracePropagation(fn, name.Name)

			if depth < a.maxDepth {
				continue
			}

			pos := a.fileSet.Position(name.Pos())
			funcName := fn.Name.Name

			pattern := suggestEnvPattern(name.Name)

			results = append(results, &EnvBooleanViolation{
				File:             file.Path,
				Line:             pos.Line,
				Column:           pos.Column,
				Function:         funcName,
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

// isBoolType checks if the type expression is "bool".
func (a *EnvBooleanAnalyzer) isBoolType(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "bool"
}

// tracePropagation traces how a bool parameter propagates through the function body.
// Returns the call chain and the propagation depth.
func (a *EnvBooleanAnalyzer) tracePropagation(fn *ast.FuncDecl, paramName string) ([]string, int) {
	if fn.Body == nil {
		return nil, 0
	}

	var chain []string
	chain = append(chain, fn.Name.Name)

	// Find call expressions in the body that pass the parameter
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check if any argument is the parameter
		for _, arg := range call.Args {
			ident, ok := arg.(*ast.Ident)
			if !ok || ident.Name != paramName {
				continue
			}

			// Get the callee name
			calleeName := a.callName(call)
			if calleeName != "" {
				chain = append(chain, calleeName)
			}
		}
		return true
	})

	depth := len(chain) - 1 // subtract the function itself
	if depth < 0 {
		depth = 0
	}

	return chain, depth
}

// callName extracts the function name from a call expression.
func (a *EnvBooleanAnalyzer) callName(call *ast.CallExpr) string {
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

// isEnvBoolName checks if a parameter name matches environment boolean patterns.
func isEnvBoolName(name string) bool {
	lower := strings.ToLower(name)
	for _, pattern := range envBoolPatterns {
		if lower == pattern {
			return true
		}
	}
	return false
}

// suggestEnvPattern suggests the appropriate replacement pattern for an env boolean.
func suggestEnvPattern(paramName string) string {
	lower := strings.ToLower(paramName)

	// Mode-like booleans suggest interface implementation
	if strings.Contains(lower, "mode") ||
		strings.Contains(lower, "prod") ||
		strings.Contains(lower, "test") ||
		strings.Contains(lower, "dev") ||
		strings.Contains(lower, "staging") ||
		strings.Contains(lower, "local") {
		return "interface_implementation"
	}

	// Debug-like booleans suggest concrete value
	if strings.Contains(lower, "debug") {
		return "concrete_value"
	}

	return "interface_implementation"
}

// buildEnvSuggestion creates a human-readable suggestion.
func buildEnvSuggestion(paramName, pattern string) string {
	switch pattern {
	case "interface_implementation":
		return "Replace '" + paramName + "' parameter with an interface. Define separate implementations for each environment (e.g., ProdService, TestService)"
	case "concrete_value":
		return "Replace '" + paramName + "' parameter with the concrete value it controls. Resolve the value at initialization time"
	}
	return "Replace '" + paramName + "' with an interface or concrete value"
}
