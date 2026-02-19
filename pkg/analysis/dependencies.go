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

// BuildDependencyGraph builds complete dependency graph for workspace
func (da *DependencyAnalyzer) BuildDependencyGraph() (*types.DependencyGraph, error) {
	da.logger.Info("building dependency graph", "packages", len(da.workspace.Packages))

	graph := &types.DependencyGraph{
		PackageImports: make(map[string][]string),
		PackageDeps:    make(map[string][]string),
		SymbolDeps:     make(map[string]map[string][]string),
		ImportCycles:   make([][]string, 0),
	}

	// Collect direct package imports
	imports := make(map[string][]string)
	for _, pkg := range da.workspace.Packages {
		da.collectPackageImports(pkg, imports)
	}

	graph.PackageImports = imports

	// Build transitive closure of dependencies
	graph.PackageDeps = da.transitiveClose(imports)

	// Detect import cycles
	cycles := da.detectCycles(imports)
	graph.ImportCycles = cycles

	if len(cycles) > 0 {
		da.logger.Warn("detected import cycles", "cycle_count", len(cycles))
	}

	// Build symbol dependencies (simplified)
	err := da.buildSymbolDependencies(graph)
	if err != nil {
		return nil, err
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

func (da *DependencyAnalyzer) detectCycles(imports map[string][]string) [][]string {
	var cycles [][]string
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var dfs func(string, []string) []string
	dfs = func(pkg string, path []string) []string {
		visited[pkg] = true
		recStack[pkg] = true
		newPath := append(append([]string{}, path...), pkg)

		for _, imp := range imports[pkg] {
			if !visited[imp] {
				if cycle := dfs(imp, newPath); cycle != nil {
					return cycle
				}
			} else if recStack[imp] {
				// Found a cycle
				cycleStart := -1
				for i, p := range newPath {
					if p == imp {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					return newPath[cycleStart:]
				}
			}
		}

		recStack[pkg] = false
		return nil
	}

	for pkg := range imports {
		if !visited[pkg] {
			if cycle := dfs(pkg, []string{}); cycle != nil {
				cycles = append(cycles, cycle)
			}
		}
	}

	return cycles
}

func (da *DependencyAnalyzer) buildSymbolDependencies(graph *types.DependencyGraph) error {
	// Simplified implementation for now
	graph.SymbolDeps = make(map[string]map[string][]string)
	return nil
}

func (da *DependencyAnalyzer) collectPackageImports(pkg *types.Package, imports map[string][]string) {
	if pkg == nil {
		return
	}

	pkgImports := make([]string, 0)
	for _, file := range pkg.Files {
		if file.AST != nil {
			for _, imp := range file.AST.Imports {
				if imp.Path != nil {
					// Remove quotes from import path
					importPath := imp.Path.Value[1 : len(imp.Path.Value)-1]
					pkgImports = append(pkgImports, importPath)
				}
			}
		}
	}

	if len(pkgImports) > 0 {
		imports[pkg.Path] = unique(pkgImports)
	}
}

func (da *DependencyAnalyzer) transitiveClose(deps map[string][]string) map[string][]string {
	result := make(map[string][]string)

	// Copy direct dependencies
	for pkg, directDeps := range deps {
		result[pkg] = make([]string, len(directDeps))
		copy(result[pkg], directDeps)
	}

	// Floyd-Warshall-like algorithm for transitive closure
	changed := true
	for changed {
		changed = false
		for pkg := range result {
			for _, intermediate := range result[pkg] {
				if intermediateDeps, exists := result[intermediate]; exists {
					for _, transitiveDep := range intermediateDeps {
						if !sliceContains(result[pkg], transitiveDep) && transitiveDep != pkg {
							result[pkg] = append(result[pkg], transitiveDep)
							changed = true
						}
					}
				}
			}
		}
	}

	return result
}

// Helper function to check if slice contains string
func sliceContains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}

func unique(slice []string) []string {
	keys := make(map[string]bool)
	result := []string{}

	for _, item := range slice {
		if !keys[item] {
			keys[item] = true
			result = append(result, item)
		}
	}

	return result
}
