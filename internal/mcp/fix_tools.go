package mcp

import (
	"bytes"
	"context"
	"fmt"
	gotypes "go/types"
	"maps"
	"path/filepath"
	"regexp"
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
			"fixed":            sel.fixed,
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

	for _, a := range selected {
		rr, err := analyzers.Run(ws, a, pkgFilter)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", a.Name, err)
		}

		sel.add(a.Name, rr.Diagnostics)
		sel.skippedPackages = mergeSorted(sel.skippedPackages, rr.Skipped)
	}

	changes, err := sel.verifiedChanges(state.GetEngine().Importer(ws))
	if err != nil {
		return nil, nil, err
	}

	return sel, changes, nil
}

// verifiedChanges returns the edits of the selected fixes. A fix whose result
// would not type-check is moved from fixed to skipped.
func (s *fixSelection) verifiedChanges(importer gotypes.Importer) ([]types.Change, error) {
	contents := analyzers.FileContents(s.ws)
	groups := make([][]types.Change, len(s.diags))

	for i, d := range s.diags {
		changes := analyzers.DiagnosticsToChanges(s.ws.FileSet, []analysis.Diagnostic{d})
		for j := range changes {
			dropLeadingBlankLine(contents[changes[j].File], &changes[j])
		}

		groups[i] = analyzers.FillOldText(s.ws, changes)
	}

	if len(groups) == 0 || verificationBlocker(refactor.VerifyPlanTypesWithImporter(s.ws, analyzers.ChangesToPlan(slices.Concat(groups...)), importer)) == "" {
		return slices.Concat(groups...), nil
	}

	var kept []types.Change

	fixed, diags := s.fixed[:0:0], s.diags[:0:0]

	for i, group := range groups {
		blocker := verificationBlocker(refactor.VerifyPlanTypesWithImporter(s.ws, analyzers.ChangesToPlan(group), importer))
		if blocker != "" {
			s.skipped = append(s.skipped, GoFixSkipped{s.fixed[i], blocker})
			continue
		}

		kept = append(kept, group...)
		fixed = append(fixed, s.fixed[i])
		diags = append(diags, s.diags[i])
	}

	s.fixed, s.diags = fixed, diags

	if len(kept) > 0 {
		blocker := verificationBlocker(refactor.VerifyPlanTypesWithImporter(s.ws, analyzers.ChangesToPlan(kept), importer))
		if blocker != "" {
			return nil, fmt.Errorf("fixes that type-check alone do not together: %s", blocker)
		}
	}

	return kept, nil
}

// dropLeadingBlankLine extends a deletion of a block's first statement so it
// also removes the line it leaves behind, and the blank line below it if any.
func dropLeadingBlankLine(src []byte, c *types.Change) {
	if c.NewText != "" || c.Start < 0 || c.End > len(src) {
		return
	}

	before := bytes.TrimRight(src[:c.Start], " \t")
	if !bytes.HasSuffix(before, []byte("{\n")) {
		return
	}

	blank := blankLineAfter.Find(src[c.End:])
	c.End += len(blank)
}

// blankLineAfter matches the rest of a line and an empty line below it.
var blankLineAfter = regexp.MustCompile(`^[ \t]*\n([ \t]*\n)?`)

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
	diags     []analysis.Diagnostic
	fixed     []GoFixItem
	skipped   []GoFixSkipped
	// skippedPackages are packages left unanalyzed for type errors.
	skippedPackages []string
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

		for _, e := range fresh {
			s.accepted = append(s.accepted, acceptedEdit{e.key, fmt.Sprintf("%s fix at %s:%d", analyzer, item.File, item.Line)})
		}

		fix.TextEdits = freshEdits(fresh)
		d.SuggestedFixes = []analysis.SuggestedFix{fix}
		s.diags = append(s.diags, d)
		s.fixed = append(s.fixed, item)
	}
}

type keyedEdit struct {
	key  editKey
	edit analysis.TextEdit
}

func freshEdits(fresh []keyedEdit) []analysis.TextEdit {
	edits := make([]analysis.TextEdit, len(fresh))
	for i, e := range fresh {
		edits[i] = e.edit
	}

	return edits
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
func (s *fixSelection) classify(fix analysis.SuggestedFix) (fresh []keyedEdit, origin string, conflict bool) {
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
			fresh = append(fresh, keyedEdit{key, e})
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
