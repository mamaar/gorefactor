package analyzers

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"sort"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"

	wsanalysis "github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	wstypes "github.com/mamaar/gorefactor/pkg/types"
)

// RunResult holds the output of running an analyzer across one or more packages.
type RunResult struct {
	Result      any
	Diagnostics []analysis.Diagnostic
}

// Run executes an analyzer against workspace packages and returns its typed result
// plus any diagnostics reported. If pkgFilter is non-empty, only the matching
// package is analysed; otherwise all packages are analysed.
func Run(ws *wstypes.Workspace, a *analysis.Analyzer, pkgFilter string) (*RunResult, error) {
	var packages []*wstypes.Package

	if pkgFilter != "" {
		resolved := wstypes.ResolvePackagePath(ws, pkgFilter)

		pkg, ok := ws.Packages[resolved]
		if !ok {
			return &RunResult{}, nil
		}

		packages = []*wstypes.Package{pkg}
	} else {
		for _, pkg := range ws.Packages {
			packages = append(packages, pkg)
		}

		sort.Slice(packages, func(i, j int) bool {
			return packages[i].ImportPath < packages[j].ImportPath
		})
	}

	rs := &runState{
		ws:      ws,
		facts:   make(map[factKey]analysis.Fact),
		results: make(map[resultKey]any),
	}

	combined := &RunResult{}

	for _, pkg := range dependenciesFirst(ws, packages) {
		rr, err := rs.runPackage(a, pkg)
		if err != nil {
			return nil, fmt.Errorf("run: %w", err)
		}

		combined.Diagnostics = append(combined.Diagnostics, rr.Diagnostics...)
		combined.Result = mergeResults(combined.Result, rr.Result)
	}

	return combined, nil
}

// mergeResults accumulates per-package analyzer results into a single value.
//
// Callers of Run type-assert the returned RunResult.Result to the analyzer's
// own concrete slice type (e.g. rr.Result.([]*ifinit.Result)), so we cannot
// simply collect results into a []any — that would break every existing
// assertion. Instead we use reflection to append same-typed slices together,
// preserving the concrete element type across the merge. Non-slice results
// (or slices of mismatched types, which should not occur in practice) fall
// back to last-write-wins, matching the pre-existing behaviour.
func mergeResults(acc, next any) any {
	if acc == nil {
		return next
	}

	if next == nil {
		return acc
	}

	accVal := reflect.ValueOf(acc)

	nextVal := reflect.ValueOf(next)
	if accVal.Kind() == reflect.Slice && nextVal.Kind() == reflect.Slice &&
		reflect.TypeOf(acc) == reflect.TypeOf(next) {
		return reflect.AppendSlice(accVal, nextVal).Interface()
	}

	return next
}

// runState is the state shared by the packages of one Run call.
type runState struct {
	ws *wstypes.Workspace
	// facts holds object facts exported so far, so a package sees the facts
	// of the workspace packages it imports.
	facts map[factKey]analysis.Fact
	// results caches required analyzers' results, one computation per package.
	results map[resultKey]any
}

type factKey struct {
	analyzer *analysis.Analyzer
	obj      types.Object
	typ      reflect.Type
}

type resultKey struct {
	pkg      *wstypes.Package
	analyzer *analysis.Analyzer
}

// dependenciesFirst orders pkgs so that a package comes after the workspace
// packages it imports; ties and import cycles keep the input order.
func dependenciesFirst(ws *wstypes.Workspace, pkgs []*wstypes.Package) []*wstypes.Package {
	edges := wsanalysis.ImportGraph(ws).Union()
	selected := make(map[*wstypes.Package]bool, len(pkgs))

	for _, pkg := range pkgs {
		selected[pkg] = true
	}

	ordered := make([]*wstypes.Package, 0, len(pkgs))
	visited := make(map[string]bool, len(pkgs))

	var visit func(path string)

	visit = func(path string) {
		if visited[path] {
			return
		}

		visited[path] = true

		for _, dep := range edges[path] {
			visit(dep)
		}

		pkg := ws.Packages[path]
		if selected[pkg] {
			ordered = append(ordered, pkg)
		}
	}

	for _, pkg := range pkgs {
		visit(pkg.Path)
	}

	return ordered
}

// runPackage executes an analyzer against a single workspace package.
func (rs *runState) runPackage(a *analysis.Analyzer, pkg *wstypes.Package) (*RunResult, error) {
	var diags []analysis.Diagnostic

	pass, err := rs.buildPass(pkg, a, func(d analysis.Diagnostic) {
		diags = append(diags, d)
	})
	if err != nil {
		return nil, err
	}

	res, err := a.Run(pass)
	if err != nil {
		return nil, err
	}

	return &RunResult{Result: res, Diagnostics: diags}, nil
}

func (rs *runState) buildPass(pkg *wstypes.Package, a *analysis.Analyzer, report func(analysis.Diagnostic)) (*analysis.Pass, error) {
	ws := rs.ws

	// Test files of both the package and its x_test package go in one pass:
	// TypesInfo covers both, and no analyzer reads pass.Pkg.
	srcFiles := slices.Concat(slices.Collect(maps.Values(pkg.Files)), slices.Collect(maps.Values(pkg.TestFiles)))

	files := make([]*ast.File, 0, len(srcFiles))
	for _, f := range srcFiles {
		files = append(files, f.AST)
	}

	typesPkg := pkg.TypesPkg
	if typesPkg == nil {
		typesPkg = types.NewPackage(pkg.ImportPath, pkg.Name)
	}

	typesInfo := pkg.TypesInfo
	if typesInfo == nil {
		typesInfo = &types.Info{}
	}

	pass := &analysis.Pass{
		Analyzer:  a,
		Fset:      ws.FileSet,
		Files:     files,
		Pkg:       typesPkg,
		TypesInfo: typesInfo,
		Report:    report,
		ResultOf:  make(map[*analysis.Analyzer]any),

		TypesSizes: types.SizesFor("gc", runtime.GOARCH),
		ReadFile: func(filename string) ([]byte, error) {
			for _, f := range srcFiles {
				if f.Path == filename {
					return f.OriginalContent, nil
				}
			}

			return nil, fmt.Errorf("%s is not a file of package %s", filename, pkg.ImportPath)
		},
		ExportObjectFact: func(obj types.Object, fact analysis.Fact) {
			rs.facts[factKey{a, obj, reflect.TypeOf(fact)}] = fact
		},
		ImportObjectFact: func(obj types.Object, fact analysis.Fact) bool {
			stored, ok := rs.facts[factKey{a, obj, reflect.TypeOf(fact)}]
			if !ok {
				return false
			}

			reflect.ValueOf(fact).Elem().Set(reflect.ValueOf(stored).Elem())

			return true
		},
	}

	// Build file content map for filedata.
	fd := &filedata.Data{Content: make(map[string][]byte)}
	for _, f := range srcFiles {
		fd.Content[f.Path] = f.OriginalContent
	}

	// Pre-compute results for required analyzers.
	for _, req := range a.Requires {
		switch {
		case req == filedata.Analyzer:
			pass.ResultOf[req] = fd
		case req.Name == "inspect":
			pass.ResultOf[req] = inspector.New(files)
		default:
			key := resultKey{pkg, req}

			res, ok := rs.results[key]
			if !ok {
				reqPass, err := rs.buildPass(pkg, req, func(analysis.Diagnostic) {})
				if err != nil {
					return nil, err
				}

				res, err = req.Run(reqPass)
				if err != nil {
					return nil, err
				}

				rs.results[key] = res
			}

			pass.ResultOf[req] = res
		}
	}

	return pass, nil
}

// DiagnosticsToChanges converts diagnostics with SuggestedFixes into types.Change slices.
// It picks the first SuggestedFix from each diagnostic (if any).
func DiagnosticsToChanges(fset *token.FileSet, diags []analysis.Diagnostic) []wstypes.Change {
	var changes []wstypes.Change

	for _, d := range diags {
		if len(d.SuggestedFixes) == 0 {
			continue
		}

		fix := d.SuggestedFixes[0]
		for _, edit := range fix.TextEdits {
			startPos := fset.Position(edit.Pos)
			endPos := fset.Position(edit.End)
			changes = append(changes, wstypes.Change{
				File:        startPos.Filename,
				Start:       startPos.Offset,
				End:         endPos.Offset,
				NewText:     string(edit.NewText),
				Description: fix.Message,
			})
		}
	}

	return changes
}

// FillOldText populates each change's OldText from the workspace's copy of the
// file it applies to.
//
// This serves two purposes. A dry run renders OldText as the "before" side of
// its diff, so without it the caller reviews a preview that only shows what the
// code would become. And on apply, Serializer.applyChange verifies OldText
// against the bytes actually at those offsets, turning a stale-offset bug into
// a clean error instead of a corrupted file.
//
// Changes whose file or offsets are not found in the workspace are left as-is.
func FillOldText(ws *wstypes.Workspace, changes []wstypes.Change) []wstypes.Change {
	contents := make(map[string][]byte)

	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			contents[file.Path] = file.OriginalContent
		}

		for _, file := range pkg.TestFiles {
			contents[file.Path] = file.OriginalContent
		}
	}

	for i := range changes {
		c := &changes[i]
		if c.Delete || c.OldText != "" {
			continue
		}

		content, ok := contents[c.File]
		if !ok {
			continue
		}

		if c.Start < 0 || c.End > len(content) || c.Start >= c.End {
			continue
		}

		c.OldText = string(content[c.Start:c.End])
	}

	return changes
}

// ChangesToPlan creates a RefactoringPlan from a set of changes.
func ChangesToPlan(changes []wstypes.Change) *wstypes.RefactoringPlan {
	affectedSet := make(map[string]bool)
	for _, c := range changes {
		affectedSet[c.File] = true
	}

	var affected []string
	for f := range affectedSet {
		affected = append(affected, f)
	}

	return &wstypes.RefactoringPlan{
		Changes:       changes,
		AffectedFiles: affected,
	}
}
