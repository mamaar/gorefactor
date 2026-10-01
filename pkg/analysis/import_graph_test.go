package analysis

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestImportGraph_SplitsProductionFromInPackageTestEdges(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go":      "package a\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/w/b\"\n)\n\nvar _ = b.B\nvar _ = fmt.Sprint\n",
		"a/a_test.go": "package a\n\nimport \"example.com/w/c\"\n\nvar _ = c.C\n",
		"a/x_test.go": "package a_test\n\nimport \"example.com/w/d\"\n\nvar _ = d.D\n",
		"b/b.go":      "package b\n\nvar B = 1\n",
		"c/c.go":      "package c\n\nvar C = 1\n",
		"d/d.go":      "package d\n\nvar D = 1\n",
	})
	g := ImportGraph(set.Primary().Workspace)
	a, b, c := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")

	if want := []string{b}; !slices.Equal(g.Production[a], want) {
		t.Errorf("Production[a] = %v, want %v", g.Production[a], want)
	}
	if want := []string{c}; !slices.Equal(g.Test[a], want) {
		t.Errorf("Test[a] = %v, want %v", g.Test[a], want)
	}
	if want := []string{b, c}; !slices.Equal(g.Union()[a], want) {
		t.Errorf("Union()[a] = %v, want %v", g.Union()[a], want)
	}
}
