package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// PlanResult is the structured output returned by mutating refactoring tools.
type PlanResult struct {
	Description   string   `json:"description"`
	AffectedFiles []string `json:"affected_files"`
	ChangeCount   int      `json:"change_count"`
	ModifiedFiles []string `json:"modified_files"`
	Success       bool     `json:"success"`
	// SkippedSites lists call sites that could not be automatically rewritten,
	// or declarations left unmoved. Each skipped call site must be updated
	// manually before the build will succeed; a skipped declaration stays put.
	// A stop_at: entry marks where a context climb stopped and is not a build break.
	SkippedSites []types.SkippedSite `json:"skipped_sites,omitempty"`
	// BuildConfigs names the build configurations the plan was computed under,
	// primary first. Omitted when there is only one.
	BuildConfigs []string `json:"build_configs,omitempty"`
	// UncoveredFiles lists the files in the packages the plan reached that no
	// build configuration type-checks. The plan holds no edits for them.
	UncoveredFiles []UncoveredFileInfo `json:"uncovered_files,omitempty"`
	// TypeErrors lists the packages with type errors in non-test files that the
	// plan reached or whose erroring files name a changed symbol. Uses inside
	// the erroring code may be missing from the plan.
	TypeErrors []TypeErrorInfo `json:"type_errors,omitempty"`

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

			if c.Carried() {
				fd.Hunks = append(fd.Hunks, DiffHunk{Description: c.Description, New: fmt.Sprintf("[file moved, %d bytes]", len(c.NewText))})
				continue
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
	ws, err := state.GetWorkspace()
	if err == nil {
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
	res.TypeErrors = planTypeErrors(plan)

	err = refactor.GeneratedFileEdits(plan.Changes)
	if err != nil {
		res.Success = false
		res.BlockingIssue = err.Error()

		return res
	}

	err = state.GetEngine().ValidateRefactoring(plan)
	if err != nil {
		res.Success = false
		res.BlockingIssue = err.Error()

		return res
	}

	if plan.Impact != nil {
		for _, issue := range plan.Impact.PotentialIssues {
			if issue.Severity != types.Error {
				continue
			}

			res.Success = false
			res.BlockingIssue = fmt.Sprintf("critical issue: %s", issue.Description)

			return res
		}
	}

	return res
}

// executePlan validates, executes, and returns a PlanResult for the given plan.
// When dryRun is true it returns the preview instead: no file is written and no
// workspace sync happens.
func executePlan(state *MCPServer, plan *types.RefactoringPlan, desc string, dryRun bool) (*PlanResult, error) {
	if dryRun {
		return previewPlan(state, plan, desc), nil
	}

	err := state.GetEngine().ExecutePlan(plan)
	if err != nil {
		return nil, fmt.Errorf("execute plan: %w", err)
	}

	// Synchronously update workspace state with the changes we just wrote
	state.SyncWorkspaceChanges(plan.AffectedFiles)

	return &PlanResult{
		Description:    desc,
		AffectedFiles:  plan.AffectedFiles,
		ChangeCount:    len(plan.Changes),
		ModifiedFiles:  plan.AffectedFiles,
		Success:        true,
		SkippedSites:   plan.SkippedSites,
		BuildConfigs:   planBuildConfigs(plan),
		UncoveredFiles: planUncoveredFiles(state, plan),
		TypeErrors:     planTypeErrors(plan),
	}, nil
}

// TypeErrorInfo is the first type error in the non-test files of a package
// under one build configuration.
type TypeErrorInfo struct {
	Package string `json:"package"`
	Config  string `json:"config"`
	Error   string `json:"error"`
}

func planTypeErrors(plan *types.RefactoringPlan) []TypeErrorInfo {
	var out []TypeErrorInfo
	for _, e := range plan.Coverage.TypeErrors {
		out = append(out, TypeErrorInfo(e))
	}

	return out
}

// planUncoveredFiles returns plan's uncovered files with paths relative to
// the workspace root.
func planUncoveredFiles(state *MCPServer, plan *types.RefactoringPlan) []UncoveredFileInfo {
	if len(plan.Coverage.Uncovered) == 0 {
		return nil
	}

	root := ""

	ws, err := state.GetWorkspace()
	if err == nil {
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

// verificationBlocker returns why a type-check verification forbids applying
// its plan: the errors the plan would introduce, or that it could not be
// checked ("" when verified clean).
func verificationBlocker(v *refactor.TypeCheckResult) string {
	if len(v.NewErrors) > 0 {
		return "applying this plan would introduce type errors: " + strings.Join(v.NewErrors, "; ")
	}

	if v.Skipped != "" {
		return "the plan could not be verified against go/types: " + v.Skipped
	}

	return ""
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
