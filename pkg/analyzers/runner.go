package analyzers

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"

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
	}

	combined := &RunResult{}

	for _, pkg := range packages {
		rr, err := RunPackage(ws, a, pkg)
		if err != nil {
			return nil, err
		}
		combined.Diagnostics = append(combined.Diagnostics, rr.Diagnostics...)
		combined.Result = rr.Result
	}

	return combined, nil
}

// RunPackage executes an analyzer against a single workspace package.
func RunPackage(ws *wstypes.Workspace, a *analysis.Analyzer, pkg *wstypes.Package) (*RunResult, error) {
	var diags []analysis.Diagnostic

	pass, err := buildPass(ws, pkg, a, func(d analysis.Diagnostic) {
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

func buildPass(ws *wstypes.Workspace, pkg *wstypes.Package, a *analysis.Analyzer, report func(analysis.Diagnostic)) (*analysis.Pass, error) {
	files := make([]*ast.File, 0, len(pkg.Files))
	for _, f := range pkg.Files {
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
	}

	// Build file content map for filedata.
	fd := &filedata.Data{Content: make(map[string][]byte)}
	for _, f := range pkg.Files {
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
			// Run required analyzer recursively.
			reqPass, err := buildPass(ws, pkg, req, func(analysis.Diagnostic) {})
			if err != nil {
				return nil, err
			}
			res, err := req.Run(reqPass)
			if err != nil {
				return nil, err
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
