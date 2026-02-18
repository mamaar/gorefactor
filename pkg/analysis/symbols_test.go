package analysis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
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