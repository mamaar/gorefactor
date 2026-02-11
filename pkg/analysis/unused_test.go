package analysis

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// createUnusedTestWorkspace builds a workspace with a mix of used/unused,
// exported/unexported symbols of various kinds.
func createUnusedTestWorkspace(t *testing.T) *types.Workspace {
	t.Helper()
	fileSet := token.NewFileSet()

	src := `package testpkg

import "fmt"

// Exported, used
func UsedExported() {
	fmt.Println("used")
	unusedUnexported()
}

// Unexported, unused
func unusedUnexported() {}

// Unexported, unused
func anotherUnused() {}

// Exported, unused
func UnusedExported() {}

// Special functions that should be skipped
func init() {}

type UsedType struct {
	Field int
}

type unusedType struct {
	field int
}

type UnusedExportedType struct {
	Field int
}

type MyInterface interface {
	DoSomething()
}

var usedVar = "used"
var unusedVar = "unused"
var UnusedExportedVar = "exported unused"

const usedConst = 1
const unusedConst = 2
const UnusedExportedConst = 3

func (u *UsedType) usedMethod() int {
	return u.Field
}

func (u *UsedType) unusedMethod() int {
	return 0
}

func (u *UsedType) UnusedExportedMethod() int {
	return 0
}

func consumer() {
	UsedExported()
	_ = UsedType{Field: 1}
	t := &UsedType{}
	t.usedMethod()
	_ = usedVar
	_ = usedConst
}
`

	astFile, err := parser.ParseFile(fileSet, "testpkg.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse test source: %v", err)
	}

	file := &types.File{
		Path:            "testpkg.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "testpkg",
		Path:  "test/testpkg",
		Files: map[string]*types.File{"testpkg.go": file},
	}
	file.Package = pkg

	workspace := &types.Workspace{
		Packages: map[string]*types.Package{
			"test/testpkg": pkg,
		},
		FileSet: fileSet,
	}

	resolver := NewSymbolResolver(workspace)
	if _, err := resolver.BuildSymbolTable(pkg); err != nil {
		t.Fatalf("Failed to build symbol table: %v", err)
	}

	return workspace
}

func TestNewUnusedAnalyzer(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)

	if analyzer == nil {
		t.Fatal("Expected non-nil analyzer")
	}
	if analyzer.workspace != ws {
		t.Error("Analyzer workspace mismatch")
	}
	if analyzer.includeExported {
		t.Error("includeExported should default to false")
	}
}

func TestFindUnusedSymbols_UnexportedOnly(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)

	unused, err := analyzer.FindUnusedSymbols()
	if err != nil {
		t.Fatalf("FindUnusedSymbols failed: %v", err)
	}

	// Should only find unexported unused symbols
	for _, u := range unused {
		if u.Symbol.Exported {
			t.Errorf("Default mode should not include exported symbol %s", u.Symbol.Name)
		}
	}

	// Should find at least some unexported unused symbols
	if len(unused) == 0 {
		t.Error("Expected to find some unused unexported symbols")
	}

	// Check that known unused unexported symbols are found
	foundNames := make(map[string]bool)
	for _, u := range unused {
		foundNames[u.Symbol.Name] = true
	}

	for _, name := range []string{"anotherUnused", "unusedType", "unusedVar", "unusedConst"} {
		if !foundNames[name] {
			t.Errorf("Expected to find unused symbol %q", name)
		}
	}
}

func TestFindUnusedSymbols_IncludeExported(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)
	analyzer.SetIncludeExported(true)

	unused, err := analyzer.FindUnusedSymbols()
	if err != nil {
		t.Fatalf("FindUnusedSymbols failed: %v", err)
	}

	// Should find both exported and unexported unused symbols
	hasExported := false
	hasUnexported := false
	for _, u := range unused {
		if u.Symbol.Exported {
			hasExported = true
		} else {
			hasUnexported = true
		}
	}

	if !hasExported {
		t.Error("Expected to find exported unused symbols with includeExported=true")
	}
	if !hasUnexported {
		t.Error("Expected to find unexported unused symbols too")
	}

	// Verify specific exported unused symbols
	foundNames := make(map[string]bool)
	for _, u := range unused {
		foundNames[u.Symbol.Name] = true
	}

	for _, name := range []string{"UnusedExported", "UnusedExportedType", "UnusedExportedVar", "UnusedExportedConst"} {
		if !foundNames[name] {
			t.Errorf("Expected to find exported unused symbol %q", name)
		}
	}
}

func TestGetUnusedUnexportedSymbols(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)

	safeToDelete, err := analyzer.GetUnusedUnexportedSymbols()
	if err != nil {
		t.Fatalf("GetUnusedUnexportedSymbols failed: %v", err)
	}

	for _, u := range safeToDelete {
		if u.Symbol.Exported {
			t.Errorf("GetUnusedUnexportedSymbols returned exported symbol %s", u.Symbol.Name)
		}
		if !u.SafeToDelete {
			t.Errorf("GetUnusedUnexportedSymbols returned symbol not safe to delete: %s", u.Symbol.Name)
		}
	}
}

func TestFindUnusedSymbols_SkipsSpecialFunctions(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)
	analyzer.SetIncludeExported(true)

	unused, err := analyzer.FindUnusedSymbols()
	if err != nil {
		t.Fatalf("FindUnusedSymbols failed: %v", err)
	}

	for _, u := range unused {
		if u.Symbol.Name == "init" {
			t.Error("init should be skipped")
		}
		if u.Symbol.Name == "main" {
			t.Error("main should be skipped")
		}
	}
}

func TestFindUnusedSymbols_SkipsTestFunctions(t *testing.T) {
	fileSet := token.NewFileSet()

	src := `package testpkg

func TestSomething() {}
func BenchmarkSomething() {}
func ExampleSomething() {}
func FuzzSomething() {}
func regularUnused() {}
`
	astFile, err := parser.ParseFile(fileSet, "testpkg_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	file := &types.File{
		Path:            "testpkg_test.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}
	pkg := &types.Package{
		Name:  "testpkg",
		Path:  "test/testpkg",
		Files: map[string]*types.File{"testpkg_test.go": file},
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/testpkg": pkg},
		FileSet:  fileSet,
	}
	resolver := NewSymbolResolver(ws)
	if _, err := resolver.BuildSymbolTable(pkg); err != nil {
		t.Fatalf("Failed to build symbol table: %v", err)
	}

	analyzer := NewUnusedAnalyzer(ws)
	unused, err := analyzer.FindUnusedSymbols()
	if err != nil {
		t.Fatalf("FindUnusedSymbols failed: %v", err)
	}

	for _, u := range unused {
		for _, prefix := range []string{"Test", "Benchmark", "Example", "Fuzz"} {
			if strings.HasPrefix(u.Symbol.Name, prefix) {
				t.Errorf("Test function %s should be skipped", u.Symbol.Name)
			}
		}
	}
}

func TestFindUnusedSymbols_Methods(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)

	unused, err := analyzer.FindUnusedSymbols()
	if err != nil {
		t.Fatalf("FindUnusedSymbols failed: %v", err)
	}

	foundUnusedMethod := false
	for _, u := range unused {
		if u.Symbol.Name == "unusedMethod" {
			foundUnusedMethod = true
			break
		}
	}

	if !foundUnusedMethod {
		t.Error("Expected to find unused method 'unusedMethod'")
	}
}

func TestFindUnusedSymbols_AllKinds(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)

	unused, err := analyzer.FindUnusedSymbols()
	if err != nil {
		t.Fatalf("FindUnusedSymbols failed: %v", err)
	}

	kindFound := make(map[types.SymbolKind]bool)
	for _, u := range unused {
		kindFound[u.Symbol.Kind] = true
	}

	expectedKinds := []types.SymbolKind{
		types.FunctionSymbol,
		types.TypeSymbol,
		types.VariableSymbol,
		types.ConstantSymbol,
	}

	for _, kind := range expectedKinds {
		if !kindFound[kind] {
			t.Errorf("Expected to find unused symbol of kind %s", kind.String())
		}
	}
}

func TestFormatUnusedSymbol(t *testing.T) {
	ws := createUnusedTestWorkspace(t)
	analyzer := NewUnusedAnalyzer(ws)

	unused := &UnusedSymbol{
		Symbol: &types.Symbol{
			Name:   "testFunc",
			Kind:   types.FunctionSymbol,
			File:   "test.go",
			Line:   10,
			Column: 5,
		},
		Reason: "No references found",
	}

	formatted := analyzer.FormatUnusedSymbol(unused)
	if !strings.Contains(formatted, "testFunc") {
		t.Error("Formatted output should contain symbol name")
	}
	if !strings.Contains(formatted, "Function") {
		t.Error("Formatted output should contain kind")
	}
	if !strings.Contains(formatted, "test.go") {
		t.Error("Formatted output should contain file")
	}
	if !strings.Contains(formatted, "No references found") {
		t.Error("Formatted output should contain reason")
	}
}
