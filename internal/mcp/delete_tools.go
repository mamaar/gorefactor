package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- safe_delete ---

type SafeDeleteInput struct {
	Symbol     string `json:"symbol" jsonschema:"name of the symbol to delete"`
	SourceFile string `json:"source_file" jsonschema:"file containing the symbol declaration"`
	Force      bool   `json:"force,omitempty" jsonschema:"delete even if references exist (removes references too)"`
}

func registerDeleteTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "safe_delete",
		Description: "Safely delete a symbol (function, type, variable, constant). Refuses to delete if references exist unless force is true.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in SafeDeleteInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		plan, err := state.GetEngine().SafeDelete(ws, types.SafeDeleteRequest{
			Symbol:     in.Symbol,
			SourceFile: resolveFile(ws, in.SourceFile),
			Force:      in.Force,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "safe delete "+in.Symbol)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
