// This fixture is modelled on the real shape that motivated boolsimplify:
// ImportGraph.DetectImportCycles in pkg/graph/import_graph.go. Flattening
// inverts the guard's condition with srctext.PushNeg, which now distributes
// De Morgan on the spot rather than emitting a blunt `!(...)` wrapper - so
// most of this condition is already simplified the moment stage 1 writes it.
// The one thing PushNeg cannot see through is a double negation buried
// inside an opaque call argument (hasCycle's argument here): it treats the
// whole call as a single unit and wraps it in one `!`, leaving the `!!`
// inside untouched. That is exactly the shape the negation cleanup pass
// exists for, so composing the two fixers still does real work.
package main

import "fmt"

type node struct {
	isExternal bool
}

func detectCycles(nodes map[string]*node, visited map[string]bool) []string {
	var cycles []string
	for pkg, n := range nodes {
		if !n.isExternal && hasCycle(!!visited[pkg]) {
			cycle := dfs(pkg)
			if cycle != "" {
				cycles = append(cycles, cycle)
			}
		}
	}
	return cycles
}

func hasCycle(seen bool) bool { return seen }

func dfs(pkg string) string { return pkg }

func main() {
	fmt.Println(detectCycles(nil, nil))
}
