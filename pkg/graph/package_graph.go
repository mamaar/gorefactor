package graph

import (
	"github.com/mamaar/gorefactor/pkg/types"
)

// PackageGraph represents dependencies between packages
type PackageGraph struct {
	Nodes map[string]*PackageNode  // package path -> node
	Edges map[string][]*PackageEdge // source package -> edges
}

// PackageNode represents a single package in the dependency graph
type PackageNode struct {
	Path         string
	Package      *types.Package
	Dependencies []*PackageNode
	Dependents   []*PackageNode
}

// PackageEdge represents a dependency relationship between packages
type PackageEdge struct {
	From   *PackageNode
	To     *PackageNode
	Type   EdgeType
	Weight int
}

type EdgeType int

const (
	PackageImportEdge EdgeType = iota
	TransitiveEdge
	TestEdge
)

// NewPackageGraph creates a new package dependency graph
func NewPackageGraph() *PackageGraph {
	return &PackageGraph{
		Nodes: make(map[string]*PackageNode),
		Edges: make(map[string][]*PackageEdge),
	}
}

// AddPackage adds a package node to the graph
func (pg *PackageGraph) AddPackage(pkg *types.Package) *PackageNode {
	if node, exists := pg.Nodes[pkg.Path]; exists {
		return node
	}

	node := &PackageNode{
		Path:         pkg.Path,
		Package:      pkg,
		Dependencies: make([]*PackageNode, 0),
		Dependents:   make([]*PackageNode, 0),
	}

	pg.Nodes[pkg.Path] = node
	pg.Edges[pkg.Path] = make([]*PackageEdge, 0)

	return node
}

// AddDependency adds a dependency edge between two packages
func (pg *PackageGraph) AddDependency(from, to string, edgeType EdgeType) {
	fromNode, fromExists := pg.Nodes[from]
	toNode, toExists := pg.Nodes[to]

	if !fromExists || !toExists {
		return
	}

	// Check if edge already exists
	for _, edge := range pg.Edges[from] {
		if edge.To.Path == to && edge.Type == edgeType {
			return
		}
	}

	edge := &PackageEdge{
		From:   fromNode,
		To:     toNode,
		Type:   edgeType,
		Weight: 1,
	}

	pg.Edges[from] = append(pg.Edges[from], edge)
	fromNode.Dependencies = append(fromNode.Dependencies, toNode)
	toNode.Dependents = append(toNode.Dependents, fromNode)
}

// GetDependencies returns direct dependencies of a package
func (pg *PackageGraph) GetDependencies(pkgPath string) []*PackageNode {
	if node, exists := pg.Nodes[pkgPath]; exists {
		return node.Dependencies
	}
	return nil
}

// GetDependents returns packages that depend on the given package
func (pg *PackageGraph) GetDependents(pkgPath string) []*PackageNode {
	if node, exists := pg.Nodes[pkgPath]; exists {
		return node.Dependents
	}
	return nil
}

// GetTransitiveDependencies returns all transitive dependencies of a package
func (pg *PackageGraph) GetTransitiveDependencies(pkgPath string) []*PackageNode {
	visited := make(map[string]bool)
	var result []*PackageNode

	var visit func(string)
	visit = func(pkg string) {
		if visited[pkg] {
			return
		}
		visited[pkg] = true

		if node, exists := pg.Nodes[pkg]; exists {
			for _, dep := range node.Dependencies {
				result = append(result, dep)
				visit(dep.Path)
			}
		}
	}

	visit(pkgPath)
	return removeDuplicateNodes(result)
}

// DetectCycles finds all cycles in the package dependency graph
func (pg *PackageGraph) DetectCycles() [][]string {
	var cycles [][]string
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var dfs func(string, []string) []string
	dfs = func(pkg string, path []string) []string {
		visited[pkg] = true
		recStack[pkg] = true
		newPath := append(path, pkg)

		for _, edge := range pg.Edges[pkg] {
			dep := edge.To.Path
			if !visited[dep] {
				if cycle := dfs(dep, newPath); cycle != nil {
					return cycle
				}
			} else if recStack[dep] {
				// Found a cycle
				cycleStart := -1
				for i, p := range newPath {
					if p == dep {
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

	for pkg := range pg.Nodes {
		if !visited[pkg] {
			if cycle := dfs(pkg, []string{}); cycle != nil {
				cycles = append(cycles, cycle)
			}
		}
	}

	return cycles
}

// TopologicalSort returns packages in topological order
func (pg *PackageGraph) TopologicalSort() ([]*PackageNode, error) {
	var result []*PackageNode
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var visit func(string) error
	visit = func(pkg string) error {
		if recStack[pkg] {
			return &types.RefactorError{
				Type:    types.CyclicDependency,
				Message: "circular dependency detected",
			}
		}
		if visited[pkg] {
			return nil
		}

		visited[pkg] = true
		recStack[pkg] = true

		if node, exists := pg.Nodes[pkg]; exists {
			for _, dep := range node.Dependencies {
				err := visit(dep.Path)
				if err != nil {
					return err
				}
			}
			result = append([]*PackageNode{node}, result...)
		}

		recStack[pkg] = false
		return nil
	}

	for pkg := range pg.Nodes {
		if !visited[pkg] {
			err := visit(pkg)
			if err != nil {
				return nil, err
			}
		}
	}

	return result, nil
}

// GetPackagesByLevel returns packages grouped by dependency level
func (pg *PackageGraph) GetPackagesByLevel() map[int][]*PackageNode {
	levels := make(map[int][]*PackageNode)
	visited := make(map[string]int)

	var calculateLevel func(string) int
	calculateLevel = func(pkg string) int {
		if level, exists := visited[pkg]; exists {
			return level
		}

		maxDepLevel := -1
		if node, exists := pg.Nodes[pkg]; exists {
			for _, dep := range node.Dependencies {
				depLevel := calculateLevel(dep.Path)
				if depLevel > maxDepLevel {
					maxDepLevel = depLevel
				}
			}
		}

		level := maxDepLevel + 1
		visited[pkg] = level

		if levels[level] == nil {
			levels[level] = make([]*PackageNode, 0)
		}
		if node, exists := pg.Nodes[pkg]; exists {
			levels[level] = append(levels[level], node)
		}

		return level
	}

	for pkg := range pg.Nodes {
		calculateLevel(pkg)
	}

	return levels
}

// RemovePackage removes a package and all its edges from the graph
func (pg *PackageGraph) RemovePackage(pkgPath string) {
	node, exists := pg.Nodes[pkgPath]
	if !exists {
		return
	}

	// Remove edges from this package
	delete(pg.Edges, pkgPath)

	// Remove edges to this package
	for _, edges := range pg.Edges {
		for i := len(edges) - 1; i >= 0; i-- {
			if edges[i].To.Path == pkgPath {
				edges = append(edges[:i], edges[i+1:]...)
			}
		}
	}

	// Update dependencies and dependents
	for _, dep := range node.Dependencies {
		removeDependentFromNode(dep, node)
	}
	for _, dependent := range node.Dependents {
		removeDependencyFromNode(dependent, node)
	}

	delete(pg.Nodes, pkgPath)
}

// Helper functions

func removeDuplicateNodes(nodes []*PackageNode) []*PackageNode {
	seen := make(map[string]bool)
	var result []*PackageNode

	for _, node := range nodes {
		if !seen[node.Path] {
			seen[node.Path] = true
			result = append(result, node)
		}
	}

	return result
}

func removeDependentFromNode(node *PackageNode, dependent *PackageNode) {
	for i, dep := range node.Dependents {
		if dep.Path == dependent.Path {
			node.Dependents = append(node.Dependents[:i], node.Dependents[i+1:]...)
			break
		}
	}
}

func removeDependencyFromNode(node *PackageNode, dependency *PackageNode) {
	for i, dep := range node.Dependencies {
		if dep.Path == dependency.Path {
			node.Dependencies = append(node.Dependencies[:i], node.Dependencies[i+1:]...)
			break
		}
	}
}