// This fixture is modelled on the real shape that motivated boolsimplify:
// ImportGraph.DetectImportCycles in pkg/graph/import_graph.go. Flattening it
// produces a correct but blunt `!(!a && !b)` wrapper, which the negation
// cleanup pass then collapses. Neither operation gets there alone.
package main

import "fmt"

type node struct {
	isExternal bool
}

func detectCycles(nodes map[string]*node, visited map[string]bool) []string {
	var cycles []string
	for pkg, n := range nodes {
		if !n.isExternal && !visited[pkg] {
			cycle := dfs(pkg)
			if cycle != "" {
				cycles = append(cycles, cycle)
			}
		}
	}
	return cycles
}

func dfs(pkg string) string { return pkg }

func main() {
	fmt.Println(detectCycles(nil, nil))
}
