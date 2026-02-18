package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- batch_operations ---

type BatchOperationsInput struct {
	Operations        []string `json:"operations" jsonschema:"list of refactoring command strings to execute as a batch"`
	RollbackOnFailure bool     `json:"rollback_on_failure,omitempty" jsonschema:"rollback all operations if any single operation fails"`
}

func registerBatchTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "batch_operations",
		Description: "Execute multiple refactoring operations as an atomic batch. Optionally rolls back all changes if any operation fails.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in BatchOperationsInput) (*mcpsdk.CallToolResult, any, error) {
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
		result, err := executePlanWithUnlock(state, plan, "batch operations")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
