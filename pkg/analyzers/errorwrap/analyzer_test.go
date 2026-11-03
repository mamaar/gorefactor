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
	if err != nil {
		return err
	}
	return nil
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

func runResults(t *testing.T, src string, opts ...errorwrap.Option) ([]*errorwrap.Result, int) {
	t.Helper()
	rr, err := analyzers.Run(createTestWorkspace(t, src), errorwrap.NewAnalyzer(opts...), "")
	if err != nil {
		t.Fatal(err)
	}
	fixes := 0
	for _, d := range rr.Diagnostics {
		fixes += len(d.SuggestedFixes)
	}
	return rr.Result.([]*errorwrap.Result), fixes
}

func TestErrorWrap_BareReturnWithoutFmtImportNeedsIt(t *testing.T) {
	results, _ := runResults(t, `package testpkg

func foo() error {
	err := bar()
	if err != nil {
		return err
	}
	return nil
}

func bar() error { return nil }
`)
	if len(results) != 1 || !results[0].NeedsFmtImport || results[0].Skipped {
		t.Fatalf("want one fixable result needing the fmt import, got %+v", results)
	}
}

func TestErrorWrap_BareReturnWithFmtImportNeedsNothing(t *testing.T) {
	results, _ := runResults(t, `package testpkg

import "fmt"

func foo() error {
	err := bar()
	fmt.Println(err)
	if err != nil {
		return err
	}
	return nil
}

func bar() error { return nil }
`)
	if len(results) != 1 || results[0].NeedsFmtImport || results[0].Skipped {
		t.Fatalf("want one fixable result with no import needed, got %+v", results)
	}
}

func TestErrorWrap_NonStdlibPackageNamedFmtSkipsSite(t *testing.T) {
	results, fixes := runResults(t, `package testpkg

import "example.com/other/fmt"

func foo() error {
	err := bar()
	fmt.Do()
	if err != nil {
		return err
	}
	return nil
}

func bar() error { return nil }
`)
	if len(results) != 1 || !results[0].Skipped || results[0].SkipReason == "" || fixes != 0 {
		t.Fatalf("want one skipped result with no fix, got %+v (fixes %d)", results, fixes)
	}
}

func TestErrorWrap_ExcludedFunctionsAreSkippedWithReason(t *testing.T) {
	src := `package testpkg

import "fmt"

type T struct{}

func plain() error {
	err := bar()
	if err != nil {
		return err
	}
	return nil
}

func (t *T) method() error {
	err := bar()
	if err != nil {
		return err
	}
	return nil
}

func (t T) other() error {
	err := bar()
	if err != nil {
		return err
	}
	return nil
}

func withLiteral() error {
	run := func() error {
		err := bar()
		if err != nil {
			return err
		}
		return nil
	}
	return run()
}

func kept() error {
	err := bar()
	return fmt.Errorf("x: %v", err)
}

func bar() error { return nil }
`
	results, fixes := runResults(t, src, errorwrap.WithExcludedFunctions("plain", "T.method", "withLiteral"))
	skipped := map[string]string{}
	for _, r := range results {
		if r.Skipped {
			skipped[r.Function] = r.SkipReason
		}
	}
	for _, name := range []string{"plain", "method", "withLiteral"} {
		if skipped[name] != "excluded" {
			t.Errorf("%s: skip reason %q, want excluded (all: %v)", name, skipped[name], skipped)
		}
	}
	if len(skipped) != 3 {
		t.Errorf("only the three excluded functions should be skipped, got %v", skipped)
	}
	// other (bare return) and kept (%v) are the only fixable sites.
	if fixes != 2 {
		t.Errorf("fixes = %d, want 2", fixes)
	}
}

func TestErrorWrap_BareReturnOnlyWhereErrorIsProvablyNonNil(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"unconditional after call", "err := bar()\n\treturn err", 0},
		{"or-condition does not prove it", "a, err := bar2()\n\tif a || err != nil {\n\t\treturn err\n\t}\n\treturn nil", 0},
		{"reassigned after guard", "err := bar()\n\tif err != nil {\n\t\terr = bar()\n\t\treturn err\n\t}\n\treturn nil", 0},
		{"guarded", "err := bar()\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil", 1},
		{"and-condition", "a, err := bar2()\n\tif a && err != nil {\n\t\treturn err\n\t}\n\treturn nil", 1},
		{"else of nil check", "err := bar()\n\tif err == nil {\n\t\treturn nil\n\t} else {\n\t\treturn err\n\t}", 1},
		{"guard on a different variable", "err := bar()\n\terr2 := bar()\n\tif err2 != nil {\n\t\treturn err\n\t}\n\treturn nil", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			results, fixes := runResults(t, "package testpkg\n\nfunc foo() error {\n\t"+c.body+"\n}\n\nfunc bar() error { return nil }\n\nfunc bar2() (bool, error) { return false, nil }\n")
			if len(results) != c.want || fixes != c.want {
				t.Fatalf("got %d results and %d fixes, want %d: %+v", len(results), fixes, c.want, results)
			}
		})
	}
}

func TestErrorWrap_FormatVerbVInExcludedFunctionIsStillFixed(t *testing.T) {
	results, fixes := runResults(t, `package testpkg

import "fmt"

func foo() error {
	err := bar()
	return fmt.Errorf("x: %v", err)
}

func bar() error { return nil }
`, errorwrap.WithExcludedFunctions("foo"))
	if len(results) != 1 || results[0].Skipped || fixes != 1 {
		t.Fatalf("want one unskipped result with a fix, got %+v (fixes %d)", results, fixes)
	}
}
