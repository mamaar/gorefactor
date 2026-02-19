package envbool_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/envbool"
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

func TestEnvBool_Violation(t *testing.T) {
	src := `package testpkg

func serve(isProd bool) {
	doStuff(isProd)
}

func doStuff(isProd bool) {}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, envbool.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*envbool.Result)
	if !ok {
		t.Fatalf("Expected []*envbool.Result, got %T", rr.Result)
	}

	// serve propagates isProd → doStuff, depth=1 which meets the default maxDepth=1 threshold.
	// doStuff also has isProd but does not propagate it further (depth=0), so only serve is flagged.
	found := false
	for _, v := range results {
		if v.Function == "serve" && v.ParameterName == "isProd" {
			found = true
			if v.PropagationDepth < 1 {
				t.Errorf("Expected PropagationDepth >= 1 for serve, got %d", v.PropagationDepth)
			}
		}
	}
	if !found {
		t.Errorf("Expected a violation for function 'serve' with parameter 'isProd', got results: %v", results)
	}
}

func TestEnvBool_NoViolationRegularBoolParam(t *testing.T) {
	src := `package testpkg

func foo(enabled bool) {
	_ = enabled
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, envbool.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*envbool.Result)
	if !ok {
		t.Fatalf("Expected []*envbool.Result, got %T", rr.Result)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 violations for non-env bool param 'enabled', got %d", len(results))
	}
}

func TestEnvBool_IsTestPropagation(t *testing.T) {
	src := `package testpkg

func run(isTest bool) {
	setup(isTest)
	execute(isTest)
}

func setup(isTest bool) {}
func execute(isTest bool) {}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, envbool.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*envbool.Result)
	if !ok {
		t.Fatalf("Expected []*envbool.Result, got %T", rr.Result)
	}

	found := false
	for _, v := range results {
		if v.Function == "run" && v.ParameterName == "isTest" {
			found = true
			if v.PropagationDepth < 1 {
				t.Errorf("Expected PropagationDepth >= 1, got %d", v.PropagationDepth)
			}
		}
	}
	if !found {
		t.Errorf("Expected a violation for function 'run' with parameter 'isTest'")
	}
}
