package analysis

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// listedExternal writes an external package's source outside the workspace and
// returns it the way a load without type information lists it.
func listedExternal(t *testing.T, dir, path, name, src string, imports ...*packages.Package) *packages.Package {
	t.Helper()
	file := filepath.Join(dir, name+".go")
	err := os.WriteFile(file, []byte(src), 0o644)
	if err != nil {
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
	got := pkg.TypesPkg.Scope().Lookup("V").Type().String()
	if got != "int" {
		t.Errorf("V has type %s, want int", got)
	}
	mode := loader.cfgs[0].Mode
	if mode&packages.NeedTypes != 0 || mode&packages.NeedCompiledGoFiles == 0 {
		t.Errorf("load mode = %v: want the compiled files listed and no types", mode)
	}
	externals := set.Primary().Parser.importer.ext
	n := externals.byPath["example.org/unused"]
	if n == nil || n.checked {
		t.Errorf("a listed package nothing imports was type-checked: %+v", n)
	}
	n = externals.byPath["example.org/dep"]
	if n == nil || !n.checked {
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
		got := pkg.TypesPkg.Scope().Lookup("V").Type().String()
		if got != want {
			t.Errorf("GOARCH=%s: V has type %s, want %s", goarch, got, want)
		}
	}
}

// excludedCause returns the recorded reason the check of dir left out file.
func excludedCause(t *testing.T, v *View, dir, file string) string {
	t.Helper()
	pkg := v.Workspace.Packages[dir]
	v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	cause := pkg.ExcludedTestFiles[file]
	if cause == nil {
		t.Fatalf("%s was not excluded; excluded: %v, TypesErr: %v", file, pkg.ExcludedTestFiles, pkg.TypesErr)
	}
	return cause.Error()
}

func TestExternal_PackageListedWithoutNameReportsItsListingError(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"p/p.go":      "package p\n\nfunc P() {}\n",
		"p/p_test.go": "package p\n\nimport \"example.org/absent\"\n\nvar _ = absent.New\n",
	})
	absent := &packages.Package{ID: "example.org/absent", PkgPath: "example.org/absent",
		Errors: []packages.Error{{Msg: "module lookup disabled by GOPROXY=off", Kind: packages.ListError}}}
	set.Primary().Parser.loadPackages = (&recordingLoader{pkgs: []*packages.Package{absent}}).load

	cause := excludedCause(t, set.Primary(), filepath.Join(root, "p"), "p_test.go")

	if !strings.Contains(cause, "could not import example.org/absent") || !strings.Contains(cause, "module lookup disabled by GOPROXY=off") {
		t.Errorf("cause must carry the listing error, got: %s", cause)
	}
	if strings.Contains(cause, "invalid package name") {
		t.Errorf("cause must not blame the package name, got: %s", cause)
	}
}

func TestExternal_ModuleMissingFromTheModuleCacheIsNamedAsSuch(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":      "package p\n\nfunc P() {}\n",
		"p/p_test.go": "package p\n\nimport \"example.org/absent\"\n\nvar _ = absent.New\n",
	})
	gomod := "module example.com/w\n\ngo 1.22\n\nrequire example.org/absent v1.0.0\n"
	err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	set, err := LoadWorkspaceSet(root, setPrimary, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	cause := excludedCause(t, set.Primary(), filepath.Join(root, "p"), "p_test.go")

	if !strings.Contains(cause, "could not import example.org/absent") || strings.Contains(cause, "invalid package name") {
		t.Errorf("cause must say why example.org/absent could not be loaded, got: %s", cause)
	}
}

func TestExternalSet_LookupOfAnUnlistedPathIsNotListed(t *testing.T) {
	for name, set := range map[string]*externalSet{
		"nil set":   nil,
		"empty set": {byPath: map[string]*externalPackage{}},
	} {
		pkg, err := set.lookup("example.org/missing")
		if pkg != nil || !errors.Is(err, errNotListed) {
			t.Errorf("%s: lookup = %v, %v; want nil, errNotListed", name, pkg, err)
		}
	}
}
