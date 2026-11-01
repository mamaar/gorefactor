package refactor

import (
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

	plan, err := e.AnalyzeDependencies(views[0].Workspace, req)
	if err != nil {
		return nil, err
	}
	graph := mergeGraphs(views)
	union := unionWorkspace(views, graph)
	plan.Impact.PackageCoupling = packageCouplingOf(union, declaredSymbolCounts(workspacesOf(views)...))
	if req.DetectBackwardsDeps {
		plan.Impact.BackwardsDependencies = detectBackwardsDependencies(union)
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
	configs := viewNames(views)
	cycles := make([][][]string, len(views))
	testCycles := make([][][]string, len(views))
	for i, v := range views {
		g := v.Workspace.Dependencies
		unionInto(out.PackageImports, g.PackageImports)
		unionInto(out.PackageDeps, g.PackageDeps)
		cycles[i], testCycles[i] = g.ImportCycles, g.TestImportCycles
	}
	out.ImportCycles = mergeCycles(configs, cycles)
	out.TestImportCycles = mergeCycles(configs, testCycles)
	return out
}

// unionInto adds the values of from to those of into, keeping each sorted and distinct.
func unionInto(into, from map[string][]string) {
	for k, vs := range from {
		merged := slices.Concat(into[k], vs)
		slices.Sort(merged)
		into[k] = slices.Compact(merged)
	}
}

// mergeCycles merges the closed paths each configuration reports. A cycle
// several configurations report, from whichever package, is one entry.
func mergeCycles(configs []string, perView [][][]string) []ImportCycle {
	found := make(map[string]*ImportCycle)
	for i, cycles := range perView {
		for _, c := range cycles {
			key := cycleKey(c)
			f := found[key]
			if f == nil {
				f = &ImportCycle{Packages: c}
				found[key] = f
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

// cycleKey identifies a closed path by its members in order, from the smallest.
func cycleKey(closed []string) string {
	ring := closed[:len(closed)-1]
	at := 0
	for i, p := range ring {
		if p < ring[at] {
			at = i
		}
	}
	return strings.Join(append(slices.Clone(ring[at:]), ring[:at]...), "\x00")
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
		if pkg := v.Workspace.Packages[fsPath]; pkg != nil && pkg.ImportPath != "" {
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
			if _, ok := union.ImportToPath[importPath]; !ok {
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

func workspacesOf(views []*analysis.View) []*types.Workspace {
	wss := make([]*types.Workspace, len(views))
	for i, v := range views {
		wss[i] = v.Workspace
	}
	return wss
}
