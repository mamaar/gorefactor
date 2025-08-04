package types

import (
	"go/ast"
	"go/token"
	"testing"
)

func TestWorkspace(t *testing.T) {
	ws := &Workspace{
		RootPath: "/test/workspace",
		Packages: make(map[string]*Package),
		FileSet:  token.NewFileSet(),
	}

	if ws.RootPath != "/test/workspace" {
		t.Errorf("Expected RootPath to be '/test/workspace', got '%s'", ws.RootPath)
	}

	if ws.Packages == nil {
		t.Error("Expected Packages to be initialized")
	}

	if ws.FileSet == nil {
		t.Error("Expected FileSet to be initialized")
	}
}

func TestPackage(t *testing.T) {
	pkg := &Package{
		Path:      "test/package",
		Name:      "testpkg",
		Dir:       "/test/package",
		Files:     make(map[string]*File),
		TestFiles: make(map[string]*File),
		Imports:   []string{"fmt", "os"},
	}

	if pkg.Path != "test/package" {
		t.Errorf("Expected Path to be 'test/package', got '%s'", pkg.Path)
	}

	if pkg.Name != "testpkg" {
		t.Errorf("Expected Name to be 'testpkg', got '%s'", pkg.Name)
	}

	if len(pkg.Imports) != 2 {
		t.Errorf("Expected 2 imports, got %d", len(pkg.Imports))
	}

	if pkg.Imports[0] != "fmt" || pkg.Imports[1] != "os" {
		t.Errorf("Expected imports ['fmt', 'os'], got %v", pkg.Imports)
	}
}

func TestFile(t *testing.T) {
	astFile := &ast.File{
		Name: &ast.Ident{Name: "test"},
	}

	file := &File{
		Path:            "/test/file.go",
		AST:             astFile,
		OriginalContent: []byte("package test\n"),
		Modifications:   make([]Modification, 0),
	}

	if file.Path != "/test/file.go" {
		t.Errorf("Expected Path to be '/test/file.go', got '%s'", file.Path)
	}

	if file.AST != astFile {
		t.Error("Expected AST to match the provided AST")
	}

	if string(file.OriginalContent) != "package test\n" {
		t.Errorf("Expected OriginalContent to be 'package test\\n', got '%s'", string(file.OriginalContent))
	}

	if len(file.Modifications) != 0 {
		t.Errorf("Expected 0 modifications, got %d", len(file.Modifications))
	}
}

func TestModule(t *testing.T) {
	module := &Module{
		Path:    "github.com/test/module",
		Version: "v1.0.0",
		GoMod:   "module github.com/test/module\n\ngo 1.21\n",
	}

	if module.Path != "github.com/test/module" {
		t.Errorf("Expected Path to be 'github.com/test/module', got '%s'", module.Path)
	}

	if module.Version != "v1.0.0" {
		t.Errorf("Expected Version to be 'v1.0.0', got '%s'", module.Version)
	}
}

func TestModification(t *testing.T) {
	mod := Modification{
		Start:   10,
		End:     20,
		NewText: "replacement text",
		Type:    Replace,
	}

	if mod.Start != 10 {
		t.Errorf("Expected Start to be 10, got %d", mod.Start)
	}

	if mod.End != 20 {
		t.Errorf("Expected End to be 20, got %d", mod.End)
	}

	if mod.NewText != "replacement text" {
		t.Errorf("Expected NewText to be 'replacement text', got '%s'", mod.NewText)
	}

	if mod.Type != Replace {
		t.Errorf("Expected Type to be Replace, got %v", mod.Type)
	}
}

func TestModificationType(t *testing.T) {
	testCases := []struct {
		name     string
		modType  ModificationType
		expected ModificationType
	}{
		{"Insert", Insert, 0},
		{"Delete", Delete, 1},
		{"Replace", Replace, 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.modType != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.modType)
			}
		})
	}
}

func TestDependencyGraph(t *testing.T) {
	graph := &DependencyGraph{
		PackageImports: make(map[string][]string),
		PackageDeps:    make(map[string][]string),
		SymbolDeps:     make(map[string]map[string][]string),
		ImportCycles:   make([][]string, 0),
	}

	// Test adding package imports
	graph.PackageImports["pkg1"] = []string{"pkg2", "pkg3"}
	graph.PackageDeps["pkg1"] = []string{"pkg2", "pkg3", "pkg4"}

	if len(graph.PackageImports["pkg1"]) != 2 {
		t.Errorf("Expected 2 direct imports for pkg1, got %d", len(graph.PackageImports["pkg1"]))
	}

	if len(graph.PackageDeps["pkg1"]) != 3 {
		t.Errorf("Expected 3 total dependencies for pkg1, got %d", len(graph.PackageDeps["pkg1"]))
	}

	// Test symbol dependencies
	graph.SymbolDeps["pkg1"] = make(map[string][]string)
	graph.SymbolDeps["pkg1"]["Symbol1"] = []string{"pkg2.Symbol2"}

	if len(graph.SymbolDeps["pkg1"]["Symbol1"]) != 1 {
		t.Errorf("Expected 1 symbol dependency, got %d", len(graph.SymbolDeps["pkg1"]["Symbol1"]))
	}

	// Test import cycles
	cycle := []string{"pkg1", "pkg2", "pkg3", "pkg1"}
	graph.ImportCycles = append(graph.ImportCycles, cycle)

	if len(graph.ImportCycles) != 1 {
		t.Errorf("Expected 1 import cycle, got %d", len(graph.ImportCycles))
	}

	if len(graph.ImportCycles[0]) != 4 {
		t.Errorf("Expected cycle length of 4, got %d", len(graph.ImportCycles[0]))
	}
}