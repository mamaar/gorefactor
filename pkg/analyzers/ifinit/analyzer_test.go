package ifinit_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/ifinit"
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

func TestIfInit_BasicViolation(t *testing.T) {
	src := `package testpkg

func foo() error {
	if x, err := bar(); err != nil {
		return err
	}
	_ = x
	return nil
}

func bar() (int, error) { return 0, nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*ifinit.Result)
	if !ok {
		t.Fatalf("Expected []*ifinit.Result, got %T", rr.Result)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}

	v := results[0]
	if v.Function != "foo" {
		t.Errorf("Expected function 'foo', got %q", v.Function)
	}
	if len(v.Variables) != 2 || v.Variables[0] != "x" || v.Variables[1] != "err" {
		t.Errorf("Expected variables [x, err], got %v", v.Variables)
	}
	if v.Expression != "bar()" {
		t.Errorf("Expected expression 'bar()', got %q", v.Expression)
	}
	if v.Line != 4 {
		t.Errorf("Expected line 4, got %d", v.Line)
	}

	// Check diagnostic was reported
	if len(rr.Diagnostics) != 1 {
		t.Fatalf("Expected 1 diagnostic, got %d", len(rr.Diagnostics))
	}

	// Check suggested fix exists
	d := rr.Diagnostics[0]
	if len(d.SuggestedFixes) != 1 {
		t.Fatalf("Expected 1 suggested fix, got %d", len(d.SuggestedFixes))
	}
}

func TestIfInit_NoViolation(t *testing.T) {
	src := `package testpkg

func foo() error {
	x, err := bar()
	if err != nil {
		return err
	}
	_ = x
	return nil
}

func bar() (int, error) { return 0, nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*ifinit.Result)
	if !ok || len(results) != 0 {
		t.Errorf("Expected 0 violations, got %d", len(results))
	}
}

func TestIfInit_PlainAssignment_NoViolation(t *testing.T) {
	src := `package testpkg

func foo() error {
	var err error
	if err = bar(); err != nil {
		return err
	}
	return nil
}

func bar() error { return nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 0 {
		t.Errorf("Expected 0 violations for plain assignment, got %d", len(results))
	}
}

func TestIfInit_NestedViolations(t *testing.T) {
	src := `package testpkg

func foo() error {
	if x, err := bar(); err != nil {
		if y, err2 := baz(); err2 != nil {
			_ = y
			return err2
		}
		_ = x
		return err
	}
	return nil
}

func bar() (int, error) { return 0, nil }
func baz() (int, error) { return 0, nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations (nested), got %d", len(results))
	}
}

func TestIfInit_SuggestedFix(t *testing.T) {
	src := `package testpkg

func foo() error {
	if x, err := bar(); err != nil {
		return err
	}
	_ = x
	return nil
}

func bar() (int, error) { return 0, nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}

	c := changes[0]
	if c.File != "testpkg.go" {
		t.Errorf("Expected file 'testpkg.go', got %q", c.File)
	}
	// The new text should contain the split assignment and if-check
	if len(c.NewText) == 0 {
		t.Error("Expected non-empty NewText")
	}
}
