package analysis

import (
	"bytes"
	"go/ast"
	"go/token"

	"github.com/mamaar/gorefactor/pkg/types"
)

// IfInitViolation represents a detected if-init assignment violation
type IfInitViolation struct {
	File       string
	Line       int
	Column     int
	Variables  []string    // LHS variable names from init stmt
	Expression string      // RHS expression text
	Snippet    string      // Full if-init line
	Function   string      // Enclosing function name
	Node       *ast.IfStmt // AST node for the if statement (used by fixer)
}

// IfInitAnalyzer detects if-init assignment statements
type IfInitAnalyzer struct {
	workspace *types.Workspace
	fileSet   *token.FileSet
}

// NewIfInitAnalyzer creates a new if-init analyzer
func NewIfInitAnalyzer(ws *types.Workspace) *IfInitAnalyzer {
	return &IfInitAnalyzer{
		workspace: ws,
		fileSet:   ws.FileSet,
	}
}

// AnalyzeWorkspace analyzes all packages in the workspace for if-init violations
func (a *IfInitAnalyzer) AnalyzeWorkspace() []*IfInitViolation {
	var results []*IfInitViolation
	for _, pkg := range a.workspace.Packages {
		results = append(results, a.AnalyzePackage(pkg)...)
	}
	return results
}

// AnalyzePackage analyzes all files in a package for if-init violations
func (a *IfInitAnalyzer) AnalyzePackage(pkg *types.Package) []*IfInitViolation {
	var results []*IfInitViolation
	for _, file := range pkg.Files {
		results = append(results, a.AnalyzeFile(file)...)
	}
	return results
}

// AnalyzeFile analyzes a single file for if-init violations
func (a *IfInitAnalyzer) AnalyzeFile(file *types.File) []*IfInitViolation {
	var results []*IfInitViolation
	var currentFunc string

	ast.Inspect(file.AST, func(n ast.Node) bool {
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

			pos := a.fileSet.Position(node.Pos())

			var vars []string
			for _, lhs := range assign.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok {
					vars = append(vars, ident.Name)
				}
			}

			expr := a.sourceText(file, assign.Rhs[0].Pos(), assign.Rhs[len(assign.Rhs)-1].End())
			snippet := a.sourceLine(file, pos.Line)

			results = append(results, &IfInitViolation{
				File:       file.Path,
				Line:       pos.Line,
				Column:     pos.Column,
				Variables:  vars,
				Expression: expr,
				Snippet:    snippet,
				Function:   currentFunc,
				Node:       node,
			})
		}
		return true
	})

	return results
}

// sourceText extracts source text between two positions
func (a *IfInitAnalyzer) sourceText(file *types.File, from, to token.Pos) string {
	content := file.OriginalContent
	if len(content) == 0 {
		return ""
	}
	start := a.fileSet.Position(from).Offset
	end := a.fileSet.Position(to).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

// sourceLine extracts a single line from the source
func (a *IfInitAnalyzer) sourceLine(file *types.File, line int) string {
	content := file.OriginalContent
	if len(content) == 0 {
		return ""
	}
	lines := bytes.Split(content, []byte("\n"))
	if line < 1 || line > len(lines) {
		return ""
	}
	return string(bytes.TrimSpace(lines[line-1]))
}
