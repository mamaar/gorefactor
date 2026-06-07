package mcp

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// --- add_context_parameter ---

type AddContextParameterInput struct {
	FunctionName string `json:"function_name" jsonschema:"function or method name (use Type.Method for methods)"`
	SourceFile   string `json:"source_file" jsonschema:"file containing the function"`
	DefaultValue string `json:"default_value,omitempty" jsonschema:"value at call sites, defaults to context.TODO()"`
	Propagate    bool   `json:"propagate,omitempty" jsonschema:"propagate changes to interface declarations and sibling implementations"`
}

func registerContextTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "add_context_parameter",
		Description: `Add ctx context.Context as the first parameter to a function, updating all call sites with a default value (context.TODO() by default).
Pairs with detect_missing_context_params to find functions that need it.`,
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AddContextParameterInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		idx, err := state.EnsureReferenceIndex(ws)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		sourceFile := resolveFile(ws, in.SourceFile)

		// Find the function declaration and extract existing params
		funcParams, err := analysis.ExtractFuncParams(ws, sourceFile, in.FunctionName)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// Check if ctx context.Context already exists as first param
		if len(funcParams) > 0 && funcParams[0].Type == "context.Context" {
			state.RUnlock()
			return errResult(fmt.Errorf("function %s already has context.Context as first parameter", in.FunctionName)), nil, nil
		}

		// Convert to refactor.Parameter and prepend ctx context.Context
		newParams := make([]refactor.Parameter, 0, len(funcParams)+1)
		newParams = append(newParams, refactor.Parameter{Name: "ctx", Type: "context.Context"})
		for _, fp := range funcParams {
			newParams = append(newParams, refactor.Parameter{Name: fp.Name, Type: fp.Type})
		}

		defaultValue := in.DefaultValue
		if defaultValue == "" {
			defaultValue = "context.TODO()"
		}

		op := &refactor.ChangeSignatureOperation{
			FunctionName:         in.FunctionName,
			SourceFile:           sourceFile,
			NewParams:            newParams,
			Scope:                pkgtypes.WorkspaceScope,
			PropagateToInterface: in.Propagate,
			DefaultValue:         defaultValue,
			NewParamPosition:     0,
			NewReturnPosition:    -1,
			RemovedReturnIndex:   -1,
			CachedIndex:          idx,
			Logger:               state.logger,
		}

		if err := op.Validate(ws); err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := op.Execute(ws)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// Bug 2 fix: remove internal "ctx := context.Background()" / "ctx := context.TODO()"
		// statements from the function body.  Now that ctx is a parameter, the internal
		// root-context creation is a redeclaration that prevents compilation.
		internalCtxChanges := removeInternalCtxCreation(ws, sourceFile, in.FunctionName)
		plan.Changes = append(plan.Changes, internalCtxChanges...)
		for _, c := range internalCtxChanges {
			found := false
			for _, f := range plan.AffectedFiles {
				if f == c.File {
					found = true
					break
				}
			}
			if !found {
				plan.AffectedFiles = append(plan.AffectedFiles, c.File)
			}
		}

		state.RUnlock()

		result, err := executePlan(state, plan, fmt.Sprintf("add context parameter: %s", in.FunctionName))
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}

// removeInternalCtxCreation scans the body of funcName in sourceFile and generates
// Change records that remove any short-variable-declaration statement of the form:
//
//	ctx := context.Background()
//	ctx := context.TODO()
//
// These become redeclarations once ctx is a parameter.  The statement is replaced
// by a blank line so that line numbers of subsequent code stay stable.
func removeInternalCtxCreation(ws *pkgtypes.Workspace, sourceFilePath string, funcName string) []pkgtypes.Change {
	// Locate the parsed file object.
	var sourceFile *pkgtypes.File
	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			if f.Path == sourceFilePath {
				sourceFile = f
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}
	if sourceFile == nil || sourceFile.AST == nil {
		return nil
	}

	// Find the target function declaration.
	var funcDecl *ast.FuncDecl
	targetName := funcName
	var receiverType string
	if idx := strings.Index(funcName, "."); idx >= 0 {
		receiverType = funcName[:idx]
		targetName = funcName[idx+1:]
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
			rt := baseReceiverTypeName(fd.Recv.List[0].Type)
			if rt != receiverType {
				return true
			}
		}
		funcDecl = fd
		return false
	})
	if funcDecl == nil || funcDecl.Body == nil {
		return nil
	}

	// Read the file content so we can extract exact text offsets.
	content, err := os.ReadFile(sourceFilePath)
	if err != nil {
		return nil
	}

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

		// Extend end to consume the trailing newline so we don't leave a blank line.
		if stmtEnd < len(content) && content[stmtEnd] == '\n' {
			stmtEnd++
		}

		oldText := string(content[stmtStart:stmtEnd])
		changes = append(changes, pkgtypes.Change{
			File:        sourceFilePath,
			Start:       stmtStart,
			End:         stmtEnd,
			OldText:     oldText,
			NewText:     "",
			Description: fmt.Sprintf("Remove internal ctx creation in %s (ctx is now a parameter)", funcName),
		})
	}

	return changes
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

// baseReceiverTypeName strips a pointer star and returns the identifier name.
func baseReceiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return baseReceiverTypeName(t.X)
	}
	return ""
}
