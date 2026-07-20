package analysis

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestNewParser(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if parser == nil {
		t.Fatal("Expected NewParser to return a non-nil parser")
	}

	if parser.fileSet == nil {
		t.Error("Expected parser to have a non-nil fileSet")
	}
}

func TestParser_ParseFile(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	testContent := `package test

import "fmt"

// TestFunction is a test function
func TestFunction() {
	fmt.Println("Hello, World!")
}

const TestConst = 42
var testVar = "test"
`

	err := os.WriteFile(testFile, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Parse the file
	file, err := parser.ParseFile(testFile)
	if err != nil {
		t.Fatalf("Failed to parse file: %v", err)
	}

	// Validate the parsed file
	if file.Path != testFile {
		t.Errorf("Expected Path to be '%s', got '%s'", testFile, file.Path)
	}

	if file.AST == nil {
		t.Error("Expected AST to be non-nil")
	}

	if file.AST.Name.Name != "test" {
		t.Errorf("Expected package name to be 'test', got '%s'", file.AST.Name.Name)
	}

	if string(file.OriginalContent) != testContent {
		t.Error("Expected OriginalContent to match test content")
	}

	if len(file.Modifications) != 0 {
		t.Errorf("Expected 0 modifications, got %d", len(file.Modifications))
	}

	// Check imports
	if len(file.AST.Imports) != 1 {
		t.Errorf("Expected 1 import, got %d", len(file.AST.Imports))
	}

	if file.AST.Imports[0].Path.Value != `"fmt"` {
		t.Errorf("Expected import to be '\"fmt\"', got %s", file.AST.Imports[0].Path.Value)
	}
}

func TestParser_ParseFile_NonExistent(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Try to parse a non-existent file
	_, err := parser.ParseFile("/non/existent/file.go")
	if err == nil {
		t.Error("Expected error when parsing non-existent file")
	}

	// Check that it's a RefactorError
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.FileSystemError {
			t.Errorf("Expected FileSystemError, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestParser_ParseFile_InvalidSyntax(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary file with invalid syntax
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "invalid.go")
	invalidContent := `package test

func InvalidSyntax( {
	// Missing closing parenthesis
}
`

	err := os.WriteFile(testFile, []byte(invalidContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Try to parse the invalid file
	_, err = parser.ParseFile(testFile)
	if err == nil {
		t.Error("Expected error when parsing invalid syntax")
	}

	// Check that it's a RefactorError with ParseError type
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.ParseError {
			t.Errorf("Expected ParseError, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestParser_ParsePackage(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary package directory
	tempDir := t.TempDir()

	// Create main.go
	mainContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(mainContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create main.go: %v", err)
	}

	// Create utils.go
	utilsContent := `package main

import "strings"

func ToUpper(s string) string {
	return strings.ToUpper(s)
}
`
	err = os.WriteFile(filepath.Join(tempDir, "utils.go"), []byte(utilsContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create utils.go: %v", err)
	}

	// Create a test file
	testContent := `package main

import "testing"

func TestToUpper(t *testing.T) {
	result := ToUpper("hello")
	if result != "HELLO" {
		t.Errorf("Expected HELLO, got %s", result)
	}
}
`
	err = os.WriteFile(filepath.Join(tempDir, "main_test.go"), []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create main_test.go: %v", err)
	}

	// Parse the package
	pkg, err := parser.ParsePackage(tempDir)
	if err != nil {
		t.Fatalf("Failed to parse package: %v", err)
	}

	// Validate the package
	if pkg.Name != "main" {
		t.Errorf("Expected package name to be 'main', got '%s'", pkg.Name)
	}

	if pkg.Dir != tempDir {
		t.Errorf("Expected Dir to be '%s', got '%s'", tempDir, pkg.Dir)
	}

	if len(pkg.Files) != 2 {
		t.Errorf("Expected 2 regular files, got %d", len(pkg.Files))
	}

	if len(pkg.TestFiles) != 1 {
		t.Errorf("Expected 1 test file, got %d", len(pkg.TestFiles))
	}

	// Check imports
	expectedImports := []string{"fmt", "strings"}
	if len(pkg.Imports) != len(expectedImports) {
		t.Errorf("Expected %d imports, got %d", len(expectedImports), len(pkg.Imports))
	}

	for _, expectedImport := range expectedImports {
		found := slices.Contains(pkg.Imports, expectedImport)
		if !found {
			t.Errorf("Expected import '%s' not found", expectedImport)
		}
	}

	// Check that files have back-references to package
	for _, file := range pkg.Files {
		if file.Package != pkg {
			t.Error("Expected file to have back-reference to package")
		}
	}

	for _, file := range pkg.TestFiles {
		if file.Package != pkg {
			t.Error("Expected test file to have back-reference to package")
		}
	}
}

func TestParser_ParsePackage_Empty(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create an empty directory
	tempDir := t.TempDir()

	// Try to parse empty package
	_, err := parser.ParsePackage(tempDir)
	if err == nil {
		t.Error("Expected error when parsing empty package")
	}

	// Check that it's a RefactorError
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.ParseError {
			t.Errorf("Expected ParseError, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestParser_ParseWorkspace(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary workspace
	tempDir := t.TempDir()

	// Create go.mod
	goModContent := `module test/workspace

go 1.21
`
	err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create go.mod: %v", err)
	}

	// Create main package
	mainDir := filepath.Join(tempDir, "cmd", "main")
	err = os.MkdirAll(mainDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create main directory: %v", err)
	}

	mainContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err = os.WriteFile(filepath.Join(mainDir, "main.go"), []byte(mainContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create main.go: %v", err)
	}

	// Create a library package
	libDir := filepath.Join(tempDir, "pkg", "lib")
	err = os.MkdirAll(libDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create lib directory: %v", err)
	}

	libContent := `package lib

func Add(a, b int) int {
	return a + b
}
`
	err = os.WriteFile(filepath.Join(libDir, "lib.go"), []byte(libContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create lib.go: %v", err)
	}

	// Parse the workspace
	ws, err := parser.ParseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("Failed to parse workspace: %v", err)
	}

	// Validate the workspace
	if ws.RootPath != tempDir {
		t.Errorf("Expected RootPath to be '%s', got '%s'", tempDir, ws.RootPath)
	}

	if ws.Module == nil {
		t.Error("Expected Module to be non-nil")
	} else if ws.Module.Path != "test/workspace" {
		t.Errorf("Expected module path to be 'test/workspace', got '%s'", ws.Module.Path)
	}

	if len(ws.Packages) < 2 {
		t.Errorf("Expected at least 2 packages, got %d", len(ws.Packages))
	}

	if ws.FileSet == nil {
		t.Error("Expected FileSet to be non-nil")
	}

	// Check that packages are properly linked
	for _, pkg := range ws.Packages {
		if pkg.Name == "" {
			t.Error("Expected package to have a name")
		}
		if len(pkg.Files) == 0 {
			t.Error("Expected package to have files")
		}
	}
}

func TestParser_UpdateFile(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := `package test

func Original() {
	// original function
}
`

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Parse the file
	file, err := parser.ParseFile(testFile)
	if err != nil {
		t.Fatalf("Failed to parse file: %v", err)
	}

	// Add some modifications
	file.Modifications = []types.Modification{
		{
			Start:   strings.Index(originalContent, "Original"),
			End:     strings.Index(originalContent, "Original") + len("Original"),
			NewText: "Modified",
			Type:    types.Replace,
		},
	}

	// Update the file
	err = parser.UpdateFile(file)
	if err != nil {
		t.Fatalf("Failed to update file: %v", err)
	}

	// Check that modifications were cleared
	if len(file.Modifications) != 0 {
		t.Errorf("Expected modifications to be cleared, got %d", len(file.Modifications))
	}

	// Check that AST was updated (this is a basic check)
	if file.AST == nil {
		t.Error("Expected AST to be non-nil after update")
	}
}

func TestParser_UpdateFile_NoModifications(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := `package test

func Test() {
	// test function
}
`

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Parse the file
	file, err := parser.ParseFile(testFile)
	if err != nil {
		t.Fatalf("Failed to parse file: %v", err)
	}

	originalAST := file.AST

	// Update file with no modifications
	err = parser.UpdateFile(file)
	if err != nil {
		t.Fatalf("Failed to update file: %v", err)
	}

	// AST should remain the same
	if file.AST != originalAST {
		t.Error("Expected AST to remain unchanged when no modifications")
	}
}

// TestParser_TypeCheckPackage_DiamondMemoization is a regression test for the
// exponential re-checking bug: a package that FAILS to type-check (here,
// because it imports an unresolvable third-party module) must still be
// memoised, or every dependent that imports it re-triggers a full re-check —
// recursively, through every package IT depends on.
//
// The workspace is a diamond: d imports b and c, both of which import a, and
// a imports a module path that can never resolve. Under the old
// "TypesPkg != nil" memo guard, a package that fails never gets cached, so
// checking a is repeated once per importer (here 2x instead of 1x). This
// test fails on the pre-fix code and passes after it — verified by stashing
// the parser.go fix and re-running.
func TestParser_TypeCheckPackage_DiamondMemoization(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tempDir := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(tempDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("failed to create dir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", rel, err)
		}
	}

	writeFile("go.mod", "module test/diamond\n\ngo 1.21\n")

	// a imports a third-party module that can never resolve (workspaceImporter
	// falls back to importer.Default(), which is GOPATH-based and can't see
	// it), so a's own type-check always fails.
	writeFile("a/a.go", `package a

import bad "example.com/nonexistent/thirdparty"

type T int

func UseBad() bad.Thing {
	var v bad.Thing
	return v
}
`)

	writeFile("b/b.go", `package b

import "test/diamond/a"

func UseA() a.T {
	var v a.T
	return v
}
`)

	writeFile("c/c.go", `package c

import "test/diamond/a"

func UseA() a.T {
	var v a.T
	return v
}
`)

	writeFile("d/d.go", `package d

import (
	"test/diamond/b"
	"test/diamond/c"
)

func UseBoth() {
	b.UseA()
	c.UseA()
}
`)

	ws, err := parser.ParseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	counts := make(map[string]int)
	parser.typeCheckHook = func(pkg *types.Package) {
		counts[pkg.ImportPath]++
	}

	fsPath, ok := ws.ImportToPath["test/diamond/d"]
	if !ok {
		t.Fatal("expected test/diamond/d to be in ImportToPath")
	}
	pkgD, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatal("expected package d to be parsed")
	}

	parser.EnsureTypeChecked(ws, pkgD)

	for _, name := range []string{"test/diamond/a", "test/diamond/b", "test/diamond/c", "test/diamond/d"} {
		if got := counts[name]; got != 1 {
			t.Errorf("expected package %s to be type-checked exactly once, got %d", name, got)
		}
	}
}

// TestParser_TypeCheckPackage_CycleThroughTestFiles is a regression test for
// the recursion guard: TypeCheckPackage folds in-package _test.go files into
// the same check as the regular files (see the comment on TypeCheckPackage),
// so a cycle "a_test.go imports b, b imports a" is reachable even though Go
// forbids import cycles in non-test code. Without a guard this recurses
// forever (stack overflow); the test asserts it terminates instead.
func TestParser_TypeCheckPackage_CycleThroughTestFiles(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tempDir := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(tempDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("failed to create dir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", rel, err)
		}
	}

	writeFile("go.mod", "module test/cycle\n\ngo 1.21\n")

	writeFile("a/a.go", `package a

func F() {}
`)
	writeFile("a/a_test.go", `package a

import "test/cycle/b"

func UseB() {
	b.G()
}
`)
	writeFile("b/b.go", `package b

import "test/cycle/a"

func G() {
	a.F()
}
`)

	ws, err := parser.ParseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	fsPath, ok := ws.ImportToPath["test/cycle/a"]
	if !ok {
		t.Fatal("expected test/cycle/a to be in ImportToPath")
	}
	pkgA, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatal("expected package a to be parsed")
	}

	done := make(chan struct{})
	go func() {
		parser.EnsureTypeChecked(ws, pkgA)
		close(done)
	}()

	select {
	case <-done:
		// Terminated, as expected.
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureTypeChecked did not terminate — cycle guard is broken")
	}

	if !pkgA.TypesChecked {
		t.Error("expected package a to be marked TypesChecked after the call returns")
	}
}

// TestParser_TypeCheckPackage_ConcurrentSharedDependency is a regression test
// for the in-progress marker's meaning. The marker exists to break cycles, and
// Import breaks a cycle by handing go/types an empty placeholder package. That
// is only correct when the marked package is on the *same* call stack; if the
// marker could also be set by another goroutine checking the same dependency,
// Import would hand out a placeholder for a perfectly good package, the
// dependent would fail to resolve anything from it, and — because results are
// memoized — that degraded type info would be cached permanently.
//
// MCP handlers hold only a read lock on the workspace state, so concurrent
// requests really do reach the importer at once. Here several goroutines
// type-check different packages that all import one shared, deliberately slow
// dependency. Every dependent must end up with a clean type-check; a leaked
// placeholder shows up as a non-nil TypesErr on whichever dependent lost the
// race.
func TestParser_TypeCheckPackage_ConcurrentSharedDependency(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tempDir := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(tempDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("failed to create dir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", rel, err)
		}
	}

	writeFile("go.mod", "module test/conc\n\ngo 1.21\n")

	// Make the shared dependency big enough that checking it takes long enough
	// for the other goroutines to pile up behind it.
	var dep strings.Builder
	dep.WriteString("package dep\n\ntype T int\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&dep, "\nfunc F%d(a, b T) T { x := a + b; y := x * 2; return y - a }\n", i)
	}
	writeFile("dep/dep.go", dep.String())

	const dependents = 8
	for i := 0; i < dependents; i++ {
		writeFile(fmt.Sprintf("user%d/user%d.go", i, i), fmt.Sprintf(`package user%d

import "test/conc/dep"

func Use() dep.T {
	var v dep.T
	return dep.F0(v, v)
}
`, i))
	}

	ws, err := parser.ParseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	pkgs := make([]*types.Package, 0, dependents)
	for i := 0; i < dependents; i++ {
		importPath := fmt.Sprintf("test/conc/user%d", i)
		fsPath, ok := ws.ImportToPath[importPath]
		if !ok {
			t.Fatalf("expected %s to be in ImportToPath", importPath)
		}
		pkg, ok := ws.Packages[fsPath]
		if !ok {
			t.Fatalf("expected package %s to be parsed", importPath)
		}
		pkgs = append(pkgs, pkg)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, pkg := range pkgs {
		wg.Add(1)
		go func(pkg *types.Package) {
			defer wg.Done()
			<-start // release all goroutines together to maximise overlap
			parser.EnsureTypeChecked(ws, pkg)
		}(pkg)
	}
	close(start)
	wg.Wait()

	for _, pkg := range pkgs {
		if !pkg.TypesChecked {
			t.Errorf("expected %s to be marked TypesChecked", pkg.ImportPath)
			continue
		}
		if pkg.TypesErr != nil {
			t.Errorf("package %s failed to type-check against the shared dependency: %v\n"+
				"this means Import handed it a placeholder for test/conc/dep instead of the real package",
				pkg.ImportPath, pkg.TypesErr)
		}
	}
}
