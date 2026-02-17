package analysis

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func createErrorWrappingTestWorkspace(t *testing.T, src string) *types.Workspace {
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

func TestErrorWrapping_BareReturn(t *testing.T) {
	src := `package testpkg

func CreateOrder() error {
	err := doWork()
	if err != nil {
		return err
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.ViolationType != BareReturn {
		t.Errorf("Expected bare_return, got %s", v.ViolationType)
	}
	if v.Function != "CreateOrder" {
		t.Errorf("Expected function 'CreateOrder', got %q", v.Function)
	}
	if v.Severity != SeverityCritical {
		t.Errorf("Expected critical severity, got %s", v.Severity)
	}
	if v.ContextSuggestion != "create order" {
		t.Errorf("Expected context 'create order', got %q", v.ContextSuggestion)
	}
}

func TestErrorWrapping_FormatVerbV(t *testing.T) {
	src := `package testpkg

import "fmt"

func GetUser() error {
	err := doWork()
	if err != nil {
		return fmt.Errorf("failed: %v", err)
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.ViolationType != FormatVerbV {
		t.Errorf("Expected format_verb_v_instead_of_w, got %s", v.ViolationType)
	}
	if v.Severity != SeverityCritical {
		t.Errorf("Expected critical severity, got %s", v.Severity)
	}
}

func TestErrorWrapping_NoContext(t *testing.T) {
	src := `package testpkg

import "fmt"

func SaveData() error {
	err := doWork()
	if err != nil {
		return fmt.Errorf("error: %w", err)
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	// Use warning severity to include no_context
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityWarning)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.ViolationType != NoContext {
		t.Errorf("Expected no_context, got %s", v.ViolationType)
	}
	if v.Severity != SeverityWarning {
		t.Errorf("Expected warning severity, got %s", v.Severity)
	}
}

func TestErrorWrapping_NoContextFilteredByCritical(t *testing.T) {
	src := `package testpkg

import "fmt"

func SaveData() error {
	err := doWork()
	if err != nil {
		return fmt.Errorf("error: %w", err)
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	// Critical filter should exclude no_context (which is warning)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations with critical filter, got %d", len(violations))
	}
}

func TestErrorWrapping_ProperWrapping_NoViolation(t *testing.T) {
	src := `package testpkg

import "fmt"

func CreateOrder() error {
	err := doWork()
	if err != nil {
		return fmt.Errorf("create order in database: %w", err)
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityInfo)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for proper wrapping, got %d", len(violations))
	}
}

func TestErrorWrapping_NonErrorFunction_NoViolation(t *testing.T) {
	src := `package testpkg

func compute() int {
	return 42
}
`
	ws := createErrorWrappingTestWorkspace(t, src)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for non-error function, got %d", len(violations))
	}
}

func TestErrorWrapping_MultipleViolations(t *testing.T) {
	src := `package testpkg

import "fmt"

func Process() error {
	err := step1()
	if err != nil {
		return err
	}
	err = step2()
	if err != nil {
		return fmt.Errorf("failed: %v", err)
	}
	return nil
}

func step1() error { return nil }
func step2() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(violations))
	}

	types := map[ErrorWrappingViolationType]bool{}
	for _, v := range violations {
		types[v.ViolationType] = true
	}
	if !types[BareReturn] {
		t.Error("Expected bare_return violation")
	}
	if !types[FormatVerbV] {
		t.Error("Expected format_verb_v violation")
	}
}

func TestErrorWrapping_PackageFiltering(t *testing.T) {
	fileSet := token.NewFileSet()

	src1 := `package pkg1

func Bad() error {
	err := doWork()
	if err != nil {
		return err
	}
	return nil
}

func doWork() error { return nil }
`
	src2 := `package pkg2

func Clean() error {
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

	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)

	v1 := analyzer.AnalyzePackage(pkg1)
	if len(v1) != 1 {
		t.Errorf("Expected 1 violation in pkg1, got %d", len(v1))
	}

	v2 := analyzer.AnalyzePackage(pkg2)
	if len(v2) != 0 {
		t.Errorf("Expected 0 violations in pkg2, got %d", len(v2))
	}
}

func TestErrorWrapping_ReturnNilErr_NoViolation(t *testing.T) {
	src := `package testpkg

func Process() error {
	return nil
}
`
	ws := createErrorWrappingTestWorkspace(t, src)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for return nil, got %d", len(violations))
	}
}

func TestErrorWrapping_MultiReturnBareError(t *testing.T) {
	src := `package testpkg

func GetUser() (*User, error) {
	u, err := fetchUser()
	if err != nil {
		return nil, err
	}
	return u, nil
}

type User struct{}
func fetchUser() (*User, error) { return nil, nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	analyzer := NewErrorWrappingAnalyzer(ws, SeverityCritical)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if violations[0].ViolationType != BareReturn {
		t.Errorf("Expected bare_return, got %s", violations[0].ViolationType)
	}
}

func TestErrorWrapping_GenericMessages(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		generic bool
	}{
		{"empty_with_wrap", `"%w"`, true},
		{"just_error", `"error: %w"`, true},
		{"just_failed", `"failed: %w"`, true},
		{"descriptive", `"create user in database: %w"`, false},
		{"with_action", `"query row: %w"`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := `package testpkg

import "fmt"

func Do() error {
	err := work()
	if err != nil {
		return fmt.Errorf(` + tt.format + `, err)
	}
	return nil
}

func work() error { return nil }
`
			ws := createErrorWrappingTestWorkspace(t, src)
			analyzer := NewErrorWrappingAnalyzer(ws, SeverityWarning)
			violations := analyzer.AnalyzeWorkspace()

			if tt.generic && len(violations) != 1 {
				t.Errorf("Expected 1 violation for generic message %s, got %d", tt.format, len(violations))
			}
			if !tt.generic && len(violations) != 0 {
				t.Errorf("Expected 0 violations for descriptive message %s, got %d", tt.format, len(violations))
			}
		})
	}
}

func TestSuggestContext(t *testing.T) {
	tests := []struct {
		funcName string
		expected string
	}{
		{"CreateOrder", "create order"},
		{"GetUser", "get user"},
		{"processHTTPRequest", "process h t t p request"},
		{"save", "save"},
		{"", ""},
	}

	for _, tt := range tests {
		got := suggestContext(tt.funcName)
		if got != tt.expected {
			t.Errorf("suggestContext(%q) = %q, want %q", tt.funcName, got, tt.expected)
		}
	}
}
