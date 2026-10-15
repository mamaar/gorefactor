package mcp

import (
	"context"
	"path/filepath"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- clean_aliases ---

type CleanAliasesInput struct {
	PreserveConflicts bool `json:"preserve_conflicts,omitempty" jsonschema:"keep aliases only where needed to resolve naming conflicts"`
	DryRun            bool `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- standardize_imports ---

type AliasRuleInput struct {
	PackagePattern string `json:"package_pattern" jsonschema:"Go import path pattern to match"`
	Alias          string `json:"alias" jsonschema:"alias to apply for matching imports"`
}

type StandardizeImportsInput struct {
	Rules  []AliasRuleInput `json:"rules" jsonschema:"list of alias rules to apply"`
	Files  []string         `json:"files,omitempty" jsonschema:"limit the change to these .go files (absolute or workspace-relative); default: the whole workspace"`
	DryRun bool             `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- resolve_alias_conflicts ---

type ResolveAliasConflictsInput struct {
	Strategy string `json:"strategy,omitempty" jsonschema:"conflict resolution strategy: full_names, shortest_unique, or custom_alias (default: full_names)"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- convert_aliases ---

type ConvertAliasesInput struct {
	ToFullNames   bool `json:"to_full_names,omitempty" jsonschema:"convert short imports to fully-qualified alias names"`
	FromFullNames bool `json:"from_full_names,omitempty" jsonschema:"convert fully-qualified alias names back to short imports"`
	DryRun        bool `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
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
		result, err := executePlanWithUnlock(state, plan, "clean aliases", in.DryRun)
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
			Files:     absPaths(ws.RootPath, in.Files),
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "standardize imports", in.DryRun)
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
		result, err := executePlanWithUnlock(state, plan, "resolve alias conflicts", in.DryRun)
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
		result, err := executePlanWithUnlock(state, plan, "convert aliases", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}

// absPaths resolves workspace-relative paths against root.
func absPaths(root string, paths []string) []string {
	abs := make([]string, len(paths))
	for i, p := range paths {
		abs[i] = p
		if !filepath.IsAbs(p) {
			abs[i] = filepath.Join(root, p)
		}
	}
	return abs
}
