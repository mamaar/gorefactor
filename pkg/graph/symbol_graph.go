package graph

import (
	"github.com/mamaar/gorefactor/pkg/types"
)

// SymbolGraph represents dependencies between symbols
type SymbolGraph struct {
	Nodes map[string]*SymbolNode  // symbol ID -> node
	Edges map[string][]*SymbolEdge // source symbol -> edges
}

// SymbolNode represents a single symbol in the dependency graph
type SymbolNode struct {
	ID           string  // package.symbolName
	Symbol       *types.Symbol
	Dependencies []*SymbolNode
	Dependents   []*SymbolNode
}

// SymbolEdge represents a dependency relationship between symbols
type SymbolEdge struct {
	From     *SymbolNode
	To       *SymbolNode
	Type     SymbolDependencyType
	Context  string  // Context of the dependency (e.g., "function call", "type reference")
}

type SymbolDependencyType int

const (
	CallDependency SymbolDependencyType = iota
	TypeDependency
	FieldDependency
	InheritanceDependency
	InterfaceDependency
)

// NewSymbolGraph creates a new symbol dependency graph
func NewSymbolGraph() *SymbolGraph {
	return &SymbolGraph{
		Nodes: make(map[string]*SymbolNode),
		Edges: make(map[string][]*SymbolEdge),
	}
}

// AddSymbol adds a symbol node to the graph
func (sg *SymbolGraph) AddSymbol(symbol *types.Symbol) *SymbolNode {
	id := sg.getSymbolID(symbol)
	if node, exists := sg.Nodes[id]; exists {
		return node
	}

	node := &SymbolNode{
		ID:           id,
		Symbol:       symbol,
		Dependencies: make([]*SymbolNode, 0),
		Dependents:   make([]*SymbolNode, 0),
	}

	sg.Nodes[id] = node
	sg.Edges[id] = make([]*SymbolEdge, 0)

	return node
}

// AddDependency adds a dependency edge between two symbols
func (sg *SymbolGraph) AddDependency(from, to *types.Symbol, depType SymbolDependencyType, context string) {
	fromID := sg.getSymbolID(from)
	toID := sg.getSymbolID(to)

	fromNode, fromExists := sg.Nodes[fromID]
	toNode, toExists := sg.Nodes[toID]

	if !fromExists {
		fromNode = sg.AddSymbol(from)
	}
	if !toExists {
		toNode = sg.AddSymbol(to)
	}

	// Check if edge already exists
	for _, edge := range sg.Edges[fromID] {
		if edge.To.ID == toID && edge.Type == depType {
			return
		}
	}

	edge := &SymbolEdge{
		From:    fromNode,
		To:      toNode,
		Type:    depType,
		Context: context,
	}

	sg.Edges[fromID] = append(sg.Edges[fromID], edge)
	fromNode.Dependencies = append(fromNode.Dependencies, toNode)
	toNode.Dependents = append(toNode.Dependents, fromNode)
}

// GetSymbolDependencies returns direct dependencies of a symbol
func (sg *SymbolGraph) GetSymbolDependencies(symbol *types.Symbol) []*SymbolNode {
	id := sg.getSymbolID(symbol)
	if node, exists := sg.Nodes[id]; exists {
		return node.Dependencies
	}
	return nil
}

// GetSymbolDependents returns symbols that depend on the given symbol
func (sg *SymbolGraph) GetSymbolDependents(symbol *types.Symbol) []*SymbolNode {
	id := sg.getSymbolID(symbol)
	if node, exists := sg.Nodes[id]; exists {
		return node.Dependents
	}
	return nil
}

// GetTransitiveSymbolDependencies returns all transitive dependencies of a symbol
func (sg *SymbolGraph) GetTransitiveSymbolDependencies(symbol *types.Symbol) []*SymbolNode {
	id := sg.getSymbolID(symbol)
	visited := make(map[string]bool)
	var result []*SymbolNode

	var visit func(string)
	visit = func(symbolID string) {
		if visited[symbolID] {
			return
		}
		visited[symbolID] = true

		if node, exists := sg.Nodes[symbolID]; exists {
			for _, dep := range node.Dependencies {
				result = append(result, dep)
				visit(dep.ID)
			}
		}
	}

	visit(id)
	return removeDuplicateSymbolNodes(result)
}

// FindSymbolsAffectedByMove returns symbols that would be affected by moving a symbol
func (sg *SymbolGraph) FindSymbolsAffectedByMove(symbol *types.Symbol, targetPackage string) []*SymbolNode {
	var affected []*SymbolNode
	dependents := sg.GetSymbolDependents(symbol)

	for _, dependent := range dependents {
		// If the dependent is in a different package and the symbol becomes unexported after move
		if dependent.Symbol.Package != targetPackage && !symbol.Exported {
			affected = append(affected, dependent)
		}
		// If moving would require import changes
		if dependent.Symbol.Package != targetPackage && dependent.Symbol.Package != symbol.Package {
			affected = append(affected, dependent)
		}
	}

	return affected
}

// FindSymbolsAffectedByRename returns symbols that would be affected by renaming a symbol
func (sg *SymbolGraph) FindSymbolsAffectedByRename(symbol *types.Symbol, newName string) []*SymbolNode {
	// Return all dependents as they would need their references updated
	return sg.GetSymbolDependents(symbol)
}

// GetSymbolsByPackage returns all symbols grouped by package
func (sg *SymbolGraph) GetSymbolsByPackage() map[string][]*SymbolNode {
	packageSymbols := make(map[string][]*SymbolNode)

	for _, node := range sg.Nodes {
		pkg := node.Symbol.Package
		if packageSymbols[pkg] == nil {
			packageSymbols[pkg] = make([]*SymbolNode, 0)
		}
		packageSymbols[pkg] = append(packageSymbols[pkg], node)
	}

	return packageSymbols
}

// GetSymbolsByType returns symbols grouped by their type
func (sg *SymbolGraph) GetSymbolsByType() map[types.SymbolKind][]*SymbolNode {
	typeSymbols := make(map[types.SymbolKind][]*SymbolNode)

	for _, node := range sg.Nodes {
		kind := node.Symbol.Kind
		if typeSymbols[kind] == nil {
			typeSymbols[kind] = make([]*SymbolNode, 0)
		}
		typeSymbols[kind] = append(typeSymbols[kind], node)
	}

	return typeSymbols
}

// FindCrossPackageDependencies returns dependencies that cross package boundaries
func (sg *SymbolGraph) FindCrossPackageDependencies() []*SymbolEdge {
	var crossPackageDeps []*SymbolEdge

	for _, edges := range sg.Edges {
		for _, edge := range edges {
			if edge.From.Symbol.Package != edge.To.Symbol.Package {
				crossPackageDeps = append(crossPackageDeps, edge)
			}
		}
	}

	return crossPackageDeps
}

// GetDependencyMetrics returns metrics about symbol dependencies
func (sg *SymbolGraph) GetDependencyMetrics() SymbolMetrics {
	metrics := SymbolMetrics{
		TotalSymbols:         len(sg.Nodes),
		TotalDependencies:    0,
		CrossPackageDeps:     0,
		AverageDependencies:  0,
		MaxDependencies:      0,
		SymbolsWithoutDeps:   0,
	}

	var totalDeps int
	maxDeps := 0

	for _, node := range sg.Nodes {
		deps := len(node.Dependencies)
		totalDeps += deps

		if deps == 0 {
			metrics.SymbolsWithoutDeps++
		}
		if deps > maxDeps {
			maxDeps = deps
		}
	}

	crossPackageDeps := sg.FindCrossPackageDependencies()
	metrics.CrossPackageDeps = len(crossPackageDeps)
	metrics.MaxDependencies = maxDeps
	
	if len(sg.Nodes) > 0 {
		metrics.AverageDependencies = float64(totalDeps) / float64(len(sg.Nodes))
	}

	for _, edges := range sg.Edges {
		metrics.TotalDependencies += len(edges)
	}

	return metrics
}

// RemoveSymbol removes a symbol and all its edges from the graph
func (sg *SymbolGraph) RemoveSymbol(symbol *types.Symbol) {
	id := sg.getSymbolID(symbol)
	node, exists := sg.Nodes[id]
	if !exists {
		return
	}

	// Remove edges from this symbol
	delete(sg.Edges, id)

	// Remove edges to this symbol
	for symbolID, edges := range sg.Edges {
		for i := len(edges) - 1; i >= 0; i-- {
			if edges[i].To.ID == id {
				sg.Edges[symbolID] = append(edges[:i], edges[i+1:]...)
			}
		}
	}

	// Update dependencies and dependents
	for _, dep := range node.Dependencies {
		removeSymbolDependentFromNode(dep, node)
	}
	for _, dependent := range node.Dependents {
		removeSymbolDependencyFromNode(dependent, node)
	}

	delete(sg.Nodes, id)
}

// Helper types and functions

type SymbolMetrics struct {
	TotalSymbols        int
	TotalDependencies   int
	CrossPackageDeps    int
	AverageDependencies float64
	MaxDependencies     int
	SymbolsWithoutDeps  int
}

func (sg *SymbolGraph) getSymbolID(symbol *types.Symbol) string {
	return symbol.Package + "." + symbol.Name
}

func removeDuplicateSymbolNodes(nodes []*SymbolNode) []*SymbolNode {
	seen := make(map[string]bool)
	var result []*SymbolNode

	for _, node := range nodes {
		if !seen[node.ID] {
			seen[node.ID] = true
			result = append(result, node)
		}
	}

	return result
}

func removeSymbolDependentFromNode(node *SymbolNode, dependent *SymbolNode) {
	for i, dep := range node.Dependents {
		if dep.ID == dependent.ID {
			node.Dependents = append(node.Dependents[:i], node.Dependents[i+1:]...)
			break
		}
	}
}

func removeSymbolDependencyFromNode(node *SymbolNode, dependency *SymbolNode) {
	for i, dep := range node.Dependencies {
		if dep.ID == dependency.ID {
			node.Dependencies = append(node.Dependencies[:i], node.Dependencies[i+1:]...)
			break
		}
	}
}