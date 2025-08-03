package refactor

import (
	"fmt"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Test cases for ExtractOperation when it gets implemented

func TestExtractMethodOperation_Type(t *testing.T) {
	op := &ExtractMethodOperation{
		SourceFile:   "test.go",
		StartLine:    10,
		EndLine:      20,
		NewMethodName: "extractedMethod",
		TargetStruct: "TestStruct",
	}

	if op.Type() != types.ExtractOperation {
		t.Errorf("Expected ExtractOperation, got %v", op.Type())
	}
}

func TestExtractMethodOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *ExtractMethodOperation
		wantErr bool
	}{
		{
			name: "valid extract method operation",
			op: &ExtractMethodOperation{
				SourceFile:    "test.go",
				StartLine:     10,
				EndLine:       20,
				NewMethodName: "extractedMethod",
				TargetStruct:  "TestStruct",
			},
			wantErr: false,
		},
		{
			name: "invalid - empty method name",
			op: &ExtractMethodOperation{
				SourceFile:    "test.go",
				StartLine:     10,
				EndLine:       20,
				NewMethodName: "",
				TargetStruct:  "TestStruct",
			},
			wantErr: true,
		},
		{
			name: "invalid - start line after end line",
			op: &ExtractMethodOperation{
				SourceFile:    "test.go",
				StartLine:     20,
				EndLine:       10,
				NewMethodName: "extractedMethod",
				TargetStruct:  "TestStruct",
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &ExtractMethodOperation{
				SourceFile:    "",
				StartLine:     10,
				EndLine:       20,
				NewMethodName: "extractedMethod",
				TargetStruct:  "TestStruct",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspace()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractMethodOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractMethodOperation_Description(t *testing.T) {
	op := &ExtractMethodOperation{
		SourceFile:    "test.go",
		StartLine:     10,
		EndLine:       20,
		NewMethodName: "extractedMethod",
		TargetStruct:  "TestStruct",
	}

	desc := op.Description()
	expectedDesc := "Extract method 'extractedMethod' from lines 10-20 in test.go to TestStruct"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

func TestExtractInterfaceOperation_Type(t *testing.T) {
	op := &ExtractInterfaceOperation{
		SourceStruct:    "TestStruct",
		InterfaceName:   "TestInterface",
		Methods:         []string{"Method1", "Method2"},
		TargetPackage:   "interfaces",
	}

	if op.Type() != types.ExtractOperation {
		t.Errorf("Expected ExtractOperation, got %v", op.Type())
	}
}

func TestExtractInterfaceOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *ExtractInterfaceOperation
		wantErr bool
	}{
		{
			name: "valid extract interface operation",
			op: &ExtractInterfaceOperation{
				SourceStruct:  "TestStruct",
				InterfaceName: "TestInterface",
				Methods:       []string{"Method1", "Method2"},
				TargetPackage: "interfaces",
			},
			wantErr: false,
		},
		{
			name: "invalid - empty interface name",
			op: &ExtractInterfaceOperation{
				SourceStruct:  "TestStruct",
				InterfaceName: "",
				Methods:       []string{"Method1", "Method2"},
				TargetPackage: "interfaces",
			},
			wantErr: true,
		},
		{
			name: "invalid - empty methods list",
			op: &ExtractInterfaceOperation{
				SourceStruct:  "TestStruct",
				InterfaceName: "TestInterface",
				Methods:       []string{},
				TargetPackage: "interfaces",
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source struct",
			op: &ExtractInterfaceOperation{
				SourceStruct:  "",
				InterfaceName: "TestInterface",
				Methods:       []string{"Method1", "Method2"},
				TargetPackage: "interfaces",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspace()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractInterfaceOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractInterfaceOperation_Description(t *testing.T) {
	op := &ExtractInterfaceOperation{
		SourceStruct:  "TestStruct",
		InterfaceName: "TestInterface",
		Methods:       []string{"Method1", "Method2"},
		TargetPackage: "interfaces",
	}

	desc := op.Description()
	expectedDesc := "Extract interface 'TestInterface' from TestStruct with methods [Method1 Method2] to package interfaces"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

func TestExtractVariableOperation_Type(t *testing.T) {
	op := &ExtractVariableOperation{
		SourceFile:    "test.go",
		StartLine:     15,
		EndLine:       15,
		VariableName:  "extractedVar",
		Expression:    "someComplexExpression()",
	}

	if op.Type() != types.ExtractOperation {
		t.Errorf("Expected ExtractOperation, got %v", op.Type())
	}
}

func TestExtractVariableOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *ExtractVariableOperation
		wantErr bool
	}{
		{
			name: "valid extract variable operation",
			op: &ExtractVariableOperation{
				SourceFile:   "test.go",
				StartLine:    15,
				EndLine:      15,
				VariableName: "extractedVar",
				Expression:   "someComplexExpression()",
			},
			wantErr: false,
		},
		{
			name: "invalid - empty variable name",
			op: &ExtractVariableOperation{
				SourceFile:   "test.go",
				StartLine:    15,
				EndLine:      15,
				VariableName: "",
				Expression:   "someComplexExpression()",
			},
			wantErr: true,
		},
		{
			name: "invalid - empty expression",
			op: &ExtractVariableOperation{
				SourceFile:   "test.go",
				StartLine:    15,
				EndLine:      15,
				VariableName: "extractedVar",
				Expression:   "",
			},
			wantErr: true,
		},
		{
			name: "invalid - start line after end line",
			op: &ExtractVariableOperation{
				SourceFile:   "test.go",
				StartLine:    20,
				EndLine:      15,
				VariableName: "extractedVar",
				Expression:   "someComplexExpression()",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspace()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractVariableOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractVariableOperation_Description(t *testing.T) {
	op := &ExtractVariableOperation{
		SourceFile:   "test.go",
		StartLine:    15,
		EndLine:      15,
		VariableName: "extractedVar",
		Expression:   "someComplexExpression()",
	}

	desc := op.Description()
	expectedDesc := "Extract variable 'extractedVar' from expression 'someComplexExpression()' at line 15 in test.go"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

// Note: Extract operation implementations are now in extract_operations.go

// Helper functions for testing
func createTestWorkspace() *types.Workspace {
	// Create a test file
	testFile := &types.File{
		Path:    "test.go",
		Package: nil, // Will be set after package creation
		AST:     nil,
		OriginalContent: []byte(`package main

func main() {
	// Test content
	x := 1 + 2
	y := x * 3
	fmt.Println(y)
}

type TestStruct struct {
	Name string
}

func (t *TestStruct) SomeMethod() {
	// Method content
}

func (t *TestStruct) Method1() {
	// Method1 content
}

func (t *TestStruct) Method2() string {
	// Method2 content
	return "result"
}
`),
	}

	// Create a test symbol for TestStruct
	testStructSymbol := &types.Symbol{
		Name:     "TestStruct",
		Kind:     types.TypeSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 100, // Approximate position
		End:      200,
		Line:     13,
		Column:   6,
		Exported: true,
	}

	// Create method symbols
	method1Symbol := &types.Symbol{
		Name:     "Method1",
		Kind:     types.MethodSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 311,
		End:      325,
		Line:     24,
		Column:   6,
		Exported: true,
	}

	method2Symbol := &types.Symbol{
		Name:     "Method2",
		Kind:     types.MethodSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 327,
		End:      350,
		Line:     28,
		Column:   6,
		Exported: true,
	}

	// Create symbol table
	symbolTable := &types.SymbolTable{
		Functions: make(map[string]*types.Symbol),
		Types: map[string]*types.Symbol{
			"TestStruct": testStructSymbol,
		},
		Variables: make(map[string]*types.Symbol),
		Constants: make(map[string]*types.Symbol),
		Methods: map[string][]*types.Symbol{
			"TestStruct": {method1Symbol, method2Symbol},
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
		Packages: map[string]*types.Package{
			"main": pkg,
		},
	}
}

func intToString(i int) string {
	return fmt.Sprintf("%d", i)
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}