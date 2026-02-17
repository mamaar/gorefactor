package refactor

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestExtractConstantOperation_Type(t *testing.T) {
	op := &ExtractConstantOperation{}
	if op.Type() != types.ExtractOperation {
		t.Errorf("Expected ExtractOperation, got %v", op.Type())
	}
}

func TestExtractConstantOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *ExtractConstantOperation
		wantErr bool
	}{
		{
			name: "valid extract constant operation",
			op: &ExtractConstantOperation{
				SourceFile:   "test.go",
				Position:     token.Pos(103),
				ConstantName: "MyConstant",
				Scope:        types.PackageScope,
			},
			wantErr: false,
		},
		{
			name: "invalid - empty constant name",
			op: &ExtractConstantOperation{
				SourceFile:   "test.go",
				Position:     token.Pos(100),
				ConstantName: "",
				Scope:        types.PackageScope,
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &ExtractConstantOperation{
				SourceFile:   "",
				Position:     token.Pos(100),
				ConstantName: "MyConstant",
				Scope:        types.PackageScope,
			},
			wantErr: true,
		},
		{
			name: "invalid - no position",
			op: &ExtractConstantOperation{
				SourceFile:   "test.go",
				Position:     token.NoPos,
				ConstantName: "MyConstant",
				Scope:        types.PackageScope,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForNewOps()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractConstantOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractConstantOperation_Description(t *testing.T) {
	op := &ExtractConstantOperation{
		SourceFile:   "test.go",
		ConstantName: "MyConstant",
		Scope:        types.PackageScope,
	}

	desc := op.Description()
	expected := "Extract constant MyConstant from test.go (scope: package)"
	if desc != expected {
		t.Errorf("Expected description %q, got %q", expected, desc)
	}
}

func TestChangeSignatureOperation_Type(t *testing.T) {
	op := &ChangeSignatureOperation{}
	if op.Type() != types.ExtractOperation {
		t.Errorf("Expected ExtractOperation, got %v", op.Type())
	}
}

func TestChangeSignatureOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *ChangeSignatureOperation
		wantErr bool
	}{
		{
			name: "valid change signature operation",
			op: &ChangeSignatureOperation{
				FunctionName: "TestFunction",
				SourceFile:   "test.go",
				NewParams: []Parameter{
					{Name: "param1", Type: "string"},
					{Name: "param2", Type: "int"},
				},
				NewReturns: []string{"error"},
				Scope:      types.PackageScope,
			},
			wantErr: false,
		},
		{
			name: "invalid - empty function name",
			op: &ChangeSignatureOperation{
				FunctionName: "",
				SourceFile:   "test.go",
				NewParams:    []Parameter{},
				Scope:        types.PackageScope,
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &ChangeSignatureOperation{
				FunctionName: "TestFunction",
				SourceFile:   "",
				NewParams:    []Parameter{},
				Scope:        types.PackageScope,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForNewOps()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("ChangeSignatureOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChangeSignatureOperation_Description(t *testing.T) {
	op := &ChangeSignatureOperation{
		FunctionName: "TestFunction",
		Scope:        types.WorkspaceScope,
	}

	desc := op.Description()
	expected := "Change signature of function TestFunction (scope: workspace)"
	if desc != expected {
		t.Errorf("Expected description %q, got %q", expected, desc)
	}
}

func TestSafeDeleteOperation_Type(t *testing.T) {
	op := &SafeDeleteOperation{}
	if op.Type() != types.InlineOperation {
		t.Errorf("Expected InlineOperation, got %v", op.Type())
	}
}

func TestSafeDeleteOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *SafeDeleteOperation
		wantErr bool
	}{
		{
			name: "invalid - empty symbol name",
			op: &SafeDeleteOperation{
				SymbolName: "",
				SourceFile: "test.go",
				Scope:      types.PackageScope,
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &SafeDeleteOperation{
				SymbolName: "TestSymbol",
				SourceFile: "",
				Scope:      types.PackageScope,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForNewOps()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("SafeDeleteOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSafeDeleteOperation_Description(t *testing.T) {
	op := &SafeDeleteOperation{
		SymbolName: "TestSymbol",
		SourceFile: "test.go",
		Scope:      types.WorkspaceScope,
		Force:      true,
	}

	desc := op.Description()
	expected := "Safe delete TestSymbol from test.go (scope: workspace) (forced)"
	if desc != expected {
		t.Errorf("Expected description %q, got %q", expected, desc)
	}
}

// Test helper functions

func TestExtractConstantOperation_isExtractableLiteral(t *testing.T) {
	op := &ExtractConstantOperation{}
	
	tests := []struct {
		name     string
		literal  string
		expected bool
	}{
		// We can't easily test this without creating actual AST nodes
		// In a full implementation, we would create test AST nodes
	}
	
	// This is a placeholder for more comprehensive testing
	_ = tests
	_ = op
}

// Helper function to create test workspace for new operations
func createTestWorkspaceForNewOps() *types.Workspace {
	// Create a test file with more comprehensive content
	testFile := &types.File{
		Path:    "test.go",
		Package: nil, // Will be set after package creation
		OriginalContent: []byte(`package main

import "fmt"

const ExistingConst = 42

func main() {
	fmt.Println("Hello World")
	x := 123
	y := "test string"
	z := true
}

func TestFunction(a int, b string) error {
	fmt.Printf("%d %s\n", a, b)
	return nil
}

type TestStruct struct {
	Name string
}

func (t *TestStruct) TestMethod() {
	fmt.Println(t.Name)
}
`),
	}

	fset := token.NewFileSet()
	parsedAST, _ := parser.ParseFile(fset, "test.go", testFile.OriginalContent, parser.ParseComments)
	testFile.AST = parsedAST

	// Create test symbols
	testFunctionSymbol := &types.Symbol{
		Name:     "TestFunction",
		Kind:     types.FunctionSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 200,
		End:      300,
		Line:     15,
		Column:   6,
		Exported: true,
	}

	testStructSymbol := &types.Symbol{
		Name:     "TestStruct",
		Kind:     types.TypeSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 350,
		End:      400,
		Line:     20,
		Column:   6,
		Exported: true,
	}

	testMethodSymbol := &types.Symbol{
		Name:     "TestMethod",
		Kind:     types.MethodSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 450,
		End:      500,
		Line:     24,
		Column:   6,
		Exported: true,
	}

	// Create symbol table
	symbolTable := &types.SymbolTable{
		Functions: map[string]*types.Symbol{
			"TestFunction": testFunctionSymbol,
		},
		Types: map[string]*types.Symbol{
			"TestStruct": testStructSymbol,
		},
		Variables: make(map[string]*types.Symbol),
		Constants: map[string]*types.Symbol{
			"ExistingConst": {
				Name:     "ExistingConst",
				Kind:     types.ConstantSymbol,
				Package:  "main",
				File:     "test.go",
				Position: 50,
				Exported: true,
			},
		},
		Methods: map[string][]*types.Symbol{
			"TestStruct": {testMethodSymbol},
		},
	}

	// Create a test package
	pkg := &types.Package{
		Path:    "main",
		Name:    "main",
		Dir:     "/test",
		Files: map[string]*types.File{
			"test.go": testFile,
		},
		Symbols: symbolTable,
	}

	// Set the package reference in the file
	testFile.Package = pkg

	return &types.Workspace{
		RootPath: "/test",
		FileSet:  fset,
		Packages: map[string]*types.Package{
			"main": pkg,
		},
	}
}