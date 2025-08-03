package refactor

import (
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Test cases for InlineOperation when it gets implemented

func TestInlineMethodOperation_Type(t *testing.T) {
	op := &InlineMethodOperation{
		MethodName:   "inlineMe",
		SourceStruct: "TestStruct",
		TargetFile:   "test.go",
	}

	if op.Type() != types.InlineOperation {
		t.Errorf("Expected InlineOperation, got %v", op.Type())
	}
}

func TestInlineMethodOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *InlineMethodOperation
		wantErr bool
	}{
		{
			name: "valid inline method operation",
			op: &InlineMethodOperation{
				MethodName:   "inlineMe",
				SourceStruct: "TestStruct",
				TargetFile:   "test.go",
			},
			wantErr: false,
		},
		{
			name: "invalid - empty method name",
			op: &InlineMethodOperation{
				MethodName:   "",
				SourceStruct: "TestStruct",
				TargetFile:   "test.go",
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source struct",
			op: &InlineMethodOperation{
				MethodName:   "inlineMe",
				SourceStruct: "",
				TargetFile:   "test.go",
			},
			wantErr: true,
		},
		{
			name: "invalid - empty target file",
			op: &InlineMethodOperation{
				MethodName:   "inlineMe",
				SourceStruct: "TestStruct",
				TargetFile:   "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForInline()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("InlineMethodOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInlineMethodOperation_Description(t *testing.T) {
	op := &InlineMethodOperation{
		MethodName:   "inlineMe",
		SourceStruct: "TestStruct",
		TargetFile:   "test.go",
	}

	desc := op.Description()
	expectedDesc := "Inline method 'inlineMe' from TestStruct into test.go"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

func TestInlineVariableOperation_Type(t *testing.T) {
	op := &InlineVariableOperation{
		VariableName: "tempVar",
		SourceFile:   "test.go",
		StartLine:    10,
		EndLine:      15,
	}

	if op.Type() != types.InlineOperation {
		t.Errorf("Expected InlineOperation, got %v", op.Type())
	}
}

func TestInlineVariableOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *InlineVariableOperation
		wantErr bool
	}{
		{
			name: "valid inline variable operation",
			op: &InlineVariableOperation{
				VariableName: "tempVar",
				SourceFile:   "test.go",
				StartLine:    10,
				EndLine:      15,
			},
			wantErr: false,
		},
		{
			name: "invalid - empty variable name",
			op: &InlineVariableOperation{
				VariableName: "",
				SourceFile:   "test.go",
				StartLine:    10,
				EndLine:      15,
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &InlineVariableOperation{
				VariableName: "tempVar",
				SourceFile:   "",
				StartLine:    10,
				EndLine:      15,
			},
			wantErr: true,
		},
		{
			name: "invalid - start line after end line",
			op: &InlineVariableOperation{
				VariableName: "tempVar",
				SourceFile:   "test.go",
				StartLine:    15,
				EndLine:      10,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForInline()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("InlineVariableOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInlineVariableOperation_Description(t *testing.T) {
	op := &InlineVariableOperation{
		VariableName: "tempVar",
		SourceFile:   "test.go",
		StartLine:    10,
		EndLine:      15,
	}

	desc := op.Description()
	expectedDesc := "Inline variable 'tempVar' from lines 10-15 in test.go"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

func TestInlineFunctionOperation_Type(t *testing.T) {
	op := &InlineFunctionOperation{
		FunctionName: "utilityFunc",
		SourceFile:   "utils.go",
		TargetFiles:  []string{"main.go", "service.go"},
	}

	if op.Type() != types.InlineOperation {
		t.Errorf("Expected InlineOperation, got %v", op.Type())
	}
}

func TestInlineFunctionOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *InlineFunctionOperation
		wantErr bool
	}{
		{
			name: "valid inline function operation",
			op: &InlineFunctionOperation{
				FunctionName: "utilityFunc",
				SourceFile:   "utils.go",
				TargetFiles:  []string{"main.go", "service.go"},
			},
			wantErr: false,
		},
		{
			name: "invalid - empty function name",
			op: &InlineFunctionOperation{
				FunctionName: "",
				SourceFile:   "utils.go",
				TargetFiles:  []string{"main.go", "service.go"},
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &InlineFunctionOperation{
				FunctionName: "utilityFunc",
				SourceFile:   "",
				TargetFiles:  []string{"main.go", "service.go"},
			},
			wantErr: true,
		},
		{
			name: "invalid - empty target files",
			op: &InlineFunctionOperation{
				FunctionName: "utilityFunc",
				SourceFile:   "utils.go",
				TargetFiles:  []string{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForInline()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("InlineFunctionOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInlineFunctionOperation_Description(t *testing.T) {
	op := &InlineFunctionOperation{
		FunctionName: "utilityFunc",
		SourceFile:   "utils.go",
		TargetFiles:  []string{"main.go", "service.go"},
	}

	desc := op.Description()
	expectedDesc := "Inline function 'utilityFunc' from utils.go into [main.go service.go]"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

func TestInlineConstantOperation_Type(t *testing.T) {
	op := &InlineConstantOperation{
		ConstantName: "MaxRetries",
		SourceFile:   "constants.go",
		Scope:        types.WorkspaceScope,
	}

	if op.Type() != types.InlineOperation {
		t.Errorf("Expected InlineOperation, got %v", op.Type())
	}
}

func TestInlineConstantOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *InlineConstantOperation
		wantErr bool
	}{
		{
			name: "valid inline constant operation",
			op: &InlineConstantOperation{
				ConstantName: "MaxRetries",
				SourceFile:   "constants.go",
				Scope:        types.WorkspaceScope,
			},
			wantErr: false,
		},
		{
			name: "invalid - empty constant name",
			op: &InlineConstantOperation{
				ConstantName: "",
				SourceFile:   "constants.go",
				Scope:        types.WorkspaceScope,
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &InlineConstantOperation{
				ConstantName: "MaxRetries",
				SourceFile:   "",
				Scope:        types.WorkspaceScope,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForInline()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("InlineConstantOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInlineConstantOperation_Description(t *testing.T) {
	op := &InlineConstantOperation{
		ConstantName: "MaxRetries",
		SourceFile:   "constants.go",
		Scope:        types.WorkspaceScope,
	}

	desc := op.Description()
	expectedDesc := "Inline constant 'MaxRetries' from constants.go with WorkspaceScope scope"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

// Note: Inline operation implementations are now in inline_operations.go

// Helper function for testing
func createTestWorkspaceForInline() *types.Workspace {
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

func (t *TestStruct) inlineMe() {
	// Method to be inlined
	fmt.Println("inline me")
}
`),
	}

	// Create a test symbol for TestStruct
	testStructSymbol := &types.Symbol{
		Name:     "TestStruct",
		Kind:     types.TypeSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 100,
		End:      200,
		Line:     13,
		Column:   6,
		Exported: true,
	}

	// Create method symbol for inlineMe
	inlineMeSymbol := &types.Symbol{
		Name:     "inlineMe",
		Kind:     types.MethodSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 250,
		End:      300,
		Line:     17,
		Column:   6,
		Exported: false, // lowercase method
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
			"TestStruct": {inlineMeSymbol},
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