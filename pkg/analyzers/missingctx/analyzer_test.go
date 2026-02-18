package missingctx_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/missingctx"
	"github.com/mamaar/gorefactor/pkg/types"
)

func createTestWorkspace(t *testing.T, src string) *types.Workspace {
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

func TestMissingCtx_Violation(t *testing.T) {
	src := `package testpkg

import "context"

func process() {
	ctx := context.TODO()
	_ = ctx
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, missingctx.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*missingctx.Result)
	if !ok {
		t.Fatalf("Expected []*missingctx.Result, got %T", rr.Result)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}

	v := results[0]
	if v.FunctionName != "process" {
		t.Errorf("Expected function 'process', got %q", v.FunctionName)
	}
	if len(v.ContextCalls) == 0 {
		t.Error("Expected at least one context call to be reported")
	}
	if v.ContextCalls[0] != "context.TODO()" {
		t.Errorf("Expected context call 'context.TODO()', got %q", v.ContextCalls[0])
	}
}

func TestMissingCtx_NoViolationWithContextParam(t *testing.T) {
	src := `package testpkg

import "context"

func process(ctx context.Context) {
	_ = ctx
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, missingctx.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*missingctx.Result)
	if !ok {
		t.Fatalf("Expected []*missingctx.Result, got %T", rr.Result)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 violations when context param is present, got %d", len(results))
	}
}

func TestMissingCtx_MainAndInitSkipped(t *testing.T) {
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
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, missingctx.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*missingctx.Result)
	if !ok {
		t.Fatalf("Expected []*missingctx.Result, got %T", rr.Result)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 violations: main and init should be skipped, got %d", len(results))
	}
}
