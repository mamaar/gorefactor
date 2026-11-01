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
	TargetPackage string            `json:"target_package" jsonschema:"package path where the facade will be created - absolute, relative to the workspace root, or a module import path"`
	Exports       []ExportSpecInput `json:"exports" jsonschema:"list of symbols to re-export through the facade"`
	DryRun        bool              `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- generate_facades ---

type GenerateFacadesInput struct {
	ModulesDir  string   `json:"modules_dir" jsonschema:"directory containing the modules to generate facades for"`
	TargetDir   string   `json:"target_dir" jsonschema:"directory where facade packages will be created"`
	ExportTypes []string `json:"export_types,omitempty" jsonschema:"types of symbols to export (e.g. commands, models, events)"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- update_facades ---

type UpdateFacadesInput struct {
	FacadePackages []string `json:"facade_packages,omitempty" jsonschema:"list of facade package paths to update"`
	AutoDetect     bool     `json:"auto_detect,omitempty" jsonschema:"automatically detect facade packages to update"`
	DryRun         bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
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
			// Raw: a facade package is new, so it cannot resolve against the
			// existing package set. CreateFacadeOperation resolves it as a path.
			TargetPackage: in.TargetPackage,
			Exports:       exports,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "create facade", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "generate_facades",
		Description: "Auto-generate facade packages for a set of modules, re-exporting their public API. Symbols from every build configuration are re-exported: those declared in files with a build constraint, GOOS/GOARCH suffix or cgo go into facade_*.go files carrying the same constraint, so the facade builds wherever the source does. An existing generated facade is regenerated in place.",
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
		result, err := executePlanWithUnlock(state, plan, "generate facades", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "update_facades",
		Description: "Update existing facade packages to reflect changes in the underlying packages they re-export from, across every build configuration. Generated facade*.go files are regenerated whole and deleted when no longer needed; hand-written files are never touched.",
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
		result, err := executePlanWithUnlock(state, plan, "update facades", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
