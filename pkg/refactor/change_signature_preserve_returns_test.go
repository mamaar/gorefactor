package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestExtractReturnTypes_SingleReturn(t *testing.T) {
	src := `package main

type save interface {
	SaveTile(key *string, data []byte) error
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	op := &ChangeSignatureOperation{
		FunctionName: "save.SaveTile",
		SourceFile:   "test.go",
	}

	file := &types.File{
		Path: "test.go",
		AST:  astFile,
	}

	returns := op.extractInterfaceMethodReturns(file, "save", "SaveTile")
	if len(returns) != 1 {
		t.Fatalf("expected 1 return type, got %d", len(returns))
	}
	if returns[0] != "error" {
		t.Errorf("expected return type 'error', got %q", returns[0])
	}
}

func TestExtractReturnTypes_MultipleReturns(t *testing.T) {
	src := `package main

type Repo interface {
	Get(id string) (string, error)
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	op := &ChangeSignatureOperation{}

	file := &types.File{
		Path: "test.go",
		AST:  astFile,
	}

	returns := op.extractInterfaceMethodReturns(file, "Repo", "Get")
	if len(returns) != 2 {
		t.Fatalf("expected 2 return types, got %d", len(returns))
	}
	if returns[0] != "string" {
		t.Errorf("expected return type 'string', got %q", returns[0])
	}
	if returns[1] != "error" {
		t.Errorf("expected return type 'error', got %q", returns[1])
	}
}

func TestExtractReturnTypes_NoReturns(t *testing.T) {
	src := `package main

type Writer interface {
	Write(data []byte)
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	op := &ChangeSignatureOperation{}

	file := &types.File{
		Path: "test.go",
		AST:  astFile,
	}

	returns := op.extractInterfaceMethodReturns(file, "Writer", "Write")
	if len(returns) != 0 {
		t.Fatalf("expected 0 return types, got %d", len(returns))
	}
}

func TestPreserveExistingReturns_InterfaceMethod(t *testing.T) {
	src := `package main

type save interface {
	SaveTile(key *string, data []byte) error
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path: "test.go",
		AST:  astFile,
	}

	op := &ChangeSignatureOperation{
		FunctionName: "save.SaveTile",
		SourceFile:   "test.go",
		NewParams: []Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "key", Type: "*string"},
			{Name: "data", Type: "[]byte"},
		},
		NewReturns: nil, // Not provided by caller
	}

	err = op.preserveExistingReturnsIfNeeded(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(op.NewReturns) != 1 {
		t.Fatalf("expected 1 return type preserved, got %d", len(op.NewReturns))
	}
	if op.NewReturns[0] != "error" {
		t.Errorf("expected preserved return type 'error', got %q", op.NewReturns[0])
	}
}

func TestPreserveExistingReturns_ConcreteMethod(t *testing.T) {
	src := `package main

type Storage struct{}

func (s *Storage) SaveTile(key *string, data []byte) error {
	return nil
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path: "test.go",
		AST:  astFile,
	}

	op := &ChangeSignatureOperation{
		FunctionName: "Storage.SaveTile",
		SourceFile:   "test.go",
		NewParams: []Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "key", Type: "*string"},
			{Name: "data", Type: "[]byte"},
		},
		NewReturns: nil, // Not provided by caller
	}

	err = op.preserveExistingReturnsIfNeeded(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(op.NewReturns) != 1 {
		t.Fatalf("expected 1 return type preserved, got %d", len(op.NewReturns))
	}
	if op.NewReturns[0] != "error" {
		t.Errorf("expected preserved return type 'error', got %q", op.NewReturns[0])
	}
}

func TestPreserveExistingReturns_ExplicitReturnsNotOverwritten(t *testing.T) {
	src := `package main

type save interface {
	SaveTile(key *string, data []byte) error
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path: "test.go",
		AST:  astFile,
	}

	op := &ChangeSignatureOperation{
		FunctionName: "save.SaveTile",
		SourceFile:   "test.go",
		NewReturns:   []string{"string", "error"}, // Explicitly provided
	}

	err = op.preserveExistingReturnsIfNeeded(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should NOT overwrite explicitly provided returns
	if len(op.NewReturns) != 2 {
		t.Fatalf("expected 2 return types (unchanged), got %d", len(op.NewReturns))
	}
	if op.NewReturns[0] != "string" || op.NewReturns[1] != "error" {
		t.Errorf("explicit returns were overwritten: %v", op.NewReturns)
	}
}

func TestGenerateInterfaceMethodSignature_PreservedReturns(t *testing.T) {
	op := &ChangeSignatureOperation{
		NewParams: []Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "key", Type: "*string"},
			{Name: "data", Type: "[]byte"},
		},
		NewReturns: []string{"error"},
	}

	sig := generateInterfaceMethodSignature(op, "SaveTile")
	expected := "SaveTile(ctx context.Context, key *string, data []byte) error"
	if sig != expected {
		t.Errorf("expected %q, got %q", expected, sig)
	}
}

func TestGenerateInterfaceMethodSignature_MultipleReturns(t *testing.T) {
	op := &ChangeSignatureOperation{
		NewParams: []Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "id", Type: "string"},
		},
		NewReturns: []string{"string", "error"},
	}

	sig := generateInterfaceMethodSignature(op, "Get")
	expected := "Get(ctx context.Context, id string) (string, error)"
	if sig != expected {
		t.Errorf("expected %q, got %q", expected, sig)
	}
}

// --- Tests for return-type change engine features ---

func TestCountFieldListEntries(t *testing.T) {
	src := `package main

func foo() (string, error) { return "", nil }
func bar() int { return 0 }
func baz() {}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	tests := []struct {
		funcName string
		expected int
	}{
		{"foo", 2},
		{"bar", 1},
		{"baz", 0},
	}

	for _, tc := range tests {
		var funcDecl *ast.FuncDecl
		ast.Inspect(astFile, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == tc.funcName {
				funcDecl = fd
				return false
			}
			return true
		})
		if funcDecl == nil {
			t.Fatalf("function %s not found", tc.funcName)
		}
		got := countFieldListEntries(funcDecl.Type.Results)
		if got != tc.expected {
			t.Errorf("countFieldListEntries(%s) = %d, want %d", tc.funcName, got, tc.expected)
		}
	}
}

func TestCountFieldListEntries_Nil(t *testing.T) {
	if got := countFieldListEntries(nil); got != 0 {
		t.Errorf("countFieldListEntries(nil) = %d, want 0", got)
	}
}

func TestWalkBodyForReturnStmts_SkipsClosures(t *testing.T) {
	src := `package main

func foo() (int, error) {
	f := func() error {
		return nil
	}
	_ = f
	return 0, nil
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var funcDecl *ast.FuncDecl
	ast.Inspect(astFile, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == "foo" {
			funcDecl = fd
			return false
		}
		return true
	})
	if funcDecl == nil {
		t.Fatal("function foo not found")
	}

	var returnCount int
	walkBodyForReturnStmts(funcDecl.Body, func(retStmt *ast.ReturnStmt) {
		returnCount++
	})

	// Should find 1 return (the outer one), not 2 (the closure's return should be skipped)
	if returnCount != 1 {
		t.Errorf("expected 1 return statement (skipping closure), got %d", returnCount)
	}
}

func TestSplitReturnValueTexts(t *testing.T) {
	src := `package main

func foo() (string, error) {
	return "hello", nil
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ws := &types.Workspace{FileSet: fset}

	var retStmt *ast.ReturnStmt
	ast.Inspect(astFile, func(n ast.Node) bool {
		if rs, ok := n.(*ast.ReturnStmt); ok {
			retStmt = rs
			return false
		}
		return true
	})
	if retStmt == nil {
		t.Fatal("return statement not found")
	}

	content := []byte(src)
	values := splitReturnValueTexts(retStmt, content, ws)
	if len(values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(values))
	}
	if values[0] != `"hello"` {
		t.Errorf("expected first value %q, got %q", `"hello"`, values[0])
	}
	if values[1] != "nil" {
		t.Errorf("expected second value %q, got %q", "nil", values[1])
	}
}

func TestResolveSourceFile_Interface(t *testing.T) {
	src := `package main

type Store interface {
	Get(id string) (string, error)
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "store.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path: "/test/store.go",
		AST:  astFile,
	}

	ws := &types.Workspace{
		FileSet: fset,
		Packages: map[string]*types.Package{
			"main": {
				Path: "main",
				Files: map[string]*types.File{
					"store.go": file,
				},
				TestFiles: map[string]*types.File{},
			},
		},
	}

	op := &ChangeSignatureOperation{
		FunctionName: "Store.Get",
		SourceFile:   "",
	}

	err = op.resolveSourceFile(ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.SourceFile != "/test/store.go" {
		t.Errorf("expected source file '/test/store.go', got %q", op.SourceFile)
	}
}

func TestResolveSourceFile_ConcreteMethod(t *testing.T) {
	src := `package main

type MyStore struct{}

func (s *MyStore) Get(id string) (string, error) {
	return "", nil
}
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "mystore.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	file := &types.File{
		Path: "/test/mystore.go",
		AST:  astFile,
	}

	ws := &types.Workspace{
		FileSet: fset,
		Packages: map[string]*types.Package{
			"main": {
				Path: "main",
				Files: map[string]*types.File{
					"mystore.go": file,
				},
				TestFiles: map[string]*types.File{},
			},
		},
	}

	op := &ChangeSignatureOperation{
		FunctionName: "MyStore.Get",
		SourceFile:   "",
	}

	err = op.resolveSourceFile(ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.SourceFile != "/test/mystore.go" {
		t.Errorf("expected source file '/test/mystore.go', got %q", op.SourceFile)
	}
}

func TestResolveSourceFile_NotFound(t *testing.T) {
	ws := &types.Workspace{
		FileSet:  token.NewFileSet(),
		Packages: map[string]*types.Package{},
	}

	op := &ChangeSignatureOperation{
		FunctionName: "Missing.Method",
		SourceFile:   "",
	}

	err := op.resolveSourceFile(ws)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "could not find type") {
		t.Errorf("expected 'could not find type' error, got: %v", err)
	}
}

func TestResolveSourceFile_PrefersNonTestFile(t *testing.T) {
	src := `package main

type Store interface {
	Get(id string) error
}
`
	fset := token.NewFileSet()

	// Parse twice with different file names
	prodAST, _ := parser.ParseFile(fset, "store.go", src, parser.ParseComments)
	testAST, _ := parser.ParseFile(fset, "store_test.go", src, parser.ParseComments)

	prodFile := &types.File{Path: "/test/store.go", AST: prodAST}
	testFile := &types.File{Path: "/test/store_test.go", AST: testAST}

	ws := &types.Workspace{
		FileSet: fset,
		Packages: map[string]*types.Package{
			"main": {
				Path: "main",
				Files: map[string]*types.File{
					"store.go": prodFile,
				},
				TestFiles: map[string]*types.File{
					"store_test.go": testFile,
				},
			},
		},
	}

	op := &ChangeSignatureOperation{
		FunctionName: "Store.Get",
		SourceFile:   "",
	}

	err := op.resolveSourceFile(ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.SourceFile != "/test/store.go" {
		t.Errorf("expected non-test file '/test/store.go', got %q", op.SourceFile)
	}
}
