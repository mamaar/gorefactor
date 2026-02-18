package missingctx

import (
	"bytes"
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File         string   `json:"file"`
	Line         int      `json:"line"`
	Column       int      `json:"column"`
	FunctionName string   `json:"function_name"`
	Signature    string   `json:"signature"`
	ContextCalls []string `json:"context_calls"`
}

var Analyzer = &analysis.Analyzer{
	Name:     "missingctx",
	Doc:      "detects functions that should accept ctx context.Context but create context internally",
	Run:      run,
	Requires: []*analysis.Analyzer{filedata.Analyzer},
}

func run(pass *analysis.Pass) (any, error) {
	fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
	var results []*Result

	for _, file := range pass.Files {
		pos := pass.Fset.Position(file.Pos())
		content := fd.Content[pos.Filename]
		results = append(results, analyzeFile(pass, file, content)...)
	}

	return results, nil
}

func analyzeFile(pass *analysis.Pass, file *ast.File, content []byte) []*Result {
	var results []*Result

	ast.Inspect(file, func(n ast.Node) bool {
		funcDecl, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}

		// Skip main() and init()
		if funcDecl.Name.Name == "main" || funcDecl.Name.Name == "init" {
			return false
		}

		if hasContextParam(funcDecl) {
			return false
		}

		if funcDecl.Body == nil {
			return false
		}

		calls := detectContextCreation(funcDecl.Body)
		if len(calls) == 0 {
			return false
		}

		pos := pass.Fset.Position(funcDecl.Pos())
		sig := extractSignatureText(pass.Fset, content, funcDecl)

		pass.Report(analysis.Diagnostic{
			Pos:     funcDecl.Pos(),
			End:     funcDecl.End(),
			Message: "function creates context internally instead of accepting context.Context parameter",
		})

		results = append(results, &Result{
			File:         pos.Filename,
			Line:         pos.Line,
			Column:       pos.Column,
			FunctionName: funcDecl.Name.Name,
			Signature:    sig,
			ContextCalls: calls,
		})

		return false
	})

	return results
}

func hasContextParam(funcDecl *ast.FuncDecl) bool {
	if funcDecl.Type.Params == nil {
		return false
	}
	for _, field := range funcDecl.Type.Params.List {
		if isContextType(field.Type) {
			return true
		}
	}
	return false
}

func isContextType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "context" && sel.Sel.Name == "Context"
}

func detectContextCreation(body *ast.BlockStmt) []string {
	var calls []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg.Name == "context" && (sel.Sel.Name == "TODO" || sel.Sel.Name == "Background") {
			calls = append(calls, "context."+sel.Sel.Name+"()")
		}
		return true
	})
	return calls
}

func extractSignatureText(fset *token.FileSet, content []byte, funcDecl *ast.FuncDecl) string {
	if len(content) == 0 {
		return ""
	}
	start := fset.Position(funcDecl.Pos()).Offset
	end := fset.Position(funcDecl.Body.Lbrace).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(bytes.TrimSpace(content[start:end]))
}
