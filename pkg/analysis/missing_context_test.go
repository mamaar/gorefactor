package analysis

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func createMissingContextTestWorkspace(t *testing.T, src string) *types.Workspace {
	t.Helper()
	fileSet := token.NewFileSet()

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

	return &types.Workspace{
		Packages: map[string]*types.Package{"test/testpkg": pkg},
		FileSet:  fileSet,
	}
}

func TestMissingContext_TODO_NoParam(t *testing.T) {
	src := `package testpkg

import "context"

func doWork() {
	ctx := context.TODO()
	_ = ctx
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	v := violations[0]
	if v.FunctionName != "doWork" {
		t.Errorf("Expected function 'doWork', got %q", v.FunctionName)
	}
	if len(v.ContextCalls) != 1 || v.ContextCalls[0] != "context.TODO()" {
		t.Errorf("Expected [context.TODO()], got %v", v.ContextCalls)
	}
}

func TestMissingContext_Background_NoParam(t *testing.T) {
	src := `package testpkg

import "context"

func startServer() {
	ctx := context.Background()
	_ = ctx
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if violations[0].ContextCalls[0] != "context.Background()" {
		t.Errorf("Expected context.Background(), got %v", violations[0].ContextCalls)
	}
}

func TestMissingContext_AlreadyHasParam_NoViolation(t *testing.T) {
	src := `package testpkg

import "context"

func doWork(ctx context.Context) {
	_ = ctx
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations, got %d", len(violations))
	}
}

func TestMissingContext_NoContextCalls_NoViolation(t *testing.T) {
	src := `package testpkg

func doWork() {
	x := 1
	_ = x
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations, got %d", len(violations))
	}
}

func TestMissingContext_MainAndInit_Skipped(t *testing.T) {
	src := `package testpkg

import "context"

func main() {
	ctx := context.Background()
	_ = ctx
}

func init() {
	ctx := context.TODO()
	_ = ctx
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for main/init, got %d", len(violations))
	}
}

func TestMissingContext_MethodWithReceiver(t *testing.T) {
	src := `package testpkg

import "context"

type Server struct{}

func (s *Server) Handle() {
	ctx := context.TODO()
	_ = ctx
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if violations[0].FunctionName != "Handle" {
		t.Errorf("Expected function 'Handle', got %q", violations[0].FunctionName)
	}
}

func TestMissingContext_MultipleCalls(t *testing.T) {
	src := `package testpkg

import "context"

func doWork() {
	ctx1 := context.TODO()
	ctx2 := context.Background()
	_ = ctx1
	_ = ctx2
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if len(violations[0].ContextCalls) != 2 {
		t.Errorf("Expected 2 context calls, got %d: %v", len(violations[0].ContextCalls), violations[0].ContextCalls)
	}
}

func TestMissingContext_PackageFiltering(t *testing.T) {
	fileSet := token.NewFileSet()

	src1 := `package pkg1

import "context"

func doWork() {
	ctx := context.TODO()
	_ = ctx
}
`
	src2 := `package pkg2

func clean() {}
`
	ast1, _ := parser.ParseFile(fileSet, "pkg1.go", src1, parser.ParseComments)
	ast2, _ := parser.ParseFile(fileSet, "pkg2.go", src2, parser.ParseComments)

	file1 := &types.File{Path: "pkg1.go", AST: ast1, OriginalContent: []byte(src1)}
	file2 := &types.File{Path: "pkg2.go", AST: ast2, OriginalContent: []byte(src2)}

	pkg1 := &types.Package{Name: "pkg1", Path: "test/pkg1", Files: map[string]*types.File{"pkg1.go": file1}}
	pkg2 := &types.Package{Name: "pkg2", Path: "test/pkg2", Files: map[string]*types.File{"pkg2.go": file2}}
	file1.Package = pkg1
	file2.Package = pkg2

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/pkg1": pkg1, "test/pkg2": pkg2},
		FileSet:  fileSet,
	}

	analyzer := NewMissingContextAnalyzer(ws)

	v1 := analyzer.AnalyzePackage(pkg1)
	if len(v1) != 1 {
		t.Errorf("Expected 1 violation in pkg1, got %d", len(v1))
	}

	v2 := analyzer.AnalyzePackage(pkg2)
	if len(v2) != 0 {
		t.Errorf("Expected 0 violations in pkg2, got %d", len(v2))
	}
}

func TestMissingContext_SignatureExtracted(t *testing.T) {
	src := `package testpkg

import "context"

func doWork(name string) error {
	ctx := context.TODO()
	_ = ctx
	return nil
}
`
	ws := createMissingContextTestWorkspace(t, src)
	analyzer := NewMissingContextAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if violations[0].Signature != "func doWork(name string) error" {
		t.Errorf("Expected signature 'func doWork(name string) error', got %q", violations[0].Signature)
	}
}
