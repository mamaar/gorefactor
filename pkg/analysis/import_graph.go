package analysis

import (
	"go/ast"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Imports holds, per package filesystem path, the workspace-local packages it
// imports, sorted and without self-edges.
type Imports struct {
	// Production are the edges of a package's non-test files.
	Production map[string][]string
	// Test are the edges of in-package test files; external test packages
	// are separate packages that nothing imports, so they add none.
	Test map[string][]string
}

// ImportGraph builds the workspace's import graph, keyed by filesystem path.
func ImportGraph(ws *types.Workspace) Imports {
	g := Imports{
		Production: make(map[string][]string, len(ws.Packages)),
		Test:       make(map[string][]string, len(ws.Packages)),
	}
	for path, pkg := range ws.Packages {
		g.Production[path] = localImports(ws, path, pkg.Files)
		g.Test[path] = localImports(ws, path, pkg.TestFiles)
	}
	return g
}

// Union returns the production and in-package test edges together; a cycle
// through either breaks the build.
func (g Imports) Union() map[string][]string {
	union := make(map[string][]string, len(g.Production))
	for path, prod := range g.Production {
		seen := make(map[string]bool)
		for _, tos := range [][]string{prod, g.Test[path]} {
			for _, to := range tos {
				if !seen[to] {
					seen[to] = true
					union[path] = append(union[path], to)
				}
			}
		}
		sort.Strings(union[path])
	}
	return union
}

func localImports(ws *types.Workspace, path string, files map[string]*types.File) []string {
	var out []string
	seen := make(map[string]bool)
	for _, file := range files {
		if file.AST == nil || isExternalTestFile(file.AST) {
			continue
		}
		for _, imp := range file.AST.Imports {
			if to, ok := ws.ImportToPath[strings.Trim(imp.Path.Value, `"`)]; ok && to != path && !seen[to] {
				seen[to] = true
				out = append(out, to)
			}
		}
	}
	sort.Strings(out)
	return out
}

func isExternalTestFile(f *ast.File) bool {
	return strings.HasSuffix(f.Name.Name, "_test")
}
