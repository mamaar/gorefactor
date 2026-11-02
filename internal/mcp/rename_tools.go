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
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- rename_package ---

type RenamePackageInput struct {
	PackagePath    string `json:"package_path" jsonschema:"path to the package directory"`
	NewPackageName string `json:"new_package_name" jsonschema:"new package name"`
	DryRun         bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- rename_method ---

type RenameMethodInput struct {
	TypeName      string `json:"type_name" jsonschema:"name of the type that owns the method"`
	MethodName    string `json:"method_name" jsonschema:"current method name"`
	NewMethodName string `json:"new_method_name" jsonschema:"new method name"`
	PackagePath   string `json:"package_path,omitempty" jsonschema:"package path (empty for workspace-wide)"`
	DryRun        bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- rename_field ---

type RenameFieldInput struct {
	TypeName     string `json:"type_name" jsonschema:"name of the struct type that owns the field"`
	FieldName    string `json:"field_name" jsonschema:"current field name"`
	NewFieldName string `json:"new_field_name" jsonschema:"new field name"`
	PackagePath  string `json:"package_path,omitempty" jsonschema:"package path (empty for workspace-wide)"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

func registerRenameTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "rename_symbol",
		Description: "Rename a symbol (function, type, variable, constant) across the workspace. All references are updated, under every build configuration (GOOS/GOARCH variants, custom build tags) that covers the workspace; a declaration with the same name in several configurations is renamed in all of them. Only package-level symbols are addressable; function-local constants, types and variables are not. A method, named Type.Method or bare, is renamed the way rename_method renames it.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RenameSymbolInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()
		plan, err := planRenameSymbol(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}
		result, err := executePlan(state, plan, "rename "+in.Symbol+" → "+in.NewName, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "rename_package",
		Description: "Rename a Go package. Updates the package declaration in all files and import statements across the workspace. Considers every .go file in the workspace regardless of build constraints; it rewrites only the package clauses and the importers' qualified references. " +
			"Uses of the old package name are renamed with type information; a file that cannot be renamed safely keeps the old name as an import alias and is listed in skipped_sites. Aliased and dot imports are only re-pathed.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RenamePackageInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().RenamePackage(ws, types.RenamePackageRequest{
			NewPackageName: in.NewPackageName,
			PackagePath:    in.PackagePath,
			UpdateImports:  true,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "rename package → "+in.NewPackageName, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "rename_method",
		Description: "Rename a method on a specific type (struct or interface). Uses are matched by go/types object identity: " +
			"calls, method values and method expressions of that method, including promoted calls through embedding; same-named " +
			"package functions and methods of other types are left untouched. The workspace interface methods the method " +
			"satisfies and their implementations are renamed with it; the rename is refused when the method satisfies an " +
			"interface declared outside the workspace (e.g. fmt.Stringer), or when it is a standard method (String() string, " +
			"MarshalJSON() ([]byte, error) and the other signatures go vet checks), or when the new name with the method's signature would be one (Name() string renamed to String). Planned under every build configuration " +
			"(GOOS/GOARCH variants, custom build tags) that covers the workspace.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RenameMethodInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()
		plan, err := planRenameMethod(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}
		result, err := executePlan(state, plan, "rename method "+in.TypeName+"."+in.MethodName+" → "+in.NewMethodName, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "rename_field",
		Description: "Rename a struct field on a specific named type. Updates the field declaration, selector expressions " +
			"(x.Field), keyed composite literals ({Field: value}), and promoted access through embedding. References are " +
			"matched by go/types object identity, so fields with the same name on other, unrelated types are left " +
			"untouched. The rename is planned under every build configuration (GOOS/GOARCH variants, custom build tags) " +
			"that covers the workspace, so a struct declared once per configuration has its field renamed in each. " +
			"To rename an embedded (anonymous) field, use rename_symbol on the embedded type instead — an " +
			"embedded field's name is the type's name, so renaming it is a type rename, not a field rename.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RenameFieldInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()
		plan, err := planRenameField(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}
		result, err := executePlan(state, plan, "rename field "+in.TypeName+"."+in.FieldName+" → "+in.NewFieldName, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}

// planRenameField plans in under every build configuration. The caller holds
// the read lock.
func planRenameField(state *MCPServer, in RenameFieldInput) (*types.RefactoringPlan, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, err
	}
	pkgPath := in.PackagePath
	if pkgPath != "" {
		pkgPath = types.ResolvePackagePath(ws, pkgPath)
	}
	return state.GetEngine().RenameFieldAcrossConfigs(types.RenameFieldRequest{
		TypeName:     in.TypeName,
		FieldName:    in.FieldName,
		NewFieldName: in.NewFieldName,
		PackagePath:  pkgPath,
	})
}

// planRenameSymbol plans in under every build configuration. The caller holds
// the read lock.
func planRenameSymbol(state *MCPServer, in RenameSymbolInput) (*types.RefactoringPlan, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, err
	}
	scope := types.WorkspaceScope
	pkg := in.Package
	if pkg != "" {
		pkg = types.ResolvePackagePath(ws, pkg)
		scope = types.PackageScope
	}
	return state.GetEngine().RenameSymbolAcrossConfigs(types.RenameSymbolRequest{
		SymbolName: in.Symbol,
		NewName:    in.NewName,
		Package:    pkg,
		Scope:      scope,
	})
}

// planRenameMethod plans in under every build configuration. The caller holds
// the read lock.
func planRenameMethod(state *MCPServer, in RenameMethodInput) (*types.RefactoringPlan, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, err
	}
	pkgPath := in.PackagePath
	if pkgPath != "" {
		pkgPath = types.ResolvePackagePath(ws, pkgPath)
	}
	return state.GetEngine().RenameMethodAcrossConfigs(types.RenameMethodRequest{
		TypeName:      in.TypeName,
		MethodName:    in.MethodName,
		NewMethodName: in.NewMethodName,
		PackagePath:   pkgPath,
	})
}
