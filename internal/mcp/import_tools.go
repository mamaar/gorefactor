package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- clean_aliases ---

type CleanAliasesInput struct {
	PreserveConflicts bool `json:"preserve_conflicts,omitempty" jsonschema:"keep aliases only where needed to resolve naming conflicts"`
}

// --- standardize_imports ---

type AliasRuleInput struct {
	PackagePattern string `json:"package_pattern" jsonschema:"Go import path pattern to match"`
	Alias          string `json:"alias" jsonschema:"alias to apply for matching imports"`
}

type StandardizeImportsInput struct {
	Rules []AliasRuleInput `json:"rules" jsonschema:"list of alias rules to apply"`
}

// --- resolve_alias_conflicts ---

type ResolveAliasConflictsInput struct {
	Strategy string `json:"strategy,omitempty" jsonschema:"conflict resolution strategy: full_names, shortest_unique, or custom_alias (default: full_names)"`
}

// --- convert_aliases ---

type ConvertAliasesInput struct {
	ToFullNames   bool `json:"to_full_names,omitempty" jsonschema:"convert short imports to fully-qualified alias names"`
	FromFullNames bool `json:"from_full_names,omitempty" jsonschema:"convert fully-qualified alias names back to short imports"`
}

func registerImportTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "clean_aliases",
		Description: "Remove unnecessary import aliases across the workspace. Optionally preserves aliases that resolve naming conflicts.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in CleanAliasesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().CleanAliases(ws, types.CleanAliasesRequest{
			Workspace:         ws.RootPath,
			PreserveConflicts: in.PreserveConflicts,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "clean aliases")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "standardize_imports",
		Description: "Standardize import aliases according to a set of rules. Each rule maps an import path pattern to a required alias.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in StandardizeImportsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		rules := make([]types.AliasRule, len(in.Rules))
		for i, r := range in.Rules {
			rules[i] = types.AliasRule{
				PackagePattern: r.PackagePattern,
				Alias:          r.Alias,
			}
		}
		plan, err := state.GetEngine().StandardizeImports(ws, types.StandardizeImportsRequest{
			Workspace: ws.RootPath,
			Rules:     rules,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "standardize imports")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "resolve_alias_conflicts",
		Description: "Resolve import alias conflicts where two packages have the same default name.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ResolveAliasConflictsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		strategy := types.UseFullNames
		switch in.Strategy {
		case "shortest_unique":
			strategy = types.UseShortestUnique
		case "custom_alias":
			strategy = types.UseCustomAlias
		}
		plan, err := state.GetEngine().ResolveAliasConflicts(ws, types.ResolveAliasConflictsRequest{
			Workspace: ws.RootPath,
			Strategy:  strategy,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "resolve alias conflicts")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "convert_aliases",
		Description: "Convert between aliased and non-aliased import styles across the workspace.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ConvertAliasesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().ConvertAliases(ws, types.ConvertAliasesRequest{
			Workspace:     ws.RootPath,
			ToFullNames:   in.ToFullNames,
			FromFullNames: in.FromFullNames,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "convert aliases")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
