package mcp

import (
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// PlanResult is the structured output returned by mutating refactoring tools.
type PlanResult struct {
	Description   string   `json:"description"`
	AffectedFiles []string `json:"affected_files"`
	ChangeCount   int      `json:"change_count"`
	ModifiedFiles []string `json:"modified_files"`
	Success       bool     `json:"success"`
}

// AnalysisResult is the structured output returned by read-only analysis tools.
type AnalysisResult struct {
	Description string `json:"description"`
	Data        any    `json:"data"`
}

// executePlan validates, executes, and returns a PlanResult for the given plan.
func executePlan(state *MCPServer, plan *types.RefactoringPlan, desc string) (*PlanResult, error) {
	if err := state.GetEngine().ExecutePlan(plan); err != nil {
		return nil, fmt.Errorf("execute plan: %w", err)
	}

	// Synchronously update workspace state with the changes we just wrote
	if err := state.SyncWorkspaceChanges(plan.AffectedFiles); err != nil {
		state.logger.Warn("workspace sync failed", "err", err)
		// Don't fail the operation - changes are already on disk
	}

	return &PlanResult{
		Description:   desc,
		AffectedFiles: plan.AffectedFiles,
		ChangeCount:   len(plan.Changes),
		ModifiedFiles: plan.AffectedFiles,
		Success:       true,
	}, nil
}

// executePlanWithUnlock releases the read lock before calling executePlan.
// This prevents deadlock when executePlan calls SyncWorkspaceChanges which needs a write lock.
// Use this when the caller holds a read lock with defer RUnlock().
func executePlanWithUnlock(state *MCPServer, plan *types.RefactoringPlan, desc string) (*PlanResult, error) {
	state.RUnlock()
	return executePlan(state, plan, desc)
}

// textResult is a convenience that marshals v to JSON and wraps it in a
// CallToolResult with a single TextContent block.
func textResult(v any) *mcpsdk.CallToolResult {
	b, _ := json.MarshalIndent(v, "", "  ")
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: string(b)},
		},
	}
}

// errResult returns a CallToolResult that signals an error.
func errResult(err error) *mcpsdk.CallToolResult {
	r := &mcpsdk.CallToolResult{}
	r.SetError(err)
	return r
}
