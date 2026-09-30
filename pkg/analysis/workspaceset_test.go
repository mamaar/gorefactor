package analysis

import (
	"errors"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
	"golang.org/x/tools/go/packages"
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

func TestExternalLoadSettings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       BuildConfig
		wantEnv   []string
		wantFlags []string
	}{
		{"host port with cgo", NewBuildConfig("darwin", "arm64", true),
			[]string{"GOPROXY=off", "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1"}, nil},
		{"host port without cgo", NewBuildConfig("darwin", "arm64", false),
			[]string{"GOPROXY=off", "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0"}, nil},
		{"other port with cgo", NewBuildConfig("windows", "amd64", true),
			[]string{"GOPROXY=off", "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0"}, nil},
		{"same os other arch", NewBuildConfig("darwin", "amd64", true),
			[]string{"GOPROXY=off", "GOOS=darwin", "GOARCH=amd64", "CGO_ENABLED=0"}, nil},
		{"tags", NewBuildConfig("darwin", "arm64", false, "b", "a"),
			[]string{"GOPROXY=off", "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0"}, []string{"-tags=a,b"}},
	} {
		env, flags := externalLoadSettings(tc.cfg, "darwin", "arm64")
		if !slices.Equal(env, tc.wantEnv) || !slices.Equal(flags, tc.wantFlags) {
			t.Errorf("%s: env = %v, flags = %v; want %v, %v", tc.name, env, flags, tc.wantEnv, tc.wantFlags)
		}
	}
}

func externalWorkspace() map[string]string {
	return map[string]string{
		"p/p.go":     "package p\n\nimport \"example.org/ext\"\n\nfunc P() { ext.F() }\n",
		"p/p_tag.go": "//go:build fts5x\n\npackage p\n\nimport \"example.org/ext\"\n\nfunc Tagged() { ext.F() }\n",
	}
}

func fakeExternal() []*packages.Package {
	pkg := gotypes.NewPackage("example.org/ext", "ext")
	sig := gotypes.NewSignatureType(nil, nil, nil, nil, nil, false)
	pkg.Scope().Insert(gotypes.NewFunc(token.NoPos, pkg, "F", sig))
	pkg.MarkComplete()
	return []*packages.Package{{ID: pkg.Path(), PkgPath: pkg.Path(), Types: pkg}}
}

type recordingLoader struct {
	pkgs []*packages.Package
	err  error
	cfgs []*packages.Config
}

func (l *recordingLoader) load(cfg *packages.Config, _ ...string) ([]*packages.Package, error) {
	l.cfgs = append(l.cfgs, cfg)
	return l.pkgs, l.err
}

func TestView_ExternalLoadUsesTheViewsConfig(t *testing.T) {
	set, root := loadSet(t, externalWorkspace())
	tagged := viewWithTag(t, set, "fts5x")
	loader := &recordingLoader{pkgs: fakeExternal()}
	tagged.Parser.loadPackages = loader.load

	checkedPackage(t, tagged, filepath.Join(root, "p"))
	if len(loader.cfgs) != 1 {
		t.Fatalf("loads = %d, want 1", len(loader.cfgs))
	}
	cfg := loader.cfgs[0]
	for _, want := range []string{"GOPROXY=off", "GOOS=linux", "GOARCH=amd64"} {
		if !slices.Contains(cfg.Env, want) {
			t.Errorf("Env lacks %s", want)
		}
	}
	if !slices.Equal(cfg.BuildFlags, []string{"-tags=fts5x"}) {
		t.Errorf("BuildFlags = %v", cfg.BuildFlags)
	}
	if approx, err := tagged.Approximate(); approx || err != nil {
		t.Errorf("Approximate = %v, %v", approx, err)
	}
}

func TestView_FailedExternalLoadFallsBackToPrimary(t *testing.T) {
	set, root := loadSet(t, externalWorkspace())
	tagged := viewWithTag(t, set, "fts5x")
	loadErr := errors.New("no cross toolchain")
	set.Primary().Parser.loadPackages = (&recordingLoader{pkgs: fakeExternal()}).load
	tagged.Parser.loadPackages = (&recordingLoader{err: loadErr}).load

	checkedPackage(t, tagged, filepath.Join(root, "p"))
	if approx, err := tagged.Approximate(); !approx || !errors.Is(err, loadErr) {
		t.Errorf("tagged Approximate = %v, %v", approx, err)
	}
	if approx, err := set.Primary().Approximate(); approx || err != nil {
		t.Errorf("primary Approximate = %v, %v", approx, err)
	}
}

func TestView_StdlibResolvesForTheViewsPort(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"p/p.go":         "package p\n",
		"p/l_linux.go":   "package p\n\nimport \"syscall\"\n\nvar E syscall.EpollEvent\n",
		"p/h_windows.go": "package p\n\nimport \"syscall\"\n\nvar H syscall.Handle\n",
	})
	dir := filepath.Join(root, "p")
	for _, v := range []*View{set.Primary(), viewWithGOOS(t, set, "windows")} {
		checkedPackage(t, v, dir)
		if approx, err := v.Approximate(); approx {
			t.Errorf("view %s is approximate: %v", v.Config.Key(), err)
		}
	}
}

func TestWorkspaceSet_ConcurrentViewRequestsBuildEachViewOnce(t *testing.T) {
	set, root := loadSet(t, taggedRegistryWorkspace())
	dir := filepath.Join(root, "p")
	results := make([][]*View, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			views, err := set.Views()
			if err != nil {
				t.Error(err)
				return
			}
			for _, v := range views {
				v.Parser.EnsureTypeChecked(v.Workspace, v.Workspace.Packages[dir])
				if _, err := v.ReferenceIndex(); err != nil {
					t.Error(err)
				}
			}
			results[i] = views
		})
	}
	wg.Wait()
	for _, views := range results[1:] {
		if !slices.Equal(views, results[0]) {
			t.Fatal("concurrent Views calls returned different instances")
		}
	}
}
