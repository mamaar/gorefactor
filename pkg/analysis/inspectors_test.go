package analysis

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/ast/inspector"
)

func indexedInspector(t *testing.T, v *View, idx *ReferenceIndex, dir string) *inspector.Inspector {
	t.Helper()
	pix := idx.workspaceIdx.packages[v.Workspace.Packages[dir].TypesPkg]
	if pix == nil {
		t.Fatalf("view %s: %s is not indexed", v.Config.Key(), dir)
	}
	return pix.inspect
}

func TestBuildReferenceIndex_ViewsShareTheInspectorOfAnIdenticalFileList(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"same/same.go":      "package same\n\nfunc S() {}\n",
		"same/same_test.go": "package same\n\nfunc helper() { S() }\n",
		"p/p.go":            "package p\n\nfunc P() {}\n",
		"p/p_tag.go":        "//go:build fts5x\n\npackage p\n\nfunc Tagged() { P() }\n",
	})
	same, p := filepath.Join(root, "same"), filepath.Join(root, "p")
	primary, tagged := set.Primary(), viewWithTag(t, set, "fts5x")
	for _, v := range []*View{primary, tagged} {
		checkedPackage(t, v, same)
		checkedPackage(t, v, p)
	}
	pIdx, tIdx := primary.Resolver.BuildReferenceIndex(), tagged.Resolver.BuildReferenceIndex()

	if indexedInspector(t, primary, pIdx, same) != indexedInspector(t, tagged, tIdx, same) {
		t.Error("the views built separate inspectors for a package with the same files")
	}
	if indexedInspector(t, primary, pIdx, p) == indexedInspector(t, tagged, tIdx, p) {
		t.Error("the views share an inspector for a package whose files differ")
	}

	obj := tagged.Workspace.Packages[p].TypesPkg.Scope().Lookup("P")
	sites := tIdx.CallSitesOf(obj)
	if len(sites) != 1 || filepath.Base(sites[0].File.Path) != "p_tag.go" {
		t.Errorf("tagged call sites of P = %+v, want the one in p_tag.go", sites)
	}
	sites = pIdx.CallSitesOf(primary.Workspace.Packages[p].TypesPkg.Scope().Lookup("P"))
	if len(sites) != 0 {
		t.Errorf("primary call sites of P = %+v, want none", sites)
	}
	helperOf := tagged.Workspace.Packages[same].TypesPkg.Scope().Lookup("S")
	sites = tIdx.CallSitesOf(helperOf)
	if len(sites) != 1 || sites[0].Pkg != tagged.Workspace.Packages[same] {
		t.Errorf("tagged call sites of S = %+v, want one in the tagged view's own package", sites)
	}
}

func TestBuildLazyReferenceIndex_IndexesNamesOnFirstUse(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go": "package a\n\nfunc New() int { return 1 }\n\nfunc Use() int { return New() }\n",
	})
	v := set.Primary()
	checkedPackage(t, v, filepath.Join(root, "a"))

	idx := v.Resolver.BuildLazyReferenceIndex()
	if idx.nameIndex != nil {
		t.Fatal("names were indexed before anything asked for them")
	}
	if !idx.HasTypeInfo() {
		t.Fatal("the object index is missing")
	}

	// Type information dropped after the build must not change the entries.
	v.Parser.InvalidateTypeCheck(v.Workspace.Packages[filepath.Join(root, "a")])
	entries, ok := idx.NameEntries("New")
	if !ok || len(entries) != 2 {
		t.Fatalf("NameEntries(New) = %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.TypesObject == nil {
			t.Errorf("entry at %v has no go/types object", v.Workspace.FileSet.Position(e.Pos))
		}
	}
}
