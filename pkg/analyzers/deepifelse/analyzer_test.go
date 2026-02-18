package deepifelse_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/deepifelse"
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

func TestDeepIfElse_DeepNestingViolation(t *testing.T) {
	src := `package testpkg

import "fmt"

func foo(x, y bool) error {
	if x {
		if y {
			return nil
		} else {
			return fmt.Errorf("y failed")
		}
	} else {
		return fmt.Errorf("x failed")
	}
}
`
	ws := createTestWorkspace(t, src)

	// Use a custom analyzer with lower thresholds to ensure detection.
	a := deepifelse.NewAnalyzer(
		deepifelse.WithMaxNesting(1),
		deepifelse.WithMinElseLines(1),
	)
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*deepifelse.Result)
	if !ok {
		t.Fatalf("Expected []*deepifelse.Result, got %T", rr.Result)
	}
	if len(results) == 0 {
		t.Fatal("Expected at least 1 violation for deeply nested if-else, got 0")
	}

	v := results[0]
	if v.Function != "foo" {
		t.Errorf("Expected function 'foo', got %q", v.Function)
	}
	if v.NestingDepth < 2 {
		t.Errorf("Expected NestingDepth >= 2, got %d", v.NestingDepth)
	}
}

func TestDeepIfElse_ShallowNoViolation(t *testing.T) {
	src := `package testpkg

import "fmt"

func foo(x bool) error {
	if x {
		return nil
	}
	return fmt.Errorf("failed")
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, deepifelse.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*deepifelse.Result)
	if !ok {
		t.Fatalf("Expected []*deepifelse.Result, got %T", rr.Result)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 violations for shallow if (no else), got %d", len(results))
	}
}

func TestDeepIfElse_DepthAndErrorBranchesReported(t *testing.T) {
	src := `package testpkg

import "fmt"

func process(a, b, c bool) error {
	if a {
		if b {
			if c {
				return nil
			} else {
				return fmt.Errorf("c failed")
			}
		} else {
			return fmt.Errorf("b failed")
		}
	} else {
		return fmt.Errorf("a failed")
	}
}
`
	ws := createTestWorkspace(t, src)

	a := deepifelse.NewAnalyzer(
		deepifelse.WithMaxNesting(1),
		deepifelse.WithMinElseLines(1),
	)
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*deepifelse.Result)
	if !ok {
		t.Fatalf("Expected []*deepifelse.Result, got %T", rr.Result)
	}
	if len(results) == 0 {
		t.Fatal("Expected at least 1 violation")
	}

	v := results[0]
	if v.NestingDepth < 3 {
		t.Errorf("Expected NestingDepth >= 3, got %d", v.NestingDepth)
	}
	if v.ComplexityReductionPercent <= 0 {
		t.Errorf("Expected ComplexityReductionPercent > 0, got %d", v.ComplexityReductionPercent)
	}
}
