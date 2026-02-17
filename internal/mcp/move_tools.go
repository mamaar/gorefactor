package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- move_symbol ---

type MoveSymbolInput struct {
	Symbol      string `json:"symbol" jsonschema:"symbol name to move"`
	FromPackage string `json:"from_package" jsonschema:"source package path (relative to workspace root)"`
	ToPackage   string `json:"to_package" jsonschema:"target package path (relative to workspace root)"`
}

// --- move_package ---

type MovePackageInput struct {
	SourcePackage string `json:"source_package" jsonschema:"source package path"`
	TargetPackage string `json:"target_package" jsonschema:"target package path"`
}

// --- move_dir ---

type MoveDirInput struct {
	SourceDir         string `json:"source_dir" jsonschema:"source directory"`
	TargetDir         string `json:"target_dir" jsonschema:"target directory"`
	PreserveStructure bool   `json:"preserve_structure,omitempty" jsonschema:"preserve directory structure"`
}

// --- move_packages ---

type PackageMappingInput struct {
	Source string `json:"source" jsonschema:"source package path"`
	Target string `json:"target" jsonschema:"target package path"`
}

type MovePackagesInput struct {
	Packages  []PackageMappingInput `json:"packages" jsonschema:"list of source→target package mappings"`
	TargetDir string                `json:"target_dir,omitempty" jsonschema:"common target directory (used when packages list uses relative targets)"`
}

func registerMoveTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_symbol",
		Description: "Move a symbol (function, type, variable, constant) from one package to another. Updates all references across the workspace.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MoveSymbolInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		from := types.ResolvePackagePath(ws, in.FromPackage)
		to := types.ResolvePackagePath(ws, in.ToPackage)
		plan, err := state.GetEngine().MoveSymbol(ws, types.MoveSymbolRequest{
			SymbolName:  in.Symbol,
			FromPackage: from,
			ToPackage:   to,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move symbol "+in.Symbol)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_package",
		Description: "Move an entire package to a new location. Updates all import paths across the workspace.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MovePackageInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		src := types.ResolvePackagePath(ws, in.SourcePackage)
		tgt := types.ResolvePackagePath(ws, in.TargetPackage)
		plan, err := state.GetEngine().MovePackage(ws, types.MovePackageRequest{
			SourcePackage: src,
			TargetPackage: tgt,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move package")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_dir",
		Description: "Move a directory (and all packages inside it) to a new location.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MoveDirInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().MoveDir(ws, types.MoveDirRequest{
			SourceDir:         in.SourceDir,
			TargetDir:         in.TargetDir,
			PreserveStructure: in.PreserveStructure,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move directory")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_packages",
		Description: "Move multiple packages atomically. All import references are updated in a single operation.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MovePackagesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		mappings := make([]types.PackageMapping, len(in.Packages))
		for i, m := range in.Packages {
			mappings[i] = types.PackageMapping{
				SourcePackage: types.ResolvePackagePath(ws, m.Source),
				TargetPackage: types.ResolvePackagePath(ws, m.Target),
			}
		}
		plan, err := state.GetEngine().MovePackages(ws, types.MovePackagesRequest{
			Packages:  mappings,
			TargetDir: in.TargetDir,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move packages")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
