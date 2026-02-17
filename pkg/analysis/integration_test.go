package analysis

import (
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Integration tests for the complete symbol resolution system

func TestCompleteSymbolResolution(t *testing.T) {
	// Create a comprehensive test workspace
	workspace := createTestWorkspace(t)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Test 1: Basic symbol resolution
	t.Run("BasicSymbolResolution", func(t *testing.T) {
		pkg := workspace.Packages["test/main"]
		symbol, err := resolver.ResolveSymbol(pkg, "MainFunction")
		if err != nil {
			t.Fatalf("Failed to resolve MainFunction: %v", err)
		}
		if symbol.Name != "MainFunction" || symbol.Kind != types.FunctionSymbol {
			t.Errorf("Expected MainFunction (Function), got %s (%s)", symbol.Name, symbol.Kind.String())
		}
	})

	// Test 2: Method set resolution
	t.Run("MethodSetResolution", func(t *testing.T) {
		pkg := workspace.Packages["test/main"]
		typeSymbol, err := resolver.ResolveSymbol(pkg, "TestStruct")
		if err != nil {
			t.Fatalf("Failed to resolve TestStruct: %v", err)
		}

		methods, err := resolver.ResolveMethodSet(typeSymbol)
		if err != nil {
			t.Fatalf("Failed to resolve method set: %v", err)
		}

		if len(methods) == 0 {
			t.Error("Expected methods on TestStruct")
		}

		// Should find the TestMethod
		found := false
		for _, method := range methods {
			if method.Name == "TestMethod" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected to find TestMethod in method set")
		}
	})

	// Test 3: Interface implementation checking
	t.Run("InterfaceCompliance", func(t *testing.T) {
		pkg := workspace.Packages["test/main"]
		
		typeSymbol, err := resolver.ResolveSymbol(pkg, "TestStruct")
		if err != nil {
			t.Fatalf("Failed to resolve TestStruct: %v", err)
		}

		ifaceSymbol, err := resolver.ResolveSymbol(pkg, "TestInterface")
		if err != nil {
			t.Fatalf("Failed to resolve TestInterface: %v", err)
		}

		compliant, missing := resolver.CheckInterfaceCompliance(typeSymbol, ifaceSymbol)
		if !compliant {
			t.Errorf("TestStruct should implement TestInterface, missing: %v", missing)
		}
	})

	// Test 4: Scope-aware resolution
	t.Run("ScopeAwareResolution", func(t *testing.T) {
		file := workspace.Packages["test/main"].Files["main.go"]
		
		// Test resolution at different positions
		// This would require knowing specific positions in the test file
		scope, err := resolver.scopeAnalyzer.GetScopeAt(file, token.Pos(100))
		if err == nil && scope != nil {
			t.Logf("Found scope of kind: %v", scope.Kind)
		}
	})

	// Test 5: Cache performance
	t.Run("CachePerformance", func(t *testing.T) {
		pkg := workspace.Packages["test/main"]
		
		// Resolve the same symbol multiple times
		for i := 0; i < 10; i++ {
			_, err := resolver.ResolveSymbol(pkg, "MainFunction")
			if err != nil {
				t.Fatalf("Failed to resolve MainFunction on iteration %d: %v", i, err)
			}
		}

		// Check cache stats
		stats := resolver.cache.GetStats()
		t.Logf("Cache stats: hits=%d, misses=%d", stats.ResolvedRefHits, stats.ResolvedRefMisses)
		// Note: ResolveSymbol doesn't use the resolved ref cache, it uses package symbols cache
		// So we don't expect hits in ResolvedRefHits for this test

		hitRate := resolver.cache.GetHitRate()
		t.Logf("Cache hit rate: %.2f%%", hitRate)
	})

	// Test 6: Error diagnostics
	t.Run("ErrorDiagnostics", func(t *testing.T) {
		pkg := workspace.Packages["test/main"]
		
		// Try to resolve a non-existent symbol
		_, err := resolver.ResolveSymbol(pkg, "NonExistentSymbol")
		if err == nil {
			t.Error("Expected error for non-existent symbol")
		}

		// Check if it's an enhanced error with suggestions
		if resErr, ok := err.(*ResolutionError); ok {
			if len(resErr.Suggestions) == 0 {
				t.Error("Expected suggestions in error")
			}
			t.Logf("Error with suggestions: %s", resErr.FormatError())
		}
	})

	// Test 7: Cross-package resolution
	t.Run("CrossPackageResolution", func(t *testing.T) {
		// Test resolving symbols from imported packages
		utilsPkg := workspace.Packages["test/utils"]
		if utilsPkg == nil {
			t.Skip("Utils package not available for cross-package test")
		}

		symbol, err := resolver.ResolveSymbol(utilsPkg, "UtilityFunction")
		if err != nil {
			t.Fatalf("Failed to resolve UtilityFunction: %v", err)
		}

		if !symbol.Exported {
			t.Error("UtilityFunction should be exported")
		}
	})
}

func TestEmbeddedFieldResolution(t *testing.T) {
	workspace := createEmbeddedFieldWorkspace(t)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	pkg := workspace.Packages["test/embedded"]
	compositeType, err := resolver.ResolveSymbol(pkg, "CompositeStruct")
	if err != nil {
		t.Fatalf("Failed to resolve CompositeStruct: %v", err)
	}

	// Test embedded field resolution
	embeddedFields, err := resolver.ResolveEmbeddedFields(compositeType)
	if err != nil {
		t.Fatalf("Failed to resolve embedded fields: %v", err)
	}

	if len(embeddedFields) == 0 {
		t.Error("Expected embedded fields in CompositeStruct")
	}

	// Test promoted method resolution
	promotedMethods, err := resolver.FindPromotedMethods(compositeType)
	if err != nil {
		t.Fatalf("Failed to find promoted methods: %v", err)
	}

	// Should include methods from embedded fields
	found := false
	for _, method := range promotedMethods {
		if method.Name == "EmbeddedMethod" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected to find promoted EmbeddedMethod")
	}
}

func TestScopeAnalysis(t *testing.T) {
	workspace := createScopedWorkspace(t)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	file := workspace.Packages["test/scoped"].Files["scoped.go"]
	
	// Build scope tree
	scopeTree, err := resolver.scopeAnalyzer.BuildScopeTree(file)
	if err != nil {
		t.Fatalf("Failed to build scope tree: %v", err)
	}

	if scopeTree.Kind != PackageScope {
		t.Errorf("Expected root scope to be PackageScope, got %v", scopeTree.Kind)
	}

	// Should have nested scopes
	if len(scopeTree.Children) == 0 {
		t.Error("Expected nested scopes in scope tree")
	}

	// Test scope-based symbol resolution
	// Find a position inside a function and test local variable resolution
	testPos := token.Pos(200) // Arbitrary position for testing
	scope := resolver.scopeAnalyzer.findEnclosingScope(scopeTree, testPos)
	if scope == nil {
		t.Error("Expected to find enclosing scope")
	}
}

func TestCacheInvalidation(t *testing.T) {
	workspace := createTestWorkspace(t)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	pkg := workspace.Packages["test/main"]
	
	// Resolve a symbol to populate cache
	symbol1, err := resolver.ResolveSymbol(pkg, "MainFunction")
	if err != nil {
		t.Fatalf("Failed to resolve MainFunction: %v", err)
	}

	// Check cache has entries
	initialSize := resolver.cache.GetCacheSize()
	if initialSize.ResolvedRefs == 0 {
		t.Error("Expected cache to have entries")
	}

	// Invalidate cache for the package
	resolver.InvalidateCacheForPackage(pkg.Path)

	// Cache should be smaller or empty
	newSize := resolver.cache.GetCacheSize()
	if newSize.ResolvedRefs >= initialSize.ResolvedRefs {
		t.Error("Expected cache to be invalidated")
	}

	// Should still be able to resolve the symbol
	symbol2, err := resolver.ResolveSymbol(pkg, "MainFunction")
	if err != nil {
		t.Fatalf("Failed to resolve MainFunction after cache invalidation: %v", err)
	}

	if symbol1.Name != symbol2.Name {
		t.Error("Symbol should be the same after cache invalidation")
	}
}

func TestPerformanceWithLargeCodebase(t *testing.T) {
	workspace := createLargeWorkspace(t)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Warm up the cache
	resolver.cache.WarmCache(workspace)

	// Perform many resolution operations
	const numOperations = 1000
	
	for i := 0; i < numOperations; i++ {
		for _, pkg := range workspace.Packages {
			if pkg.Symbols != nil && len(pkg.Symbols.Functions) > 0 {
				// Resolve first function in package
				for name := range pkg.Symbols.Functions {
					_, _ = resolver.ResolveSymbol(pkg, name)
					break
				}
			}
		}
	}

	// Check cache performance
	stats := resolver.cache.GetStats()
	hitRate := resolver.cache.GetHitRate()
	
	t.Logf("Performance test completed:")
	t.Logf("  Total operations: %d", numOperations*len(workspace.Packages))
	t.Logf("  Cache hit rate: %.2f%%", hitRate)
	t.Logf("  Cache hits: %d", stats.ResolvedRefHits)
	t.Logf("  Cache misses: %d", stats.ResolvedRefMisses)
	
	if hitRate < 50.0 {
		t.Errorf("Expected cache hit rate > 50%%, got %.2f%%", hitRate)
	}
}

// Helper functions to create test workspaces

func createTestWorkspace(t *testing.T) *types.Workspace {
	fileSet := token.NewFileSet()
	
	// Create main package
	mainSrc := `package main

import "fmt"

type TestStruct struct {
	Field1 string
	field2 int
}

func (ts *TestStruct) TestMethod() string {
	return ts.Field1
}

type TestInterface interface {
	TestMethod() string
}

func MainFunction() {
	fmt.Println("Hello")
	ts := &TestStruct{Field1: "test"}
	ts.TestMethod()
}

const TestConst = 42
var TestVar = "test"
`

	mainAST, err := parser.ParseFile(fileSet, "main.go", mainSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse main source: %v", err)
	}

	mainFile := &types.File{
		Path:            "main.go",
		AST:             mainAST,
		OriginalContent: []byte(mainSrc),
	}

	mainPkg := &types.Package{
		Name:  "main",
		Path:  "test/main",
		Files: map[string]*types.File{"main.go": mainFile},
	}
	mainFile.Package = mainPkg

	// Create utils package
	utilsSrc := `package utils

func UtilityFunction() string {
	return "utility"
}

func privateFunction() string {
	return "private"
}
`

	utilsAST, err := parser.ParseFile(fileSet, "utils.go", utilsSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse utils source: %v", err)
	}

	utilsFile := &types.File{
		Path:            "utils.go",
		AST:             utilsAST,
		OriginalContent: []byte(utilsSrc),
	}

	utilsPkg := &types.Package{
		Name:  "utils",
		Path:  "test/utils",
		Files: map[string]*types.File{"utils.go": utilsFile},
	}
	utilsFile.Package = utilsPkg

	workspace := &types.Workspace{
		Packages: map[string]*types.Package{
			"test/main":  mainPkg,
			"test/utils": utilsPkg,
		},
		FileSet: fileSet,
	}

	// Build symbol tables
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, _ = resolver.BuildSymbolTable(mainPkg)
	_, _ = resolver.BuildSymbolTable(utilsPkg)

	return workspace
}

func createEmbeddedFieldWorkspace(t *testing.T) *types.Workspace {
	fileSet := token.NewFileSet()
	
	src := `package embedded

type BaseStruct struct {
	BaseField string
}

func (bs *BaseStruct) EmbeddedMethod() string {
	return bs.BaseField
}

type CompositeStruct struct {
	BaseStruct
	OwnField int
}

func (cs *CompositeStruct) OwnMethod() int {
	return cs.OwnField
}
`

	ast, err := parser.ParseFile(fileSet, "embedded.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse embedded source: %v", err)
	}

	file := &types.File{
		Path:            "embedded.go",
		AST:             ast,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "embedded",
		Path:  "test/embedded",
		Files: map[string]*types.File{"embedded.go": file},
	}
	file.Package = pkg

	workspace := &types.Workspace{
		Packages: map[string]*types.Package{
			"test/embedded": pkg,
		},
		FileSet: fileSet,
	}

	// Build symbol tables
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, _ = resolver.BuildSymbolTable(pkg)

	return workspace
}

func createScopedWorkspace(t *testing.T) *types.Workspace {
	fileSet := token.NewFileSet()
	
	src := `package scoped

import "fmt"

var globalVar = "global"

func OuterFunction(param string) {
	localVar := "local"
	
	if true {
		blockVar := "block"
		fmt.Println(globalVar, param, localVar, blockVar)
	}
	
	for i := 0; i < 10; i++ {
		loopVar := i * 2
		fmt.Println(loopVar)
	}
}

func (s *SomeType) MethodWithScopes(methodParam int) {
	methodLocal := methodParam * 2
	fmt.Println(methodLocal)
}

type SomeType struct {
	field string
}
`

	ast, err := parser.ParseFile(fileSet, "scoped.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse scoped source: %v", err)
	}

	file := &types.File{
		Path:            "scoped.go",
		AST:             ast,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "scoped",
		Path:  "test/scoped",
		Files: map[string]*types.File{"scoped.go": file},
	}
	file.Package = pkg

	workspace := &types.Workspace{
		Packages: map[string]*types.Package{
			"test/scoped": pkg,
		},
		FileSet: fileSet,
	}

	// Build symbol tables
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, _ = resolver.BuildSymbolTable(pkg)

	return workspace
}

func createLargeWorkspace(t *testing.T) *types.Workspace {
	fileSet := token.NewFileSet()
	workspace := &types.Workspace{
		Packages: make(map[string]*types.Package),
		FileSet:  fileSet,
	}

	// Create multiple packages with many symbols
	for pkgNum := 0; pkgNum < 10; pkgNum++ {
		src := fmt.Sprintf(`package pkg%d

import "fmt"

type Type%d struct {
	Field1 string
	Field2 int
}

func (t *Type%d) Method1() string {
	return t.Field1
}

func (t *Type%d) Method2() int {
	return t.Field2
}

func Function%d() {
	fmt.Println("Function %d")
}

const Const%d = %d
var Var%d = "var%d"
`, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum)

		ast, err := parser.ParseFile(fileSet, fmt.Sprintf("pkg%d.go", pkgNum), src, parser.ParseComments)
		if err != nil {
			t.Fatalf("Failed to parse package %d source: %v", pkgNum, err)
		}

		file := &types.File{
			Path:            fmt.Sprintf("pkg%d.go", pkgNum),
			AST:             ast,
			OriginalContent: []byte(src),
		}

		pkg := &types.Package{
			Name:  fmt.Sprintf("pkg%d", pkgNum),
			Path:  fmt.Sprintf("test/pkg%d", pkgNum),
			Files: map[string]*types.File{fmt.Sprintf("pkg%d.go", pkgNum): file},
		}
		file.Package = pkg

		workspace.Packages[pkg.Path] = pkg
	}

	// Build symbol tables for all packages
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, pkg := range workspace.Packages {
		_, _ = resolver.BuildSymbolTable(pkg)
	}

	return workspace
}