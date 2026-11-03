package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// refreshMiddleware re-syncs files changed on disk before every tool call, so
// each tool (dry run or apply) plans against current content instead of the
// parse from load time or the debounced watcher's last delivery.
func refreshMiddleware(state *MCPServer) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/call" {
				state.RefreshStaleFiles()
			}

			return next(ctx, method, req)
		}
	}
}
