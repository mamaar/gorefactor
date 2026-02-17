package mcp

import (
	"context"
	"sort"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- load_workspace ---

type LoadWorkspaceInput struct {
	Path string `json:"path" jsonschema:"absolute path to workspace root (go.mod directory)"`
}

type LoadWorkspaceOutput struct {
	Module             string `json:"module"`
	PackageCount       int    `json:"package_count"`
	RootPath           string `json:"root_path"`
	ReferenceIndexBuilt bool   `json:"reference_index_built"`
}

// --- workspace_status ---

type WorkspaceStatusInput struct{}

type WorkspaceStatusOutput struct {
	Loaded       bool     `json:"loaded"`
	Module       string   `json:"module,omitempty"`
	RootPath     string   `json:"root_path,omitempty"`
	PackageCount int      `json:"package_count"`
	Packages     []string `json:"packages,omitempty"`
}

func registerWorkspaceTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "load_workspace",
		Description: "Load a Go workspace into memory for refactoring. Must be called before any other tool.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in LoadWorkspaceInput) (*mcpsdk.CallToolResult, any, error) {
		indexBuilt, err := state.LoadWorkspace(ctx, in.Path)
		if err != nil {
			return errResult(err), nil, nil
		}
		ws, _ := state.GetWorkspace()
		out := LoadWorkspaceOutput{
			PackageCount:        len(ws.Packages),
			RootPath:            ws.RootPath,
			ReferenceIndexBuilt: indexBuilt,
		}
		if ws.Module != nil {
			out.Module = ws.Module.Path
		}
		return textResult(out), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "workspace_status",
		Description: "Return the current workspace status: loaded state, module name, package count, and package list.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in WorkspaceStatusInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return textResult(WorkspaceStatusOutput{Loaded: false}), nil, nil
		}
		out := WorkspaceStatusOutput{
			Loaded:       true,
			RootPath:     ws.RootPath,
			PackageCount: len(ws.Packages),
		}
		if ws.Module != nil {
			out.Module = ws.Module.Path
		}
		for _, pkg := range ws.Packages {
			out.Packages = append(out.Packages, pkg.ImportPath)
		}
		sort.Strings(out.Packages)
		return textResult(out), nil, nil
	})
}
