package analysis

import (
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func depGraph(t *testing.T, files map[string]string) (*types.DependencyGraph, string) {
	t.Helper()
	set, root := loadSet(t, files)
	return set.Primary().Workspace.Dependencies, root
}

func TestDependencyGraph_PackageDepsAreTransitiveFilesystemPaths(t *testing.T) {
	g, root := depGraph(t, map[string]string{
		"a/a.go": "package a\n\nimport _ \"example.com/w/b\"\n",
		"b/b.go": "package b\n\nimport _ \"example.com/w/c\"\n",
		"c/c.go": "package c\n",
	})
	a, b, c := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")

	if want := []string{b, c}; !slices.Equal(g.PackageDeps[a], want) {
		t.Errorf("PackageDeps[a] = %v, want %v", g.PackageDeps[a], want)
	}
	if len(g.ImportCycles) != 0 || len(g.TestImportCycles) != 0 {
		t.Errorf("unexpected cycles: %v %v", g.ImportCycles, g.TestImportCycles)
	}
}

func TestDependencyGraph_ProductionCycleIsReportedOnce(t *testing.T) {
	g, root := depGraph(t, map[string]string{
		"a/a.go": "package a\n\nimport _ \"example.com/w/b\"\n",
		"b/b.go": "package b\n\nimport _ \"example.com/w/a\"\n",
	})
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")

	if want := [][]string{{a, b, a}}; !reflect.DeepEqual(g.ImportCycles, want) {
		t.Errorf("ImportCycles = %v, want %v", g.ImportCycles, want)
	}
	if len(g.TestImportCycles) != 0 {
		t.Errorf("TestImportCycles = %v, want none", g.TestImportCycles)
	}
}

func TestDependencyGraph_InPackageTestImportCycleIsSeparate(t *testing.T) {
	g, root := depGraph(t, map[string]string{
		"a/a.go":      "package a\n",
		"a/a_test.go": "package a\n\nimport _ \"example.com/w/b\"\n",
		"b/b.go":      "package b\n\nimport _ \"example.com/w/a\"\n",
	})
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")

	if len(g.ImportCycles) != 0 {
		t.Errorf("ImportCycles = %v, want none", g.ImportCycles)
	}
	if want := [][]string{{a, b, a}}; !reflect.DeepEqual(g.TestImportCycles, want) {
		t.Errorf("TestImportCycles = %v, want %v", g.TestImportCycles, want)
	}
}

func TestDependencyGraph_TestFilesImportingEachOtherIsLegal(t *testing.T) {
	g, _ := depGraph(t, map[string]string{
		"a/a.go":      "package a\n",
		"a/a_test.go": "package a\n\nimport _ \"example.com/w/b\"\n",
		"b/b.go":      "package b\n",
		"b/b_test.go": "package b\n\nimport _ \"example.com/w/a\"\n",
	})

	if len(g.ImportCycles) != 0 || len(g.TestImportCycles) != 0 {
		t.Errorf("unexpected cycles: %v %v", g.ImportCycles, g.TestImportCycles)
	}
}

func TestDependencyGraph_ThreeNodeCycleIsDeterministic(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go": "package a\n\nimport _ \"example.com/w/b\"\n",
		"b/b.go": "package b\n\nimport _ \"example.com/w/c\"\n",
		"c/c.go": "package c\n\nimport _ \"example.com/w/a\"\n",
		"d/d.go": "package d\n\nimport _ \"example.com/w/a\"\n",
		"e/e.go": "package e\n",
	})
	a, b, c := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")
	want := [][]string{{a, b, c, a}}

	ws := set.Primary().Workspace
	for range 10 {
		g := NewDependencyAnalyzer(ws, slog.New(slog.NewTextHandler(io.Discard, nil))).BuildDependencyGraph()
		if !reflect.DeepEqual(g.ImportCycles, want) {
			t.Fatalf("ImportCycles = %v, want %v", g.ImportCycles, want)
		}
	}
}

func TestDependencyGraph_PackageImportsKeepRawImportStrings(t *testing.T) {
	g, root := depGraph(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/w/b\"\n)\n\nvar _ = fmt.Sprint\nvar _ = b.B\n",
		"b/b.go": "package b\n\nvar B = 1\n",
	})

	want := []string{"example.com/w/b", "fmt"}
	if got := g.PackageImports[filepath.Join(root, "a")]; !slices.Equal(got, want) {
		t.Errorf("PackageImports[a] = %v, want %v", got, want)
	}
}
