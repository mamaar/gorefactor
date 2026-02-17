package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- rename_symbol ---

type RenameSymbolInput struct {
	Symbol  string `json:"symbol" jsonschema:"current symbol name"`
	NewName string `json:"new_name" jsonschema:"new name for the symbol"`
	Package string `json:"package,omitempty" jsonschema:"package path (empty for workspace-wide)"`
}

// --- rename_package ---

type RenamePackageInput struct {
	PackagePath    string `json:"package_path" jsonschema:"path to the package directory"`
	NewPackageName string `json:"new_package_name" jsonschema:"new package name"`
}

// --- rename_method ---

type RenameMethodInput struct {
	TypeName      string `json:"type_name" jsonschema:"name of the type that owns the method"`
	MethodName    string `json:"method_name" jsonschema:"current method name"`
	NewMethodName string `json:"new_method_name" jsonschema:"new method name"`
	PackagePath   string `json:"package_path,omitempty" jsonschema:"package path (empty for workspace-wide)"`
}

func registerRenameTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "rename_symbol",
		Description: "Rename a symbol (function, type, variable, constant) across the workspace. All references are updated.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RenameSymbolInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		scope := types.WorkspaceScope
		pkg := in.Package
		if pkg != "" {
			pkg = types.ResolvePackagePath(ws, pkg)
			scope = types.PackageScope
		}
		plan, err := state.GetEngine().RenameSymbol(ws, types.RenameSymbolRequest{
			SymbolName: in.Symbol,
			NewName:    in.NewName,
			Package:    pkg,
			Scope:      scope,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "rename "+in.Symbol+" → "+in.NewName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "rename_package",
		Description: "Rename a Go package. Updates the package declaration in all files and import statements across the workspace.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RenamePackageInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		resolved := types.ResolvePackagePath(ws, in.PackagePath)
		pkg := ws.Packages[resolved]
		oldName := ""
		if pkg != nil {
			oldName = pkg.Name
		}
		plan, err := state.GetEngine().RenamePackage(ws, types.RenamePackageRequest{
			OldPackageName: oldName,
			NewPackageName: in.NewPackageName,
			PackagePath:    resolved,
			UpdateImports:  true,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "rename package → "+in.NewPackageName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "rename_method",
		Description: "Rename a method on a specific type (struct or interface). Updates all call sites and, for interfaces, all implementations.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RenameMethodInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		pkgPath := in.PackagePath
		if pkgPath != "" {
			pkgPath = types.ResolvePackagePath(ws, pkgPath)
		}
		plan, err := state.GetEngine().RenameMethod(ws, types.RenameMethodRequest{
			TypeName:      in.TypeName,
			MethodName:    in.MethodName,
			NewMethodName: in.NewMethodName,
			PackagePath:   pkgPath,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "rename method "+in.TypeName+"."+in.MethodName+" → "+in.NewMethodName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
