package analysis

import (
	"bytes"
	"go/ast"
	"go/token"

	"github.com/mamaar/gorefactor/pkg/types"
)

// MissingContextViolation represents a function that creates context internally
// instead of accepting it as a parameter.
type MissingContextViolation struct {
	File         string
	Line         int
	Column       int
	FunctionName string
	Signature    string
	ContextCalls []string // e.g. ["context.TODO()", "context.Background()"]
}

// MissingContextAnalyzer detects functions that call context.TODO() or
// context.Background() but don't accept context.Context as a parameter.
type MissingContextAnalyzer struct {
	workspace *types.Workspace
	fileSet   *token.FileSet
}

// NewMissingContextAnalyzer creates a new missing context analyzer.
func NewMissingContextAnalyzer(ws *types.Workspace) *MissingContextAnalyzer {
	return &MissingContextAnalyzer{
		workspace: ws,
		fileSet:   ws.FileSet,
	}
}

// AnalyzeWorkspace analyzes all packages in the workspace.
func (a *MissingContextAnalyzer) AnalyzeWorkspace() []*MissingContextViolation {
	var results []*MissingContextViolation
	for _, pkg := range a.workspace.Packages {
		results = append(results, a.AnalyzePackage(pkg)...)
	}
	return results
}

// AnalyzePackage analyzes all files in a package.
func (a *MissingContextAnalyzer) AnalyzePackage(pkg *types.Package) []*MissingContextViolation {
	var results []*MissingContextViolation
	for _, file := range pkg.Files {
		results = append(results, a.AnalyzeFile(file)...)
	}
	return results
}

// AnalyzeFile analyzes a single file for missing context parameter violations.
func (a *MissingContextAnalyzer) AnalyzeFile(file *types.File) []*MissingContextViolation {
	var results []*MissingContextViolation

	ast.Inspect(file.AST, func(n ast.Node) bool {
		funcDecl, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}

		// Skip main() and init() — legitimate places to create root contexts
		if funcDecl.Name.Name == "main" || funcDecl.Name.Name == "init" {
			return false
		}

		// Skip if already has context.Context parameter
		if hasContextParam(funcDecl) {
			return false
		}

		// Check body for context creation calls
		if funcDecl.Body == nil {
			return false
		}

		calls := detectContextCreation(funcDecl.Body)
		if len(calls) == 0 {
			return false
		}

		pos := a.fileSet.Position(funcDecl.Pos())
		sig := extractSignatureText(file, funcDecl, a.fileSet)

		results = append(results, &MissingContextViolation{
			File:         file.Path,
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

// hasContextParam checks if any parameter of the function is context.Context.
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

// isContextType checks if an expression represents context.Context.
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

// detectContextCreation walks a function body and returns all context.TODO()
// and context.Background() call expressions found.
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

// extractSignatureText extracts the function signature from source bytes.
func extractSignatureText(file *types.File, funcDecl *ast.FuncDecl, fset *token.FileSet) string {
	content := file.OriginalContent
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
