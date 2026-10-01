package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- move_by_dependencies ---

type MoveByDependenciesInput struct {
	MoveSharedTo string   `json:"move_shared_to,omitempty" jsonschema:"target directory for shared symbols (e.g. pkg/)"`
	KeepInternal []string `json:"keep_internal,omitempty" jsonschema:"packages that should stay internal"`
	AnalyzeOnly  bool     `json:"analyze_only,omitempty" jsonschema:"if true, only analyze and suggest moves without applying"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- organize_by_layers ---

type OrganizeByLayersInput struct {
	DomainLayer         string `json:"domain_layer,omitempty" jsonschema:"directory for domain layer (e.g. modules/)"`
	InfrastructureLayer string `json:"infrastructure_layer,omitempty" jsonschema:"directory for infrastructure layer (e.g. pkg/)"`
	ApplicationLayer    string `json:"application_layer,omitempty" jsonschema:"directory for application layer (e.g. internal/)"`
	ReorderImports      bool   `json:"reorder_imports,omitempty" jsonschema:"whether to reorder imports according to layers"`
	DryRun              bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

func registerDependencyTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_by_dependencies",
		Description: "Analyze the dependency graph and move symbols to reduce coupling. Shared symbols are moved to a common package. Candidates are identified in the default build configuration; each move is checked under every configuration and skipped, with the reason in skipped_sites, if it would break one. Every candidate whose move is acceptable on its own is moved in the same call, planned as one combined move; a candidate that is refused, or that uses another symbol moved in the same call from a different package, stays and is listed in skipped_sites.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MoveByDependenciesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().MoveByDependencies(ws, types.MoveByDependenciesRequest{
			Workspace:    ws.RootPath,
			MoveSharedTo: in.MoveSharedTo,
			KeepInternal: in.KeepInternal,
			AnalyzeOnly:  in.AnalyzeOnly,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		if in.AnalyzeOnly {
			state.RUnlock()
			return textResult(map[string]any{
				"description":    "dependency analysis",
				"affected_files": plan.AffectedFiles,
				"change_count":   len(plan.Changes),
				"impact":         plan.Impact,
			}), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move by dependencies", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "organize_by_layers",
		Description: "Organize packages according to an architectural layer structure (domain, infrastructure, application).",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in OrganizeByLayersInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().OrganizeByLayers(ws, types.OrganizeByLayersRequest{
			Workspace:           ws.RootPath,
			DomainLayer:         in.DomainLayer,
			InfrastructureLayer: in.InfrastructureLayer,
			ApplicationLayer:    in.ApplicationLayer,
			ReorderImports:      in.ReorderImports,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "organize by layers", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_cycles",
		Description: "Report production import cycles and import cycles through in-package test files, as import paths. Read-only; default build configuration only.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in struct{}) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}
		cycles, testCycles := state.GetEngine().DetectCycles(ws)
		return textResult(map[string]any{
			"cycles_found": len(cycles),
			"cycles":       cycles,
			"test_cycles":  testCycles,
		}), nil, nil
	})
}
