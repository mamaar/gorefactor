// Package nilnil finds returns of nil for every result of a function whose last result is an error.
package nilnil

import (
	"bytes"
	"go/ast"
	"go/types"
	"sort"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
)

// Result is one return of all-nil values with a nil error.
type Result struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	// Function is empty for a func literal outside any function declaration.
	Function    string   `json:"function,omitempty"`
	InClosure   bool     `json:"in_closure"`
	ResultTypes []string `json:"result_types"`
	Code        string   `json:"code"`
}

// Analyzer reports nil, nil returns outside generated files.
var Analyzer = &analysis.Analyzer{
	Name:     "nilnil",
	Doc:      "detects returns of nil values together with a nil error",
	Requires: []*analysis.Analyzer{filedata.Analyzer},
	Run: func(pass *analysis.Pass) (any, error) {
		return run(pass), nil
	},
}

func run(pass *analysis.Pass) []*Result {
	fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
	var results []*Result

	for _, file := range pass.Files {
		if ast.IsGenerated(file) {
			continue
		}
		content := fd.Content[pass.Fset.Position(file.Pos()).Filename]
		for _, decl := range file.Decls {
			name := ""
			fn, ok := decl.(*ast.FuncDecl)
			if ok {
				name = funcName(fn)
				if fn.Body != nil {
					results = append(results, checkFunc(pass, fn.Type, fn.Body, name, false, content)...)
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.FuncLit)
				if ok {
					results = append(results, checkFunc(pass, lit.Type, lit.Body, name, true, content)...)
				}
				return true
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].File != results[j].File {
			return results[i].File < results[j].File
		}
		return results[i].Line < results[j].Line
	})
	return results
}

// checkFunc reports the all-nil returns of one function body, leaving nested
// func literals to their own call.
func checkFunc(pass *analysis.Pass, ft *ast.FuncType, body *ast.BlockStmt, name string, inClosure bool, content []byte) []*Result {
	resultTypes := valueResultTypes(pass.TypesInfo, ft)
	if resultTypes == nil {
		return nil
	}

	var results []*Result
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(n.Results) != len(resultTypes)+1 || !allNil(pass.TypesInfo, n.Results) {
				return true
			}
			pos := pass.Fset.Position(n.Pos())
			results = append(results, &Result{
				File:        pos.Filename,
				Line:        pos.Line,
				Column:      pos.Column,
				Function:    name,
				InClosure:   inClosure,
				ResultTypes: resultTypes,
				Code:        sourceLine(content, pos.Line),
			})
		}
		return true
	})
	return results
}

// valueResultTypes returns the source types of the results before a final
// error, or nil when the function does not end in an error after at least one other result.
func valueResultTypes(info *types.Info, ft *ast.FuncType) []string {
	if ft.Results == nil {
		return nil
	}
	var all []ast.Expr
	for _, field := range ft.Results.List {
		n := max(len(field.Names), 1)
		for range n {
			all = append(all, field.Type)
		}
	}
	if len(all) < 2 || !isError(info, all[len(all)-1]) {
		return nil
	}
	out := make([]string, len(all)-1)
	for i, e := range all[:len(all)-1] {
		out[i] = types.ExprString(e)
	}
	return out
}

var errorType = types.Universe.Lookup("error").Type()

// isError falls back to matching by name in packages without type information.
func isError(info *types.Info, e ast.Expr) bool {
	t := info.TypeOf(e)
	if t != nil {
		return types.Identical(t, errorType)
	}
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "error"
}

var nilObj = types.Universe.Lookup("nil")

func allNil(info *types.Info, exprs []ast.Expr) bool {
	for _, e := range exprs {
		id, ok := e.(*ast.Ident)
		if !ok || id.Name != "nil" {
			return false
		}
		obj, ok := info.Uses[id]
		if ok && obj != nilObj {
			return false
		}
	}
	return true
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return baseTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func baseTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return baseTypeName(t.X)
	case *ast.ParenExpr:
		return baseTypeName(t.X)
	case *ast.IndexExpr:
		return baseTypeName(t.X)
	case *ast.IndexListExpr:
		return baseTypeName(t.X)
	}
	return types.ExprString(e)
}

func sourceLine(content []byte, line int) string {
	lines := bytes.Split(content, []byte("\n"))
	if line < 1 || line > len(lines) {
		return ""
	}
	return string(bytes.TrimSpace(lines[line-1]))
}
