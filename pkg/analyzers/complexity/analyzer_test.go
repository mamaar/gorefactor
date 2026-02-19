package complexity_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/complexity"
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

func TestComplexity_SimpleFunction_BelowThreshold(t *testing.T) {
	src := `package testpkg

func simple() int {
	return 42
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, complexity.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*complexity.Result)
	if !ok {
		t.Fatalf("Expected []*complexity.Result, got %T", rr.Result)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 violations for a simple function, got %d", len(results))
	}
}

func TestComplexity_ComplexFunction_AboveThreshold(t *testing.T) {
	// This function has many branches: 1 base + 10 if/else pairs = cyclomatic >= 10.
	src := `package testpkg

func complex(a, b, c, d, e int) int {
	result := 0
	if a > 0 {
		result++
	}
	if b > 0 {
		result++
	}
	if c > 0 {
		result++
	}
	if d > 0 {
		result++
	}
	if e > 0 {
		result++
	}
	if a > b {
		result++
	}
	if b > c {
		result++
	}
	if c > d {
		result++
	}
	if d > e {
		result++
	}
	if a+b > c+d {
		result++
	}
	return result
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, complexity.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*complexity.Result)
	if !ok {
		t.Fatalf("Expected []*complexity.Result, got %T", rr.Result)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation for complex function, got %d", len(results))
	}

	v := results[0]
	if v.Function != "complex" {
		t.Errorf("Expected function 'complex', got %q", v.Function)
	}
	if v.CyclomaticComplexity < 10 {
		t.Errorf("Expected CyclomaticComplexity >= 10, got %d", v.CyclomaticComplexity)
	}
	if v.Level == "" {
		t.Error("Expected a non-empty Level string")
	}
}

func TestComplexity_CustomThreshold(t *testing.T) {
	// A function with 3 if-statements has cyclomatic complexity of 4 (1 base + 3).
	// With minComplexity=3 it should be reported; with default (10) it should not.
	src := `package testpkg

func medium(a, b, c int) int {
	result := 0
	if a > 0 {
		result++
	}
	if b > 0 {
		result++
	}
	if c > 0 {
		result++
	}
	return result
}
`
	ws := createTestWorkspace(t, src)

	// Default analyzer (threshold=10) — no result expected.
	rr, err := analyzers.Run(ws, complexity.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}
	defaultResults, ok := rr.Result.([]*complexity.Result)
	if !ok {
		t.Fatalf("Expected []*complexity.Result, got %T", rr.Result)
	}
	if len(defaultResults) != 0 {
		t.Errorf("Expected 0 results at default threshold, got %d", len(defaultResults))
	}

	// Low-threshold analyzer — should detect the function.
	lowAnalyzer := complexity.NewAnalyzer(complexity.WithMinComplexity(3))
	rr2, err := analyzers.Run(ws, lowAnalyzer, "")
	if err != nil {
		t.Fatal(err)
	}
	lowResults, ok := rr2.Result.([]*complexity.Result)
	if !ok {
		t.Fatalf("Expected []*complexity.Result, got %T", rr2.Result)
	}
	if len(lowResults) != 1 {
		t.Fatalf("Expected 1 result at threshold=3, got %d", len(lowResults))
	}
	if lowResults[0].Function != "medium" {
		t.Errorf("Expected function 'medium', got %q", lowResults[0].Function)
	}
}
