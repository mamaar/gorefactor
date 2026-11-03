package refactor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// ImportCycle is a closed path of packages and the configurations it occurs in.
type ImportCycle struct {
	Packages []string `json:"packages"`
	// Configs names the configurations the cycle occurs in; empty when all do.
	Configs []string `json:"configs,omitempty"`
}

// CyclesReport is the outcome of DetectCycles.
type CyclesReport struct {
	Configs    []string // display names, primary first
	Cycles     []ImportCycle
	TestCycles []ImportCycle
}

// DependencyGraphReport is the dependency graph over every configuration.
// Keys and cycle members are filesystem paths.
type DependencyGraphReport struct {
	// PackageImports and PackageDeps are the unions of the views' graphs.
	PackageImports   map[string][]string
	PackageDeps      map[string][]string
	ImportCycles     []ImportCycle
	TestImportCycles []ImportCycle
}

// DependenciesReport is the outcome of AnalyzeDependenciesAcrossConfigs.
type DependenciesReport struct {
	Configs []string // display names, primary first
	Graph   DependencyGraphReport
	// Plan holds the coupling and backwards dependencies over every
	// configuration; its suggested moves come from the default one.
	Plan *types.RefactoringPlan
}

// importsRun is a run over the views that reads only import statements, so no
// view is type-checked.
type importsRun struct{}

func (importsRun) prepare([]*analysis.View) {}
func (importsRun) finish()                  {}

// DetectCycles reports the production import cycles and the cycles through
// in-package test files of each build configuration, as import paths. A cycle
// is the same cycle in any configuration that closes it; edges from different
// configurations never combine into one.
func (e *DefaultEngine) DetectCycles() (*CyclesReport, error) {
	views, err := e.beginRun(importsRun{})
	if err != nil {
		return nil, err
	}
	defer e.endRun(importsRun{})

	graph := mergeGraphs(views)
	return &CyclesReport{
		Configs:    viewNames(views),
		Cycles:     importPathCycles(views, graph.ImportCycles),
		TestCycles: importPathCycles(views, graph.TestImportCycles),
	}, nil
}

// AnalyzeDependenciesAcrossConfigs analyzes the dependency graph over every
// build configuration. Coupling and backwards dependencies derive from the
// union of the views' imports; suggested moves are those of the default
// configuration, as move_by_dependencies picks its candidates there.
func (e *DefaultEngine) AnalyzeDependenciesAcrossConfigs(req types.AnalyzeDependenciesRequest) (*DependenciesReport, error) {
	views, err := e.beginRun(importsRun{})
	if err != nil {
		return nil, err
	}
	defer e.endRun(importsRun{})

	primary := views[0].Workspace
	// Suggested moves match references by type information when available.
	e.EnsureTypeChecked(primary)
	op := &AnalyzeDependenciesOperation{Request: req}
	if err := op.Validate(primary); err != nil {
		return nil, fmt.Errorf("analyze dependencies operation validation failed: %w", err)
	}

	graph := mergeGraphs(views)
	union := unionWorkspace(views, graph)
	wss := make([]*types.Workspace, len(views))
	for i, v := range views {
		wss[i] = v.Workspace
	}
	impact := &types.ImpactAnalysis{PackageCoupling: packageCouplingOf(union, declaredSymbolCounts(wss...))}
	if req.DetectBackwardsDeps {
		impact.BackwardsDependencies = detectBackwardsDependencies(union)
	}
	if req.SuggestMoves {
		impact.SuggestedMoves = op.generateSuggestedMoves(primary)
	}
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
		Impact:        impact,
	}
	return &DependenciesReport{Configs: viewNames(views), Graph: graph, Plan: plan}, nil
}

func viewNames(views []*analysis.View) []string {
	names := make([]string, len(views))
	for i, v := range views {
		names[i] = v.Config.String()
	}
	return names
}

// mergeGraphs unites the dependency graphs of the views. Cycles are found per
// view: a linux-only edge and a windows-only edge never close one.
func mergeGraphs(views []*analysis.View) DependencyGraphReport {
	out := DependencyGraphReport{
		PackageImports: make(map[string][]string),
		PackageDeps:    make(map[string][]string),
	}
	cycles := make([][]groupedCycle, len(views))
	testCycles := make([][]groupedCycle, len(views))
	for i, v := range views {
		g := v.Workspace.Dependencies
		unionInto(out.PackageImports, g.PackageImports)
		unionInto(out.PackageDeps, g.PackageDeps)
		production := analysis.ImportGraph(v.Workspace).Production
		for _, c := range g.ImportCycles {
			cycles[i] = append(cycles[i], groupedCycle{c, strings.Join(analysis.Component(production, c[0]), "\x00")})
		}
		for _, c := range g.TestImportCycles {
			testCycles[i] = append(testCycles[i], groupedCycle{c, c[0]})
		}
	}
	configs := viewNames(views)
	out.ImportCycles = mergeCycles(configs, cycles)
	out.TestImportCycles = mergeCycles(configs, testCycles)
	return out
}

// groupedCycle is a closed path and the key of what it belongs to: the member
// set of its strongly connected component, or for a test cycle its package.
type groupedCycle struct {
	path  []string
	group string
}

// unionInto adds the values of from to those of into, keeping each sorted and distinct.
func unionInto(into, from map[string][]string) {
	for k, vs := range from {
		merged := slices.Concat(into[k], vs)
		slices.Sort(merged)
		into[k] = slices.Compact(merged)
	}
}

// mergeCycles merges the cycles each configuration reports. Cycles of one
// group are one entry, showing the path of the first configuration that has it
// and naming every configuration where the group is cyclic.
func mergeCycles(configs []string, perView [][]groupedCycle) []ImportCycle {
	found := make(map[string]*ImportCycle)
	for i, cycles := range perView {
		for _, c := range cycles {
			f := found[c.group]
			if f == nil {
				f = &ImportCycle{Packages: c.path}
				found[c.group] = f
			}
			f.Configs = append(f.Configs, configs[i])
		}
	}
	out := make([]ImportCycle, 0, len(found))
	for _, f := range found {
		if len(f.Configs) == len(configs) {
			f.Configs = nil
		}
		out = append(out, *f)
	}
	slices.SortFunc(out, func(a, b ImportCycle) int { return slices.Compare(a.Packages, b.Packages) })
	return out
}

// importPathCycles names the members of cycles by import path.
func importPathCycles(views []*analysis.View, cycles []ImportCycle) []ImportCycle {
	out := make([]ImportCycle, len(cycles))
	for i, c := range cycles {
		named := make([]string, len(c.Packages))
		for j, fsPath := range c.Packages {
			named[j] = importPathIn(views, fsPath)
		}
		out[i] = ImportCycle{Packages: named, Configs: c.Configs}
	}
	return out
}

// importPathIn names the package at fsPath by the import path of the first view
// that holds it.
func importPathIn(views []*analysis.View, fsPath string) string {
	for _, v := range views {
		pkg := v.Workspace.Packages[fsPath]
		if pkg != nil && pkg.ImportPath != "" {
			return pkg.ImportPath
		}
	}
	return importPathOf(views[0].Workspace, fsPath)
}

// unionWorkspace returns a workspace holding the packages and import paths of
// every view, with graph as its dependency graph. Where views share a package
// the first holds it.
func unionWorkspace(views []*analysis.View, graph DependencyGraphReport) *types.Workspace {
	union := &types.Workspace{
		RootPath:     views[0].Workspace.RootPath,
		Packages:     make(map[string]*types.Package),
		ImportToPath: make(map[string]string),
		Dependencies: &types.DependencyGraph{
			PackageImports: graph.PackageImports,
			PackageDeps:    graph.PackageDeps,
		},
	}
	for _, v := range views {
		for path, pkg := range v.Workspace.Packages {
			if union.Packages[path] == nil {
				union.Packages[path] = pkg
			}
		}
		for importPath, path := range v.Workspace.ImportToPath {
			_, ok := union.ImportToPath[importPath]
			if !ok {
				union.ImportToPath[importPath] = path
			}
		}
	}
	return union
}

// declaredSymbolCounts counts, per package path, the declarations of functions,
// types, variables and constants over all workspaces; one declaration in a file
// several of them include counts once.
func declaredSymbolCounts(wss ...*types.Workspace) map[string]int {
	seen := make(map[string]map[declSite]bool)
	for _, ws := range wss {
		for path, pkg := range ws.Packages {
			if seen[path] == nil {
				seen[path] = make(map[declSite]bool)
			}
			if pkg.Symbols == nil {
				continue
			}
			for _, group := range []map[string]*types.Symbol{pkg.Symbols.Functions, pkg.Symbols.Types, pkg.Symbols.Variables, pkg.Symbols.Constants} {
				for _, sym := range group {
					seen[path][declSite{sym.File, sym.Position}] = true
				}
			}
		}
	}
	counts := make(map[string]int, len(seen))
	for path, sites := range seen {
		counts[path] = len(sites)
	}
	return counts
}
