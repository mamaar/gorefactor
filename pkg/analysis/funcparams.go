package analysis

import (
	"errors"
	"fmt"
	"go/ast"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// FuncParam represents a function parameter with name and type.
type FuncParam struct {
	Name string
	Type string
}

// FindFuncType finds a function declaration in the workspace and returns its signature.
// A missing file or function is a SymbolNotFound RefactorError.
func FindFuncType(ws *types.Workspace, sourceFile, functionName string) (*ast.FuncType, error) {
	file, _, err := ws.FindFile(sourceFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("source file not found: %s", sourceFile),
		}
	}
	if err != nil {
		return nil, fmt.Errorf("find func type: %w", err)
	}
	if file.AST == nil {
		return nil, fmt.Errorf("source file has no AST: %s", sourceFile)
	}

	// Find the function declaration
	funcDecl := FindFuncDeclByName(file.AST, functionName)
	if funcDecl == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("function %s not found in %s", functionName, sourceFile),
		}
	}

	return funcDecl.Type, nil
}

// FuncTypeParams returns the parameters of a function signature, one entry per
// name.
func FuncTypeParams(ft *ast.FuncType) []FuncParam {
	if ft.Params == nil || len(ft.Params.List) == 0 {
		return nil
	}
	var params []FuncParam
	for _, field := range ft.Params.List {
		typeStr := ASTExprToString(field.Type)
		if len(field.Names) == 0 {
			params = append(params, FuncParam{Name: "", Type: typeStr})
		} else {
			for _, name := range field.Names {
				params = append(params, FuncParam{Name: name.Name, Type: typeStr})
			}
		}
	}
	return params
}

// FindFuncDeclByName finds a function or method declaration by name, supporting Type.Method syntax.
func FindFuncDeclByName(astFile *ast.File, name string) *ast.FuncDecl {
	if strings.Contains(name, ".") {
		parts := strings.SplitN(name, ".", 2)
		typeName, methodName := parts[0], parts[1]
		var found *ast.FuncDecl
		ast.Inspect(astFile, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Name.Name != methodName {
				return true
			}
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				if MatchesReceiverType(fd.Recv.List[0].Type, typeName) {
					found = fd
					return false
				}
			}
			return true
		})
		return found
	}

	var found *ast.FuncDecl
	ast.Inspect(astFile, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Name != nil && fd.Name.Name == name {
			found = fd
			return false
		}
		return true
	})
	return found
}

// MatchesReceiverType checks if a receiver type expression matches a given type name.
func MatchesReceiverType(expr ast.Expr, typeName string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == typeName
	case *ast.StarExpr:
		return MatchesReceiverType(t.X, typeName)
	case *ast.IndexExpr:
		return MatchesReceiverType(t.X, typeName)
	case *ast.IndexListExpr:
		return MatchesReceiverType(t.X, typeName)
	}
	return false
}

// ASTExprToString converts an AST type expression to its string representation.
func ASTExprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + ASTExprToString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + ASTExprToString(t.Elt)
		}
		return "[...]" + ASTExprToString(t.Elt)
	case *ast.SelectorExpr:
		return ASTExprToString(t.X) + "." + t.Sel.Name
	case *ast.MapType:
		return "map[" + ASTExprToString(t.Key) + "]" + ASTExprToString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.ChanType:
		return "chan " + ASTExprToString(t.Value)
	case *ast.Ellipsis:
		return "..." + ASTExprToString(t.Elt)
	default:
		return "interface{}"
	}
}
