package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- create_facade ---

type ExportSpecInput struct {
	SourcePackage string `json:"source_package" jsonschema:"import path of the package to re-export from"`
	SymbolName    string `json:"symbol_name" jsonschema:"name of the symbol to export"`
	Alias         string `json:"alias,omitempty" jsonschema:"optional alias for the re-exported symbol"`
}

type CreateFacadeInput struct {
	TargetPackage string            `json:"target_package" jsonschema:"package path where the facade will be created"`
	Exports       []ExportSpecInput `json:"exports" jsonschema:"list of symbols to re-export through the facade"`
}

// --- generate_facades ---

type GenerateFacadesInput struct {
	ModulesDir  string   `json:"modules_dir" jsonschema:"directory containing the modules to generate facades for"`
	TargetDir   string   `json:"target_dir" jsonschema:"directory where facade packages will be created"`
	ExportTypes []string `json:"export_types,omitempty" jsonschema:"types of symbols to export (e.g. commands, models, events)"`
}

// --- update_facades ---

type UpdateFacadesInput struct {
	FacadePackages []string `json:"facade_packages,omitempty" jsonschema:"list of facade package paths to update"`
	AutoDetect     bool     `json:"auto_detect,omitempty" jsonschema:"automatically detect facade packages to update"`
}

func registerFacadeTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "create_facade",
		Description: "Create a facade package that re-exports symbols from other packages, providing a unified public API.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in CreateFacadeInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		exports := make([]types.ExportSpec, len(in.Exports))
		for i, e := range in.Exports {
			exports[i] = types.ExportSpec{
				SourcePackage: e.SourcePackage,
				SymbolName:    e.SymbolName,
				Alias:         e.Alias,
			}
		}
		plan, err := state.GetEngine().CreateFacade(ws, types.CreateFacadeRequest{
			TargetPackage: types.ResolvePackagePath(ws, in.TargetPackage),
			Exports:       exports,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "create facade")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "generate_facades",
		Description: "Auto-generate facade packages for a set of modules, re-exporting their public API.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in GenerateFacadesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().GenerateFacades(ws, types.GenerateFacadesRequest{
			ModulesDir:  in.ModulesDir,
			TargetDir:   in.TargetDir,
			ExportTypes: in.ExportTypes,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "generate facades")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "update_facades",
		Description: "Update existing facade packages to reflect changes in the underlying packages they re-export from.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in UpdateFacadesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().UpdateFacades(ws, types.UpdateFacadesRequest{
			FacadePackages: in.FacadePackages,
			AutoDetect:     in.AutoDetect,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "update facades")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
