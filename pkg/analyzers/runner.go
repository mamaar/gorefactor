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
	"golang.org/x/tools/go/types/objectpath"

	wsanalysis "github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	wstypes "github.com/mamaar/gorefactor/pkg/types"
)

// RunResult holds the output of running an analyzer across one or more packages.
type RunResult struct {
	Result      any
	Diagnostics []analysis.Diagnostic
	// Skipped lists the import paths of packages left unanalyzed because they
	// have type errors and the analyzer does not set RunDespiteErrors.
	Skipped []string
}

// Run executes an analyzer against workspace packages and returns its typed result
// plus any diagnostics reported. If pkgFilter is non-empty, only the matching
// package is reported on; otherwise all packages are. An analyzer that uses
// facts also runs, unreported, on the workspace packages the filtered package
// imports, so their facts exist. A panic in an analyzer is returned as an error.
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

	for _, pkg := range dependenciesFirst(ws, packages, pkgFilter != "" && usesFacts(a)) {
		reported := slices.Contains(packages, pkg)

		rr, err := rs.runPackage(a, pkg)
		if err != nil {
			return nil, fmt.Errorf("run: %w", err)
		}

		if !reported {
			continue
		}

		combined.Diagnostics = append(combined.Diagnostics, rr.Diagnostics...)
		combined.Skipped = append(combined.Skipped, rr.Skipped...)
		combined.Result = mergeResults(combined.Result, rr.Result)
	}

	return combined, nil
}

// usesFacts reports whether a or an analyzer it requires exchanges facts.
func usesFacts(a *analysis.Analyzer) bool {
	return len(a.FactTypes) > 0 || slices.ContainsFunc(a.Requires, usesFacts)
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
	// results caches required analyzers' results, one computation per unit.
	results map[resultKey]any
}

// factKey names a fact. Objects reachable by an object path are keyed by it,
// so the several go/types copies of one package (the type checker hands out a
// production-only view while a test import cycle is in progress) share facts.
type factKey struct {
	analyzer *analysis.Analyzer
	typ      reflect.Type
	pkg      string
	path     objectpath.Path
	obj      types.Object
}

func newFactKey(a *analysis.Analyzer, obj types.Object, fact analysis.Fact, external *types.Package) factKey {
	key := factKey{analyzer: a, typ: reflect.TypeOf(fact)}

	// Nothing imports an external test package, and its objects would share
	// paths with the package under test.
	if obj.Pkg() != nil && obj.Pkg() != external {
		path, err := objectpath.For(obj)
		if err == nil {
			key.pkg, key.path = obj.Pkg().Path(), path
			return key
		}
	}

	key.obj = obj

	return key
}

type resultKey struct {
	unit     *unit
	analyzer *analysis.Analyzer
}

// unit is what one pass covers: a package's own files, or the files of its
// external test package, each with its own type information.
type unit struct {
	name     string
	files    []*wstypes.File
	pkg      *types.Package
	info     *types.Info
	external bool
}

// unitsOf splits pkg into its units, leaving out test files that were dropped
// from type-checking. The second result reports whether pkg's own check
// failed, in which case its analyzer-visible information is incomplete.
func unitsOf(pkg *wstypes.Package) (units []*unit, failed bool) {
	typesPkg := pkg.TypesPkg
	if typesPkg == nil {
		typesPkg = types.NewPackage(pkg.ImportPath, pkg.Name)
	}

	typesInfo := pkg.TypesInfo
	if typesInfo == nil {
		typesInfo = &types.Info{}
	}

	own := &unit{name: pkg.ImportPath, pkg: typesPkg, info: typesInfo}
	ext := &unit{name: pkg.ImportPath + "_test", pkg: pkg.ExternalTestTypesPkg, info: pkg.ExternalTestTypesInfo, external: true}

	for _, name := range slices.Sorted(maps.Keys(pkg.Files)) {
		own.files = append(own.files, pkg.Files[name])
	}

	for _, name := range slices.Sorted(maps.Keys(pkg.TestFiles)) {
		f := pkg.TestFiles[name]

		switch {
		case f.AST == nil, pkg.ExcludedTestFiles[name] != nil:
		case pkg.ExternalTestTypesPkg != nil && f.AST.Name.Name != pkg.Name:
			ext.files = append(ext.files, f)
		default:
			own.files = append(own.files, f)
		}
	}

	units = append(units, own)
	if len(ext.files) > 0 && ext.info != nil {
		units = append(units, ext)
	}

	return units, pkg.TypesChecked && pkg.TypesErr != nil
}

// dependenciesFirst orders pkgs so that a package comes after the workspace
// packages it imports; ties and import cycles keep the input order. With
// withDeps the packages pkgs import, directly or not, are included.
func dependenciesFirst(ws *wstypes.Workspace, pkgs []*wstypes.Package, withDeps bool) []*wstypes.Package {
	edges := importEdges(ws)
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
		if selected[pkg] || (withDeps && pkg != nil) {
			ordered = append(ordered, pkg)
		}
	}

	for _, pkg := range pkgs {
		visit(pkg.Path)
	}

	return ordered
}

// importEdges returns each package's workspace imports: the production ones,
// then the test ones that do not lead back through production imports. Facts
// flow along production imports, which cannot cycle; a test import that closes
// a cycle with them must not reorder the packages it closes it with.
func importEdges(ws *wstypes.Workspace) map[string][]string {
	graph := wsanalysis.ImportGraph(ws)
	edges := make(map[string][]string, len(graph.Production))

	var reaches func(from, to string, seen map[string]bool) bool

	reaches = func(from, to string, seen map[string]bool) bool {
		if from == to {
			return true
		}

		if seen[from] {
			return false
		}

		seen[from] = true

		return slices.ContainsFunc(graph.Production[from], func(next string) bool { return reaches(next, to, seen) })
	}

	for path, prod := range graph.Production {
		edges[path] = slices.Clone(prod)

		for _, dep := range graph.Test[path] {
			if !slices.Contains(prod, dep) && !reaches(dep, path, map[string]bool{}) {
				edges[path] = append(edges[path], dep)
			}
		}
	}

	return edges
}

// runPackage executes an analyzer against the units of a single workspace package.
func (rs *runState) runPackage(a *analysis.Analyzer, pkg *wstypes.Package) (*RunResult, error) {
	units, failed := unitsOf(pkg)
	if failed && !a.RunDespiteErrors {
		return &RunResult{Skipped: []string{pkg.ImportPath}}, nil
	}

	combined := &RunResult{}

	for _, u := range units {
		rr, err := rs.runUnit(a, u)
		if err != nil {
			return nil, err
		}

		combined.Diagnostics = append(combined.Diagnostics, rr.Diagnostics...)
		combined.Result = mergeResults(combined.Result, rr.Result)
	}

	return combined, nil
}

// runUnit runs a on u, returning a panic inside an analyzer as an error.
func (rs *runState) runUnit(a *analysis.Analyzer, u *unit) (res *RunResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("analyzer %s panicked on package %s: %v", a.Name, u.name, r)
		}
	}()

	var diags []analysis.Diagnostic

	pass, err := rs.buildPass(u, a, func(d analysis.Diagnostic) {
		diags = append(diags, d)
	})
	if err != nil {
		return nil, err
	}

	out, err := a.Run(pass)
	if err != nil {
		return nil, err
	}

	return &RunResult{Result: out, Diagnostics: diags}, nil
}

func (rs *runState) buildPass(u *unit, a *analysis.Analyzer, report func(analysis.Diagnostic)) (*analysis.Pass, error) {
	files := make([]*ast.File, 0, len(u.files))
	for _, f := range u.files {
		files = append(files, f.AST)
	}

	var external *types.Package
	if u.external {
		external = u.pkg
	}

	pass := &analysis.Pass{
		Analyzer:  a,
		Fset:      rs.ws.FileSet,
		Files:     files,
		Pkg:       u.pkg,
		TypesInfo: u.info,
		Report:    report,
		ResultOf:  make(map[*analysis.Analyzer]any),

		TypesSizes: types.SizesFor("gc", runtime.GOARCH),
		ReadFile: func(filename string) ([]byte, error) {
			for _, f := range u.files {
				if f.Path == filename {
					return f.OriginalContent, nil
				}
			}

			return nil, fmt.Errorf("%s is not a file of package %s", filename, u.name)
		},
		ExportObjectFact: func(obj types.Object, fact analysis.Fact) {
			rs.facts[newFactKey(a, obj, fact, external)] = fact
		},
		ImportObjectFact: func(obj types.Object, fact analysis.Fact) bool {
			stored, ok := rs.facts[newFactKey(a, obj, fact, external)]
			if !ok {
				return false
			}

			reflect.ValueOf(fact).Elem().Set(reflect.ValueOf(stored).Elem())

			return true
		},
	}

	// Build file content map for filedata.
	fd := &filedata.Data{Content: make(map[string][]byte)}
	for _, f := range u.files {
		fd.Content[f.Path] = f.OriginalContent
	}

	// Pre-compute results for required analyzers.
	for _, req := range a.Requires {
		switch {
		case req == filedata.Analyzer:
			pass.ResultOf[req] = fd
		case req.Name == "inspect":
			// One inspector per unit: typeindex rejects results built on a different one.
			key := resultKey{u, req}

			res, ok := rs.results[key]
			if !ok {
				res = inspector.New(files)
				rs.results[key] = res
			}

			pass.ResultOf[req] = res
		default:
			key := resultKey{u, req}

			res, ok := rs.results[key]
			if !ok {
				reqPass, err := rs.buildPass(u, req, func(analysis.Diagnostic) {})
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
			// Ignore //line directives: edits address the file's own bytes.
			startPos := fset.PositionFor(edit.Pos, false)
			endPos := fset.PositionFor(edit.End, false)
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
