package graph

import (
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestNewPackageGraph(t *testing.T) {
	graph := NewPackageGraph()
	if graph == nil {
		t.Fatal("Expected NewPackageGraph to return a non-nil graph")
	}

	if graph.Nodes == nil {
		t.Error("Expected Nodes to be initialized")
	}

	if graph.Edges == nil {
		t.Error("Expected Edges to be initialized")
	}

	if len(graph.Nodes) != 0 {
		t.Errorf("Expected empty Nodes map, got %d entries", len(graph.Nodes))
	}

	if len(graph.Edges) != 0 {
		t.Errorf("Expected empty Edges map, got %d entries", len(graph.Edges))
	}
}

func TestPackageGraph_AddPackage(t *testing.T) {
	graph := NewPackageGraph()
	
	pkg := &types.Package{
		Name: "testpkg",
		Path: "test/package",
		Dir:  "/test/package",
	}

	node := graph.AddPackage(pkg)
	if node == nil {
		t.Fatal("Expected AddPackage to return a non-nil node")
	}

	if node.Path != pkg.Path {
		t.Errorf("Expected node path '%s', got '%s'", pkg.Path, node.Path)
	}

	if node.Package != pkg {
		t.Error("Expected node to reference the package")
	}

	if len(node.Dependencies) != 0 {
		t.Errorf("Expected empty dependencies, got %d", len(node.Dependencies))
	}

	if len(node.Dependents) != 0 {
		t.Errorf("Expected empty dependents, got %d", len(node.Dependents))
	}

	// Check that node was added to graph
	if len(graph.Nodes) != 1 {
		t.Errorf("Expected 1 node in graph, got %d", len(graph.Nodes))
	}

	if graph.Nodes[pkg.Path] != node {
		t.Error("Expected node to be stored in graph")
	}

	// Test adding the same package again
	node2 := graph.AddPackage(pkg)
	if node2 != node {
		t.Error("Expected AddPackage to return the same node for the same package")
	}

	if len(graph.Nodes) != 1 {
		t.Errorf("Expected still 1 node in graph, got %d", len(graph.Nodes))
	}
}

func TestPackageGraph_AddDependency(t *testing.T) {
	graph := NewPackageGraph()
	
	pkg1 := &types.Package{Path: "pkg1"}
	pkg2 := &types.Package{Path: "pkg2"}
	
	node1 := graph.AddPackage(pkg1)
	node2 := graph.AddPackage(pkg2)

	// Add dependency from pkg1 to pkg2
	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)

	// Check that dependency was added
	if len(node1.Dependencies) != 1 {
		t.Errorf("Expected 1 dependency for pkg1, got %d", len(node1.Dependencies))
	}

	if node1.Dependencies[0] != node2 {
		t.Error("Expected pkg1 to depend on pkg2")
	}

	if len(node2.Dependents) != 1 {
		t.Errorf("Expected 1 dependent for pkg2, got %d", len(node2.Dependents))
	}

	if node2.Dependents[0] != node1 {
		t.Error("Expected pkg2 to have pkg1 as dependent")
	}

	// Check edges
	if len(graph.Edges["pkg1"]) != 1 {
		t.Errorf("Expected 1 edge from pkg1, got %d", len(graph.Edges["pkg1"]))
	}

	edge := graph.Edges["pkg1"][0]
	if edge.From != node1 {
		t.Error("Expected edge to start from node1")
	}

	if edge.To != node2 {
		t.Error("Expected edge to end at node2")
	}

	if edge.Type != PackageImportEdge {
		t.Errorf("Expected PackageImportEdge, got %v", edge.Type)
	}

	// Test adding duplicate dependency
	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	if len(graph.Edges["pkg1"]) != 1 {
		t.Errorf("Expected still 1 edge from pkg1 after duplicate, got %d", len(graph.Edges["pkg1"]))
	}
}

func TestPackageGraph_GetDependencies(t *testing.T) {
	graph := NewPackageGraph()
	
	pkg1 := &types.Package{Path: "pkg1"}
	pkg2 := &types.Package{Path: "pkg2"}
	pkg3 := &types.Package{Path: "pkg3"}
	
	graph.AddPackage(pkg1)
	graph.AddPackage(pkg2)
	graph.AddPackage(pkg3)

	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg1", "pkg3", PackageImportEdge)

	deps := graph.GetDependencies("pkg1")
	if len(deps) != 2 {
		t.Errorf("Expected 2 dependencies, got %d", len(deps))
	}

	// Test non-existent package
	deps = graph.GetDependencies("nonexistent")
	if deps != nil {
		t.Error("Expected nil for non-existent package")
	}
}

func TestPackageGraph_GetDependents(t *testing.T) {
	graph := NewPackageGraph()
	
	pkg1 := &types.Package{Path: "pkg1"}
	pkg2 := &types.Package{Path: "pkg2"}
	pkg3 := &types.Package{Path: "pkg3"}
	
	graph.AddPackage(pkg1)
	graph.AddPackage(pkg2)
	graph.AddPackage(pkg3)

	graph.AddDependency("pkg1", "pkg3", PackageImportEdge)
	graph.AddDependency("pkg2", "pkg3", PackageImportEdge)

	dependents := graph.GetDependents("pkg3")
	if len(dependents) != 2 {
		t.Errorf("Expected 2 dependents, got %d", len(dependents))
	}

	// Test non-existent package
	dependents = graph.GetDependents("nonexistent")
	if dependents != nil {
		t.Error("Expected nil for non-existent package")
	}
}

func TestPackageGraph_GetTransitiveDependencies(t *testing.T) {
	graph := NewPackageGraph()
	
	// Create a chain: pkg1 -> pkg2 -> pkg3 -> pkg4
	packages := []*types.Package{
		{Path: "pkg1"},
		{Path: "pkg2"},
		{Path: "pkg3"},
		{Path: "pkg4"},
	}

	for _, pkg := range packages {
		graph.AddPackage(pkg)
	}

	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg2", "pkg3", PackageImportEdge)
	graph.AddDependency("pkg3", "pkg4", PackageImportEdge)

	// Get transitive dependencies of pkg1
	transitive := graph.GetTransitiveDependencies("pkg1")
	
	// Should include pkg2, pkg3, pkg4
	if len(transitive) != 3 {
		t.Errorf("Expected 3 transitive dependencies, got %d", len(transitive))
	}

	expectedPaths := map[string]bool{"pkg2": true, "pkg3": true, "pkg4": true}
	for _, dep := range transitive {
		if !expectedPaths[dep.Path] {
			t.Errorf("Unexpected transitive dependency: %s", dep.Path)
		}
		delete(expectedPaths, dep.Path)
	}

	if len(expectedPaths) > 0 {
		t.Errorf("Missing transitive dependencies: %v", expectedPaths)
	}
}

func TestPackageGraph_DetectCycles(t *testing.T) {
	graph := NewPackageGraph()
	
	// Create packages
	packages := []*types.Package{
		{Path: "pkg1"},
		{Path: "pkg2"},
		{Path: "pkg3"},
	}

	for _, pkg := range packages {
		graph.AddPackage(pkg)
	}

	// Create a cycle: pkg1 -> pkg2 -> pkg3 -> pkg1
	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg2", "pkg3", PackageImportEdge)
	graph.AddDependency("pkg3", "pkg1", PackageImportEdge)

	cycles := graph.DetectCycles()
	if len(cycles) == 0 {
		t.Error("Expected to detect at least one cycle")
	}

	// Check that detected cycle contains expected packages
	if len(cycles) > 0 {
		cycle := cycles[0]
		if len(cycle) < 3 {
			t.Errorf("Expected cycle length >= 3, got %d", len(cycle))
		}

		// Verify cycle contains our packages
		cycleMap := make(map[string]bool)
		for _, pkg := range cycle {
			cycleMap[pkg] = true
		}

		expectedPackages := []string{"pkg1", "pkg2", "pkg3"}
		for _, expectedPkg := range expectedPackages {
			if !cycleMap[expectedPkg] {
				t.Errorf("Expected package %s to be in cycle", expectedPkg)
			}
		}
	}
}

func TestPackageGraph_DetectCycles_NoCycle(t *testing.T) {
	graph := NewPackageGraph()
	
	// Create packages with no cycles
	packages := []*types.Package{
		{Path: "pkg1"},
		{Path: "pkg2"},
		{Path: "pkg3"},
	}

	for _, pkg := range packages {
		graph.AddPackage(pkg)
	}

	// Create a linear dependency: pkg1 -> pkg2 -> pkg3
	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg2", "pkg3", PackageImportEdge)

	cycles := graph.DetectCycles()
	if len(cycles) != 0 {
		t.Errorf("Expected no cycles, got %d", len(cycles))
	}
}

func TestPackageGraph_TopologicalSort(t *testing.T) {
	graph := NewPackageGraph()
	
	// Create packages
	packages := []*types.Package{
		{Path: "pkg1"},
		{Path: "pkg2"},
		{Path: "pkg3"},
		{Path: "pkg4"},
	}

	for _, pkg := range packages {
		graph.AddPackage(pkg)
	}

	// Create dependencies: pkg1 -> pkg2, pkg1 -> pkg3, pkg2 -> pkg4, pkg3 -> pkg4
	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg1", "pkg3", PackageImportEdge)
	graph.AddDependency("pkg2", "pkg4", PackageImportEdge)
	graph.AddDependency("pkg3", "pkg4", PackageImportEdge)

	sorted, err := graph.TopologicalSort()
	if err != nil {
		t.Fatalf("Failed to perform topological sort: %v", err)
	}

	if len(sorted) != 4 {
		t.Errorf("Expected 4 packages in sorted order, got %d", len(sorted))
	}

	// Create position map for validation
	positions := make(map[string]int)
	for i, node := range sorted {
		positions[node.Path] = i
	}

	// Validate that dependencies come after dependents in the sorted order
	if positions["pkg4"] <= positions["pkg2"] {
		t.Error("pkg4 should come after pkg2 in topological order")
	}
	if positions["pkg4"] <= positions["pkg3"] {
		t.Error("pkg4 should come after pkg3 in topological order")
	}
	if positions["pkg2"] <= positions["pkg1"] {
		t.Error("pkg2 should come after pkg1 in topological order")
	}
	if positions["pkg3"] <= positions["pkg1"] {
		t.Error("pkg3 should come after pkg1 in topological order")
	}
}

func TestPackageGraph_TopologicalSort_WithCycle(t *testing.T) {
	graph := NewPackageGraph()
	
	// Create packages
	packages := []*types.Package{
		{Path: "pkg1"},
		{Path: "pkg2"},
	}

	for _, pkg := range packages {
		graph.AddPackage(pkg)
	}

	// Create a cycle
	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg2", "pkg1", PackageImportEdge)

	_, err := graph.TopologicalSort()
	if err == nil {
		t.Error("Expected error when performing topological sort with cycle")
	}

	// Check that it's a RefactorError with CyclicDependency
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.CyclicDependency {
			t.Errorf("Expected CyclicDependency error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestPackageGraph_GetPackagesByLevel(t *testing.T) {
	graph := NewPackageGraph()
	
	// Create packages
	packages := []*types.Package{
		{Path: "pkg1"}, // Level 0 (no dependencies)
		{Path: "pkg2"}, // Level 1 (depends on pkg1)
		{Path: "pkg3"}, // Level 1 (depends on pkg1)
		{Path: "pkg4"}, // Level 2 (depends on pkg2 and pkg3)
	}

	for _, pkg := range packages {
		graph.AddPackage(pkg)
	}

	// Create dependencies
	graph.AddDependency("pkg2", "pkg1", PackageImportEdge)
	graph.AddDependency("pkg3", "pkg1", PackageImportEdge)
	graph.AddDependency("pkg4", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg4", "pkg3", PackageImportEdge)

	levels := graph.GetPackagesByLevel()

	// Check level 0
	if len(levels[0]) != 1 {
		t.Errorf("Expected 1 package at level 0, got %d", len(levels[0]))
	}
	if levels[0][0].Path != "pkg1" {
		t.Errorf("Expected pkg1 at level 0, got %s", levels[0][0].Path)
	}

	// Check level 1
	if len(levels[1]) != 2 {
		t.Errorf("Expected 2 packages at level 1, got %d", len(levels[1]))
	}

	level1Paths := make(map[string]bool)
	for _, node := range levels[1] {
		level1Paths[node.Path] = true
	}
	if !level1Paths["pkg2"] || !level1Paths["pkg3"] {
		t.Error("Expected pkg2 and pkg3 at level 1")
	}

	// Check level 2
	if len(levels[2]) != 1 {
		t.Errorf("Expected 1 package at level 2, got %d", len(levels[2]))
	}
	if levels[2][0].Path != "pkg4" {
		t.Errorf("Expected pkg4 at level 2, got %s", levels[2][0].Path)
	}
}

func TestPackageGraph_RemovePackage(t *testing.T) {
	graph := NewPackageGraph()
	
	packages := []*types.Package{
		{Path: "pkg1"},
		{Path: "pkg2"},
		{Path: "pkg3"},
	}

	for _, pkg := range packages {
		graph.AddPackage(pkg)
	}

	// Create dependencies
	graph.AddDependency("pkg1", "pkg2", PackageImportEdge)
	graph.AddDependency("pkg2", "pkg3", PackageImportEdge)

	// Remove pkg2
	graph.RemovePackage("pkg2")

	// Check that pkg2 was removed
	if _, exists := graph.Nodes["pkg2"]; exists {
		t.Error("Expected pkg2 to be removed from nodes")
	}

	if _, exists := graph.Edges["pkg2"]; exists {
		t.Error("Expected pkg2 to be removed from edges")
	}

	// Check that references to pkg2 were cleaned up
	pkg1Node := graph.Nodes["pkg1"]
	if len(pkg1Node.Dependencies) != 0 {
		t.Errorf("Expected pkg1 to have 0 dependencies after pkg2 removal, got %d", len(pkg1Node.Dependencies))
	}

	pkg3Node := graph.Nodes["pkg3"]
	if len(pkg3Node.Dependents) != 0 {
		t.Errorf("Expected pkg3 to have 0 dependents after pkg2 removal, got %d", len(pkg3Node.Dependents))
	}

	// Test removing non-existent package (should not panic)
	graph.RemovePackage("nonexistent")
}

func TestEdgeType(t *testing.T) {
	testCases := []struct {
		name     string
		edgeType EdgeType
		expected EdgeType
	}{
		{"PackageImportEdge", PackageImportEdge, 0},
		{"TransitiveEdge", TransitiveEdge, 1},
		{"TestEdge", TestEdge, 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.edgeType != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.edgeType)
			}
		})
	}
}