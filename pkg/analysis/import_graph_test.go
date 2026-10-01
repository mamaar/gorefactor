package analysis

import (
	gotypes "go/types"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
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

func TestImporters_FollowsProductionTestAndExternalTestImportsTransitively(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go":      "package a\n\nvar A = 1\n",
		"b/b.go":      "package b\n\nimport \"example.com/w/a\"\n\nvar B = a.A\n",
		"c/c.go":      "package c\n\nvar C = 1\n",
		"c/c_test.go": "package c\n\nimport \"example.com/w/a\"\n\nvar _ = a.A\n",
		"d/d.go":      "package d\n\nvar D = 1\n",
		"d/d_test.go": "package d_test\n\nimport \"example.com/w/a\"\n\nvar _ = a.A\n",
		"e/e.go":      "package e\n\nimport \"example.com/w/b\"\n\nvar E = b.B\n",
		"f/f.go":      "package f\n\nvar F = 1\n",
	})
	ws := set.Primary().Workspace

	got := Importers(ws, []string{filepath.Join(root, "a")})

	var want []string
	for _, d := range []string{"b", "c", "d", "e"} {
		want = append(want, filepath.Join(root, d))
	}
	if !slices.Equal(got, want) {
		t.Errorf("Importers(a) = %v, want %v", got, want)
	}
}

func TestInvalidateImporters_DropsProductionOnlyPackages(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go": "package a\n\nvar A = 1\n",
		"b/b.go": "package b\n\nimport \"example.com/w/a\"\n\nvar B = a.A\n",
	})
	v := set.Primary()
	pkg := v.Workspace.Packages[filepath.Join(root, "b")]
	v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	v.Parser.importer.productionOnly = map[*types.Package]*gotypes.Package{pkg: nil}

	v.Parser.InvalidateImporters(v.Workspace, []string{filepath.Join(root, "a")})

	if v.Parser.importer.productionOnly != nil {
		t.Error("production-only packages survived the invalidation")
	}
	if pkg.TypesChecked {
		t.Error("importer b kept its type information")
	}
}
