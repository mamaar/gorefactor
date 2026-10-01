package analysis

import (
	"log/slog"
	"slices"

	"github.com/mamaar/gorefactor/pkg/types"
)

// DependencyAnalyzer analyzes package and symbol dependencies
type DependencyAnalyzer struct {
	workspace *types.Workspace
	logger    *slog.Logger
}

func NewDependencyAnalyzer(ws *types.Workspace, logger *slog.Logger) *DependencyAnalyzer {
	return &DependencyAnalyzer{
		workspace: ws,
		logger:    logger,
	}
}

// BuildDependencyGraph builds the dependency graph for the workspace.
func (da *DependencyAnalyzer) BuildDependencyGraph() (*types.DependencyGraph, error) {
	da.logger.Info("building dependency graph", "packages", len(da.workspace.Packages))

	imports := make(map[string][]string)
	for _, pkg := range da.workspace.Packages {
		da.collectPackageImports(pkg, imports)
	}

	edges := ImportGraph(da.workspace)
	graph := &types.DependencyGraph{
		PackageImports:   imports,
		PackageDeps:      transitiveClose(edges.Production),
		ImportCycles:     importCycles(edges.Production),
		TestImportCycles: testImportCycles(edges),
	}

	if n := len(graph.ImportCycles); n > 0 {
		da.logger.Warn("detected import cycles", "cycle_count", n)
	}

	da.workspace.Dependencies = graph
	return graph, nil
}

// AnalyzeImpact analyzes impact of a potential change
func (da *DependencyAnalyzer) AnalyzeImpact(op types.Operation) (*types.ImpactAnalysis, error) {
	// For now, return a basic impact analysis for all operations
	// TODO: Implement operation-specific impact analysis
	impact := &types.ImpactAnalysis{
		AffectedPackages: make([]string, 0),
		AffectedFiles:    make([]string, 0),
		AffectedSymbols:  make([]*types.Symbol, 0),
		PotentialIssues:  make([]types.Issue, 0),
		ImportChanges:    make([]types.ImportChange, 0),
	}
	return impact, nil
}

// DetectCycles detects import cycles in package graph
func (da *DependencyAnalyzer) DetectCycles() ([][]string, error) {
	if da.workspace.Dependencies == nil {
		_, err := da.BuildDependencyGraph()
		if err != nil {
			return nil, err
		}
	}

	return da.workspace.Dependencies.ImportCycles, nil
}

func (da *DependencyAnalyzer) collectPackageImports(pkg *types.Package, imports map[string][]string) {
	if pkg == nil {
		return
	}

	var pkgImports []string
	for _, file := range pkg.Files {
		if file.AST == nil {
			continue
		}
		for _, imp := range file.AST.Imports {
			if imp.Path != nil {
				pkgImports = append(pkgImports, imp.Path.Value[1:len(imp.Path.Value)-1])
			}
		}
	}

	if len(pkgImports) > 0 {
		slices.Sort(pkgImports)
		imports[pkg.Path] = slices.Compact(pkgImports)
	}
}

// reachable returns the nodes reachable from start over one or more edges;
// start itself is included only when it lies on a cycle.
func reachable(edges map[string][]string, start string) map[string]bool {
	seen := make(map[string]bool)
	stack := slices.Clone(edges[start])
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, edges[n]...)
	}
	return seen
}

// transitiveClose maps each node to the sorted nodes it reaches, excluding itself.
func transitiveClose(edges map[string][]string) map[string][]string {
	result := make(map[string][]string, len(edges))
	for pkg := range edges {
		deps := make([]string, 0)
		for dep := range reachable(edges, pkg) {
			if dep != pkg {
				deps = append(deps, dep)
			}
		}
		slices.Sort(deps)
		result[pkg] = deps
	}
	return result
}

// shortestPath returns a shortest edge path from one of starts to target,
// preferring earlier starts and neighbors, or nil when none exists.
func shortestPath(edges map[string][]string, starts []string, target string) []string {
	prev := make(map[string]string)
	seen := make(map[string]bool)
	queue := slices.Clone(starts)
	for _, s := range starts {
		seen[s] = true
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n == target {
			path := []string{n}
			for p, ok := prev[n]; ok; p, ok = prev[p] {
				path = append([]string{p}, path...)
			}
			return path
		}
		for _, next := range edges[n] {
			if !seen[next] {
				seen[next] = true
				prev[next] = n
				queue = append(queue, next)
			}
		}
	}
	return nil
}

// importCycles returns one closed path per cyclic strongly connected
// component, rotated to start at its smallest node, sorted.
func importCycles(edges map[string][]string) [][]string {
	nodes := make([]string, 0, len(edges))
	for n := range edges {
		nodes = append(nodes, n)
	}
	slices.Sort(nodes)

	cycles := make([][]string, 0)
	covered := make(map[string]bool)
	for _, start := range nodes {
		if covered[start] {
			continue
		}
		reach := reachable(edges, start)
		if !reach[start] {
			continue
		}
		for n := range reach {
			if reachable(edges, n)[start] {
				covered[n] = true
			}
		}
		cycles = append(cycles, append([]string{start}, shortestPath(edges, edges[start], start)...))
	}
	return cycles
}

// testImportCycles returns, per package, the closed path through which an
// in-package test import leads back to the package via production edges.
func testImportCycles(g Imports) [][]string {
	pkgs := make([]string, 0, len(g.Test))
	for p := range g.Test {
		pkgs = append(pkgs, p)
	}
	slices.Sort(pkgs)

	cycles := make([][]string, 0)
	for _, p := range pkgs {
		var best []string
		for _, t := range g.Test[p] {
			if path := shortestPath(g.Production, []string{t}, p); path != nil && (best == nil || len(path) < len(best)) {
				best = path
			}
		}
		if best != nil {
			cycles = append(cycles, append([]string{p}, best...))
		}
	}
	return cycles
}
