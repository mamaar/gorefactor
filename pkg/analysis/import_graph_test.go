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

	want := []string{b}
	if !slices.Equal(g.Production[a], want) {
		t.Errorf("Production[a] = %v, want %v", g.Production[a], want)
	}
	want = []string{c}
	if !slices.Equal(g.Test[a], want) {
		t.Errorf("Test[a] = %v, want %v", g.Test[a], want)
	}
	want = []string{b, c}
	if !slices.Equal(g.Union()[a], want) {
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

func TestInvalidateImporters_DropsProductionOnlyPackagesOfTheInvalidated(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go": "package a\n\nvar A = 1\n",
		"b/b.go": "package b\n\nimport \"example.com/w/a\"\n\nvar B = a.A\n",
		"c/c.go": "package c\n\nvar C = 1\n",
	})
	v := set.Primary()
	pkgs := make(map[string]*types.Package)
	for _, name := range []string{"a", "b", "c"} {
		pkgs[name] = v.Workspace.Packages[filepath.Join(root, name)]
		v.Parser.EnsureTypeChecked(v.Workspace, pkgs[name])
	}
	rescued := map[*types.Package]*gotypes.Package{pkgs["a"]: nil, pkgs["b"]: nil, pkgs["c"]: nil}
	v.Parser.importer.productionOnly = rescued

	v.Parser.InvalidateImporters(v.Workspace, []string{filepath.Join(root, "a")})

	for _, name := range []string{"a", "b"} {
		_, kept := rescued[pkgs[name]]
		if kept {
			t.Errorf("production-only package of %s survived the invalidation", name)
		}
		if pkgs[name].TypesChecked {
			t.Errorf("package %s kept its type information", name)
		}
	}
	_, kept := rescued[pkgs["c"]]
	if !kept {
		t.Error("production-only package of c, which does not import a, was dropped while c stays checked against it")
	}
	if !pkgs["c"].TypesChecked {
		t.Error("package c does not import a and must keep its type information")
	}
}
