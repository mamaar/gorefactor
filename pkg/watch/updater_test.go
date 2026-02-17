package watch

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

	parser := analysis.NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ws, err := parser.ParseWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, pkg := range ws.Packages {
		if _, err := resolver.BuildSymbolTable(pkg); err != nil {
			t.Fatal(err)
		}
	}

	analyzer := analysis.NewDependencyAnalyzer(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := analyzer.BuildDependencyGraph(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	u := NewUpdater(ws, parser, resolver, analyzer, logger)
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

func safeFuncCount(pkg *types.Package) int {
	if pkg.Symbols == nil {
		return 0
	}
	return len(pkg.Symbols.Functions)
}
