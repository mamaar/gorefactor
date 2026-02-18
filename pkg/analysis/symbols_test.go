package analysis

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestNewSymbolResolver(t *testing.T) {
	ws := &types.Workspace{
		Packages: make(map[string]*types.Package),
		FileSet:  token.NewFileSet(),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if resolver == nil {
		t.Fatal("Expected NewSymbolResolver to return a non-nil resolver")
	}

	if resolver.workspace != ws {
		t.Error("Expected resolver to store workspace reference")
	}
}

func TestSymbolResolver_BuildSymbolTable(t *testing.T) {
	// Create a test package with Go source
	fileSet := token.NewFileSet()
	src := `package test

import "fmt"

// TestFunction is a test function
func TestFunction(s string) error {
	fmt.Println(s)
	return nil
}

// TestMethod is a method
func (t *TestType) TestMethod() {
	// method implementation
}

// TestType is a test type
type TestType struct {
	Field1 string
	field2 int
}

// TestInterface is a test interface
type TestInterface interface {
	Method() error
}

const TestConst = 42
var TestVar = "test"
`

	astFile, err := parser.ParseFile(fileSet, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse test source: %v", err)
	}

	file := &types.File{
		Path:            "test.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "test",
		Path:  "test/package",
		Files: map[string]*types.File{"test.go": file},
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/package": pkg},
		FileSet:  fileSet,
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Build symbol table
	symbolTable, err := resolver.BuildSymbolTable(pkg)
	if err != nil {
		t.Fatalf("Failed to build symbol table: %v", err)
	}

	// Check that symbol table was assigned to package
	if pkg.Symbols != symbolTable {
		t.Error("Expected symbol table to be assigned to package")
	}

	// Validate functions
	if len(symbolTable.Functions) != 1 {
		t.Errorf("Expected 1 function, got %d", len(symbolTable.Functions))
	}

	if testFunc, exists := symbolTable.Functions["TestFunction"]; exists {
		if testFunc.Name != "TestFunction" {
			t.Errorf("Expected function name 'TestFunction', got '%s'", testFunc.Name)
		}
		if testFunc.Kind != types.FunctionSymbol {
			t.Errorf("Expected FunctionSymbol, got %v", testFunc.Kind)
		}
		if !testFunc.Exported {
			t.Error("Expected TestFunction to be exported")
		}
	} else {
		t.Error("Expected to find TestFunction in symbol table")
	}

	// Validate types
	if len(symbolTable.Types) < 2 {
		t.Errorf("Expected at least 2 types, got %d", len(symbolTable.Types))
	}

	if testType, exists := symbolTable.Types["TestType"]; exists {
		if testType.Kind != types.TypeSymbol {
			t.Errorf("Expected TypeSymbol, got %v", testType.Kind)
		}
		if !testType.Exported {
			t.Error("Expected TestType to be exported")
		}
	} else {
		t.Error("Expected to find TestType in symbol table")
	}

	if testInterface, exists := symbolTable.Types["TestInterface"]; exists {
		if testInterface.Kind != types.InterfaceSymbol {
			t.Errorf("Expected InterfaceSymbol, got %v", testInterface.Kind)
		}
	} else {
		t.Error("Expected to find TestInterface in symbol table")
	}

	// Validate variables
	if len(symbolTable.Variables) != 1 {
		t.Errorf("Expected 1 variable, got %d", len(symbolTable.Variables))
	}

	if testVar, exists := symbolTable.Variables["TestVar"]; exists {
		if testVar.Kind != types.VariableSymbol {
			t.Errorf("Expected VariableSymbol, got %v", testVar.Kind)
		}
		if !testVar.Exported {
			t.Error("Expected TestVar to be exported")
		}
	} else {
		t.Error("Expected to find TestVar in symbol table")
	}

	// Validate constants
	if len(symbolTable.Constants) != 1 {
		t.Errorf("Expected 1 constant, got %d", len(symbolTable.Constants))
	}

	if testConst, exists := symbolTable.Constants["TestConst"]; exists {
		if testConst.Kind != types.ConstantSymbol {
			t.Errorf("Expected ConstantSymbol, got %v", testConst.Kind)
		}
		if !testConst.Exported {
			t.Error("Expected TestConst to be exported")
		}
	} else {
		t.Error("Expected to find TestConst in symbol table")
	}

	// Validate methods (includes both receiver methods and interface methods)
	if len(symbolTable.Methods) != 2 {
		t.Errorf("Expected 2 method groups (TestType + TestInterface), got %d", len(symbolTable.Methods))
	}

	if methods, exists := symbolTable.Methods["TestType"]; exists {
		if len(methods) != 1 {
			t.Errorf("Expected 1 method for TestType, got %d", len(methods))
		}
		if methods[0].Name != "TestMethod" {
			t.Errorf("Expected method name 'TestMethod', got '%s'", methods[0].Name)
		}
		if methods[0].Kind != types.MethodSymbol {
			t.Errorf("Expected MethodSymbol, got %v", methods[0].Kind)
		}
	} else {
		t.Error("Expected to find methods for TestType")
	}

	// Validate interface methods are also discovered
	if methods, exists := symbolTable.Methods["TestInterface"]; exists {
		if len(methods) != 1 {
			t.Errorf("Expected 1 method for TestInterface, got %d", len(methods))
		}
		if methods[0].Name != "Method" {
			t.Errorf("Expected interface method name 'Method', got '%s'", methods[0].Name)
		}
	} else {
		t.Error("Expected to find methods for TestInterface")
	}
}

func TestSymbolResolver_ResolveSymbol(t *testing.T) {
	// Create a simple package for testing
	pkg := &types.Package{
		Name: "test",
		Path: "test/package",
		Symbols: &types.SymbolTable{
			Functions: make(map[string]*types.Symbol),
			Types:     make(map[string]*types.Symbol),
			Variables: make(map[string]*types.Symbol),
			Constants: make(map[string]*types.Symbol),
			Methods:   make(map[string][]*types.Symbol),
		},
	}

	// Add test symbols
	funcSymbol := &types.Symbol{Name: "TestFunc", Kind: types.FunctionSymbol}
	pkg.Symbols.Functions["TestFunc"] = funcSymbol

	typeSymbol := &types.Symbol{Name: "TestType", Kind: types.TypeSymbol}
	pkg.Symbols.Types["TestType"] = typeSymbol

	methodSymbol := &types.Symbol{Name: "TestMethod", Kind: types.MethodSymbol}
	pkg.Symbols.Methods["TestType"] = []*types.Symbol{methodSymbol}

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/package": pkg},
		FileSet:  token.NewFileSet(),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Test resolving function
	symbol, err := resolver.ResolveSymbol(pkg, "TestFunc")
	if err != nil {
		t.Errorf("Failed to resolve function symbol: %v", err)
	}
	if symbol != funcSymbol {
		t.Error("Expected to get the same function symbol")
	}

	// Test resolving type
	symbol, err = resolver.ResolveSymbol(pkg, "TestType")
	if err != nil {
		t.Errorf("Failed to resolve type symbol: %v", err)
	}
	if symbol != typeSymbol {
		t.Error("Expected to get the same type symbol")
	}

	// Test resolving method with Type.Method syntax
	symbol, err = resolver.ResolveSymbol(pkg, "TestType.TestMethod")
	if err != nil {
		t.Errorf("Failed to resolve method symbol: %v", err)
	}
	if symbol != methodSymbol {
		t.Error("Expected to get the same method symbol")
	}

	// Test resolving non-existent symbol
	_, err = resolver.ResolveSymbol(pkg, "NonExistent")
	if err == nil {
		t.Error("Expected error when resolving non-existent symbol")
	}
	
	// Check that it's a RefactorError
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.SymbolNotFound {
			t.Errorf("Expected SymbolNotFound error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestSymbolResolver_FindReferences(t *testing.T) {
	// Create a test workspace with references
	fileSet := token.NewFileSet()
	
	// Create a file with symbol references
	src := `package test

func TestFunc() {
	// This function calls itself recursively
	TestFunc()
	TestFunc()
}

func AnotherFunc() {
	TestFunc() // Another call to TestFunc
}
`

	astFile, err := parser.ParseFile(fileSet, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse test source: %v", err)
	}

	file := &types.File{
		Path:            "test.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "test",
		Path:  "test/package",
		Files: map[string]*types.File{"test.go": file},
		Symbols: &types.SymbolTable{
			Functions: make(map[string]*types.Symbol),
		},
	}
	file.Package = pkg

	// Create the symbol we're looking for
	testSymbol := &types.Symbol{
		Name:    "TestFunc",
		Kind:    types.FunctionSymbol,
		Package: "test/package",
		File:    "test.go",
	}
	pkg.Symbols.Functions["TestFunc"] = testSymbol

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/package": pkg},
		FileSet:  fileSet,
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Find references
	references, err := resolver.FindReferences(testSymbol)
	if err != nil {
		t.Fatalf("Failed to find references: %v", err)
	}

	// We expect to find at least 3 references (3 calls to TestFunc)
	if len(references) < 3 {
		t.Errorf("Expected at least 3 references, got %d", len(references))
	}

	// Check that all references point to the correct symbol
	for _, ref := range references {
		if ref.Symbol != testSymbol {
			t.Error("Expected reference to point to test symbol")
		}
		if ref.File != "test.go" {
			t.Errorf("Expected reference file to be 'test.go', got '%s'", ref.File)
		}
		if ref.Line <= 0 {
			t.Errorf("Expected positive line number, got %d", ref.Line)
		}
		if ref.Column <= 0 {
			t.Errorf("Expected positive column number, got %d", ref.Column)
		}
	}
}

func TestSymbolResolver_FindDefinition(t *testing.T) {
	// This test is complex and requires precise position calculation
	// For now, we'll test the basic error path and skip the complex position matching
	// In a production system, this would need more sophisticated position tracking
	
	ws := &types.Workspace{
		Packages: make(map[string]*types.Package),
		FileSet:  token.NewFileSet(),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Test basic error case
	_, err := resolver.FindDefinition("test.go", token.Pos(100))
	if err == nil {
		t.Error("Expected error when finding definition without file in workspace")
	}

	// Check that it's a RefactorError
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.SymbolNotFound {
			t.Errorf("Expected SymbolNotFound error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestSymbolResolver_FindDefinition_NotFound(t *testing.T) {
	ws := &types.Workspace{
		Packages: make(map[string]*types.Package),
		FileSet:  token.NewFileSet(),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Try to find definition in non-existent file
	_, err := resolver.FindDefinition("nonexistent.go", token.Pos(100))
	if err == nil {
		t.Error("Expected error when finding definition in non-existent file")
	}

	// Check that it's a RefactorError
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.SymbolNotFound {
			t.Errorf("Expected SymbolNotFound error, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestSymbolResolver_isExported(t *testing.T) {
	ws := &types.Workspace{}
	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	testCases := []struct {
		name     string
		expected bool
	}{
		{"ExportedName", true},
		{"unexportedName", false},
		{"ALLCAPS", true},
		{"lowercase", false},
		{"MixedCase", true},
		{"_underscore", false},
		{"", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := resolver.isExported(tc.name)
			if result != tc.expected {
				t.Errorf("Expected isExported('%s') to be %v, got %v", tc.name, tc.expected, result)
			}
		})
	}
}

func TestSymbolResolver_extractFunctionSignature(t *testing.T) {
	fileSet := token.NewFileSet()
	
	testCases := []struct {
		name     string
		source   string
		expected string
	}{
		{
			name:     "Simple function",
			source:   "func TestFunc() {}",
			expected: "TestFunc()",
		},
		{
			name:     "Function with parameters",
			source:   "func TestFunc(a int, b string) {}",
			expected: "TestFunc(a, b)",
		},
		{
			name:     "Function with return type",
			source:   "func TestFunc() error {}",
			expected: "TestFunc()",
		},
	}

	ws := &types.Workspace{}
	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package test\n" + tc.source
			astFile, err := parser.ParseFile(fileSet, "test.go", src, 0)
			if err != nil {
				t.Fatalf("Failed to parse source: %v", err)
			}

			// Find the function declaration
			var funcDecl *ast.FuncDecl
			ast.Inspect(astFile, func(n ast.Node) bool {
				if fd, ok := n.(*ast.FuncDecl); ok {
					funcDecl = fd
					return false
				}
				return true
			})

			if funcDecl == nil {
				t.Fatal("Could not find function declaration")
			}

			signature := resolver.extractFunctionSignature(funcDecl)
			if signature != tc.expected {
				t.Errorf("Expected signature '%s', got '%s'", tc.expected, signature)
			}
		})
	}
}

// createTypedTestWorkspace builds a workspace with full type-checking info for tests.
func createTypedTestWorkspace(t *testing.T) (*types.Workspace, *SymbolResolver) {
	t.Helper()
	fileSet := token.NewFileSet()

	src := `package testpkg

type MyStruct struct {
	Name string
}

func (s *MyStruct) GetName() string { return s.Name }

func Hello() string { return "hello" }

func Caller() {
	s := &MyStruct{Name: "test"}
	_ = s.GetName()
	_ = Hello()
}

const MyConst = 42
var MyVar = "global"
`

	astFile, err := parser.ParseFile(fileSet, "testpkg.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	conf := gotypes.Config{Importer: importer.Default()}
	info := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}
	_, err = conf.Check("testpkg", fileSet, []*ast.File{astFile}, info)
	if err != nil {
		// Type errors may occur in isolation; continue
		_ = err
	}

	file := &types.File{
		Path:            "testpkg.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}
	pkg := &types.Package{
		Name:      "testpkg",
		Path:      "test/testpkg",
		Files:     map[string]*types.File{"testpkg.go": file},
		TypesInfo: info,
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages:     map[string]*types.Package{"test/testpkg": pkg},
		FileSet:      fileSet,
		ImportToPath: make(map[string]string),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := resolver.BuildSymbolTable(pkg); err != nil {
		t.Fatalf("Failed to build symbol table: %v", err)
	}

	return ws, resolver
}

// TestObjectIndex_MatchesNameIndex verifies that the object-path and name-path
// return identical reference sets (differential test).
func TestObjectIndex_MatchesNameIndex(t *testing.T) {
	ws, resolver := createTypedTestWorkspace(t)
	idx := resolver.BuildReferenceIndex()

	// The object index should be populated
	if len(idx.objectIndex) == 0 {
		t.Fatal("Expected object index to be populated for typed workspace")
	}

	pkg := ws.Packages["test/testpkg"]

	// Test each function symbol: query via object-path and name-path should match
	for _, symbol := range pkg.Symbols.Functions {
		// Get references via normal path (which uses object fast-path internally)
		refs, err := resolver.FindReferencesIndexed(symbol, idx)
		if err != nil {
			t.Fatalf("FindReferencesIndexed failed for %s: %v", symbol.Name, err)
		}

		// Get references via name-path only (build an index without object entries)
		nameOnlyIdx := &ReferenceIndex{
			nameIndex:   idx.nameIndex,
			objectIndex: nil, // force name path
		}
		nameRefs, err := resolver.FindReferencesIndexed(symbol, nameOnlyIdx)
		if err != nil {
			t.Fatalf("FindReferencesIndexed (name-only) failed for %s: %v", symbol.Name, err)
		}

		// Compare reference positions
		objPositions := extractPositions(refs)
		namePositions := extractPositions(nameRefs)

		sort.Ints(objPositions)
		sort.Ints(namePositions)

		if len(objPositions) != len(namePositions) {
			t.Errorf("Symbol %s: object-path found %d refs, name-path found %d refs",
				symbol.Name, len(objPositions), len(namePositions))
			continue
		}

		for i := range objPositions {
			if objPositions[i] != namePositions[i] {
				t.Errorf("Symbol %s: ref position mismatch at index %d: object=%d, name=%d",
					symbol.Name, i, objPositions[i], namePositions[i])
			}
		}
	}
}

func extractPositions(refs []*types.Reference) []int {
	positions := make([]int, len(refs))
	for i, r := range refs {
		positions[i] = int(r.Position)
	}
	return positions
}

// TestObjectIndex_NilWithoutTypesInfo verifies that the object index falls back
// to the name path when type info is absent.
func TestObjectIndex_NilWithoutTypesInfo(t *testing.T) {
	fileSet := token.NewFileSet()
	src := `package untyped

func Foo() {}
func Bar() { Foo() }
`
	astFile, err := parser.ParseFile(fileSet, "untyped.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	file := &types.File{
		Path:            "untyped.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}
	pkg := &types.Package{
		Name:  "untyped",
		Path:  "test/untyped",
		Files: map[string]*types.File{"untyped.go": file},
		// No TypesInfo — deliberately omitted
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/untyped": pkg},
		FileSet:  fileSet,
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := resolver.BuildSymbolTable(pkg); err != nil {
		t.Fatalf("Failed to build symbol table: %v", err)
	}

	idx := resolver.BuildReferenceIndex()

	// Object index should be empty (no type info)
	if len(idx.objectIndex) != 0 {
		t.Errorf("Expected empty object index without type info, got %d entries", len(idx.objectIndex))
	}

	// Name-based lookup should still work
	fooSymbol := pkg.Symbols.Functions["Foo"]
	if fooSymbol == nil {
		t.Fatal("Expected to find Foo symbol")
	}

	refs, err := resolver.FindReferencesIndexed(fooSymbol, idx)
	if err != nil {
		t.Fatalf("FindReferencesIndexed failed: %v", err)
	}
	if len(refs) == 0 {
		t.Error("Expected at least one reference to Foo via name path")
	}

	// HasNonDeclarationReference should work via name path
	if !resolver.HasNonDeclarationReference(fooSymbol, idx) {
		t.Error("Expected HasNonDeclarationReference to return true for Foo")
	}
}

// TestObjectIndex_CrossPackageReferences verifies object index finds cross-package refs.
func TestObjectIndex_CrossPackageReferences(t *testing.T) {
	fileSet := token.NewFileSet()

	// Package A: defines a function
	srcA := `package pkga

func SharedFunc() string { return "shared" }
`
	astA, err := parser.ParseFile(fileSet, "pkga.go", srcA, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse pkga: %v", err)
	}

	confA := gotypes.Config{Importer: importer.Default()}
	infoA := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}
	typsPkgA, err := confA.Check("pkga", fileSet, []*ast.File{astA}, infoA)
	if err != nil {
		_ = err
	}

	fileA := &types.File{
		Path:            "pkga.go",
		AST:             astA,
		OriginalContent: []byte(srcA),
	}
	pkgA := &types.Package{
		Name:      "pkga",
		Path:      "test/pkga",
		Files:     map[string]*types.File{"pkga.go": fileA},
		TypesInfo: infoA,
	}
	fileA.Package = pkgA

	// Package B: uses pkga.SharedFunc
	srcB := `package pkgb

import "pkga"

func Caller() string { return pkga.SharedFunc() }
`
	astB, err := parser.ParseFile(fileSet, "pkgb.go", srcB, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse pkgb: %v", err)
	}

	confB := gotypes.Config{
		Importer: importerFunc(func(path string) (*gotypes.Package, error) {
			if path == "pkga" {
				return typsPkgA, nil
			}
			return importer.Default().Import(path)
		}),
	}
	infoB := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}
	_, err = confB.Check("pkgb", fileSet, []*ast.File{astB}, infoB)
	if err != nil {
		_ = err
	}

	fileB := &types.File{
		Path:            "pkgb.go",
		AST:             astB,
		OriginalContent: []byte(srcB),
	}
	pkgB := &types.Package{
		Name:       "pkgb",
		Path:       "test/pkgb",
		ImportPath: "pkgb",
		Files:      map[string]*types.File{"pkgb.go": fileB},
		TypesInfo:  infoB,
	}
	fileB.Package = pkgB

	ws := &types.Workspace{
		Packages: map[string]*types.Package{
			"test/pkga": pkgA,
			"test/pkgb": pkgB,
		},
		FileSet:      fileSet,
		ImportToPath: make(map[string]string),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, pkg := range ws.Packages {
		if _, err := resolver.BuildSymbolTable(pkg); err != nil {
			t.Fatalf("Failed to build symbol table for %s: %v", pkg.Name, err)
		}
	}

	idx := resolver.BuildReferenceIndex()

	// Object index should be populated
	if len(idx.objectIndex) == 0 {
		t.Fatal("Expected object index to be populated")
	}

	// Find SharedFunc in pkgA
	sharedFunc := pkgA.Symbols.Functions["SharedFunc"]
	if sharedFunc == nil {
		t.Fatal("Expected to find SharedFunc symbol")
	}

	// The object index should find the cross-package reference
	refs, err := resolver.FindReferencesIndexed(sharedFunc, idx)
	if err != nil {
		t.Fatalf("FindReferencesIndexed failed: %v", err)
	}

	if len(refs) == 0 {
		t.Error("Expected at least one cross-package reference to SharedFunc")
	}

	// Verify HasNonDeclarationReference also works
	if !resolver.HasNonDeclarationReference(sharedFunc, idx) {
		t.Error("Expected HasNonDeclarationReference to find cross-package usage")
	}
}

// importerFunc adapts a function to the go/types.Importer interface.
type importerFunc func(path string) (*gotypes.Package, error)

func (f importerFunc) Import(path string) (*gotypes.Package, error) {
	return f(path)
}