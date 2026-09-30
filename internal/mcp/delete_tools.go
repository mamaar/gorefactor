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
func safeDeleteConfirmMessage(symbol string, refs []types.Blocker) string {
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
		fmt.Fprintf(&b, "  %s:%d%s\n", ref.File, ref.Line, ref.Note())
	}
	if len(refs) > maxListedRefs {
		fmt.Fprintf(&b, "  …and %d more\n", len(refs)-maxListedRefs)
	}
	b.WriteString("\nDelete anyway?")
	return b.String()
}

// safeDeleteRequest resolves in against the loaded workspace. The caller holds
// the read lock.
func safeDeleteRequest(state *MCPServer, in SafeDeleteInput) (types.SafeDeleteRequest, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return types.SafeDeleteRequest{}, err
	}
	return types.SafeDeleteRequest{Symbol: in.Symbol, SourceFile: resolveFile(ws, in.SourceFile), Force: in.Force}, nil
}

// safeDeleteBlockers returns what blocks deleting in's symbol under any build
// configuration.
func safeDeleteBlockers(state *MCPServer, in SafeDeleteInput) ([]types.Blocker, error) {
	state.RLock()
	defer state.RUnlock()
	req, err := safeDeleteRequest(state, in)
	if err != nil {
		return nil, err
	}
	return state.GetEngine().SafeDeleteBlockingReferences(req)
}

// planSafeDelete plans in under every build configuration. The read lock stays
// held until the merged plan exists: a watcher batch takes the write lock and
// may drop a view being planned in.
func planSafeDelete(state *MCPServer, in SafeDeleteInput) (*types.RefactoringPlan, error) {
	state.RLock()
	defer state.RUnlock()
	req, err := safeDeleteRequest(state, in)
	if err != nil {
		return nil, err
	}
	return state.GetEngine().SafeDeleteAcrossConfigs(req)
}

// confirmSafeDelete asks the user whether to delete past the blockers of an
// unforced, applying delete. It reports whether the delete is to be forced;
// a client that cannot be asked gets the unforced delete and its refusal.
func confirmSafeDelete(ctx context.Context, sess *mcpsdk.ServerSession, state *MCPServer, in SafeDeleteInput) (bool, error) {
	if in.Force || in.DryRun {
		return in.Force, nil
	}
	refs, err := safeDeleteBlockers(state, in)
	if err != nil || len(refs) == 0 {
		return false, err
	}
	// No lock is held here: the dialog may stay open indefinitely.
	accepted, err := confirm(ctx, sess, safeDeleteConfirmMessage(in.Symbol, refs))
	switch {
	case errors.Is(err, errElicitUnsupported):
		return false, nil
	case err != nil:
		return false, err
	case !accepted:
		return false, fmt.Errorf("deletion of %q cancelled: user did not confirm removing %d blocking reference(s)", in.Symbol, len(refs))
	}
	return true, nil
}

func registerDeleteTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "safe_delete",
		Description: "Safely delete a symbol (function, type, variable, constant, method, struct field). If references exist, the human user is asked to confirm; the deletion is refused unless they accept or force is true. Surviving call sites are reported in skipped_sites, not rewritten. " +
			"References are looked for under every build configuration (GOOS/GOARCH variants, custom build tags) that covers the workspace, a declaration of the symbol in each configuration is deleted, and a mention in a file no configuration type-checks, or one the type checker could not resolve in a file with a type error, counts as a reference.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in SafeDeleteInput) (*mcpsdk.CallToolResult, any, error) {
		force, err := confirmSafeDelete(ctx, req.Session, state, in)
		if err != nil {
			return errResult(err), nil, nil
		}
		in.Force = force
		plan, err := planSafeDelete(state, in)
		if err != nil {
			return errResult(err), nil, nil
		}
		result, err := executePlan(state, plan, "safe delete "+in.Symbol, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
