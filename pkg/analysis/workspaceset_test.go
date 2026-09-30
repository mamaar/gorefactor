package analysis

import (
	"go/ast"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

var setPrimary = NewBuildConfig("linux", "amd64", true)

func loadSet(t *testing.T, files map[string]string) (*WorkspaceSet, string) {
	t.Helper()
	root := writeWorkspace(t, files)
	set, err := LoadWorkspaceSet(root, setPrimary, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return set, root
}

func allViews(t *testing.T, set *WorkspaceSet) []*View {
	t.Helper()
	views, err := set.Views()
	if err != nil {
		t.Fatal(err)
	}
	return views
}

func viewWithTag(t *testing.T, set *WorkspaceSet, tag string) *View {
	t.Helper()
	for _, v := range allViews(t, set) {
		if slices.Contains(v.Config.Tags, tag) {
			return v
		}
	}
	t.Fatalf("no view with tag %s", tag)
	return nil
}

func viewWithGOOS(t *testing.T, set *WorkspaceSet, goos string) *View {
	t.Helper()
	for _, v := range allViews(t, set) {
		if v.Config.GOOS == goos {
			return v
		}
	}
	t.Fatalf("no view with GOOS %s", goos)
	return nil
}

func checkedPackage(t *testing.T, v *View, dir string) *types.Package {
	t.Helper()
	pkg := v.Workspace.Packages[dir]
	if pkg == nil {
		t.Fatalf("view %s: no package at %s", v.Config.Key(), dir)
	}
	v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	if pkg.TypesErr != nil {
		t.Fatalf("view %s: %s TypesErr = %v", v.Config.Key(), dir, pkg.TypesErr)
	}
	if pkg.TypesInfo == nil {
		t.Fatalf("view %s: %s has no TypesInfo", v.Config.Key(), dir)
	}
	if pkg.TestTypesExcluded {
		t.Fatalf("view %s: %s test files excluded: %v", v.Config.Key(), dir, pkg.TestTypesExcludedErr)
	}
	return pkg
}

func taggedRegistryWorkspace() map[string]string {
	return map[string]string{
		"p/p.go":            "package p\n\nfunc P() {}\n",
		"p/x_tag_test.go":   "//go:build fts5x\n\npackage p\n\nvar registry = []string{\"on\"}\n",
		"p/x_notag_test.go": "//go:build !fts5x\n\npackage p\n\nvar registry = []string{}\n",
	}
}

func TestWorkspaceSet_ViewsShareSourceButNotPackages(t *testing.T) {
	set, root := loadSet(t, taggedRegistryWorkspace())
	dir := filepath.Join(root, "p")
	primary, tagged := set.Primary(), viewWithTag(t, set, "fts5x")

	pp, tp := checkedPackage(t, primary, dir), checkedPackage(t, tagged, dir)
	if got := fileNames(pp.TestFiles); !slices.Equal(got, []string{"x_notag_test.go"}) {
		t.Errorf("primary TestFiles = %v", got)
	}
	if got := fileNames(tp.TestFiles); !slices.Equal(got, []string{"x_tag_test.go"}) {
		t.Errorf("tagged TestFiles = %v", got)
	}
	if pp == tp {
		t.Error("views share a *types.Package")
	}
	if pp.Files["p.go"] == tp.Files["p.go"] {
		t.Error("views share a *types.File")
	}
	if pp.Files["p.go"].AST != tp.Files["p.go"].AST {
		t.Error("views hold different *ast.File for p.go")
	}
	if pp.Files["p.go"].Package != pp || tp.Files["p.go"].Package != tp {
		t.Error("File.Package does not point at the view's own package")
	}
	if primary.Workspace.FileSet != tagged.Workspace.FileSet {
		t.Error("views do not share the FileSet")
	}
	if pp.TypesPkg == tp.TypesPkg {
		t.Error("views share a go/types package")
	}
}

func TestWorkspaceSet_SecondaryViewsAreBuiltOnRequest(t *testing.T) {
	set, _ := loadSet(t, taggedRegistryWorkspace())
	if built := set.BuiltViews(); len(built) != 1 || built[0] != set.Primary() {
		t.Fatalf("after load BuiltViews = %d views, want only the primary", len(built))
	}

	views := allViews(t, set)
	var keys, want []string
	for _, v := range views {
		keys = append(keys, v.Config.Key())
	}
	for _, c := range set.Covering().Configs {
		want = append(want, c.Key())
	}
	if !slices.Equal(keys, want) || len(keys) != 2 {
		t.Fatalf("Views keys = %v, covering = %v", keys, want)
	}
	if views[0] != set.Primary() {
		t.Error("first view is not the primary")
	}
	if again := allViews(t, set); !slices.Equal(again, views) {
		t.Error("second Views call returned different instances")
	}
}

func TestWorkspaceSet_TypeInfoIsPerView(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"p/open_linux.go":   "package p\n\nfunc Open() {}\n",
		"p/open_windows.go": "package p\n\nfunc Open() {}\n",
		"p/use.go":          "package p\n\nfunc Use() { Open() }\n",
	})
	dir := filepath.Join(root, "p")
	for _, tc := range []struct {
		view *View
		want string
	}{
		{set.Primary(), "open_linux.go"},
		{viewWithGOOS(t, set, "windows"), "open_windows.go"},
	} {
		pkg := checkedPackage(t, tc.view, dir)
		if got := useDefinitionFile(t, tc.view, pkg, "use.go", "Open"); got != tc.want {
			t.Errorf("view %s: Open defined in %s, want %s", tc.view.Config.Key(), got, tc.want)
		}
	}
}

func useDefinitionFile(t *testing.T, v *View, pkg *types.Package, file, name string) string {
	t.Helper()
	var def string
	ast.Inspect(pkg.Files[file].AST, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			if obj := pkg.TypesInfo.Uses[id]; obj != nil {
				def = filepath.Base(v.Workspace.FileSet.Position(obj.Pos()).Filename)
			}
		}
		return true
	})
	if def == "" {
		t.Fatalf("view %s: no use of %s in %s", v.Config.Key(), name, file)
	}
	return def
}

func TestWorkspaceSet_ImportResolvesToTheViewsOwnDependency(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go":       "package a\n\nfunc Base() {}\n",
		"a/a_extra.go": "//go:build fts5x\n\npackage a\n\nfunc Extra() {}\n",
		"b/b.go":       "package b\n\nimport \"example.com/w/a\"\n\nfunc B() { a.Base() }\n",
		"b/b_extra.go": "//go:build fts5x\n\npackage b\n\nimport \"example.com/w/a\"\n\nfunc BX() { a.Extra() }\n",
	})
	aDir, bDir := filepath.Join(root, "a"), filepath.Join(root, "b")

	tagged := viewWithTag(t, set, "fts5x")
	if got := fileNames(checkedPackage(t, tagged, bDir).Files); !slices.Equal(got, []string{"b.go", "b_extra.go"}) {
		t.Errorf("tagged b Files = %v", got)
	}
	if checkedPackage(t, tagged, aDir).TypesPkg.Scope().Lookup("Extra") == nil {
		t.Error("tagged a lacks Extra")
	}

	primary := set.Primary()
	if got := fileNames(checkedPackage(t, primary, bDir).Files); !slices.Equal(got, []string{"b.go"}) {
		t.Errorf("primary b Files = %v", got)
	}
	if got := fileNames(checkedPackage(t, primary, aDir).Files); !slices.Equal(got, []string{"a.go"}) {
		t.Errorf("primary a Files = %v", got)
	}
}
