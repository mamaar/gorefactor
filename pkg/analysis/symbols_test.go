package analysis

import (
	"errors"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"sort"
	"strings"
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

	symbolTable := resolver.BuildSymbolTable(pkg)

	if pkg.Symbols != symbolTable {
		t.Error("Expected symbol table to be assigned to package")
	}

	t.Run("functions", func(t *testing.T) {
		if len(symbolTable.Functions) != 1 {
			t.Errorf("Expected 1 function, got %d", len(symbolTable.Functions))
		}

		testFunc, exists := symbolTable.Functions["TestFunction"]
		if !exists {
			t.Fatal("Expected to find TestFunction in symbol table")
		}

		if testFunc.Name != "TestFunction" {
			t.Errorf("Expected function name 'TestFunction', got '%s'", testFunc.Name)
		}

		if testFunc.Kind != types.FunctionSymbol {
			t.Errorf("Expected FunctionSymbol, got %v", testFunc.Kind)
		}

		if !testFunc.Exported {
			t.Error("Expected TestFunction to be exported")
		}
	})

	t.Run("types", func(t *testing.T) {
		if len(symbolTable.Types) < 2 {
			t.Errorf("Expected at least 2 types, got %d", len(symbolTable.Types))
		}

		testType, exists := symbolTable.Types["TestType"]
		if !exists {
			t.Error("Expected to find TestType in symbol table")
		} else {
			if testType.Kind != types.TypeSymbol {
				t.Errorf("Expected TypeSymbol, got %v", testType.Kind)
			}

			if !testType.Exported {
				t.Error("Expected TestType to be exported")
			}
		}

		testInterface, exists := symbolTable.Types["TestInterface"]
		if !exists {
			t.Error("Expected to find TestInterface in symbol table")
		} else if testInterface.Kind != types.InterfaceSymbol {
			t.Errorf("Expected InterfaceSymbol, got %v", testInterface.Kind)
		}
	})

	t.Run("variables", func(t *testing.T) {
		if len(symbolTable.Variables) != 1 {
			t.Errorf("Expected 1 variable, got %d", len(symbolTable.Variables))
		}

		testVar, exists := symbolTable.Variables["TestVar"]
		if !exists {
			t.Fatal("Expected to find TestVar in symbol table")
		}

		if testVar.Kind != types.VariableSymbol {
			t.Errorf("Expected VariableSymbol, got %v", testVar.Kind)
		}

		if !testVar.Exported {
			t.Error("Expected TestVar to be exported")
		}
	})

	t.Run("constants", func(t *testing.T) {
		if len(symbolTable.Constants) != 1 {
			t.Errorf("Expected 1 constant, got %d", len(symbolTable.Constants))
		}

		testConst, exists := symbolTable.Constants["TestConst"]
		if !exists {
			t.Fatal("Expected to find TestConst in symbol table")
		}

		if testConst.Kind != types.ConstantSymbol {
			t.Errorf("Expected ConstantSymbol, got %v", testConst.Kind)
		}

		if !testConst.Exported {
			t.Error("Expected TestConst to be exported")
		}
	})

	t.Run("receiver and interface methods", func(t *testing.T) {
		if len(symbolTable.Methods) != 2 {
			t.Errorf("Expected 2 method groups (TestType + TestInterface), got %d", len(symbolTable.Methods))
		}

		methods, exists := symbolTable.Methods["TestType"]
		if !exists {
			t.Error("Expected to find methods for TestType")
		} else {
			if len(methods) != 1 {
				t.Fatalf("Expected 1 method for TestType, got %d", len(methods))
			}

			if methods[0].Name != "TestMethod" {
				t.Errorf("Expected method name 'TestMethod', got '%s'", methods[0].Name)
			}

			if methods[0].Kind != types.MethodSymbol {
				t.Errorf("Expected MethodSymbol, got %v", methods[0].Kind)
			}
		}

		methods, exists = symbolTable.Methods["TestInterface"]
		if !exists {
			t.Error("Expected to find methods for TestInterface")
		} else {
			if len(methods) != 1 {
				t.Fatalf("Expected 1 method for TestInterface, got %d", len(methods))
			}

			if methods[0].Name != "Method" {
				t.Errorf("Expected interface method name 'Method', got '%s'", methods[0].Name)
			}
		}
	})
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
	refErr, ok := errors.AsType[*types.RefactorError](err)
	if ok {
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
	references := resolver.FindReferences(testSymbol)

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
				fd, ok := n.(*ast.FuncDecl)
				if ok {
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

	typesPkg, err := conf.Check("testpkg", fileSet, []*ast.File{astFile}, info)
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
		TypesPkg:  typesPkg,
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages:     map[string]*types.Package{"test/testpkg": pkg},
		FileSet:      fileSet,
		ImportToPath: make(map[string]string),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = resolver.BuildSymbolTable(pkg)

	return ws, resolver
}

// TestObjectIndex_MatchesNameIndex verifies that the object-path and name-path
// return identical reference sets (differential test).
func TestObjectIndex_MatchesNameIndex(t *testing.T) {
	ws, resolver := createTypedTestWorkspace(t)
	idx := resolver.BuildReferenceIndex()

	// The workspace index should be populated
	if idx.workspaceIdx == nil || len(idx.workspaceIdx.packages) == 0 {
		t.Fatal("Expected workspace index to be populated for typed workspace")
	}

	pkg := ws.Packages["test/testpkg"]

	// Test each function symbol: query via workspace-path and name-path should match
	for _, symbol := range pkg.Symbols.Functions {
		// Get references via normal path (which uses workspace fast-path internally)
		refs, err := resolver.FindReferencesIndexed(symbol, idx)
		if err != nil {
			t.Fatalf("FindReferencesIndexed failed for %s: %v", symbol.Name, err)
		}

		// Get references via name-path only (build an index without workspace entries)
		nameOnlyIdx := &ReferenceIndex{
			nameIndex: idx.nameIndex,
			// workspaceIdx nil — forces name path
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
	_ = resolver.BuildSymbolTable(pkg)

	idx := resolver.BuildReferenceIndex()

	// Workspace index should have no packages (no type info)
	if idx.workspaceIdx != nil && len(idx.workspaceIdx.packages) != 0 {
		t.Errorf("Expected empty workspace index without type info, got %d packages",
			len(idx.workspaceIdx.packages))
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

	// The usage graph should record the use via the name path
	if !hasUse(ws, resolver, idx, fooSymbol) {
		t.Error("Expected the usage graph to record a use of Foo")
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
		TypesPkg:  typsPkgA,
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

	typsPkgB, err := confB.Check("pkgb", fileSet, []*ast.File{astB}, infoB)
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
		TypesPkg:   typsPkgB,
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
		_ = resolver.BuildSymbolTable(pkg)
	}

	idx := resolver.BuildReferenceIndex()

	// Workspace index should be populated
	if idx.workspaceIdx == nil || len(idx.workspaceIdx.packages) == 0 {
		t.Fatal("Expected workspace index to be populated")
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

	if !hasUse(ws, resolver, idx, sharedFunc) {
		t.Error("Expected the usage graph to record the cross-package use")
	}
}

// TestSymbolResolver_ExtractStructFieldSymbols verifies that struct fields are
// indexed into symbolTable.Fields, that the recorded Position resolves via
// pkg.TypesInfo.Defs to a *go/types.Var with IsField() == true, that embedded
// fields are skipped, and that interfaces contribute no fields.
func TestSymbolResolver_ExtractStructFieldSymbols(t *testing.T) {
	fileSet := token.NewFileSet()

	src := `package fieldtest

type Embedded struct {
	X int
}

// User represents a person.
type User struct {
	Embedded
	Name string
	Age  int
}

type Greeter interface {
	Greet() string
}
`

	astFile, err := parser.ParseFile(fileSet, "fieldtest.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse test source: %v", err)
	}

	conf := gotypes.Config{Importer: importer.Default()}
	info := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}

	typesPkg, err := conf.Check("fieldtest", fileSet, []*ast.File{astFile}, info)
	if err != nil {
		_ = err
	}

	file := &types.File{
		Path:            "fieldtest.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:      "fieldtest",
		Path:      "test/fieldtest",
		Files:     map[string]*types.File{"fieldtest.go": file},
		TypesInfo: info,
		TypesPkg:  typesPkg,
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages:     map[string]*types.Package{"test/fieldtest": pkg},
		FileSet:      fileSet,
		ImportToPath: make(map[string]string),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	symbolTable := resolver.BuildSymbolTable(pkg)

	// User should have exactly Name and Age indexed (Embedded is skipped).
	userFields, exists := symbolTable.Fields["User"]
	if !exists {
		t.Fatal("Expected symbolTable.Fields[\"User\"] to exist")
	}

	if len(userFields) != 2 {
		t.Fatalf("Expected 2 fields for User, got %d", len(userFields))
	}

	var nameField *types.Symbol

	t.Run("embedded field is not indexed", func(t *testing.T) {
		for _, f := range userFields {
			if f.Name == "Embedded" {
				t.Error("Expected embedded field 'Embedded' to NOT be indexed")
			}

			if f.Name == "Name" {
				nameField = f
			}
		}
	})

	if nameField == nil {
		t.Fatal("Expected to find Name field symbol for User")
	}

	t.Run("field symbol attributes", func(t *testing.T) {
		if nameField.Kind != types.StructFieldSymbol {
			t.Errorf("Expected StructFieldSymbol, got %v", nameField.Kind)
		}

		if !nameField.Exported {
			t.Error("Expected Name field to be exported")
		}

		if nameField.Parent == nil || nameField.Parent.Name != "User" {
			t.Error("Expected Name field's Parent to be the User type symbol")
		}
	})

	t.Run("position resolves to a struct field var", func(t *testing.T) {
		var resolvedObj gotypes.Object

		for ident, obj := range info.Defs {
			if ident.Pos() == nameField.Position {
				resolvedObj = obj
				break
			}
		}

		if resolvedObj == nil {
			t.Fatal("Expected Name field's Position to resolve via TypesInfo.Defs")
		}

		fieldVar, ok := resolvedObj.(*gotypes.Var)
		if !ok {
			t.Fatalf("Expected resolved object to be *go/types.Var, got %T", resolvedObj)
		}

		if !fieldVar.IsField() {
			t.Error("Expected resolved *go/types.Var to have IsField() == true")
		}
	})

	t.Run("interfaces contribute no fields", func(t *testing.T) {
		_, exists := symbolTable.Fields["Greeter"]
		if exists {
			t.Error("Expected interface type 'Greeter' to have no entry in symbolTable.Fields")
		}
	})

	t.Run("embedded struct keeps its own fields", func(t *testing.T) {
		embeddedFields, exists := symbolTable.Fields["Embedded"]
		if !exists || len(embeddedFields) != 1 || embeddedFields[0].Name != "X" {
			t.Error("Expected symbolTable.Fields[\"Embedded\"] to contain field X")
		}
	})
}

// importerFunc adapts a function to the go/types.Importer interface.
type importerFunc func(path string) (*gotypes.Package, error)

func (f importerFunc) Import(path string) (*gotypes.Package, error) {
	return f(path)
}

// TestWorkspaceIndex_MatchesNameIndex verifies that the workspaceIndex and name-path
// return identical reference sets for every function/type symbol (differential test).
func TestWorkspaceIndex_MatchesNameIndex(t *testing.T) {
	ws, resolver := createTypedTestWorkspace(t)
	idx := resolver.BuildReferenceIndex()

	// workspaceIndex should be populated
	if idx.workspaceIdx == nil || len(idx.workspaceIdx.packages) == 0 {
		t.Fatal("Expected workspaceIndex to be populated for typed workspace")
	}

	pkg := ws.Packages["test/testpkg"]

	// Test each function symbol: workspace-path and name-path should match
	for _, symbol := range pkg.Symbols.Functions {
		// Get references via workspace-path (normal flow)
		refs, err := resolver.FindReferencesIndexed(symbol, idx)
		if err != nil {
			t.Fatalf("FindReferencesIndexed failed for %s: %v", symbol.Name, err)
		}

		// Get references via name-path only
		nameOnlyIdx := &ReferenceIndex{
			nameIndex: idx.nameIndex,
		}

		nameRefs, err := resolver.FindReferencesIndexed(symbol, nameOnlyIdx)
		if err != nil {
			t.Fatalf("FindReferencesIndexed (name-only) failed for %s: %v", symbol.Name, err)
		}

		wsPos := extractPositions(refs)
		namePos := extractPositions(nameRefs)

		sort.Ints(wsPos)
		sort.Ints(namePos)

		if len(wsPos) != len(namePos) {
			t.Errorf("Symbol %s: workspace-path found %d refs, name-path found %d refs",
				symbol.Name, len(wsPos), len(namePos))

			continue
		}

		for i := range wsPos {
			if wsPos[i] != namePos[i] {
				t.Errorf("Symbol %s: ref position mismatch at index %d: ws=%d, name=%d",
					symbol.Name, i, wsPos[i], namePos[i])
			}
		}
	}
}

// TestWorkspaceIndex_NilWithoutTypesInfo verifies graceful fallback when
// type info is absent.
func TestWorkspaceIndex_NilWithoutTypesInfo(t *testing.T) {
	fileSet := token.NewFileSet()
	src := `package untyped

func Foo() {}
func Bar() { Foo() }
`

	astFile, err := parser.ParseFile(fileSet, "untyped2.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	file := &types.File{
		Path:            "untyped2.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}
	pkg := &types.Package{
		Name:  "untyped",
		Path:  "test/untyped2",
		Files: map[string]*types.File{"untyped2.go": file},
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/untyped2": pkg},
		FileSet:  fileSet,
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = resolver.BuildSymbolTable(pkg)

	idx := resolver.BuildReferenceIndex()

	// workspaceIndex should have no packages (no TypesInfo)
	if idx.workspaceIdx != nil && len(idx.workspaceIdx.packages) != 0 {
		t.Errorf("Expected empty workspaceIndex without type info, got %d packages",
			len(idx.workspaceIdx.packages))
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
}

// TestWorkspaceIndex_CrossPackageReferences verifies cross-package refs are found
// via the workspaceIndex path.
func TestWorkspaceIndex_CrossPackageReferences(t *testing.T) {
	fileSet := token.NewFileSet()

	// Package A
	srcA := `package pkga

func SharedFunc() string { return "shared" }
`

	astA, err := parser.ParseFile(fileSet, "ws_pkga.go", srcA, parser.ParseComments)
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
		Path:            "ws_pkga.go",
		AST:             astA,
		OriginalContent: []byte(srcA),
	}
	pkgA := &types.Package{
		Name:      "pkga",
		Path:      "test/ws_pkga",
		Files:     map[string]*types.File{"ws_pkga.go": fileA},
		TypesInfo: infoA,
		TypesPkg:  typsPkgA,
	}
	fileA.Package = pkgA

	// Package B
	srcB := `package pkgb

import "pkga"

func Caller() string { return pkga.SharedFunc() }
`

	astB, err := parser.ParseFile(fileSet, "ws_pkgb.go", srcB, parser.ParseComments)
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

	typsPkgB, err := confB.Check("pkgb", fileSet, []*ast.File{astB}, infoB)
	if err != nil {
		_ = err
	}

	fileB := &types.File{
		Path:            "ws_pkgb.go",
		AST:             astB,
		OriginalContent: []byte(srcB),
	}
	pkgB := &types.Package{
		Name:       "pkgb",
		Path:       "test/ws_pkgb",
		ImportPath: "pkgb",
		Files:      map[string]*types.File{"ws_pkgb.go": fileB},
		TypesInfo:  infoB,
		TypesPkg:   typsPkgB,
	}
	fileB.Package = pkgB

	ws := &types.Workspace{
		Packages: map[string]*types.Package{
			"test/ws_pkga": pkgA,
			"test/ws_pkgb": pkgB,
		},
		FileSet:      fileSet,
		ImportToPath: make(map[string]string),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, pkg := range ws.Packages {
		_ = resolver.BuildSymbolTable(pkg)
	}

	idx := resolver.BuildReferenceIndex()

	if idx.workspaceIdx == nil || len(idx.workspaceIdx.packages) == 0 {
		t.Fatal("Expected workspaceIndex to be populated")
	}

	// Find SharedFunc in pkgA
	sharedFunc := pkgA.Symbols.Functions["SharedFunc"]
	if sharedFunc == nil {
		t.Fatal("Expected to find SharedFunc symbol")
	}

	// workspaceIndex should find the cross-package reference
	refs, err := resolver.FindReferencesIndexed(sharedFunc, idx)
	if err != nil {
		t.Fatalf("FindReferencesIndexed failed: %v", err)
	}

	if len(refs) == 0 {
		t.Error("Expected at least one cross-package reference to SharedFunc")
	}

	if !hasUse(ws, resolver, idx, sharedFunc) {
		t.Error("Expected the usage graph to record the cross-package use")
	}
}

// createFieldRefTestWorkspace builds a type-checked single-package workspace
// containing two unrelated structs that both declare a field named "Name",
// plus a struct with a field that is never referenced. Used by the
// FindMemberReferences tests.
func createFieldRefTestWorkspace(t *testing.T) (*types.Workspace, *SymbolResolver, *types.Package) {
	t.Helper()

	fileSet := token.NewFileSet()

	src := `package fieldrefs

type User struct {
	Name string
	Age  int
}

type Product struct {
	Name  string
	Price int
}

type Unused struct {
	Ghost string
}

func Build() {
	u := User{Name: "alice"}
	_ = u.Name
	u.Name = "bob"

	p := Product{Name: "widget"}
	_ = p.Name
	_ = Unused{}
}
`

	astFile, err := parser.ParseFile(fileSet, "fieldrefs.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	conf := gotypes.Config{Importer: importer.Default()}
	info := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}

	typesPkg, err := conf.Check("fieldrefs", fileSet, []*ast.File{astFile}, info)
	if err != nil {
		t.Fatalf("Type-check failed: %v", err)
	}

	file := &types.File{
		Path:            "fieldrefs.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}
	pkg := &types.Package{
		Name:      "fieldrefs",
		Path:      "test/fieldrefs",
		Files:     map[string]*types.File{"fieldrefs.go": file},
		TypesInfo: info,
		TypesPkg:  typesPkg,
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages:     map[string]*types.Package{"test/fieldrefs": pkg},
		FileSet:      fileSet,
		ImportToPath: make(map[string]string),
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = resolver.BuildSymbolTable(pkg)

	return ws, resolver, pkg
}

// TestFindMemberReferences_ErrorWhenTypesInfoNil verifies the load-bearing
// fail-closed guarantee: with no type information available, FindMemberReferences
// returns an error and NOT an empty-but-successful (name-fallback) result.
func TestFindMemberReferences_ErrorWhenTypesInfoNil(t *testing.T) {
	_, resolver, pkg := createFieldRefTestWorkspace(t)

	// Build the index while type info is present so the workspace index itself
	// is populated; this isolates the TypesInfo-nil guard specifically.
	idx := resolver.BuildReferenceIndex()
	if idx.workspaceIdx == nil || len(idx.workspaceIdx.packages) == 0 {
		t.Fatal("expected a populated workspace index for this fixture")
	}

	field := pkg.Symbols.FindSymbol("User.Name")
	if field == nil {
		t.Fatal("expected to resolve User.Name field symbol")
	}

	// Remove type info to trigger the fail-closed guard.
	pkg.TypesInfo = nil

	refs, err := resolver.FindMemberReferences(field, idx)
	if err == nil {
		t.Fatalf("expected an error when TypesInfo is nil, got nil (refs=%d)", len(refs))
	}

	if refs != nil {
		t.Errorf("expected nil refs on error, got %d references", len(refs))
	}

	_, ok := errors.AsType[*types.RefactorError](err)
	if !ok {
		t.Errorf("expected *types.RefactorError, got %T", err)
	}
}

// TestFindMemberReferences_EmptyIsNotError verifies that a field with zero uses
// resolves to its object and returns an empty slice with no error — this must be
// distinguished from the nil-object failure case.
func TestFindMemberReferences_EmptyIsNotError(t *testing.T) {
	_, resolver, pkg := createFieldRefTestWorkspace(t)
	idx := resolver.BuildReferenceIndex()

	ghost := pkg.Symbols.FindSymbol("Unused.Ghost")
	if ghost == nil {
		t.Fatal("expected to resolve Unused.Ghost field symbol")
	}

	refs, err := resolver.FindMemberReferences(ghost, idx)
	if err != nil {
		t.Fatalf("expected no error for an unreferenced field, got: %v", err)
	}

	if len(refs) != 0 {
		t.Errorf("expected 0 references for unreferenced field Ghost, got %d", len(refs))
	}
}

// TestFindMemberReferences_DoesNotConflateSameNamedFields verifies that
// FindMemberReferences for User.Name returns only User.Name uses and never a
// reference to the identically-named Product.Name field.
func TestFindMemberReferences_DoesNotConflateSameNamedFields(t *testing.T) {
	_, resolver, pkg := createFieldRefTestWorkspace(t)
	idx := resolver.BuildReferenceIndex()

	userName := pkg.Symbols.FindSymbol("User.Name")

	productName := pkg.Symbols.FindSymbol("Product.Name")
	if userName == nil || productName == nil {
		t.Fatalf("expected to resolve both User.Name and Product.Name (user=%v product=%v)", userName, productName)
	}

	userRefs, err := resolver.FindMemberReferences(userName, idx)
	if err != nil {
		t.Fatalf("FindMemberReferences(User.Name) failed: %v", err)
	}

	productRefs, err := resolver.FindMemberReferences(productName, idx)
	if err != nil {
		t.Fatalf("FindMemberReferences(Product.Name) failed: %v", err)
	}

	if len(userRefs) == 0 {
		t.Fatal("expected at least one reference to User.Name")
	}

	if len(productRefs) == 0 {
		t.Fatal("expected at least one reference to Product.Name")
	}

	// The position sets must be disjoint. If name-based matching leaked in, the
	// same "Name" identifiers would appear in both sets.
	productPositions := make(map[token.Pos]bool)
	for _, r := range productRefs {
		productPositions[r.Position] = true
	}

	for _, r := range userRefs {
		if productPositions[r.Position] {
			t.Errorf("User.Name reference at %s collides with a Product.Name reference",
				resolver.workspace.FileSet.Position(r.Position))
		}
	}

	// Sanity: a pure name walk would have returned every "Name" occurrence to
	// both. Confirm the union exceeds either individual set.
	if len(userRefs)+len(productRefs) <= len(userRefs) {
		t.Error("expected distinct references for the two same-named fields")
	}
}

// TestResolveSymbol_DottedFieldAndBareNameGuard verifies that ResolveSymbol
// resolves the dotted form "User.Name" to a StructFieldSymbol, while a bare
// "Name" does not resolve to a field.
func TestResolveSymbol_DottedFieldAndBareNameGuard(t *testing.T) {
	_, resolver, pkg := createFieldRefTestWorkspace(t)

	sym, err := resolver.ResolveSymbol(pkg, "User.Name")
	if err != nil {
		t.Fatalf("expected User.Name to resolve, got error: %v", err)
	}

	if sym == nil || sym.Kind != types.StructFieldSymbol {
		t.Fatalf("expected a StructFieldSymbol for User.Name, got %+v", sym)
	}

	if sym.Name != "Name" || sym.Parent == nil || sym.Parent.Name != "User" {
		t.Errorf("resolved symbol is not the User.Name field: %+v", sym)
	}

	// A bare field name must NOT resolve; it should error and suggest the
	// Type.Field form.
	bare, err := resolver.ResolveSymbol(pkg, "Name")
	if err == nil {
		t.Fatalf("expected bare %q not to resolve to a field, got %+v", "Name", bare)
	}

	if bare != nil {
		t.Errorf("expected nil symbol for bare field name, got %+v", bare)
	}
}

// symbolTableOf parses name->source files into one package (files named
// *_test.go go to TestFiles) and builds its symbol table.
func symbolTableOf(t *testing.T, files map[string]string) *types.SymbolTable {
	t.Helper()

	fset := token.NewFileSet()
	pkg := &types.Package{
		Name:      "x",
		Path:      "x",
		Files:     map[string]*types.File{},
		TestFiles: map[string]*types.File{},
	}

	for name, src := range files {
		astFile, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		f := &types.File{Path: name, AST: astFile, Package: pkg}
		if strings.HasSuffix(name, "_test.go") {
			pkg.TestFiles[name] = f
		} else {
			pkg.Files[name] = f
		}
	}

	ws := &types.Workspace{Packages: map[string]*types.Package{"x": pkg}, FileSet: fset}
	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	st := resolver.BuildSymbolTable(pkg)

	return st
}

func requireSymbolAt(t *testing.T, kind string, table map[string]*types.Symbol, name, file string, line int) {
	t.Helper()

	sym := table[name]
	if sym == nil {
		t.Fatalf("%s %s missing", kind, name)
	}

	if sym.File != file || sym.Line != line {
		t.Errorf("%s %s at %s:%d, want %s:%d", kind, name, sym.File, sym.Line, file, line)
	}
}

const (
	pkgLevelSrc = "package x\n\nconst Limit = 1\n\nvar Counter string\n\ntype T int\n"
	localsSrc   = "package x\n\nfunc F() {\n\tconst Limit = 2\n\tvar Counter int\n\ttype T struct{}\n\tconst OnlyLocal = 3\n\tvar localVar int\n\ttype localType struct{}\n}\n"
)

func TestBuildSymbolTable_FunctionLocalDeclarationsDoNotShadowPackageLevel(t *testing.T) {
	for range 20 {
		st := symbolTableOf(t, map[string]string{"a.go": pkgLevelSrc, "b.go": localsSrc})
		requireSymbolAt(t, "const", st.Constants, "Limit", "a.go", 3)
		requireSymbolAt(t, "var", st.Variables, "Counter", "a.go", 5)
		requireSymbolAt(t, "type", st.Types, "T", "a.go", 7)
	}
}

func TestBuildSymbolTable_FunctionLocalNamesAreAbsent(t *testing.T) {
	st := symbolTableOf(t, map[string]string{"a.go": pkgLevelSrc, "b.go": localsSrc})

	_, ok := st.Constants["OnlyLocal"]
	if ok {
		t.Error("local const OnlyLocal in Constants")
	}

	_, ok = st.Variables["localVar"]
	if ok {
		t.Error("local var localVar in Variables")
	}

	_, ok = st.Types["localType"]
	if ok {
		t.Error("local type localType in Types")
	}
}

func TestBuildSymbolTable_TestFilesDoNotReplaceProductionSymbols(t *testing.T) {
	prod := "package x\n\nfunc Helper() {}\n\nconst Name = 1\n\nvar Shared int\n"
	inPkgTest := "package x\n\nfunc Helper() {}\n\nconst Name = 2\n\nvar Shared string\n"

	extTest := "package x_test\n\nfunc Helper() {}\n\nconst Name = 3\n\nvar Shared bool\n"
	for range 20 {
		st := symbolTableOf(t, map[string]string{"x.go": prod, "x_test.go": inPkgTest, "ext_test.go": extTest})
		requireSymbolAt(t, "func", st.Functions, "Helper", "x.go", 3)
		requireSymbolAt(t, "const", st.Constants, "Name", "x.go", 5)
		requireSymbolAt(t, "var", st.Variables, "Shared", "x.go", 7)
	}
}

func TestBuildSymbolTable_EachInterfaceIsRecordedOnce(t *testing.T) {
	src := "package x\n\ntype I interface {\n\tM()\n}\n\ntype S struct {\n\tA int\n}\n"
	st := symbolTableOf(t, map[string]string{"a.go": src})

	n := len(st.Methods["I"])
	if n != 1 {
		t.Errorf("interface I has %d method symbols, want 1", n)
	}

	n = len(st.Fields["S"])
	if n != 1 {
		t.Errorf("struct S has %d field symbols, want 1", n)
	}

	if st.Methods["I"][0].Parent != st.Types["I"] {
		t.Error("interface method Parent is not the retained type symbol")
	}

	if st.Fields["S"][0].Parent != st.Types["S"] {
		t.Error("field Parent is not the retained type symbol")
	}
}

// hasUse reports whether the usage graph of ws records a reference to symbol.
func hasUse(ws *types.Workspace, resolver *SymbolResolver, idx *ReferenceIndex, symbol *types.Symbol) bool {
	n := buildUsageGraph(ws, resolver, idx).nodes[symbol.Position]
	return n != nil && n.prodRefs+n.testRefs > 0
}
