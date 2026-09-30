package mcp

import (
	"context"
	"path/filepath"
	"sort"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/analysis"
)

// --- load_workspace ---

type LoadWorkspaceInput struct {
	Path string `json:"path" jsonschema:"absolute path to workspace root (go.mod directory)"`
}

type LoadWorkspaceOutput struct {
	Module              string              `json:"module"`
	PackageCount        int                 `json:"package_count"`
	RootPath            string              `json:"root_path"`
	ReferenceIndexBuilt bool                `json:"reference_index_built"`
	BuildConfigs        []BuildConfigInfo   `json:"build_configs"`
	UncoveredFiles      []UncoveredFileInfo `json:"uncovered_files,omitempty"`
}

// BuildConfigInfo describes one configuration of the covering set.
type BuildConfigInfo struct {
	Name   string   `json:"name"`
	GOOS   string   `json:"goos"`
	GOARCH string   `json:"goarch"`
	Cgo    bool     `json:"cgo"`
	Tags   []string `json:"tags"`
	Files  int      `json:"files"`
}

// UncoveredFileInfo is a file no configuration of the covering set includes,
// or one that each configuration including it could not type-check.
type UncoveredFileInfo struct {
	File       string   `json:"file"`
	Constraint string   `json:"constraint,omitempty"`
	Configs    []string `json:"configs,omitempty"`
	Reason     string   `json:"reason"`
}

func buildConfigInfos(cov analysis.Covering) []BuildConfigInfo {
	out := make([]BuildConfigInfo, len(cov.Configs))
	for i, c := range cov.Configs {
		tags := c.Tags
		if tags == nil {
			tags = []string{}
		}
		out[i] = BuildConfigInfo{Name: c.String(), GOOS: c.GOOS, GOARCH: c.GOARCH, Cgo: c.Cgo, Tags: tags, Files: cov.Files[i]}
	}
	return out
}

func uncoveredFileInfos(cov analysis.Covering, root string) []UncoveredFileInfo {
	var out []UncoveredFileInfo
	for _, u := range cov.Uncovered {
		out = append(out, UncoveredFileInfo{File: relativeTo(root, u.Path), Constraint: u.Constraint, Reason: u.Reason})
	}
	return out
}

// relativeTo returns path relative to root with forward slashes, or path
// itself when it cannot be made relative.
func relativeTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// --- workspace_status ---

type WorkspaceStatusInput struct{}

type WorkspaceStatusOutput struct {
	Loaded       bool     `json:"loaded"`
	Module       string   `json:"module,omitempty"`
	RootPath     string   `json:"root_path,omitempty"`
	PackageCount int      `json:"package_count"`
	Packages     []string `json:"packages,omitempty"`

	BuildConfigs   []BuildConfigInfo   `json:"build_configs,omitempty"`
	UncoveredFiles []UncoveredFileInfo `json:"uncovered_files,omitempty"`
}

func registerWorkspaceTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "load_workspace",
		Description: "Load a Go workspace into memory for refactoring. Must be called before any other tool. Reports the build configurations covering the workspace as build_configs and the files none of them includes as uncovered_files.",
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
		cov := state.Covering()
		out.BuildConfigs = buildConfigInfos(cov)
		out.UncoveredFiles = uncoveredFileInfos(cov, ws.RootPath)
		return textResult(out), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "workspace_status",
		Description: "Return the current workspace status: loaded state, module name, package count, package list, build_configs and uncovered_files.",
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
		cov := state.Covering()
		out.BuildConfigs = buildConfigInfos(cov)
		out.UncoveredFiles = uncoveredFileInfos(cov, ws.RootPath)
		return textResult(out), nil, nil
	})
}
