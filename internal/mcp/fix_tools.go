package mcp

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/modernize"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/types"
)

// --- run_go_fix ---

type RunGoFixInput struct {
	Analyzers []string `json:"analyzers,omitempty" jsonschema:"modernizer names to run (default: all of modernize.Suite); an unknown name errors with the valid names"`
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
			"Runs every analyzer in golang.org/x/tools modernize.Suite (e.g. any, rangeint, minmax, slicescontains, mapsloop, forvar) or the subset named in `analyzers`. " +
			"Does not cover `//go:fix inline`, buildtag or hostport. Generated files are skipped and listed under `skipped`, as are fixes that overlap an earlier fix; run again to pick those up. Packages with type errors are not analyzed and are listed under `skipped_packages`.",
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

	return sel, analyzers.FillOldText(ws, analyzers.DiagnosticsToChanges(ws.FileSet, sel.diags)), nil
}

func mergeSorted(a, b []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(slices.Concat(a, b))))
}

// selectModernizers resolves names against modernize.Suite; no names means the whole suite.
func selectModernizers(names []string) ([]*analysis.Analyzer, error) {
	if len(names) == 0 {
		return modernize.Suite, nil
	}

	byName := make(map[string]*analysis.Analyzer, len(modernize.Suite))
	for _, a := range modernize.Suite {
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

		// Edits are text fragments; the Validator skips its standalone-syntax check for descriptions containing "Replace".
		fix.Message = "Replace text: " + fix.Message
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
		start, end := s.ws.FileSet.PositionFor(e.Pos, false), s.ws.FileSet.PositionFor(e.End, false)
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
