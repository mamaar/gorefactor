package booleanbranch_test

import (
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/booleanbranch"
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

func TestBooleanBranch_Violation(t *testing.T) {
	src := `package testpkg

func foo(x string) {
	isA := x == "a"
	isB := x == "b"
	if isA {
		doA()
	} else if isB {
		doB()
	}
}

func doA() {}
func doB() {}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, booleanbranch.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*booleanbranch.Result)
	if !ok {
		t.Fatalf("Expected []*booleanbranch.Result, got %T", rr.Result)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}

	v := results[0]
	if v.Function != "foo" {
		t.Errorf("Expected function 'foo', got %q", v.Function)
	}
	if v.BranchCount < 2 {
		t.Errorf("Expected BranchCount >= 2, got %d", v.BranchCount)
	}
	if v.SourceVariable != "x" {
		t.Errorf("Expected SourceVariable 'x', got %q", v.SourceVariable)
	}
}

func TestBooleanBranch_NoViolationSingleBoolean(t *testing.T) {
	// Only one boolean assignment from the same source — does not meet the
	// minBranches=2 threshold, so no violation should be reported.
	src := `package testpkg

func foo(x string) {
	isA := x == "a"
	if isA {
		doA()
	}
}

func doA() {}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, booleanbranch.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*booleanbranch.Result)
	if !ok {
		t.Fatalf("Expected []*booleanbranch.Result, got %T", rr.Result)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 violations for single boolean branch, got %d", len(results))
	}
}

func TestBooleanBranch_MultipleSources_OnlyGroupedViolation(t *testing.T) {
	// Two boolean vars derived from the same source "x" plus one from a different
	// source "y". Only the "x" group (2 booleans) should be flagged.
	src := `package testpkg

func foo(x, y string) {
	isA := x == "a"
	isB := x == "b"
	isC := y == "c"
	if isA {
		doA()
	} else if isB {
		doB()
	}
	if isC {
		doC()
	}
}

func doA() {}
func doB() {}
func doC() {}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, booleanbranch.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*booleanbranch.Result)
	if !ok {
		t.Fatalf("Expected []*booleanbranch.Result, got %T", rr.Result)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation (only the 'x' group), got %d", len(results))
	}
	if results[0].SourceVariable != "x" {
		t.Errorf("Expected SourceVariable 'x', got %q", results[0].SourceVariable)
	}
}

func TestBooleanBranch_FixesNestedIndependentBranchesAndKeepsDataUse(t *testing.T) {
	src := `package testpkg

func parse(matches []string) bool {
	if len(matches) > 1 {
		isRepeated := matches[1] == "repeated"
		hasPresence := matches[1] == "optional"
		value := "plain"
		if hasPresence {
			value = "optional"
		}
		// repeated values use a collection shape
		if isRepeated {
			value = "repeated"
		}
		println(value)
		return hasPresence
	}
	return false
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, booleanbranch.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	if len(changes) != 3 {
		t.Fatalf("expected one removal and two switch replacements, got %d", len(changes))
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Start > changes[j].Start })
	fixed := src
	for _, change := range changes {
		fixed = fixed[:change.Start] + change.NewText + fixed[change.End:]
	}
	formatted, err := format.Source([]byte(fixed))
	if err != nil {
		t.Fatalf("fix does not produce valid Go: %v\n%s", err, fixed)
	}
	got := string(formatted)
	if strings.Contains(got, "isRepeated :=") {
		t.Errorf("branch-only boolean was not removed:\n%s", got)
	}
	if !strings.Contains(got, `hasPresence := matches[1] == "optional"`) {
		t.Errorf("boolean used as data should remain:\n%s", got)
	}
	if strings.Count(got, "switch matches[1]") != 2 {
		t.Errorf("expected both independent branches to become switches:\n%s", got)
	}
	if !strings.Contains(got, "\t\t// repeated values use a collection shape") {
		t.Errorf("intervening comment was not preserved:\n%s", got)
	}
}
