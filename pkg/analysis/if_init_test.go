package analysis

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func createIfInitTestWorkspace(t *testing.T, src string) *types.Workspace {
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
	ws := createIfInitTestWorkspace(t, src)
	analyzer := NewIfInitAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
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
}

func TestIfInit_SimpleCondition_NoViolation(t *testing.T) {
	src := `package testpkg

func foo() error {
	var err error
	if err != nil {
		return err
	}
	return nil
}
`
	ws := createIfInitTestWorkspace(t, src)
	analyzer := NewIfInitAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations, got %d", len(violations))
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
	ws := createIfInitTestWorkspace(t, src)
	analyzer := NewIfInitAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for plain assignment (=), got %d", len(violations))
	}
}

func TestIfInit_MultipleVariables(t *testing.T) {
	src := `package testpkg

func foo() error {
	if a, b, err := multi(); err != nil {
		return err
	}
	_ = a
	_ = b
	return nil
}

func multi() (int, string, error) { return 0, "", nil }
`
	ws := createIfInitTestWorkspace(t, src)
	analyzer := NewIfInitAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if len(violations[0].Variables) != 3 {
		t.Errorf("Expected 3 variables, got %d: %v", len(violations[0].Variables), violations[0].Variables)
	}
	expected := []string{"a", "b", "err"}
	for i, name := range expected {
		if violations[0].Variables[i] != name {
			t.Errorf("Variable %d: expected %q, got %q", i, name, violations[0].Variables[i])
		}
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
	ws := createIfInitTestWorkspace(t, src)
	analyzer := NewIfInitAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 2 {
		t.Fatalf("Expected 2 violations (nested), got %d", len(violations))
	}
}

func TestIfInit_NoViolations(t *testing.T) {
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
	ws := createIfInitTestWorkspace(t, src)
	analyzer := NewIfInitAnalyzer(ws)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations, got %d", len(violations))
	}
}

func TestIfInit_PackageFiltering(t *testing.T) {
	fileSet := token.NewFileSet()

	src1 := `package pkg1

func foo() error {
	if _, err := bar(); err != nil {
		return err
	}
	return nil
}

func bar() (int, error) { return 0, nil }
`
	src2 := `package pkg2

func clean() error {
	return nil
}
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

	analyzer := NewIfInitAnalyzer(ws)

	// Only pkg1 should have violations
	v1 := analyzer.AnalyzePackage(pkg1)
	if len(v1) != 1 {
		t.Errorf("Expected 1 violation in pkg1, got %d", len(v1))
	}

	v2 := analyzer.AnalyzePackage(pkg2)
	if len(v2) != 0 {
		t.Errorf("Expected 0 violations in pkg2, got %d", len(v2))
	}
}
