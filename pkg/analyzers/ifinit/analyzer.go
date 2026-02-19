package ifinit

import (
	"bytes"
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Column     int      `json:"column"`
	Variables  []string `json:"variables"`
	Expression string   `json:"expression"`
	Snippet    string   `json:"snippet"`
	Function   string   `json:"function"`
}

var Analyzer = &analysis.Analyzer{
	Name:     "ifinit",
	Doc:      "detects if-init assignments that should be split into separate assignment and if-check",
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
	var currentFunc string

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		switch node := n.(type) {
		case *ast.FuncDecl:
			currentFunc = node.Name.Name
		case *ast.IfStmt:
			if node.Init == nil {
				return true
			}
			assign, ok := node.Init.(*ast.AssignStmt)
			if !ok || assign.Tok != token.DEFINE {
				return true
			}

			pos := pass.Fset.Position(node.Pos())

			var vars []string
			for _, lhs := range assign.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok {
					vars = append(vars, ident.Name)
				}
			}

			expr := sourceText(pass.Fset, content, assign.Rhs[0].Pos(), assign.Rhs[len(assign.Rhs)-1].End())
			snippet := sourceLine(content, pos.Line)

			assignText := sourceText(pass.Fset, content, node.Init.Pos(), node.Init.End())
			condText := sourceText(pass.Fset, content, node.Cond.Pos(), node.Cond.End())

			var fixes []analysis.SuggestedFix
			startOffset := pass.Fset.Position(node.Pos()).Offset
			lbraceOffset := pass.Fset.Position(node.Body.Lbrace).Offset
			endOffset := lbraceOffset + 1

			if startOffset >= 0 && endOffset <= len(content) && startOffset < endOffset {
				indent := extractIndentation(content, startOffset)

				var sb strings.Builder
				sb.WriteString(assignText)
				sb.WriteByte('\n')
				sb.WriteString(indent)
				sb.WriteString("if ")
				sb.WriteString(condText)
				sb.WriteString(" {")

				fixes = append(fixes, analysis.SuggestedFix{
					Message: "Split if-init assignment into separate assignment and if-check",
					TextEdits: []analysis.TextEdit{
						{
							Pos:     node.Pos(),
							End:     node.Body.Lbrace + 1,
							NewText: []byte(sb.String()),
						},
					},
				})
			}

			pass.Report(analysis.Diagnostic{
				Pos:            node.Pos(),
				End:            node.End(),
				Message:        "if-init assignment should be split into separate assignment and if-check",
				SuggestedFixes: fixes,
			})

			results = append(results, &Result{
				File:       pos.Filename,
				Line:       pos.Line,
				Column:     pos.Column,
				Variables:  vars,
				Expression: expr,
				Snippet:    snippet,
				Function:   currentFunc,
			})
		}
		return true
	})

	return results
}

func sourceText(fset *token.FileSet, content []byte, from, to token.Pos) string {
	if len(content) == 0 {
		return ""
	}
	start := fset.Position(from).Offset
	end := fset.Position(to).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

func sourceLine(content []byte, line int) string {
	if len(content) == 0 {
		return ""
	}
	lines := bytes.Split(content, []byte("\n"))
	if line < 1 || line > len(lines) {
		return ""
	}
	return string(bytes.TrimSpace(lines[line-1]))
}

func extractIndentation(content []byte, offset int) string {
	lineStart := offset
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}
	var buf bytes.Buffer
	for i := lineStart; i < len(content); i++ {
		if content[i] == ' ' || content[i] == '\t' {
			buf.WriteByte(content[i])
		} else {
			break
		}
	}
	return buf.String()
}
