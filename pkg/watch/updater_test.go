package watch

import (
	"go/ast"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// setupWorkspace creates a temp directory with a go.mod and one package, parses
// it into a workspace, and returns a ready-to-use WorkspaceUpdater.
func setupWorkspace(t *testing.T) (*WorkspaceUpdater, string) {
	t.Helper()
	dir := t.TempDir()

	// Write go.mod
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.25\n"), 0644)

	// Write initial package
	pkgDir := filepath.Join(dir, "pkg", "a")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "a.go"), []byte("package a\n\nfunc Hello() {}\n"), 0644)

	set, err := analysis.LoadWorkspaceSet(dir, analysis.DefaultBuildConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	u := NewUpdater(set, logger)
	return u, dir
}

func TestUpdater_ModifyReParsesFile(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")
	filePath := filepath.Join(pkgDir, "a.go")

	// Verify initial state: one function (Hello)
	pkg := u.FindPackage(pkgDir)
	if pkg == nil {
		t.Fatal("expected package at pkg/a")
	}
	if pkg.Symbols == nil || len(pkg.Symbols.Functions) != 1 {
		t.Fatalf("expected 1 function, got %d", safeFuncCount(pkg))
	}

	// Modify the file to add a second function
	_ = os.WriteFile(filePath, []byte("package a\n\nfunc Hello() {}\nfunc World() {}\n"), 0644)

	u.HandleChanges([]ChangeEvent{{Path: filePath, Op: fsnotify.Write}})

	if len(pkg.Symbols.Functions) != 2 {
		t.Fatalf("expected 2 functions after modify, got %d", len(pkg.Symbols.Functions))
	}
}

func TestUpdater_ModifyInvalidatesAndRefreshesTypeInformation(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")
	filePath := filepath.Join(pkgDir, "a.go")
	pkg := u.FindPackage(pkgDir)
	if pkg == nil {
		t.Fatal("expected package at pkg/a")
	}

	u.set.Primary().Parser.EnsureTypeChecked(u.Workspace(), pkg)
	if !pkg.TypesChecked || pkg.TypesInfo == nil {
		t.Fatal("expected initial type information")
	}

	_ = os.WriteFile(filePath, []byte("package a\n\nfunc Hello() {}\nfunc World() int { return 1 }\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: filePath, Op: fsnotify.Write}})
	if pkg.TypesChecked || pkg.TypesInfo != nil || pkg.TypesPkg != nil {
		t.Fatal("reparsed package retained stale type information")
	}

	u.set.Primary().Parser.EnsureTypeChecked(u.Workspace(), pkg)
	var world *ast.FuncDecl
	for _, file := range pkg.Files {
		for _, decl := range file.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "World" {
				world = fn
			}
		}
	}
	if world == nil || pkg.TypesInfo.Defs[world.Name] == nil {
		t.Fatal("fresh type information does not describe the reparsed World declaration")
	}
}

func TestUpdater_CreateAddsToPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")

	pkg := u.FindPackage(pkgDir)
	if pkg == nil {
		t.Fatal("expected package at pkg/a")
	}
	initialFiles := len(pkg.Files)

	// Create a new file in the existing package
	newPath := filepath.Join(pkgDir, "b.go")
	_ = os.WriteFile(newPath, []byte("package a\n\nfunc Extra() {}\n"), 0644)

	u.HandleChanges([]ChangeEvent{{Path: newPath, Op: fsnotify.Create}})

	if len(pkg.Files) != initialFiles+1 {
		t.Fatalf("expected %d files, got %d", initialFiles+1, len(pkg.Files))
	}
	if _, ok := pkg.Files["b.go"]; !ok {
		t.Fatal("b.go not found in package files")
	}
}

func TestUpdater_DeleteRemovesFromPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")

	// First, create a second file so the package won't become empty.
	secondPath := filepath.Join(pkgDir, "b.go")
	_ = os.WriteFile(secondPath, []byte("package a\n\nfunc Keep() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: secondPath, Op: fsnotify.Create}})

	pkg := u.FindPackage(pkgDir)
	if pkg == nil {
		t.Fatal("expected package")
	}
	if len(pkg.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(pkg.Files))
	}

	// Delete the original file
	origPath := filepath.Join(pkgDir, "a.go")
	_ = os.Remove(origPath)

	u.HandleChanges([]ChangeEvent{{Path: origPath, Op: fsnotify.Remove}})

	if len(pkg.Files) != 1 {
		t.Fatalf("expected 1 file after delete, got %d", len(pkg.Files))
	}
	if _, ok := pkg.Files["a.go"]; ok {
		t.Fatal("a.go should have been removed")
	}
}

func TestUpdater_DeleteLastFileRemovesPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")

	initialCount := u.PackageCount()

	// Delete the only file
	filePath := filepath.Join(pkgDir, "a.go")
	_ = os.Remove(filePath)

	u.HandleChanges([]ChangeEvent{{Path: filePath, Op: fsnotify.Remove}})

	if u.PackageCount() != initialCount-1 {
		t.Fatalf("expected package count %d, got %d", initialCount-1, u.PackageCount())
	}

	if u.FindPackage(pkgDir) != nil {
		t.Fatal("package should have been removed")
	}
}

func TestUpdater_DeletingLastNonTestFileKeepsPackageWithTests(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")
	testPath := filepath.Join(pkgDir, "a_test.go")
	_ = os.WriteFile(testPath, []byte("package a\n\nfunc helper() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: testPath, Op: fsnotify.Create}})

	filePath := filepath.Join(pkgDir, "a.go")
	_ = os.Remove(filePath)
	u.HandleChanges([]ChangeEvent{{Path: filePath, Op: fsnotify.Remove}})

	if u.FindPackage(pkgDir) == nil {
		t.Fatal("package with test files left was removed")
	}
}

func TestUpdater_ModifyInTestOnlyDirectoryKeepsPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "integration")
	_ = os.MkdirAll(pkgDir, 0755)
	testPath := filepath.Join(pkgDir, "run_test.go")
	_ = os.WriteFile(testPath, []byte("package integration\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: testPath, Op: fsnotify.Create}})

	_ = os.WriteFile(testPath, []byte("package integration\n\nfunc helper() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: testPath, Op: fsnotify.Write}})

	if u.FindPackage(pkgDir) == nil {
		t.Fatal("test-only package removed by an edit to its test file")
	}
}

func TestUpdater_NewDirectoryCreatesPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	initialCount := u.PackageCount()

	// Create a new package directory with a Go file
	newPkgDir := filepath.Join(dir, "pkg", "b")
	_ = os.MkdirAll(newPkgDir, 0755)
	newFile := filepath.Join(newPkgDir, "b.go")
	_ = os.WriteFile(newFile, []byte("package b\n\nfunc NewThing() {}\n"), 0644)

	u.HandleChanges([]ChangeEvent{{Path: newFile, Op: fsnotify.Create}})

	if u.PackageCount() != initialCount+1 {
		t.Fatalf("expected package count %d, got %d", initialCount+1, u.PackageCount())
	}

	pkg := u.FindPackage(newPkgDir)
	if pkg == nil {
		t.Fatal("new package not found")
	}
	if pkg.Name != "b" {
		t.Fatalf("expected package name 'b', got %q", pkg.Name)
	}
}

const neverBuilt = "//go:build neverset\n\n"

func TestUpdater_CreateOfNonMatchingFileLeavesPackageUnchanged(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")
	pkg := u.FindPackage(pkgDir)

	newPath := filepath.Join(pkgDir, "b.go")
	_ = os.WriteFile(newPath, []byte(neverBuilt+"package a\n\nfunc Hidden() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: newPath, Op: fsnotify.Create}})

	if _, ok := pkg.Files["b.go"]; ok || len(pkg.Files) != 1 {
		t.Fatalf("package files = %v, want only a.go", pkg.Files)
	}
	if pkg.Symbols.FindSymbol("Hidden") != nil {
		t.Fatal("symbol of an excluded file entered the package")
	}
	if u.set.Sources().File(newPath) == nil {
		t.Fatal("excluded file must still be recorded in the source tree")
	}
}

func TestUpdater_CreateInNewDirectoryWithOnlyNonMatchingFilesCreatesNoPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	initialCount := u.PackageCount()

	newPkgDir := filepath.Join(dir, "pkg", "b")
	_ = os.MkdirAll(newPkgDir, 0755)
	newFile := filepath.Join(newPkgDir, "b.go")
	_ = os.WriteFile(newFile, []byte(neverBuilt+"package b\n\nfunc Hidden() {}\n"), 0644)

	u.HandleChanges([]ChangeEvent{{Path: newFile, Op: fsnotify.Create}})

	if u.PackageCount() != initialCount || u.FindPackage(newPkgDir) != nil {
		t.Fatal("a directory with only excluded files must not become a package")
	}
}

func TestUpdater_ModifyAddingConstraintRemovesFileFromPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")
	pkg := u.FindPackage(pkgDir)

	extra := filepath.Join(pkgDir, "b.go")
	_ = os.WriteFile(extra, []byte("package a\n\nfunc Extra() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: extra, Op: fsnotify.Create}})
	if pkg.Symbols.FindSymbol("Extra") == nil {
		t.Fatal("precondition: Extra should be in the package")
	}

	_ = os.WriteFile(extra, []byte(neverBuilt+"package a\n\nfunc Extra() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: extra, Op: fsnotify.Write}})

	if _, ok := pkg.Files["b.go"]; ok {
		t.Fatal("b.go should have left the package")
	}
	if pkg.Symbols.FindSymbol("Extra") != nil {
		t.Fatal("symbol of the excluded file is still in the package")
	}
}

func TestUpdater_ModifyRemovingConstraintAddsFileToPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")
	pkg := u.FindPackage(pkgDir)

	extra := filepath.Join(pkgDir, "b.go")
	_ = os.WriteFile(extra, []byte(neverBuilt+"package a\n\nfunc Extra() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: extra, Op: fsnotify.Create}})

	_ = os.WriteFile(extra, []byte("package a\n\nfunc Extra() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: extra, Op: fsnotify.Write}})

	if pkg.Symbols.FindSymbol("Extra") == nil {
		t.Fatal("symbol should appear once the constraint is gone")
	}
}

func TestUpdater_ModifyToUnparsableFileRemovesItFromPackage(t *testing.T) {
	u, dir := setupWorkspace(t)
	pkgDir := filepath.Join(dir, "pkg", "a")
	pkg := u.FindPackage(pkgDir)

	extra := filepath.Join(pkgDir, "b.go")
	_ = os.WriteFile(extra, []byte("package a\n\nfunc Extra() {}\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: extra, Op: fsnotify.Create}})

	_ = os.WriteFile(extra, []byte("package a\n\nfunc {"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: extra, Op: fsnotify.Write}})

	if _, ok := pkg.Files["b.go"]; ok {
		t.Fatal("unparsable file should not be in the package")
	}
	if src := u.set.Sources().File(extra); src == nil || src.Err == nil {
		t.Fatal("parse error should be recorded in the source tree")
	}
}

func safeFuncCount(pkg *types.Package) int {
	if pkg.Symbols == nil {
		return 0
	}
	return len(pkg.Symbols.Functions)
}

func TestUpdater_RemovingPackageDropsItFromDependencyGraph(t *testing.T) {
	u, dir := setupWorkspace(t)
	aDir := filepath.Join(dir, "pkg", "a")
	bDir := filepath.Join(dir, "pkg", "b")
	_ = os.MkdirAll(bDir, 0755)
	bFile := filepath.Join(bDir, "b.go")
	_ = os.WriteFile(bFile, []byte("package b\n\nimport _ \"example.com/test/pkg/a\"\n"), 0644)
	u.HandleChanges([]ChangeEvent{{Path: bFile, Op: fsnotify.Create}})

	g := u.Workspace().Dependencies
	if !slices.Contains(g.PackageDeps[bDir], aDir) {
		t.Fatalf("PackageDeps[b] = %v, want it to contain %s", g.PackageDeps[bDir], aDir)
	}

	aFile := filepath.Join(aDir, "a.go")
	_ = os.Remove(aFile)
	u.HandleChanges([]ChangeEvent{{Path: aFile, Op: fsnotify.Remove}})

	g = u.Workspace().Dependencies
	if _, ok := g.PackageImports[aDir]; ok {
		t.Errorf("PackageImports still has %s", aDir)
	}
	if _, ok := g.PackageDeps[aDir]; ok {
		t.Errorf("PackageDeps still has key %s", aDir)
	}
	if slices.Contains(g.PackageDeps[bDir], aDir) {
		t.Errorf("PackageDeps[b] = %v, still lists removed %s", g.PackageDeps[bDir], aDir)
	}
}

func TestUpdater_CreatedPackageClosingACycleIsReportedNotRecursed(t *testing.T) {
	u, dir := setupWorkspace(t)
	parser := u.set.Primary().Parser
	write := func(rel, content string, op fsnotify.Op) {
		path := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		_ = os.WriteFile(path, []byte(content), 0644)
		u.HandleChanges([]ChangeEvent{{Path: path, Op: op}})
	}

	write("pkg/a/a.go", "package a\n\nimport \"example.com/test/pkg/b\"\n\nfunc Hello() { b.Bye() }\n", fsnotify.Write)
	write("pkg/d/d.go", "package d\n\nimport \"example.com/test/pkg/a\"\n\nfunc D() { a.Hello() }\n", fsnotify.Create)
	parser.EnsureTypeChecked(u.Workspace(), u.FindPackage(filepath.Join(dir, "pkg", "d")))
	write("pkg/b/b.go", "package b\n\nimport \"example.com/test/pkg/a\"\n\nfunc Bye() { a.Hello() }\n", fsnotify.Create)

	pkgB := u.FindPackage(filepath.Join(dir, "pkg", "b"))
	parser.EnsureTypeChecked(u.Workspace(), pkgB)

	if len(pkgB.ProductionErrs) == 0 {
		t.Fatal("no production error for the import cycle")
	}
}
