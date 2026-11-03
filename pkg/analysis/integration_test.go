package analysis

import (
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
	promotedMethods, err := resolver.findPromotedMethods(compositeType)
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

func TestCacheInvalidation(t *testing.T) {
	workspace := createTestWorkspace(t)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	pkg := workspace.Packages["test/main"]

	// Resolve a symbol to populate cache
	symbol1, err := resolver.ResolveSymbol(pkg, "MainFunction")
	if err != nil {
		t.Fatalf("Failed to resolve MainFunction: %v", err)
	}

	key := pkg.Path + ":MainFunction"
	resolver.cache.SetResolvedRef(key, symbol1)

	resolver.InvalidateCacheForPackage(pkg.Path)

	if resolver.cache.GetResolvedRef(key) != nil {
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

// Helper functions to create test workspaces

func createTestWorkspace(t *testing.T) *types.Workspace {
	t.Helper()

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
	t.Helper()

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
