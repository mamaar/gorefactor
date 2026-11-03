package analysis

import (
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestView_WarmExternalsChecksWhatTheWorkspaceImports(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"p/p.go":      "package p\n\nimport \"example.org/ext\"\n\nvar V = ext.F()\n",
		"p/p_test.go": "package p\n\nimport \"example.org/onlytests\"\n\nvar T = onlytests.G()\n",
	})
	dir := t.TempDir()
	ext := listedExternal(t, dir, "example.org/ext", "ext", "package ext\n\nfunc F() int { return 1 }\n")
	onlyTests := listedExternal(t, dir, "example.org/onlytests", "onlytests", "package onlytests\n\nfunc G() int { return 1 }\n")
	unused := listedExternal(t, dir, "example.org/unused", "unused", "package unused\n")
	v := set.Primary()
	v.Parser.loadPackages = (&recordingLoader{pkgs: []*packages.Package{ext, onlyTests, unused}}).load

	v.WarmExternals()

	externals := v.Parser.importer.ext
	for path, want := range map[string]bool{"example.org/ext": true, "example.org/onlytests": true, "example.org/unused": false} {
		n := externals.byPath[path]
		if n == nil || n.checked != want {
			t.Errorf("%s type-checked = %v, want %v", path, n != nil && n.checked, want)
		}
	}

	pkg := v.Workspace.Packages[filepath.Join(root, "p")]
	if pkg.TypesChecked {
		t.Error("warming the externals type-checked a workspace package")
	}

	checkedPackage(t, v, filepath.Join(root, "p"))
}

func TestView_DropTypeInfoKeepsWhatTheChecksRecorded(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"p/p.go":        "package p\n\nfunc F() {}\n",
		"p/ok_test.go":  "package p\n\nfunc useF() { F() }\n",
		"p/bad_test.go": "package p\n\nimport _ \"example.com/missing/dep\"\n",
		"q/q.go":        "package q\n\nimport \"example.com/w/p\"\n\nfunc Q() { p.F() }\n",
	})
	v := set.Primary()
	dirs := []string{filepath.Join(root, "p"), filepath.Join(root, "q")}

	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	}

	views := []*View{v}

	before := UncheckedFiles(views, dirs)
	if len(before) != 1 || before[0].Source.Name != "bad_test.go" {
		t.Fatalf("unchecked before the drop = %+v, want bad_test.go", before)
	}

	v.DropTypeInfo()

	for _, pkg := range v.Workspace.Packages {
		if pkg.TypesInfo != nil || pkg.TypesPkg != nil {
			t.Errorf("%s still holds type information", pkg.ImportPath)
		}
	}

	after := UncheckedFiles(views, dirs)
	if !reflect.DeepEqual(after, before) {
		t.Errorf("unchecked after the drop = %+v, want %+v", after, before)
	}

	v.ReleaseTypeInfo()

	q := checkedPackage(t, v, dirs[1])
	if useDefinitionFile(t, v, q, "q.go", "F") != "p.go" {
		t.Error("q does not resolve p.F after the re-check")
	}

	again := UncheckedFiles(views, dirs)
	if len(again) != 1 || again[0].Reason != before[0].Reason {
		t.Errorf("unchecked after the re-check = %+v, want %+v", again, before)
	}
}
