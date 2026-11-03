// Package nilreceiver finds methods that compare their own receiver to nil.
package nilreceiver

import (
	"bytes"
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
)

// Result is one comparison of a method's receiver to nil.
type Result struct {
	File           string `json:"file"`
	Line           int    `json:"line"`
	Column         int    `json:"column"`
	Function       string `json:"function"`
	Receiver       string `json:"receiver"`
	ReceiverType   string `json:"receiver_type"`
	FirstStatement bool   `json:"first_statement"`
	Code           string `json:"code"`
}

// Analyzer reports receiver nil comparisons outside generated files.
var Analyzer = &analysis.Analyzer{
	Name:     "nilreceiver",
	Doc:      "detects methods that compare their receiver to nil",
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
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}

			results = append(results, analyzeMethod(pass, fn, content)...)
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

func analyzeMethod(pass *analysis.Pass, fn *ast.FuncDecl, content []byte) []*Result {
	recv := fn.Recv.List[0]
	if len(recv.Names) == 0 || recv.Names[0].Name == "_" {
		return nil
	}

	recvName := recv.Names[0]

	var results []*Result

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		cmp, ok := n.(*ast.BinaryExpr)
		if !ok || !comparesToNil(pass.TypesInfo, cmp, recvName) {
			return true
		}

		pos := pass.Fset.Position(cmp.Pos())
		results = append(results, &Result{
			File:           pos.Filename,
			Line:           pos.Line,
			Column:         pos.Column,
			Function:       baseTypeName(recv.Type) + "." + fn.Name.Name,
			Receiver:       recvName.Name,
			ReceiverType:   types.ExprString(recv.Type),
			FirstStatement: inFirstStatementCond(fn.Body, cmp.Pos()),
			Code:           sourceLine(content, pos.Line),
		})

		return true
	})

	return results
}

func comparesToNil(info *types.Info, cmp *ast.BinaryExpr, recv *ast.Ident) bool {
	if cmp.Op != token.EQL && cmp.Op != token.NEQ {
		return false
	}

	return (isNil(cmp.Y) && refersTo(info, cmp.X, recv)) || (isNil(cmp.X) && refersTo(info, cmp.Y, recv))
}

func isNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// refersTo falls back to matching by name in packages without type information,
// where a shadowing local would be a false positive.
func refersTo(info *types.Info, e ast.Expr, recv *ast.Ident) bool {
	id, ok := e.(*ast.Ident)
	if !ok || id.Name != recv.Name {
		return false
	}

	use, ok := info.Uses[id]
	if !ok {
		return true
	}

	return use == info.Defs[recv]
}

func inFirstStatementCond(body *ast.BlockStmt, pos token.Pos) bool {
	if len(body.List) == 0 {
		return false
	}

	first, ok := body.List[0].(*ast.IfStmt)

	return ok && first.Cond.Pos() <= pos && pos < first.Cond.End()
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
