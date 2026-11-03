package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// RemoveInternalCtxCreation scans the body of funcName in sourceFilePath and generates
// Change records that remove any short-variable-declaration statement of the form:
//
//	ctx := context.Background()
//	ctx := context.TODO()
//
// These become redeclarations once ctx is a parameter.  The statement is replaced
// by a blank line so that line numbers of subsequent code stay stable.
func RemoveInternalCtxCreation(ws *pkgtypes.Workspace, sourceFilePath string, funcName string) []pkgtypes.Change {
	// findParsedFile searches TestFiles as well as Files: a target declared in a
	// _test.go would otherwise be silently skipped, leaving its ctx creation in
	// place to collide with the new parameter.
	sourceFile := findParsedFile(ws, sourceFilePath)
	if sourceFile == nil {
		return nil
	}

	// Find the target function declaration.
	var funcDecl *ast.FuncDecl
	targetName := funcName
	var receiverType string
	before, after, ok := strings.Cut(funcName, ".")
	if ok {
		receiverType = before
		targetName = after
	}

	ast.Inspect(sourceFile.AST, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if fd.Name == nil || fd.Name.Name != targetName {
			return true
		}
		// If a receiver type was specified, match it.
		if receiverType != "" {
			if fd.Recv == nil || len(fd.Recv.List) == 0 {
				return true
			}
			if !analysis.MatchesReceiverType(fd.Recv.List[0].Type, receiverType) {
				return true
			}
		}
		funcDecl = fd
		return false
	})
	if funcDecl == nil || funcDecl.Body == nil {
		return nil
	}

	content := sourceFile.OriginalContent

	var changes []pkgtypes.Change

	// Walk only the top-level statements of the function body (not nested closures).
	for _, stmt := range funcDecl.Body.List {
		assignStmt, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		// Must be ":=" with exactly one LHS and one RHS.
		if assignStmt.Tok != token.DEFINE {
			continue
		}
		if len(assignStmt.Lhs) != 1 || len(assignStmt.Rhs) != 1 {
			continue
		}

		// LHS must be the identifier "ctx".
		lhsIdent, ok := assignStmt.Lhs[0].(*ast.Ident)
		if !ok || lhsIdent.Name != "ctx" {
			continue
		}

		// RHS must be context.Background() or context.TODO().
		if !isRootContextCall(assignStmt.Rhs[0]) {
			continue
		}

		// Generate a change that replaces the entire statement (including its
		// trailing newline) with nothing, effectively deleting the line.
		stmtStart := ws.FileSet.Position(assignStmt.Pos()).Offset
		stmtEnd := ws.FileSet.Position(assignStmt.End()).Offset
		changes = append(changes, ctxAssignDeletionChange(sourceFilePath, content, stmtStart, stmtEnd, funcName))
	}

	return changes
}

// ctxAssignDeletionChange builds the Change that deletes a top-level `ctx :=
// context.Background()` / `ctx := context.TODO()` statement given its byte
// range, extending the end to swallow the trailing newline so no blank line
// is left behind. Shared by RemoveInternalCtxCreation above (which locates
// the statement by function name and re-finds its byte range) and
// thread_context_render.go's renderFakeDeletion (which already holds the
// exact *ast.AssignStmt recorded in threadPlan.Fake, so it computes
// start/end itself and calls straight through).
func ctxAssignDeletionChange(filePath string, content []byte, start, end int, label string) pkgtypes.Change {
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return pkgtypes.Change{
		File:        filePath,
		Start:       start,
		End:         end,
		OldText:     string(content[start:end]),
		NewText:     "",
		Description: fmt.Sprintf("Remove internal ctx creation in %s (ctx is now a parameter)", label),
	}
}

// isRootContextCall returns true when expr is context.Background() or context.TODO().
func isRootContextCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name != "context" {
		return false
	}
	return sel.Sel.Name == "Background" || sel.Sel.Name == "TODO"
}
