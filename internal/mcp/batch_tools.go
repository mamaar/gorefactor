package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- batch_operations ---

type BatchOperationsInput struct {
	Operations        []string `json:"operations" jsonschema:"steps, each a JSON object string with a type and that type's keys: rename_symbol (symbol, new_name, package optional); move_symbol (symbol, from_package, to_package); rename_method (type_name, method_name, new_method_name, package_path optional); rename_field (type_name, field_name, new_field_name, package_path optional); rename_package (package_path, new_name, old_name optional); move_package (source, target)"`
	RollbackOnFailure bool     `json:"rollback_on_failure,omitempty" jsonschema:"rollback all operations if any single operation fails"`
	DryRun            bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

func registerBatchTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "batch_operations",
		Description: "Execute multiple refactoring operations as an atomic batch. Every step is planned against the unmodified workspace and nothing is written unless all steps plan; steps whose edits overlap fail the batch; for example, two move_symbol steps that share a target package or a referring file overlap and must be run as separate calls. " +
			"rename_symbol, rename_method and rename_field steps are planned under every build configuration (GOOS/GOARCH variants, custom build tags) that covers the workspace, " +
			"and move_symbol is planned for the default configuration and checked under the others, as in the standalone tools. " +
			"rename_package and move_package steps act on the default build configuration only and can miss files excluded by build constraints. " +
			"A batch is refused when a rename_package or move_package step shares a package with any other step (as a symbol, method or field package, a move's source or target, or another package step), " +
			"or when a step names no package at all next to a package step; run those steps as separate calls.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in BatchOperationsInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().BatchOperations(ws, types.BatchOperationRequest{
			Operations:        in.Operations,
			RollbackOnFailure: in.RollbackOnFailure,
			DryRun:            false,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "batch operations", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
