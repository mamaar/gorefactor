package graph

import (
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// ImportGraph represents import relationships in a Go workspace
type ImportGraph struct {
	Nodes        map[string]*ImportNode  // import path -> node
	Edges        map[string][]*ImportEdge // source -> edges
	ExternalDeps map[string]bool         // external dependencies
}

// ImportNode represents a single import in the dependency graph
type ImportNode struct {
	Path         string
	IsExternal   bool
	Package      *types.Package  // nil for external packages
	ImportedBy   []*ImportNode
	Imports      []*ImportNode
}

// ImportEdge represents an import relationship
type ImportEdge struct {
	From     *ImportNode
	To       *ImportNode
	IsTest   bool  // true if this is a test-only import
}

// NewImportGraph creates a new import dependency graph
func NewImportGraph() *ImportGraph {
	return &ImportGraph{
		Nodes:        make(map[string]*ImportNode),
		Edges:        make(map[string][]*ImportEdge),
		ExternalDeps: make(map[string]bool),
	}
}

// AddPackage adds a package and its imports to the graph
func (ig *ImportGraph) AddPackage(pkg *types.Package) *ImportNode {
	node := ig.getOrCreateNode(pkg.Path, false)
	node.Package = pkg

	// Add direct imports
	for _, importPath := range pkg.Imports {
		ig.AddImport(pkg.Path, importPath, false)
	}

	// Add test imports
	for _, testFile := range pkg.TestFiles {
		for _, imp := range testFile.AST.Imports {
			importPath := strings.Trim(imp.Path.Value, "\"")
			ig.AddImport(pkg.Path, importPath, true)
		}
	}

	return node
}

// AddImport adds an import relationship between two packages
func (ig *ImportGraph) AddImport(from, to string, isTest bool) {
	fromNode := ig.getOrCreateNode(from, false)
	toNode := ig.getOrCreateNode(to, ig.isExternalPackage(to))

	// Check if edge already exists
	for _, edge := range ig.Edges[from] {
		if edge.To.Path == to && edge.IsTest == isTest {
			return
		}
	}

	edge := &ImportEdge{
		From:   fromNode,
		To:     toNode,
		IsTest: isTest,
	}

	ig.Edges[from] = append(ig.Edges[from], edge)
	fromNode.Imports = append(fromNode.Imports, toNode)
	toNode.ImportedBy = append(toNode.ImportedBy, fromNode)

	if toNode.IsExternal {
		ig.ExternalDeps[to] = true
	}
}

// GetDirectImports returns direct imports of a package
func (ig *ImportGraph) GetDirectImports(pkgPath string) []*ImportNode {
	if node, exists := ig.Nodes[pkgPath]; exists {
		return node.Imports
	}
	return nil
}

// GetImporters returns packages that import the given package
func (ig *ImportGraph) GetImporters(pkgPath string) []*ImportNode {
	if node, exists := ig.Nodes[pkgPath]; exists {
		return node.ImportedBy
	}
	return nil
}

// GetTransitiveImports returns all transitive imports of a package
func (ig *ImportGraph) GetTransitiveImports(pkgPath string) []*ImportNode {
	visited := make(map[string]bool)
	var result []*ImportNode

	var visit func(string)
	visit = func(pkg string) {
		if visited[pkg] {
			return
		}
		visited[pkg] = true

		if node, exists := ig.Nodes[pkg]; exists {
			for _, imp := range node.Imports {
				if !imp.IsExternal { // Only include workspace packages
					result = append(result, imp)
					visit(imp.Path)
				}
			}
		}
	}

	visit(pkgPath)
	return removeDuplicateImportNodes(result)
}

// DetectImportCycles finds all import cycles in the graph
func (ig *ImportGraph) DetectImportCycles() [][]string {
	var cycles [][]string
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var dfs func(string, []string) []string
	dfs = func(pkg string, path []string) []string {
		visited[pkg] = true
		recStack[pkg] = true
		newPath := append(path, pkg)

		for _, edge := range ig.Edges[pkg] {
			if edge.IsTest || edge.To.IsExternal {
				continue // Skip test imports and external deps for cycle detection
			}

			imp := edge.To.Path
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

	for pkg, node := range ig.Nodes {
		if !node.IsExternal && !visited[pkg] {
			if cycle := dfs(pkg, []string{}); cycle != nil {
				cycles = append(cycles, cycle)
			}
		}
	}

	return cycles
}

// GetExternalDependencies returns all external dependencies
func (ig *ImportGraph) GetExternalDependencies() []string {
	var external []string
	for dep := range ig.ExternalDeps {
		external = append(external, dep)
	}
	return external
}

// GetPackageImportLevel calculates the import level of each package
func (ig *ImportGraph) GetPackageImportLevel() map[string]int {
	levels := make(map[string]int)
	visited := make(map[string]int)

	var calculateLevel func(string) int
	calculateLevel = func(pkg string) int {
		if level, exists := visited[pkg]; exists {
			return level
		}

		if node, exists := ig.Nodes[pkg]; exists && node.IsExternal {
			visited[pkg] = 0
			return 0
		}

		maxImportLevel := -1
		if node, exists := ig.Nodes[pkg]; exists {
			for _, imp := range node.Imports {
				if !imp.IsExternal {
					impLevel := calculateLevel(imp.Path)
					if impLevel > maxImportLevel {
						maxImportLevel = impLevel
					}
				}
			}
		}

		level := maxImportLevel + 1
		visited[pkg] = level
		return level
	}

	for pkg, node := range ig.Nodes {
		if !node.IsExternal {
			levels[pkg] = calculateLevel(pkg)
		}
	}

	return levels
}

// FindUnusedImports finds imports that are not used
func (ig *ImportGraph) FindUnusedImports(pkg *types.Package) []string {
	// This is a simplified implementation
	// A complete implementation would analyze AST usage
	var unused []string

	if pkg == nil {
		return unused
	}

	// Get all imports for the package
	_, exists := ig.Nodes[pkg.Path]
	if !exists {
		return unused
	}

	// For now, just return empty slice
	// Real implementation would check if imported symbols are actually used
	return unused
}

// GetImportMetrics returns metrics about imports
func (ig *ImportGraph) GetImportMetrics() ImportMetrics {
	metrics := ImportMetrics{
		TotalPackages:       0,
		ExternalPackages:    len(ig.ExternalDeps),
		TotalImports:        0,
		AverageImports:      0,
		MaxImports:          0,
		CyclicalImports:     0,
	}

	var totalImports int
	var internalPackages int
	maxImports := 0

	for _, node := range ig.Nodes {
		if !node.IsExternal {
			internalPackages++
			imports := len(node.Imports)
			totalImports += imports
			if imports > maxImports {
				maxImports = imports
			}
		}
	}

	cycles := ig.DetectImportCycles()
	for _, cycle := range cycles {
		metrics.CyclicalImports += len(cycle)
	}

	metrics.TotalPackages = internalPackages
	metrics.MaxImports = maxImports
	
	if internalPackages > 0 {
		metrics.AverageImports = float64(totalImports) / float64(internalPackages)
	}

	for _, edges := range ig.Edges {
		metrics.TotalImports += len(edges)
	}

	return metrics
}

// WouldCreateCycle checks if adding an import would create a cycle
func (ig *ImportGraph) WouldCreateCycle(from, to string) bool {
	// Check if 'to' transitively imports 'from'
	transitiveImports := ig.GetTransitiveImports(to)
	for _, imp := range transitiveImports {
		if imp.Path == from {
			return true
		}
	}
	return false
}

// Helper types and functions

type ImportMetrics struct {
	TotalPackages    int
	ExternalPackages int
	TotalImports     int
	AverageImports   float64
	MaxImports       int
	CyclicalImports  int
}

func (ig *ImportGraph) getOrCreateNode(path string, isExternal bool) *ImportNode {
	if node, exists := ig.Nodes[path]; exists {
		return node
	}

	node := &ImportNode{
		Path:       path,
		IsExternal: isExternal,
		ImportedBy: make([]*ImportNode, 0),
		Imports:    make([]*ImportNode, 0),
	}

	ig.Nodes[path] = node
	if _, exists := ig.Edges[path]; !exists {
		ig.Edges[path] = make([]*ImportEdge, 0)
	}

	return node
}

func (ig *ImportGraph) isExternalPackage(path string) bool {
	// Simple heuristic: if it doesn't start with the workspace module path
	// or common internal patterns, it's external
	if strings.HasPrefix(path, ".") || strings.HasPrefix(path, "/") {
		return false
	}
	
	// Check for standard library packages
	if !strings.Contains(path, ".") {
		return true
	}
	
	// Check for common external patterns
	if strings.Contains(path, "github.com") || 
	   strings.Contains(path, "golang.org") ||
	   strings.Contains(path, "google.golang.org") {
		return true
	}
	
	return false
}

func removeDuplicateImportNodes(nodes []*ImportNode) []*ImportNode {
	seen := make(map[string]bool)
	var result []*ImportNode

	for _, node := range nodes {
		if !seen[node.Path] {
			seen[node.Path] = true
			result = append(result, node)
		}
	}

	return result
}