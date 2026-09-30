package watch

import (
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

const taggedHeader = "//go:build fts5x\n\n"

func setupViews(t *testing.T, files map[string]string) (*WorkspaceUpdater, *analysis.WorkspaceSet, string) {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.com/test\n\ngo 1.25\n"
	for name, content := range files {
		writeFile(t, filepath.Join(root, name), content)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	set, err := analysis.LoadWorkspaceSet(root, analysis.NewBuildConfig("linux", "amd64", true), logger)
	if err != nil {
		t.Fatal(err)
	}
	return NewUpdater(set, logger), set, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func builtViews(t *testing.T, set *analysis.WorkspaceSet, want int) []*analysis.View {
	t.Helper()
	views, err := set.Views()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != want {
		t.Fatalf("views = %d, want %d", len(views), want)
	}
	return views
}

func typedPackage(t *testing.T, v *analysis.View, dir string) *types.Package {
	t.Helper()
	pkg := v.Workspace.Packages[dir]
	if pkg == nil {
		t.Fatalf("view %s: no package at %s", v.Config.Key(), dir)
	}
	v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	if pkg.TypesErr != nil {
		t.Fatalf("view %s: TypesErr = %v", v.Config.Key(), pkg.TypesErr)
	}
	return pkg
}

func fileNames(pkg *types.Package) []string {
	return slices.Sorted(maps.Keys(pkg.Files))
}

func taggedWorkspace() map[string]string {
	return map[string]string{
		"p/a.go": "package p\n\nfunc A() {}\n",
		"p/b.go": "package p\n\nfunc B() {}\n",
		"p/t.go": taggedHeader + "package p\n\nfunc T() {}\n",
	}
}

func TestUpdater_ModifyReachesEveryBuiltView(t *testing.T) {
	u, set, root := setupViews(t, taggedWorkspace())
	dir := filepath.Join(root, "p")
	views := builtViews(t, set, 2)
	for _, v := range views {
		typedPackage(t, v, dir)
	}

	path := filepath.Join(dir, "a.go")
	updated := "package p\n\nfunc A() {}\n\nfunc Added() {}\n"
	writeFile(t, path, updated)
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	for _, v := range views {
		pkg := v.Workspace.Packages[dir]
		if pkg.TypesChecked {
			t.Errorf("view %s kept stale type information", v.Config.Key())
		}
		if got := string(pkg.Files["a.go"].OriginalContent); got != updated {
			t.Errorf("view %s content = %q", v.Config.Key(), got)
		}
		if typedPackage(t, v, dir).TypesPkg.Scope().Lookup("Added") == nil {
			t.Errorf("view %s does not type-check the new declaration", v.Config.Key())
		}
	}
}

func TestUpdater_FileGainingATagMovesBetweenViews(t *testing.T) {
	u, set, root := setupViews(t, taggedWorkspace())
	dir := filepath.Join(root, "p")
	views := builtViews(t, set, 2)

	path := filepath.Join(dir, "b.go")
	updated := taggedHeader + "package p\n\nfunc B() {}\n"
	writeFile(t, path, updated)
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	if got := fileNames(views[0].Workspace.Packages[dir]); !slices.Equal(got, []string{"a.go"}) {
		t.Errorf("primary Files = %v", got)
	}
	if got := fileNames(views[1].Workspace.Packages[dir]); !slices.Equal(got, []string{"a.go", "b.go", "t.go"}) {
		t.Errorf("tagged Files = %v", got)
	}
	if got := string(views[1].Workspace.Packages[dir].Files["b.go"].OriginalContent); got != updated {
		t.Errorf("tagged b.go content = %q", got)
	}
	if views[0].Workspace.Packages[dir].Symbols.FindSymbol("B") != nil {
		t.Error("primary still has symbol B")
	}
}

func TestUpdater_UnbuiltViewGetsCurrentContentWhenBuilt(t *testing.T) {
	u, set, root := setupViews(t, taggedWorkspace())
	dir := filepath.Join(root, "p")

	path := filepath.Join(dir, "t.go")
	updated := taggedHeader + "package p\n\nfunc T() {}\n\nfunc Later() {}\n"
	writeFile(t, path, updated)
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	if built := set.BuiltViews(); len(built) != 1 {
		t.Fatalf("BuiltViews = %d, want only the primary", len(built))
	}
	tagged := builtViews(t, set, 2)[1]
	if got := string(tagged.Workspace.Packages[dir].Files["t.go"].OriginalContent); got != updated {
		t.Errorf("tagged content = %q", got)
	}
}

func TestUpdater_ViewLeavingTheCoveringIsDropped(t *testing.T) {
	u, set, root := setupViews(t, taggedWorkspace())
	tagged := builtViews(t, set, 2)[1]

	path := filepath.Join(root, "p", "t.go")
	writeFile(t, path, "package p\n\nfunc T() {}\n")
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	if built := set.BuiltViews(); len(built) != 1 || built[0] != set.Primary() {
		t.Fatalf("BuiltViews = %d, want only the primary", len(built))
	}
	if set.ViewOf(tagged.Workspace) != nil {
		t.Error("dropped view is still owned by the set")
	}
	builtViews(t, set, 1)
}

func TestUpdater_ChangeInvalidatesEveryBuiltViewsReferenceIndex(t *testing.T) {
	u, set, root := setupViews(t, taggedWorkspace())
	views := builtViews(t, set, 2)
	before := make([]*analysis.ReferenceIndex, len(views))
	for i, v := range views {
		idx, err := v.ReferenceIndex()
		if err != nil {
			t.Fatal(err)
		}
		if again, _ := v.ReferenceIndex(); again != idx {
			t.Fatal("reference index is not cached")
		}
		before[i] = idx
	}

	path := filepath.Join(root, "p", "a.go")
	writeFile(t, path, "package p\n\nfunc A() {}\n\nfunc Added() {}\n")
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	for i, v := range views {
		if idx, _ := v.ReferenceIndex(); idx == before[i] {
			t.Errorf("view %s kept its reference index", v.Config.Key())
		}
	}
}
