package analysis

import (
	"fmt"
	gotypes "go/types"
	"io"
	"log/slog"
	"os"
	"os/exec"
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

// TestParser_WorkspaceImporter_ExternalModuleFallback is a regression test for
// the workspaceImporter.Import fallback: replacing importer.Default() (GOPATH
// only, cannot see the module cache) with a go/packages-backed loader. It
// loads this very repository as the workspace — a real module with real
// third-party dependencies already in the local module cache — and
// type-checks pkg/analysis itself, which imports golang.org/x/tools/go/packages.
// Before the fix this package's TypesErr would be non-nil (the import
// unresolvable) and any type-aware logic guarded on TypeOf() for its symbols
// would silently degrade; this asserts both the clean type-check and that a
// symbol from the external package actually resolves.
func TestParser_WorkspaceImporter_ExternalModuleFallback(t *testing.T) {
	// Skip rather than fail if the module cache doesn't already have
	// golang.org/x/tools: GOPROXY=off (set inside loadExternal) makes a cold
	// cache fail fast instead of hanging on a network fetch, but a cold cache
	// is a legitimate reason to skip this test, not to fail it — the test
	// must never become flaky in a clean environment with no prior `go build`.
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "golang.org/x/tools")
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("golang.org/x/tools not present in the local module cache; skipping")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("failed to resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Skipf("repo root %s has no go.mod; skipping", repoRoot)
	}

	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ws, err := parser.ParseWorkspace(repoRoot)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	const importPath = "github.com/mamaar/gorefactor/pkg/analysis"
	fsPath, ok := ws.ImportToPath[importPath]
	if !ok {
		t.Fatalf("expected %s to be in ImportToPath", importPath)
	}
	pkg, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatalf("expected %s to be parsed", importPath)
	}

	parser.EnsureTypeChecked(ws, pkg)

	if pkg.TypesErr != nil {
		t.Fatalf("expected %s to type-check cleanly via the go/packages fallback, got: %v", importPath, pkg.TypesErr)
	}
	if pkg.TypesPkg == nil {
		t.Fatal("expected TypesPkg to be populated")
	}

	var extPkg *gotypes.Package
	for _, imp := range pkg.TypesPkg.Imports() {
		if imp.Path() == "golang.org/x/tools/go/packages" {
			extPkg = imp
			break
		}
	}
	if extPkg == nil {
		t.Fatal("expected golang.org/x/tools/go/packages among pkg/analysis's resolved imports")
	}
	if obj := extPkg.Scope().Lookup("Load"); obj == nil {
		t.Error("expected golang.org/x/tools/go/packages.Load to resolve from the externally-loaded package")
	}
}

// TestSymbolResolver_CallSitesOf_TestOnlyThirdPartyImport is a regression
// test for a silent gap in workspaceImporter.loadExternal: with cfg.Tests
// true, packages.Load(cfg, "./...") returns TWO *packages.Package values for
// a workspace-local package that has tests — the production variant and the
// "p [p.test]" in-package-test variant — sharing one PkgPath but distinguished
// only by ID. loadExternal's cycle-avoidance walk used to dedupe on PkgPath,
// so whichever of the two variants it visited first marked the PkgPath seen
// and the OTHER variant's extra import edges (anything only a _test.go file
// imports) were never walked — so a third-party module imported EXCLUSIVELY
// from _test.go files anywhere in the workspace never reached imp.ext.
//
// The consequence reached far past loadExternal: Import() then failed to
// resolve that module, which failed the package's "with tests" type-check
// attempt, and (*GoParser).TypeCheckPackage's fallback silently retried
// WITHOUT any of that package's test files — dropping every _test.go
// identifier from TypesInfo, in every package with such an import, not just
// the one that has it. That is how a call site in an entirely unrelated
// _test.go file (in the same package) went missing from CallSitesOf with no
// error anywhere: this test's own case, ss_arcgis_tokens_test.go calling
// GetTokenForService while a SIBLING test file in the same package imported
// github.com/DATA-DOG/go-sqlmock.
//
// This loads a synthetic module (not this repo) whose only package, foo, has
// a production call site (bar.go) and a _test.go call site guarded by a
// real third-party import (golang.org/x/tools/go/ast/inspector, chosen
// because it is already a direct dependency of this very tool and so is
// already present in the local module cache — see the skip guard below,
// mirrored from TestParser_WorkspaceImporter_ExternalModuleFallback).
func TestSymbolResolver_CallSitesOf_TestOnlyThirdPartyImport(t *testing.T) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "golang.org/x/tools")
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("golang.org/x/tools not present in the local module cache; skipping")
	}

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

	writeFile("go.mod", "module example.com/testws\n\ngo 1.26.0\n\nrequire golang.org/x/tools v0.50.0\n")
	writeFile("go.sum",
		"golang.org/x/tools v0.50.0 h1:c2ifzfcuY7L90lZ2aKd8S4K2NpASF08SZx9ZuJkHmSU=\n"+
			"golang.org/x/tools v0.50.0/go.mod h1:7ulVMw3831Mwi5EZD6RomGyffr4VFjuNYXf2BbCEAV0=\n")
	writeFile("foo/foo.go", `package foo

func Process(name string) string { return name }
`)
	// A normal production call site, so this test also confirms the fix does
	// not somehow cost the package its non-test call sites.
	writeFile("foo/bar.go", `package foo

func Run() string { return Process("prod") }
`)
	// The test-only third-party import that triggers the bug: nothing in
	// foo's production files imports golang.org/x/tools, only this file does.
	writeFile("foo/foo_test.go", `package foo

import (
	"testing"

	"golang.org/x/tools/go/ast/inspector"
)

func TestProcess(t *testing.T) {
	_ = inspector.New(nil)
	Process("test")
}
`)

	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ws, err := parser.ParseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	fsPath, ok := ws.ImportToPath["example.com/testws/foo"]
	if !ok {
		t.Fatal("expected example.com/testws/foo to be in ImportToPath")
	}
	pkg, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatal("expected package foo to be parsed")
	}

	parser.EnsureTypeChecked(ws, pkg)
	if pkg.TestTypesExcluded {
		t.Fatalf("expected foo's test files to type-check together with the package, got excluded (cause: %v)",
			pkg.TestTypesExcludedErr)
	}
	if pkg.TypesErr != nil {
		t.Fatalf("expected foo to type-check cleanly, got: %v", pkg.TypesErr)
	}

	var seed *gotypes.Func
	for ident, obj := range pkg.TypesInfo.Defs {
		if ident.Name == "Process" {
			seed, _ = obj.(*gotypes.Func)
		}
	}
	if seed == nil {
		t.Fatal("expected Process to resolve to a go/types object")
	}

	idx := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil))).BuildReferenceIndex()
	if !idx.HasTypeInfo() {
		t.Fatal("expected the reference index to have type info")
	}

	sites := idx.CallSitesOf(seed)
	var sawProd, sawTest bool
	for _, site := range sites {
		switch {
		case strings.HasSuffix(site.File.Path, "bar.go"):
			sawProd = true
		case strings.HasSuffix(site.File.Path, "foo_test.go"):
			sawTest = true
		}
	}
	if !sawProd {
		t.Errorf("expected a call site in bar.go, got sites: %+v", sites)
	}
	if !sawTest {
		t.Errorf("expected a call site in foo_test.go (this is the bug: it used to go missing), got sites: %+v", sites)
	}
}

// TestParser_WorkspaceImporter_HasExternalImports covers the check that
// decides whether loading the module graph via go/packages is worth doing.
// The interesting case is the third one: pkg.Imports is only accumulated from
// non-test files, so a workspace whose sole third-party dependency sits in a
// _test.go looks dependency-free unless test files are scanned too. Getting
// that wrong silently costs those test files their type information, because
// TypeCheckPackage folds in-package test files into the same check.
func TestParser_WorkspaceImporter_HasExternalImports(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{
			name: "stdlib and local imports only",
			files: map[string]string{
				"go.mod": "module test/ext\n\ngo 1.21\n",
				"a/a.go": "package a\n\nimport \"fmt\"\n\nfunc A() { fmt.Println(\"x\") }\n",
				"b/b.go": "package b\n\nimport \"test/ext/a\"\n\nfunc B() { a.A() }\n",
				"c/c.go": "package c\n\nimport \"strings\"\n\nfunc C() string { return strings.TrimSpace(\" x \") }\n",
			},
			want: false,
		},
		{
			name: "third-party import in a regular file",
			files: map[string]string{
				"go.mod": "module test/ext\n\ngo 1.21\n",
				"a/a.go": "package a\n\nimport \"golang.org/x/tools/go/packages\"\n\nvar _ = packages.Load\n",
			},
			want: true,
		},
		{
			name: "third-party import only in a test file",
			files: map[string]string{
				"go.mod":      "module test/ext\n\ngo 1.21\n",
				"a/a.go":      "package a\n\nfunc A() int { return 1 }\n",
				"a/a_test.go": "package a\n\nimport (\n\t\"testing\"\n\n\t\"golang.org/x/tools/go/packages\"\n)\n\nvar _ = packages.Load\n\nfunc TestA(t *testing.T) { _ = A() }\n",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
			tempDir := t.TempDir()
			for rel, content := range tt.files {
				full := filepath.Join(tempDir, rel)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatalf("failed to create dir for %s: %v", rel, err)
				}
				if err := os.WriteFile(full, []byte(content), 0644); err != nil {
					t.Fatalf("failed to write %s: %v", rel, err)
				}
			}

			if _, err := parser.ParseWorkspace(tempDir); err != nil {
				t.Fatalf("failed to parse workspace: %v", err)
			}

			if got := parser.importer.hasExternalImports(); got != tt.want {
				t.Errorf("hasExternalImports() = %v, want %v", got, tt.want)
			}
		})
	}
}
