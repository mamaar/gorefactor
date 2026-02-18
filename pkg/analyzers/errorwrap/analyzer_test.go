package errorwrap_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/errorwrap"
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

func TestErrorWrap_BareReturn(t *testing.T) {
	src := `package testpkg

func foo() error {
	err := bar()
	return err
}

func bar() error { return nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, errorwrap.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*errorwrap.Result)
	if !ok {
		t.Fatalf("Expected []*errorwrap.Result, got %T", rr.Result)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}

	v := results[0]
	if v.ViolationType != errorwrap.BareReturn {
		t.Errorf("Expected violation type %q, got %q", errorwrap.BareReturn, v.ViolationType)
	}
	if v.Function != "foo" {
		t.Errorf("Expected function 'foo', got %q", v.Function)
	}
	if v.Severity != string(errorwrap.SeverityCritical) {
		t.Errorf("Expected severity %q, got %q", errorwrap.SeverityCritical, v.Severity)
	}
}

func TestErrorWrap_FormatVerbV(t *testing.T) {
	src := `package testpkg

import "fmt"

func foo() error {
	err := bar()
	return fmt.Errorf("failed: %v", err)
}

func bar() error { return nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, errorwrap.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*errorwrap.Result)
	if !ok {
		t.Fatalf("Expected []*errorwrap.Result, got %T", rr.Result)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}

	v := results[0]
	if v.ViolationType != errorwrap.FormatVerbV {
		t.Errorf("Expected violation type %q, got %q", errorwrap.FormatVerbV, v.ViolationType)
	}
	if v.Function != "foo" {
		t.Errorf("Expected function 'foo', got %q", v.Function)
	}
}

func TestErrorWrap_SeverityFilteringExcludesNoContext(t *testing.T) {
	// NoContext violations are SeverityWarning; the default Analyzer uses SeverityCritical,
	// so they should be filtered out.
	src := `package testpkg

import "fmt"

func createOrder() error {
	err := bar()
	return fmt.Errorf("failed: %w", err)
}

func bar() error { return nil }
`
	ws := createTestWorkspace(t, src)

	// Default analyzer uses SeverityCritical — should produce 0 results because
	// "failed" is a generic message (SeverityWarning), not a BareReturn or FormatVerbV.
	rr, err := analyzers.Run(ws, errorwrap.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*errorwrap.Result)
	if !ok {
		t.Fatalf("Expected []*errorwrap.Result, got %T", rr.Result)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 violations with SeverityCritical filter, got %d", len(results))
	}

	// Now use a Warning-level analyzer — should detect the NoContext violation.
	warnAnalyzer := errorwrap.NewAnalyzer(errorwrap.WithSeverity(errorwrap.SeverityWarning))
	rr2, err := analyzers.Run(ws, warnAnalyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results2, ok := rr2.Result.([]*errorwrap.Result)
	if !ok {
		t.Fatalf("Expected []*errorwrap.Result, got %T", rr2.Result)
	}
	if len(results2) != 1 {
		t.Fatalf("Expected 1 NoContext violation with SeverityWarning, got %d", len(results2))
	}
	if results2[0].ViolationType != errorwrap.NoContext {
		t.Errorf("Expected violation type %q, got %q", errorwrap.NoContext, results2[0].ViolationType)
	}
}
