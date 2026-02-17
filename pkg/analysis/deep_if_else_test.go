package analysis

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func createDeepIfElseTestWorkspace(t *testing.T, src string) *types.Workspace {
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

func TestDeepIfElse_BasicViolation(t *testing.T) {
	src := `package testpkg

import "errors"

func process(err error, user *User, active bool) error {
	if err == nil {
		if user != nil {
			if active {
				return doWork()
			} else {
				return errors.New("inactive")
			}
		} else {
			return errors.New("not found")
		}
	} else {
		return errors.New("db error")
	}
}

type User struct{}
func doWork() error { return nil }
`
	ws := createDeepIfElseTestWorkspace(t, src)
	analyzer := NewDeepIfElseAnalyzer(ws, 2, 3)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.Function != "process" {
		t.Errorf("Expected function 'process', got %q", v.Function)
	}
	if v.NestingDepth < 3 {
		t.Errorf("Expected nesting depth >= 3, got %d", v.NestingDepth)
	}
	if v.ErrorBranches < 2 {
		t.Errorf("Expected at least 2 error branches, got %d", v.ErrorBranches)
	}
	if v.HappyPathDepth < 2 {
		t.Errorf("Expected happy path depth >= 2, got %d", v.HappyPathDepth)
	}
}

func TestDeepIfElse_ShallowNesting_NoViolation(t *testing.T) {
	src := `package testpkg

import "errors"

func process(err error) error {
	if err != nil {
		return errors.New("error")
	}
	return nil
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	analyzer := NewDeepIfElseAnalyzer(ws, 2, 3)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for shallow nesting, got %d", len(violations))
	}
}

func TestDeepIfElse_NoElse_NoViolation(t *testing.T) {
	src := `package testpkg

import "errors"

func process(err error) error {
	if err != nil {
		return errors.New("error")
	}
	if true {
		if true {
			if true {
				return nil
			}
		}
	}
	return nil
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	analyzer := NewDeepIfElseAnalyzer(ws, 2, 3)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for nested if without else, got %d", len(violations))
	}
}

func TestDeepIfElse_EarlyReturnPattern_NoViolation(t *testing.T) {
	src := `package testpkg

import "errors"

func process(err error, user *User) error {
	if err != nil {
		return errors.New("db error")
	}
	if user == nil {
		return errors.New("not found")
	}
	return doWork()
}

type User struct{}
func doWork() error { return nil }
`
	ws := createDeepIfElseTestWorkspace(t, src)
	analyzer := NewDeepIfElseAnalyzer(ws, 2, 3)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for early return pattern, got %d", len(violations))
	}
}

func TestDeepIfElse_MultipleViolations(t *testing.T) {
	src := `package testpkg

import "errors"

func process1(ok bool) error {
	if ok {
		if true {
			return nil
		} else {
			return errors.New("inner error")
		}
	} else {
		return errors.New("outer error")
	}
}

func process2(ok bool) error {
	if ok {
		if true {
			return nil
		} else {
			return errors.New("inner error 2")
		}
	} else {
		return errors.New("outer error 2")
	}
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	analyzer := NewDeepIfElseAnalyzer(ws, 1, 1)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(violations))
	}

	funcs := map[string]bool{}
	for _, v := range violations {
		funcs[v.Function] = true
	}
	if !funcs["process1"] || !funcs["process2"] {
		t.Errorf("Expected violations in process1 and process2, got functions: %v", funcs)
	}
}

func TestDeepIfElse_PackageFiltering(t *testing.T) {
	fileSet := token.NewFileSet()

	src1 := `package pkg1

import "errors"

func bad(ok bool) error {
	if ok {
		if true {
			return nil
		} else {
			return errors.New("inner")
		}
	} else {
		return errors.New("outer")
	}
}
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

	analyzer := NewDeepIfElseAnalyzer(ws, 1, 1)

	v1 := analyzer.AnalyzePackage(pkg1)
	if len(v1) != 1 {
		t.Errorf("Expected 1 violation in pkg1, got %d", len(v1))
	}

	v2 := analyzer.AnalyzePackage(pkg2)
	if len(v2) != 0 {
		t.Errorf("Expected 0 violations in pkg2, got %d", len(v2))
	}
}

func TestDeepIfElse_SmallElse_NoViolation(t *testing.T) {
	src := `package testpkg

func process(ok bool) int {
	if ok {
		if true {
			return 1
		} else {
			return 2
		}
	} else {
		return 3
	}
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	// minElseLines=10 means the small else blocks won't trigger
	analyzer := NewDeepIfElseAnalyzer(ws, 1, 10)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations when else is too small, got %d", len(violations))
	}
}

func TestDeepIfElse_ComplexityReduction(t *testing.T) {
	src := `package testpkg

import "errors"

func process(a, b, c bool) error {
	if a {
		if b {
			if c {
				return nil
			} else {
				return errors.New("c failed")
			}
		} else {
			return errors.New("b failed")
		}
	} else {
		return errors.New("a failed")
	}
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	analyzer := NewDeepIfElseAnalyzer(ws, 2, 1)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.ComplexityReductionPercent <= 0 {
		t.Errorf("Expected positive complexity reduction, got %d%%", v.ComplexityReductionPercent)
	}
	if v.Suggestion == "" {
		t.Error("Expected non-empty suggestion")
	}
}
