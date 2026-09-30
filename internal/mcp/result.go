package mcp

import (
	"encoding/json"
	"fmt"
	"sort"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/types"
)

// PlanResult is the structured output returned by mutating refactoring tools.
type PlanResult struct {
	Description   string   `json:"description"`
	AffectedFiles []string `json:"affected_files"`
	ChangeCount   int      `json:"change_count"`
	ModifiedFiles []string `json:"modified_files"`
	Success       bool     `json:"success"`
	// SkippedSites lists call sites that could not be automatically rewritten.
	// Each entry must be updated manually before the build will succeed.
	SkippedSites []types.SkippedSite `json:"skipped_sites,omitempty"`
	// BuildConfigs names the build configurations the plan was computed under,
	// primary first. Omitted when there is only one.
	BuildConfigs []string `json:"build_configs,omitempty"`
	// UncoveredFiles lists the files in the packages the plan reached that no
	// build configuration type-checks. The plan holds no edits for them.
	UncoveredFiles []UncoveredFileInfo `json:"uncovered_files,omitempty"`

	// DryRun is true when the plan was previewed rather than applied. Nothing
	// was written to disk and ModifiedFiles is empty.
	DryRun bool `json:"dry_run,omitempty"`
	// Diffs holds the full before/after text of every change the plan would
	// make. Populated only on a dry run - it is the artifact the caller
	// reviews before re-running with dry_run=false.
	Diffs []FileDiff `json:"diffs,omitempty"`
	// BlockingIssue is set on a dry run when the plan would be rejected at
	// apply time (validation failure or a critical impact issue). A dry run
	// reports this instead of returning an error so the caller can still see
	// the diffs that led to it.
	BlockingIssue string `json:"blocking_issue,omitempty"`
}

// FileDiff groups every planned change to a single file.
type FileDiff struct {
	File string `json:"file"`
	// Action is "modify" or "delete".
	Action string     `json:"action"`
	Hunks  []DiffHunk `json:"hunks,omitempty"`
}

// DiffHunk is one planned edit, with its text untruncated so the reviewer sees
// the actual code rather than a summary.
type DiffHunk struct {
	Description string `json:"description,omitempty"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Old         string `json:"old,omitempty"`
	New         string `json:"new,omitempty"`
}

// maxHunkText caps a single hunk's before/after text. Generous enough to show
// a whole function, bounded so a package-wide rewrite cannot blow up the
// caller's context. Truncation is marked inline so it is never mistaken for
// the real content.
const maxHunkText = 8000

func truncateHunk(s string) string {
	if len(s) <= maxHunkText {
		return s
	}
	return s[:maxHunkText] + fmt.Sprintf("\n... [truncated, %d more bytes]", len(s)-maxHunkText)
}

// buildDiffs groups changes by file and sorts them into apply order.
func buildDiffs(changes []types.Change) []FileDiff {
	byFile := make(map[string][]types.Change)
	for _, c := range changes {
		byFile[c.File] = append(byFile[c.File], c)
	}

	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	diffs := make([]FileDiff, 0, len(files))
	for _, f := range files {
		fileChanges := byFile[f]
		sort.SliceStable(fileChanges, func(i, j int) bool {
			return fileChanges[i].Start < fileChanges[j].Start
		})

		fd := FileDiff{File: f, Action: "modify"}
		for _, c := range fileChanges {
			if c.Delete {
				fd.Action = "delete"
				fd.Hunks = nil
				break
			}
			fd.Hunks = append(fd.Hunks, DiffHunk{
				Description: c.Description,
				Start:       c.Start,
				End:         c.End,
				Old:         truncateHunk(c.OldText),
				New:         truncateHunk(c.NewText),
			})
		}
		diffs = append(diffs, fd)
	}
	return diffs
}

// previewPlan renders a plan without touching the filesystem. It runs the same
// validation ExecutePlan would run, but reports a failure as BlockingIssue
// rather than an error so the diffs are still returned for review.
func previewPlan(state *MCPServer, plan *types.RefactoringPlan, desc string) *PlanResult {
	// Backfill OldText so every tool's preview shows a real before/after pair.
	//
	// Operations in pkg/refactor set OldText as they construct each change, but
	// analyzer-driven tools build theirs from analysis.Diagnostic suggested
	// fixes via analyzers.DiagnosticsToChanges, which only ever sets NewText.
	// Those handlers were each expected to remember a follow-up
	// analyzers.FillOldText call, and several did not — so their dry runs
	// rendered a "diff" with no before side at all, silently violating the
	// documented contract that a preview returns the full before/after text of
	// every planned change. Doing it here instead makes the contract uniform
	// for every mutating tool, present and future, rather than depending on
	// each handler opting in.
	//
	// FillOldText is idempotent (it skips changes that already carry OldText)
	// and deliberately leaves pure insertions alone: it requires Start < End,
	// so a hunk that inserts at a point keeps an empty "old" because there
	// genuinely is no prior text there.
	if ws, err := state.GetWorkspace(); err == nil {
		plan.Changes = analyzers.FillOldText(ws, plan.Changes)
	}

	res := &PlanResult{
		Description:   desc + " (dry run - nothing was written)",
		AffectedFiles: plan.AffectedFiles,
		ChangeCount:   len(plan.Changes),
		Success:       true,
		DryRun:        true,
		SkippedSites:  plan.SkippedSites,
		BuildConfigs:  planBuildConfigs(plan),
		Diffs:         buildDiffs(plan.Changes),
	}
	res.UncoveredFiles = planUncoveredFiles(state, plan)

	if err := state.GetEngine().ValidateRefactoring(plan); err != nil {
		res.Success = false
		res.BlockingIssue = err.Error()
		return res
	}
	if plan.Impact != nil {
		for _, issue := range plan.Impact.PotentialIssues {
			if issue.Severity == types.Error {
				res.Success = false
				res.BlockingIssue = fmt.Sprintf("critical issue: %s", issue.Description)
				return res
			}
		}
	}
	return res
}

// AnalysisResult is the structured output returned by read-only analysis tools.
type AnalysisResult struct {
	Description string `json:"description"`
	Data        any    `json:"data"`
}

// executePlan validates, executes, and returns a PlanResult for the given plan.
// When dryRun is true it returns the preview instead: no file is written and no
// workspace sync happens.
func executePlan(state *MCPServer, plan *types.RefactoringPlan, desc string, dryRun bool) (*PlanResult, error) {
	if dryRun {
		return previewPlan(state, plan, desc), nil
	}

	if err := state.GetEngine().ExecutePlan(plan); err != nil {
		return nil, fmt.Errorf("execute plan: %w", err)
	}

	// Synchronously update workspace state with the changes we just wrote
	if err := state.SyncWorkspaceChanges(plan.AffectedFiles); err != nil {
		state.logger.Warn("workspace sync failed", "err", err)
		// Don't fail the operation - changes are already on disk
	}

	return &PlanResult{
		Description:    desc,
		AffectedFiles:  plan.AffectedFiles,
		ChangeCount:    len(plan.Changes),
		ModifiedFiles:  plan.AffectedFiles,
		Success:        true,
		SkippedSites:   plan.SkippedSites,
		BuildConfigs:   planBuildConfigs(plan),
		UncoveredFiles: planUncoveredFiles(state, plan),
	}, nil
}

// planUncoveredFiles returns plan's uncovered files with paths relative to
// the workspace root.
func planUncoveredFiles(state *MCPServer, plan *types.RefactoringPlan) []UncoveredFileInfo {
	if len(plan.Coverage.Uncovered) == 0 {
		return nil
	}
	root := ""
	if ws, err := state.GetWorkspace(); err == nil {
		root = ws.RootPath
	}
	out := make([]UncoveredFileInfo, len(plan.Coverage.Uncovered))
	for i, u := range plan.Coverage.Uncovered {
		out[i] = UncoveredFileInfo{File: relativeTo(root, u.Path), Configs: u.Configs, Reason: u.Reason}
	}
	return out
}

// planBuildConfigs returns the configurations plan covers when it spans more
// than one.
func planBuildConfigs(plan *types.RefactoringPlan) []string {
	if len(plan.Coverage.Configs) < 2 {
		return nil
	}
	return plan.Coverage.Configs
}

// executePlanWithUnlock releases the read lock before calling executePlan.
// This prevents deadlock when executePlan calls SyncWorkspaceChanges which needs a write lock.
// Use this when the caller holds a read lock with defer RUnlock().
func executePlanWithUnlock(state *MCPServer, plan *types.RefactoringPlan, desc string, dryRun bool) (*PlanResult, error) {
	state.RUnlock()
	return executePlan(state, plan, desc, dryRun)
}

// withDryRun annotates a tool response with preview details when nothing was
// written, so an empty files_modified is not mistaken for a no-op apply.
func withDryRun(out map[string]any, result *PlanResult) map[string]any {
	if result.DryRun {
		out["dry_run"] = true
		out["diffs"] = result.Diffs
		out["message"] = result.Description
		if result.BlockingIssue != "" {
			out["blocking_issue"] = result.BlockingIssue
		}
	}
	return out
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
