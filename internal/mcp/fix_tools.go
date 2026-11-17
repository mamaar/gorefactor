package mcp

import (
	"context"
	"fmt"
	gotypes "go/types"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inline"
	"golang.org/x/tools/go/analysis/passes/modernize"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// --- run_go_fix ---

type RunGoFixInput struct {
	Analyzers []string `json:"analyzers,omitempty" jsonschema:"analyzer names to run (default: modernize.Suite plus inline); name appendclipped, bloop, fmtappendf or slicesdelete here to run those; an unknown name errors with the valid names"`
	Package   string   `json:"package,omitempty" jsonschema:"specific package to fix"`
	DryRun    bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

type GoFixItem struct {
	Analyzer string `json:"analyzer"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

type GoFixSkipped struct {
	GoFixItem

	Reason string `json:"reason"`
}

// editKey identifies a text edit for deduplication.
type editKey struct {
	file       string
	start, end int
	text       string
}

// acceptedEdit is an edit already chosen for the plan, with its origin for
// overlap messages.
type acceptedEdit struct {
	editKey

	origin string
}

func registerFixTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "run_go_fix",
		Description: "Run go fix's modernizers in-process and apply their suggested fixes through the refactoring plan pipeline (dry_run gives diffs). " +
			"Runs every analyzer in golang.org/x/tools modernize.Suite (e.g. any, rangeint, minmax, slicescontains, mapsloop, forvar) plus `inline`, or the subset named in `analyzers`; appendclipped, bloop, fmtappendf and slicesdelete are left out of the default set and run only when named. " +
			"`inline` applies `//go:fix inline` directives on functions declared in the workspace, not ones from dependencies. buildtag and hostport are not covered. Every fix is type-checked before it is applied; one whose result would not type-check is listed under `skipped` with the error. " +
			"Generated files are skipped and listed under `skipped`, as are fixes that overlap an earlier fix; run again to pick those up. Packages with type errors are not analyzed and are listed under `skipped_packages`.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RunGoFixInput) (*mcpsdk.CallToolResult, any, error) {
		selected, err := selectModernizers(in.Analyzers)
		if err != nil {
			return errResult(err), nil, nil
		}

		sel, changes, err := collectFixes(state, selected, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		if len(changes) == 0 {
			return textResult(map[string]any{
				"files_modified":   []string{},
				"changes_count":    0,
				"fixed":            []GoFixItem{},
				"skipped":          sel.skipped,
				"skipped_packages": sel.skippedPackages,
				"message":          "No modernizer fixes found",
			}), nil, nil
		}

		plan := analyzers.ChangesToPlan(changes)

		result, err := executePlan(state, plan, "Apply go fix modernizers", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}

		out := map[string]any{
			"files_modified":   result.ModifiedFiles,
			"changes_count":    result.ChangeCount,
			"fixed":            sel.fixedItems(),
			"skipped":          sel.skipped,
			"skipped_packages": sel.skippedPackages,
		}

		return textResult(withDryRun(out, result)), nil, nil
	})
}

// collectFixes runs the selected analyzers and picks their fixes, holding the
// state's read lock only while it reads the workspace.
func collectFixes(state *MCPServer, selected []*analysis.Analyzer, pkgFilter string) (*fixSelection, []types.Change, error) {
	state.RLock()
	defer state.RUnlock()

	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, nil, err
	}

	// Type-checking is lazy and the modernizers need TypesInfo.
	state.GetEngine().EnsureTypeCheckedPackage(ws, pkgFilter)

	sel := &fixSelection{ws: ws, generated: generatedFiles(ws)}

	var runs []analyzerDiagnostics

	var all []analysis.Diagnostic

	for _, a := range selected {
		rr, err := analyzers.Run(ws, a, pkgFilter)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", a.Name, err)
		}

		runs = append(runs, analyzerDiagnostics{a.Name, rr.Diagnostics})
		all = append(all, rr.Diagnostics...)
		sel.skippedPackages = mergeSorted(sel.skippedPackages, rr.Skipped)
	}

	// Widen before selection so overlaps are judged on the edits that will apply.
	widenLeadingDeletions(ws, all)

	for _, run := range runs {
		sel.add(run.analyzer, run.diags)
	}

	return sel, sel.verifiedChanges(state.GetEngine().Importer(ws)), nil
}

type analyzerDiagnostics struct {
	analyzer string
	diags    []analysis.Diagnostic
}

// verifiedChanges returns the edits of the selected fixes, with the imports
// they leave unused removed. If the merged edits do not type-check, each fix is
// checked alone with its complete edits and the failing ones are skipped; fixes
// that still fail together are skipped file by file.
func (s *fixSelection) verifiedChanges(importer gotypes.Importer) []types.Change {
	v := &fixVerifier{ws: s.ws, importer: importer}

	changes, res := v.check(s.picks)
	if verificationBlocker(res) == "" {
		return changes
	}

	var kept []fixPick

	for _, p := range s.picks {
		_, res := v.check([]fixPick{p})

		blocker := verificationBlocker(res)
		if blocker != "" {
			s.skipped = append(s.skipped, GoFixSkipped{p.item, blocker})
			continue
		}

		kept = append(kept, p)
	}

	for len(kept) > 0 {
		changes, res := v.check(kept)

		blocker := verificationBlocker(res)
		if blocker == "" {
			s.picks = kept
			return changes
		}

		kept = s.refuseFilesIn(kept, blocker)
	}

	s.picks = nil

	return nil
}

// refuseFilesIn skips the fixes that edit a file the blocker names, or all of
// them if it names none, and returns the rest.
func (s *fixSelection) refuseFilesIn(picks []fixPick, blocker string) []fixPick {
	var kept []fixPick

	for _, p := range picks {
		if p.touchesFileIn(s.ws, blocker) {
			s.skipped = append(s.skipped, GoFixSkipped{p.item, blocker})
			continue
		}

		kept = append(kept, p)
	}

	if len(kept) < len(picks) {
		return kept
	}

	for _, p := range picks {
		s.skipped = append(s.skipped, GoFixSkipped{p.item, blocker})
	}

	return nil
}

// fixVerifier type-checks sets of fixes against the workspace.
type fixVerifier struct {
	ws       *types.Workspace
	importer gotypes.Importer
}

// changes returns the picks' edits as plan changes, identical ones once.
func (v *fixVerifier) changes(picks []fixPick) []types.Change {
	var changes []types.Change

	seen := make(map[editKey]bool)

	for _, p := range picks {
		for _, c := range analyzers.DiagnosticsToChanges(v.ws.FileSet, []analysis.Diagnostic{p.diag}) {
			key := editKey{c.File, c.Start, c.End, c.NewText}
			if !seen[key] {
				seen[key] = true

				changes = append(changes, c)
			}
		}
	}

	return analyzers.FillOldText(v.ws, changes)
}

// check verifies the picks together. Imports the edits leave unused are
// removed first, as go fix does.
func (v *fixVerifier) check(picks []fixPick) ([]types.Change, *refactor.TypeCheckResult) {
	changes := v.changes(picks)
	res := v.verify(changes)

	removals := unusedImportRemovals(v.ws, changes, res.NewErrors)
	if len(removals) == 0 {
		return changes, res
	}

	changes = analyzers.FillOldText(v.ws, slices.Concat(changes, removals))

	return changes, v.verify(changes)
}

func (v *fixVerifier) verify(changes []types.Change) *refactor.TypeCheckResult {
	return refactor.VerifyPlanTypesWithImporter(v.ws, analyzers.ChangesToPlan(changes), v.importer)
}

func mergeSorted(a, b []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(slices.Concat(a, b))))
}

// defaultAnalyzers is every analyzer a run covers when none is named.
func defaultAnalyzers() []*analysis.Analyzer {
	return append(slices.Clone(modernize.Suite), inline.Analyzer)
}

// namedOnlyAnalyzers are the modernizers x/tools leaves out of Suite as unsafe;
// they run only when named.
var namedOnlyAnalyzers = []*analysis.Analyzer{
	modernize.AppendClippedAnalyzer,
	modernize.BLoopAnalyzer,
	modernize.FmtAppendfAnalyzer,
	modernize.SlicesDeleteAnalyzer,
}

// selectModernizers resolves names against the default and named-only analyzers; no names means the default set.
func selectModernizers(names []string) ([]*analysis.Analyzer, error) {
	if len(names) == 0 {
		return defaultAnalyzers(), nil
	}

	byName := make(map[string]*analysis.Analyzer)
	for _, a := range slices.Concat(defaultAnalyzers(), namedOnlyAnalyzers) {
		byName[a.Name] = a
	}

	var selected []*analysis.Analyzer

	for _, name := range slices.Compact(slices.Sorted(slices.Values(names))) {
		a, ok := byName[name]
		if !ok {
			valid := slices.Sorted(maps.Keys(byName))
			return nil, fmt.Errorf("unknown analyzer %q; valid analyzers: %s", name, strings.Join(valid, ", "))
		}

		selected = append(selected, a)
	}

	return selected, nil
}

func generatedFiles(ws *types.Workspace) map[string]bool {
	generated := make(map[string]bool)

	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			generated[f.Path] = generated[f.Path] || f.Generated
		}

		for _, f := range pkg.TestFiles {
			generated[f.Path] = generated[f.Path] || f.Generated
		}
	}

	return generated
}

// fixSelection picks, in analyzer order, the first suggested fix of each
// diagnostic, dropping fixes in generated files and fixes that overlap one
// already accepted.
type fixSelection struct {
	ws        *types.Workspace
	generated map[string]bool
	accepted  []acceptedEdit
	picks     []fixPick
	skipped   []GoFixSkipped
	// skippedPackages are packages left unanalyzed for type errors.
	skippedPackages []string
}

// fixPick is a selected fix with all of its edits, including any a duplicate
// in an earlier fix makes redundant, so it can be checked alone.
type fixPick struct {
	item GoFixItem
	diag analysis.Diagnostic
}

// touchesFileIn reports whether text names a file the fix edits.
func (p fixPick) touchesFileIn(ws *types.Workspace, text string) bool {
	return slices.ContainsFunc(p.diag.SuggestedFixes[0].TextEdits, func(e analysis.TextEdit) bool {
		return strings.Contains(text, ws.FileSet.PositionFor(e.Pos, false).Filename)
	})
}

func (s *fixSelection) fixedItems() []GoFixItem {
	items := make([]GoFixItem, len(s.picks))
	for i, p := range s.picks {
		items[i] = p.item
	}

	return items
}

func (s *fixSelection) add(analyzer string, diags []analysis.Diagnostic) {
	for _, d := range diags {
		if len(d.SuggestedFixes) == 0 {
			continue
		}

		pos := s.ws.FileSet.PositionFor(d.Pos, false)

		item := GoFixItem{Analyzer: analyzer, File: s.relative(pos.Filename), Line: pos.Line, Message: d.Message}
		fix := d.SuggestedFixes[0]

		if file := s.generatedFileOf(fix); file != "" {
			s.skipped = append(s.skipped, GoFixSkipped{item, "generated file " + s.relative(file)})
			continue
		}

		fresh, origin, conflict := s.classify(fix)
		if conflict {
			s.skipped = append(s.skipped, GoFixSkipped{item, fmt.Sprintf("overlaps %s; run again", origin)})
			continue
		}

		if len(fresh) == 0 {
			continue
		}

		for _, key := range fresh {
			s.accepted = append(s.accepted, acceptedEdit{key, fmt.Sprintf("%s fix at %s:%d", analyzer, item.File, item.Line)})
		}

		s.picks = append(s.picks, fixPick{item, d})
	}
}

func (s *fixSelection) generatedFileOf(fix analysis.SuggestedFix) string {
	for _, e := range fix.TextEdits {
		file := s.ws.FileSet.PositionFor(e.Pos, false).Filename
		if s.generated[file] {
			return file
		}
	}

	return ""
}

// classify returns the fix's edits that are not already accepted verbatim, or
// the origin of the accepted fix one of them overlaps.
func (s *fixSelection) classify(fix analysis.SuggestedFix) (fresh []editKey, origin string, conflict bool) {
	for _, e := range fix.TextEdits {
		start, end := analyzers.EditRange(s.ws.FileSet, e)
		key := editKey{start.Filename, start.Offset, end.Offset, string(e.NewText)}

		duplicate := false

		for _, a := range s.accepted {
			if a.editKey == key {
				duplicate = true
				break
			}

			if a.file == key.file && (a.start == key.start || (a.start < key.end && key.start < a.end)) {
				return nil, a.origin, true
			}
		}

		if !duplicate {
			fresh = append(fresh, key)
		}
	}

	return fresh, "", false
}

func (s *fixSelection) relative(file string) string {
	rel, err := filepath.Rel(s.ws.RootPath, file)
	if err != nil {
		return file
	}

	return rel
}
