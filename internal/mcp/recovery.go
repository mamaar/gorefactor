package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// recoveryMiddleware turns a panic inside any MCP method handler (in
// particular, any of the ~40 tool handlers registered by RegisterAllTools)
// into a normal error result instead of letting it unwind past the SDK's
// per-request goroutine and take down the whole server process.
//
// Before this middleware existed, only move_symbol had its own ad-hoc
// recover() (see move_tools.go); every other tool category (rename,
// extract, inline, change_signature, delete, dependency, batch, fix,
// import, context, analysis, touch, workspace, facade) had none. A panic
// in any of those handlers killed the process and disconnected every
// client session, not just the one that triggered it - this is what
// actually caused the client disconnect for the move_symbol nil-pointer
// bug (see pkg/refactor/operations.go's getOrCreateTargetFile and
// pkg/watch/updater.go's recollectImports for the underlying bug fix).
// Wiring recovery once, centrally, via the SDK's receiving-middleware hook
// means every current and future tool is covered without needing to
// duplicate recover() boilerplate in each handler closure.
func recoveryMiddleware(logger *slog.Logger) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (result mcpsdk.Result, err error) {
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					logger.Error("panic in MCP method handler",
						"method", method,
						"panic", fmt.Sprintf("%v", r),
						"stack", string(stack),
					)
					result = nil
					err = fmt.Errorf("internal error (panic) in %s: %v", method, r)
				}
			}()
			return next(ctx, method, req)
		}
	}
}
