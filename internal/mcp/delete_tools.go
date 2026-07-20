package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- safe_delete ---

type SafeDeleteInput struct {
	Symbol     string `json:"symbol" jsonschema:"name of the symbol to delete"`
	SourceFile string `json:"source_file" jsonschema:"file containing the symbol declaration"`
	Force      bool   `json:"force,omitempty" jsonschema:"delete even if references exist (surviving call sites are reported in skipped_sites, not rewritten)"`
	DryRun     bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// maxListedRefs bounds how many blocking references are spelled out in the
// elicitation dialog, so a symbol with hundreds of call sites still renders
// a small, readable prompt.
const maxListedRefs = 10

// safeDeleteConfirmMessage builds the message shown to the human user when a
// safe_delete would break one or more live references and force/dry_run
// were not set, so we need to ask before deleting.
func safeDeleteConfirmMessage(symbol string, refs []types.Reference) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Deleting %q will break %d reference", symbol, len(refs))
	if len(refs) != 1 {
		b.WriteByte('s')
	}
	b.WriteString(":\n")

	n := len(refs)
	if n > maxListedRefs {
		n = maxListedRefs
	}
	for _, ref := range refs[:n] {
		fmt.Fprintf(&b, "  %s:%d\n", ref.File, ref.Line)
	}
	if len(refs) > maxListedRefs {
		fmt.Fprintf(&b, "  …and %d more\n", len(refs)-maxListedRefs)
	}
	b.WriteString("\nDelete anyway?")
	return b.String()
}

func registerDeleteTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "safe_delete",
		Description: "Safely delete a symbol (function, type, variable, constant). If references exist, the human user is asked to confirm; the deletion is refused unless they accept or force is true. Surviving call sites are reported in skipped_sites, not rewritten.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in SafeDeleteInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		deleteReq := types.SafeDeleteRequest{
			Symbol:     in.Symbol,
			SourceFile: resolveFile(ws, in.SourceFile),
			Force:      in.Force,
		}

		// Only ask the user when the delete would actually run unattended
		// (not forced, not a dry run) and something would block it.
		if !in.Force && !in.DryRun {
			refs, err := state.GetEngine().SafeDeleteBlockingReferences(ws, deleteReq)
			if err != nil {
				state.RUnlock()
				return errResult(err), nil, nil
			}

			if len(refs) > 0 {
				// Never hold the read lock while waiting on a human - the
				// dialog may sit open indefinitely and would otherwise
				// stall every other workspace operation.
				state.RUnlock()

				accepted, cerr := confirm(ctx, req.Session, safeDeleteConfirmMessage(in.Symbol, refs))
				switch {
				case cerr != nil && !errors.Is(cerr, errElicitUnsupported):
					// A real transport/protocol error - surface it.
					return errResult(cerr), nil, nil

				case cerr == nil && !accepted:
					// User explicitly declined or cancelled the dialog.
					// Nothing was written.
					return errResult(fmt.Errorf("deletion of %q cancelled: user did not confirm removing %d blocking reference(s)", in.Symbol, len(refs))), nil, nil

				case cerr == nil && accepted:
					// User confirmed - re-acquire the lock and re-fetch the
					// workspace, since it may have been reloaded while the
					// dialog was open, then force the delete through.
					state.RLock()
					ws, err = state.GetWorkspace()
					if err != nil {
						state.RUnlock()
						return errResult(err), nil, nil
					}
					forcedReq := deleteReq
					forcedReq.Force = true
					plan, err := state.GetEngine().SafeDelete(ws, forcedReq)
					if err != nil {
						state.RUnlock()
						return errResult(err), nil, nil
					}
					result, err := executePlanWithUnlock(state, plan, "safe delete "+in.Symbol, in.DryRun)
					if err != nil {
						return errResult(err), nil, nil
					}
					return textResult(result), nil, nil
				}

				// errors.Is(cerr, errElicitUnsupported): the client cannot
				// be asked. Fall through to the ordinary (non-forced)
				// SafeDelete call below so the caller sees exactly today's
				// error message.
				state.RLock()
				ws, err = state.GetWorkspace()
				if err != nil {
					state.RUnlock()
					return errResult(err), nil, nil
				}
			}
		}

		plan, err := state.GetEngine().SafeDelete(ws, deleteReq)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "safe delete "+in.Symbol, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
