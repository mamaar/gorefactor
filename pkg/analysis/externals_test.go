package analysis

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

// listedExternal writes an external package's source outside the workspace and
// returns it the way a load without type information lists it.
func listedExternal(t *testing.T, dir, path, name, src string, imports ...*packages.Package) *packages.Package {
	t.Helper()
	file := filepath.Join(dir, name+".go")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := &packages.Package{ID: path, PkgPath: path, Name: name, CompiledGoFiles: []string{file}, Imports: map[string]*packages.Package{}}
	for _, imp := range imports {
		pkg.Imports[imp.PkgPath] = imp
	}
	return pkg
}

func TestExternal_TypeCheckedFromListedFilesWithoutBodies(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"p/p.go": "package p\n\nimport \"example.org/ext\"\n\nvar V = ext.F().N\n",
	})
	dir := t.TempDir()
	dep := listedExternal(t, dir, "example.org/dep", "dep", "package dep\n\ntype T struct{ N int }\n")
	ext := listedExternal(t, dir, "example.org/ext", "ext",
		"package ext\n\nimport \"example.org/dep\"\n\nfunc F() dep.T { return undefinedInBody }\n", dep)
	unused := &packages.Package{ID: "example.org/unused", PkgPath: "example.org/unused", Name: "unused",
		CompiledGoFiles: []string{filepath.Join(dir, "missing.go")}}
	loader := &recordingLoader{pkgs: []*packages.Package{ext, unused}}
	set.Primary().Parser.loadPackages = loader.load

	pkg := checkedPackage(t, set.Primary(), filepath.Join(root, "p"))
	if got := pkg.TypesPkg.Scope().Lookup("V").Type().String(); got != "int" {
		t.Errorf("V has type %s, want int", got)
	}
	if mode := loader.cfgs[0].Mode; mode&packages.NeedTypes != 0 || mode&packages.NeedCompiledGoFiles == 0 {
		t.Errorf("load mode = %v: want the compiled files listed and no types", mode)
	}
	externals := set.Primary().Parser.importer.ext
	if n := externals.byPath["example.org/unused"]; n == nil || n.checked {
		t.Errorf("a listed package nothing imports was type-checked: %+v", n)
	}
	if n := externals.byPath["example.org/dep"]; n == nil || !n.checked {
		t.Errorf("the dependency of an imported package was not type-checked: %+v", n)
	}
}

func TestExternal_SizesFollowTheViewsArchitecture(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go": "package p\n\nimport \"example.org/ext\"\n\nvar V = ext.Word\n",
	})
	dir := t.TempDir()
	ext := listedExternal(t, dir, "example.org/ext", "ext",
		"package ext\n\nimport \"unsafe\"\n\nvar Word [unsafe.Sizeof(uintptr(0))]byte\n")
	for goarch, want := range map[string]string{"386": "[4]byte", "amd64": "[8]byte"} {
		parser, ws := loadWithConfig(t, root, NewBuildConfig("linux", goarch, false))
		parser.loadPackages = (&recordingLoader{pkgs: []*packages.Package{ext}}).load
		pkg := ws.Packages[filepath.Join(root, "p")]
		parser.EnsureTypeChecked(ws, pkg)
		if got := pkg.TypesPkg.Scope().Lookup("V").Type().String(); got != want {
			t.Errorf("GOARCH=%s: V has type %s, want %s", goarch, got, want)
		}
	}
}
