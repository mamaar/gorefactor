package ifinit_test

import (
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"sort"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/ifinit"
	"github.com/mamaar/gorefactor/pkg/types"
)

// createTestWorkspace builds a workspace with no TypesInfo. Use this only
// for cases that don't need real collision/downgrade type checking —
// otherwise those checks pass vacuously (see canDowngrade's conservative
// "type information unavailable" skip).
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

// createTypeCheckedWorkspace builds a workspace like createTestWorkspace,
// but actually runs go/types over the parsed file so pkg.TypesPkg and
// pkg.TypesInfo are populated (mirroring how pkg/analysis/parser.go's
// TypeCheckPackage does it). Tests exercising collision/downgrade decisions
// must use this helper.
func createTypeCheckedWorkspace(t *testing.T, src string) *types.Workspace {
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

	conf := gotypes.Config{
		Importer: importer.Default(),
		Error:    func(error) {}, // silently ignore type errors, matching TypeCheckPackage
	}
	info := &gotypes.Info{
		Types: make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:  make(map[*ast.Ident]gotypes.Object),
		Uses:  make(map[*ast.Ident]gotypes.Object),
		// Implicits records, among other things, the per-clause implicit
		// variable of a type-switch case clause (keyed by *ast.CaseClause).
		// It must be pre-allocated to be populated during checking.
		Implicits: make(map[ast.Node]gotypes.Object),
	}
	typesPkg, _ := conf.Check(pkg.Path, fileSet, []*ast.File{astFile}, info)
	pkg.TypesPkg = typesPkg
	pkg.TypesInfo = info

	return &types.Workspace{
		Packages: map[string]*types.Package{"test/testpkg": pkg},
		FileSet:  fileSet,
	}
}

// applyChanges applies changes (as produced by analyzers.DiagnosticsToChanges)
// to src by offset, in descending-Start order, mirroring how the serializer
// applies them.
func applyChanges(t *testing.T, src string, changes []types.Change) string {
	t.Helper()
	sort.SliceStable(changes, func(i, j int) bool {
		return changes[i].Start > changes[j].Start
	})
	content := src
	for _, c := range changes {
		if c.Start < 0 || c.End > len(content) || c.Start > c.End {
			t.Fatalf("invalid change bounds: start=%d end=%d len=%d", c.Start, c.End, len(content))
		}
		content = content[:c.Start] + c.NewText + content[c.End:]
	}
	return content
}

// mustFormat applies changes to src and formats the result, failing the
// test if the result does not parse as valid Go.
func mustFormat(t *testing.T, src string, changes []types.Change) string {
	t.Helper()
	applied := applyChanges(t, src, changes)
	formatted, err := format.Source([]byte(applied))
	if err != nil {
		t.Fatalf("fixed source does not parse: %v\n--- applied (unformatted) ---\n%s", err, applied)
	}
	return string(formatted)
}

// typeCheckErrors parses and type-checks src, returning every error message
// go/types reports (rather than swallowing them, unlike the workspace
// helpers above, which mirror this project's TypeCheckPackage).
func typeCheckErrors(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "check.go", src, 0)
	if err != nil {
		t.Fatalf("re-parse failed: %v\n--- source ---\n%s", err, src)
	}
	var errs []string
	conf := gotypes.Config{
		Importer: importer.Default(),
		Error:    func(e error) { errs = append(errs, e.Error()) },
	}
	info := &gotypes.Info{
		Types: make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:  make(map[*ast.Ident]gotypes.Object),
		Uses:  make(map[*ast.Ident]gotypes.Object),
	}
	_, _ = conf.Check("check", fset, []*ast.File{f}, info)
	return errs
}

// mustCompile applies changes to src, formats it, and re-type-checks the
// result — failing the test if the fix produced source that does not
// type-check (e.g. "no new variables on left side of :="), which
// format.Source alone would not catch.
func mustCompile(t *testing.T, src string, changes []types.Change) string {
	t.Helper()
	formatted := mustFormat(t, src, changes)
	errs := typeCheckErrors(t, formatted)
	if len(errs) > 0 {
		t.Fatalf("fixed source does not type-check: %v\n--- formatted ---\n%s", errs, formatted)
	}
	return formatted
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
	if v.Kind != "if" {
		t.Errorf("Expected kind 'if', got %q", v.Kind)
	}
	if v.IsElseIf {
		t.Errorf("Expected IsElseIf false")
	}
	if v.Skipped {
		t.Errorf("Expected Skipped false")
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

func TestIfInit_PlainAssignment_IsHoisted(t *testing.T) {
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

	results, ok := rr.Result.([]*ifinit.Result)
	if !ok || len(results) != 1 {
		t.Fatalf("Expected 1 violation for plain assignment, got %d", len(results))
	}
	if results[0].Skipped {
		t.Errorf("Expected plain assignment not to be skipped")
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)
	if !strings.Contains(formatted, "err = bar()") {
		t.Errorf("Expected hoisted 'err = bar()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "if err != nil {") {
		t.Errorf("Expected 'if err != nil {', got:\n%s", formatted)
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
	// Description must stay byte-identical: Validator and import logic
	// string-match on it.
	if c.Description != "Split if-init assignment into separate assignment and if-check" {
		t.Errorf("Unexpected change description: %q", c.Description)
	}
}

func TestIfInit_ElseIf_Single(t *testing.T) {
	src := `package testpkg

func foo() int {
	if a := f(); a == 1 {
		return 1
	} else if b := g(); b == 2 {
		return 2
	}
	return 0
}

func f() int { return 0 }
func g() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations (head + else-if), got %d", len(results))
	}
	if results[0].IsElseIf {
		t.Errorf("Expected first result to not be an else-if")
	}
	if !results[1].IsElseIf {
		t.Errorf("Expected second result to be an else-if")
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	if len(changes) != 3 {
		t.Fatalf("Expected 3 text edits (1 head + 2 else-if), got %d", len(changes))
	}
	if changes[1].NewText != "{\n\t\tb := g()\n\t\tif b == 2 {" {
		t.Errorf("else-if preview header is not splice-indented:\n%s", changes[1].NewText)
	}
	if changes[2].NewText != "\n\t}" {
		t.Errorf("else-if preview closing brace is not splice-indented: %q", changes[2].NewText)
	}

	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "a := f()") {
		t.Errorf("Expected hoisted 'a := f()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "} else {") {
		t.Errorf("Expected desugared '} else {', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "b := g()") {
		t.Errorf("Expected hoisted 'b := g()' inside the wrapping block, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "if b == 2 {") {
		t.Errorf("Expected nested 'if b == 2 {', got:\n%s", formatted)
	}
}

func TestIfInit_ElseIf_Chained(t *testing.T) {
	src := `package testpkg

func foo() int {
	if a := f(); a == 1 {
		return 1
	} else if b := g(); b == 2 {
		return 2
	} else if c := h(); c == 3 {
		return 3
	}
	return 0
}

func f() int { return 0 }
func g() int { return 0 }
func h() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 3 {
		t.Fatalf("Expected 3 violations (head + 2 else-ifs), got %d", len(results))
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	// head: 1 edit; each else-if: 2 edits (header + zero-width close).
	if len(changes) != 5 {
		t.Fatalf("Expected 5 text edits, got %d", len(changes))
	}

	formatted := mustFormat(t, src, changes)

	if strings.Count(formatted, "} else {") != 2 {
		t.Errorf("Expected two desugared '} else {' blocks, got:\n%s", formatted)
	}
	for _, want := range []string{"a := f()", "b := g()", "c := h()", "if b == 2 {", "if c == 3 {"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("Expected %q in formatted output, got:\n%s", want, formatted)
		}
	}
}

func TestIfInit_ElseIf_WithTrailingElse(t *testing.T) {
	src := `package testpkg

func foo() int {
	if a := f(); a == 1 {
		return 1
	} else if b := g(); b == 2 {
		return 2
	} else {
		return 3
	}
}

func f() int { return 0 }
func g() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations (head + else-if), got %d", len(results))
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "b := g()") {
		t.Errorf("Expected hoisted 'b := g()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "return 3") {
		t.Errorf("Expected trailing else body preserved, got:\n%s", formatted)
	}
	// The trailing plain else must end up nested inside the synthetic
	// wrapping block, alongside "if b == 2 {".
	if !strings.Contains(formatted, "if b == 2 {") {
		t.Errorf("Expected nested 'if b == 2 {', got:\n%s", formatted)
	}
}

func TestIfInit_SiblingCollision_Two(t *testing.T) {
	src := `package testpkg

func foo() error {
	if err := a(); err != nil {
		return err
	}
	if err := b(); err != nil {
		return err
	}
	return nil
}

func a() error { return nil }
func b() error { return nil }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(results))
	}
	if results[0].Skipped || results[1].Skipped {
		t.Fatalf("Expected neither result skipped, got %+v / %+v", results[0], results[1])
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "err := a()") {
		t.Errorf("Expected first hoist to stay 'err := a()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "err = b()") {
		t.Errorf("Expected second hoist to downgrade to 'err = b()', got:\n%s", formatted)
	}
	if strings.Contains(formatted, "err := b()") {
		t.Errorf("Did not expect 'err := b()' (should have been downgraded), got:\n%s", formatted)
	}
}

func TestIfInit_SiblingCollision_Three(t *testing.T) {
	src := `package testpkg

func foo() error {
	if err := a(); err != nil {
		return err
	}
	if err := b(); err != nil {
		return err
	}
	if err := c(); err != nil {
		return err
	}
	return nil
}

func a() error { return nil }
func b() error { return nil }
func c() error { return nil }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 3 {
		t.Fatalf("Expected 3 violations, got %d", len(results))
	}
	for i, r := range results {
		if r.Skipped {
			t.Fatalf("result %d unexpectedly skipped: %+v", i, r)
		}
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "err := a()") {
		t.Errorf("Expected first hoist to stay 'err := a()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "err = b()") {
		t.Errorf("Expected second hoist to downgrade to 'err = b()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "err = c()") {
		t.Errorf("Expected third hoist to downgrade to 'err = c()', got:\n%s", formatted)
	}
}

func TestIfInit_PartialCollision_StaysDefine(t *testing.T) {
	src := `package testpkg

func foo() error {
	if err := a(); err != nil {
		return err
	}
	if x, err := f(); err != nil {
		_ = x
		return err
	}
	return nil
}

func a() error { return nil }
func f() (int, error) { return 0, nil }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(results))
	}
	if results[1].Skipped {
		t.Fatalf("Expected partial-collision result not skipped: %+v", results[1])
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "x, err := f()") {
		t.Errorf("Expected partial collision to stay 'x, err := f()', got:\n%s", formatted)
	}
}

func TestIfInit_ParamCollision_Skipped(t *testing.T) {
	src := `package testpkg

func foo(err error) error {
	if err := doit(); err != nil {
		return err
	}
	return err
}

func doit() error { return nil }
`
	assertSkipped(t, src, "already declared")
}

func TestIfInit_TypeIncompatible_Skipped(t *testing.T) {
	src := `package testpkg

func foo() {
	err := okNumber()
	if err := trouble(); err != "" {
		_ = err
	}
}

func okNumber() int    { return 0 }
func trouble() string  { return "" }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if !results[0].Skipped {
		t.Fatalf("Expected the incompatible collision to be skipped, got %+v", results[0])
	}
	if results[0].SkipReason == "" {
		t.Errorf("Expected a non-empty SkipReason")
	}

	if len(rr.Diagnostics) != 1 {
		t.Fatalf("Expected 1 diagnostic, got %d", len(rr.Diagnostics))
	}
	if len(rr.Diagnostics[0].SuggestedFixes) != 0 {
		t.Errorf("Expected no SuggestedFixes for a skipped site, got %d", len(rr.Diagnostics[0].SuggestedFixes))
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	if len(changes) != 0 {
		t.Errorf("Expected no changes for a skipped site, got %d", len(changes))
	}
}

func TestSwitchInit_WithTag(t *testing.T) {
	src := `package testpkg

func foo() {
	switch v := compute(); v {
	case 1:
	}
}

func compute() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}
	if results[0].Kind != "switch" {
		t.Errorf("Expected kind 'switch', got %q", results[0].Kind)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "v := compute()") {
		t.Errorf("Expected hoisted 'v := compute()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "switch v {") {
		t.Errorf("Expected 'switch v {', got:\n%s", formatted)
	}
}

func TestSwitchInit_NilTag(t *testing.T) {
	src := `package testpkg

func foo() {
	switch v := compute(); {
	case v == 1:
	}
}

func compute() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "v := compute()") {
		t.Errorf("Expected hoisted 'v := compute()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "switch {") {
		t.Errorf("Expected nil-tag 'switch {', got:\n%s", formatted)
	}
}

func TestTypeSwitchInit(t *testing.T) {
	src := `package testpkg

func foo(x interface{}) {
	switch v := f(); y := x.(type) {
	case int:
		_ = y
		_ = v
	}
}

func f() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}
	if results[0].Kind != "type_switch" {
		t.Errorf("Expected kind 'type_switch', got %q", results[0].Kind)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)

	if !strings.Contains(formatted, "v := f()") {
		t.Errorf("Expected hoisted 'v := f()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "switch y := x.(type) {") {
		t.Errorf("Expected untouched Assign 'switch y := x.(type) {', got:\n%s", formatted)
	}
}

func TestForInit_NoViolation(t *testing.T) {
	src := `package testpkg

func foo() {
	for i := 0; i < 10; i++ {
		_ = i
	}
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 0 {
		t.Errorf("Expected 0 violations for a for-loop, got %d", len(results))
	}
}

func TestLabeledIf_NoViolation(t *testing.T) {
	src := `package testpkg

func foo() {
Loop:
	if a := f(); a == 1 {
		goto Loop
	}
}

func f() int { return 0 }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 0 {
		t.Errorf("Expected 0 violations for a labeled if, got %d", len(results))
	}
	if len(rr.Diagnostics) != 0 {
		t.Errorf("Expected 0 diagnostics for a labeled if, got %d", len(rr.Diagnostics))
	}
}

func TestIfInit_BlankIdentifier(t *testing.T) {
	src := `package testpkg

func foo() error {
	if _, err := f(); err != nil {
		return err
	}
	return nil
}

func f() (int, error) { return 0, nil }
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}
	if len(results[0].Variables) != 2 || results[0].Variables[0] != "_" || results[0].Variables[1] != "err" {
		t.Errorf("Expected variables [_, err], got %v", results[0].Variables)
	}
	if results[0].Skipped {
		t.Errorf("Expected blank-identifier case not to be skipped")
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustFormat(t, src, changes)
	if !strings.Contains(formatted, "_, err := f()") {
		t.Errorf("Expected unchanged '_, err := f()', got:\n%s", formatted)
	}
}

// --- regression: clause-scoped guard/receive variables ---
//
// A type-switch case clause's guard variable, and a select comm clause's
// receive variable, are declared in that clause's own implicit block (per
// the Go spec) — the same block as the clause body statements. A hoist
// landing in that body must see the name as already declared, or it emits a
// bare ':=' that collides and fails to compile ("no new variables on left
// side of :=").

func TestTypeSwitchClauseCollision_NotBareDefine(t *testing.T) {
	src := `package testpkg

func foo(x interface{}) {
	switch v := x.(type) {
	case int:
		if v := f(); v > 0 {
			_ = v
		}
		_ = v
	}
}

func f() int { return 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation (the inner if), got %d", len(results))
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustCompile(t, src, changes)

	if !results[0].Skipped {
		t.Errorf("expected the site skipped: the clause variable is read after the if, got:\n%s", formatted)
	}
}

func TestSelectClauseCollision_NotBareDefine(t *testing.T) {
	src := `package testpkg

func foo(ch chan int) {
	select {
	case v := <-ch:
		if v := g(); v > 0 {
			_ = v
		}
		_ = v
	}
}

func g() int { return 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation (the inner if), got %d", len(results))
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustCompile(t, src, changes)

	if !results[0].Skipped {
		t.Errorf("expected the site skipped: the clause variable is read after the if, got:\n%s", formatted)
	}
}

// TestOrdinarySwitchClause_NoImplicitVariable is the negative control: a
// plain (non-type) switch's case clauses have no implicit clause-scoped
// variable, so a hoist inside one must still emit a normal ':='.
func TestOrdinarySwitchClause_NoImplicitVariable(t *testing.T) {
	src := `package testpkg

func foo(tag int) {
	switch tag {
	case 1:
		if v := f(); v > 0 {
			_ = v
		}
		_ = v
	}
}

func f() int { return 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}
	if results[0].Skipped {
		t.Fatalf("Expected no collision (ordinary switch clauses have no implicit variable), got %+v", results[0])
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustCompile(t, src, changes)

	if !strings.Contains(formatted, "v := f()") {
		t.Errorf("Expected normal hoist 'v := f()', got:\n%s", formatted)
	}
}

// TestTypeSwitchSiblingClauses_IndependentScopes proves each case clause
// gets its own fresh scope map: a name hoisted in one clause must not be
// seen as already declared by a sibling clause processed afterward.
func TestTypeSwitchSiblingClauses_IndependentScopes(t *testing.T) {
	src := `package testpkg

func foo(x interface{}) {
	switch v := x.(type) {
	case int:
		_ = v
		if w := f(); w > 0 {
			_ = w
		}
	case string:
		_ = v
		if w := g(); w > 0 {
			_ = w
		}
	}
}

func f() int { return 0 }
func g() int { return 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations (one per sibling clause), got %d", len(results))
	}
	for i, r := range results {
		if r.Skipped {
			t.Fatalf("result %d unexpectedly skipped: %+v", i, r)
		}
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustCompile(t, src, changes)

	if strings.Count(formatted, "w := f()") != 1 {
		t.Errorf("Expected exactly one 'w := f()', got:\n%s", formatted)
	}
	if strings.Count(formatted, "w := g()") != 1 {
		t.Errorf("Expected exactly one 'w := g()' (not downgraded to '=' — the sibling "+
			"clause's 'w' must not leak into this clause's scope), got:\n%s", formatted)
	}
}

// --- regression: hoisting breaks a later pre-existing ':=' ---
//
// Hoisting introduces a name into the block earlier than it existed before.
// That can turn a later, already-legal ':=' sibling into "no new variables
// on left side of :=". These tests cover the "look it up, return if found,
// otherwise construct it" idiom that triggered this in the real repo
// (pkg/graph/package_graph.go and friends), and the cascade machinery that
// keeps such later statements compiling.

func TestIfInit_CascadeLaterDefine_LookupOrConstructIdiom(t *testing.T) {
	src := `package testpkg

type PackageNode struct{}

type PackageGraph struct {
	Nodes map[string]*PackageNode
}

func (pg *PackageGraph) AddPackage(path string) *PackageNode {
	if node, exists := pg.Nodes[path]; exists {
		return node
	}
	node := &PackageNode{}
	pg.Nodes[path] = node
	return node
}
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation (the if), got %d", len(results))
	}
	if results[0].Skipped {
		t.Fatalf("Expected the hoist not to be skipped: %+v", results[0])
	}

	if len(rr.Diagnostics) != 1 || len(rr.Diagnostics[0].SuggestedFixes) != 1 {
		t.Fatalf("Expected 1 diagnostic with 1 suggested fix, got %+v", rr.Diagnostics)
	}
	got := len(rr.Diagnostics[0].SuggestedFixes[0].TextEdits)
	if got != 2 {
		t.Fatalf("Expected 2 TextEdits in the one SuggestedFix (hoist + cascaded downgrade), got %d", got)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	if len(changes) != 2 {
		t.Fatalf("Expected 2 changes, got %d", len(changes))
	}

	formatted := mustCompile(t, src, changes)

	if !strings.Contains(formatted, "node, exists := pg.Nodes[path]") {
		t.Errorf("Expected hoisted 'node, exists := pg.Nodes[path]', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "if exists {") {
		t.Errorf("Expected 'if exists {', got:\n%s", formatted)
	}
	if strings.Contains(formatted, "node := &PackageNode{}") {
		t.Fatalf("must not leave a bare 'node := &PackageNode{}' — it now collides with the "+
			"hoisted 'node' in the same block, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "node = &PackageNode{}") {
		t.Errorf("Expected the later statement cascaded to 'node = &PackageNode{}', got:\n%s", formatted)
	}
}

func TestIfInit_CascadeStaged_TwoHoistsThenCombinedDefine(t *testing.T) {
	src := `package testpkg

func foo() {
	if a := f(); a > 0 {
		return
	}
	if b := g(); b > 0 {
		return
	}
	a, b := h()
	_ = a
	_ = b
}

func f() int          { return 0 }
func g() int          { return 0 }
func h() (int, int)   { return 0, 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(results))
	}
	for i, r := range results {
		if r.Skipped {
			t.Fatalf("result %d unexpectedly skipped: %+v", i, r)
		}
	}

	if len(rr.Diagnostics) != 2 {
		t.Fatalf("Expected 2 diagnostics, got %d", len(rr.Diagnostics))
	}
	// The first hoist ('a') sees 'a, b := h()' as only a partial collision
	// (b is still new at that point) and must not touch it.
	got := len(rr.Diagnostics[0].SuggestedFixes[0].TextEdits)
	if got != 1 {
		t.Errorf("Expected the first hoist to have 1 TextEdit (no cascade yet), got %d", got)
	}
	// The second hoist ('b') now sees 'a, b := h()' as a full collision and
	// must cascade it.
	got = len(rr.Diagnostics[1].SuggestedFixes[0].TextEdits)
	if got != 2 {
		t.Errorf("Expected the second hoist to have 2 TextEdits (hoist + cascade), got %d", got)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustCompile(t, src, changes)

	if !strings.Contains(formatted, "a := f()") {
		t.Errorf("Expected hoisted 'a := f()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "b := g()") {
		t.Errorf("Expected hoisted 'b := g()', got:\n%s", formatted)
	}
	if strings.Contains(formatted, "a, b := h()") {
		t.Fatalf("must not leave a bare 'a, b := h()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "a, b = h()") {
		t.Errorf("Expected 'a, b := h()' cascaded to 'a, b = h()', got:\n%s", formatted)
	}
}

func TestIfInit_CascadeDoesNotCrossNestedBlock(t *testing.T) {
	src := `package testpkg

func foo() int {
	if node, exists := lookup(); exists {
		return node
	}
	{
		node := create()
		_ = node
	}
	return 0
}

func lookup() (int, bool) { return 0, false }
func create() int         { return 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}
	if results[0].Skipped {
		t.Fatalf("Expected the hoist not to be skipped: %+v", results[0])
	}
	got := len(rr.Diagnostics[0].SuggestedFixes[0].TextEdits)
	if got != 1 {
		t.Errorf("Expected 1 TextEdit — the nested block's own 'node' is a different "+
			"scope and must not be cascaded, got %d", got)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustCompile(t, src, changes)

	if !strings.Contains(formatted, "node, exists := lookup()") {
		t.Errorf("Expected hoisted 'node, exists := lookup()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "node := create()") {
		t.Errorf("Expected the nested block's 'node := create()' to stay ':=' "+
			"(different scope, no cascade), got:\n%s", formatted)
	}
}

func TestIfInit_CascadeSkipsPartialCollision(t *testing.T) {
	src := `package testpkg

func foo() {
	if node, exists := lookup(); exists {
		return
	}
	node, other := create()
	_ = node
	_ = other
}

func lookup() (int, bool)  { return 0, false }
func create() (int, int)   { return 0, 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}
	if results[0].Skipped {
		t.Fatalf("Expected the hoist not to be skipped: %+v", results[0])
	}
	got := len(rr.Diagnostics[0].SuggestedFixes[0].TextEdits)
	if got != 1 {
		t.Errorf("Expected 1 TextEdit — 'node, other := create()' is only a partial "+
			"collision ('other' is still new) and stays legal ':=', got %d", got)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	formatted := mustCompile(t, src, changes)

	if !strings.Contains(formatted, "node, other := create()") {
		t.Errorf("Expected 'node, other := create()' to stay unchanged, got:\n%s", formatted)
	}
}

func TestIfInit_CascadeTypeIncompatible_SkipsHoistEntirely(t *testing.T) {
	src := `package testpkg

func foo() {
	if node, exists := lookup(); exists {
		return
	}
	node := makeString()
	_ = node
}

func lookup() (int, bool) { return 0, false }
func makeString() string  { return "" }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(results))
	}
	if !results[0].Skipped {
		t.Fatalf("Expected the hoist to be skipped (the later ':=' would become type-"+
			"incompatible once downgraded), got %+v", results[0])
	}
	if results[0].SkipReason == "" {
		t.Errorf("Expected a non-empty SkipReason")
	}
	if !strings.Contains(results[0].SkipReason, "reuse the hoisted variable") {
		t.Errorf("Expected SkipReason to explain the cascade failure, got %q", results[0].SkipReason)
	}

	if len(rr.Diagnostics) != 1 || len(rr.Diagnostics[0].SuggestedFixes) != 0 {
		t.Fatalf("Expected no SuggestedFixes for a fully skipped hoist, got %+v", rr.Diagnostics)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	if len(changes) != 0 {
		t.Fatalf("Expected no changes, got %d", len(changes))
	}
	applied := applyChanges(t, src, changes)
	if applied != src {
		t.Errorf("Expected the file to be left byte-identical when the hoist is skipped,\n--- got ---\n%s\n--- want ---\n%s", applied, src)
	}
}

func TestIfInit_CascadeSameTarget_OnlyOneDowngradeEdit(t *testing.T) {
	src := `package testpkg

func foo() {
	if a := f(); a > 0 {
		return
	}
	if a2 := g(); a2 > 0 {
		return
	}
	a := h()
	_ = a
}

func f() int  { return 0 }
func g() int  { return 0 }
func h() int  { return 0 }
`
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(results))
	}
	for i, r := range results {
		if r.Skipped {
			t.Fatalf("result %d unexpectedly skipped: %+v", i, r)
		}
	}

	if len(rr.Diagnostics) != 2 {
		t.Fatalf("Expected 2 diagnostics, got %d", len(rr.Diagnostics))
	}
	// The first hoist ('a') claims the later 'a := h()' as its own cascade.
	got := len(rr.Diagnostics[0].SuggestedFixes[0].TextEdits)
	if got != 2 {
		t.Errorf("Expected the first hoist to have 2 TextEdits (hoist + cascade), got %d", got)
	}
	// The second hoist ('a2') would independently also see 'a := h()' as a
	// full collision, but it was already claimed — must not double-edit it.
	got = len(rr.Diagnostics[1].SuggestedFixes[0].TextEdits)
	if got != 1 {
		t.Errorf("Expected the second hoist to have 1 TextEdit (no duplicate cascade), got %d", got)
	}

	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	if len(changes) != 3 {
		t.Fatalf("Expected 3 total changes (2 + 1), got %d", len(changes))
	}

	// mustCompile also proves the serializer wouldn't hit an
	// overlapping-change error: applyChanges (which mirrors the serializer's
	// apply loop) would corrupt or fail to parse the source if the same
	// range were edited twice.
	formatted := mustCompile(t, src, changes)

	if !strings.Contains(formatted, "a := f()") {
		t.Errorf("Expected hoisted 'a := f()', got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "a2 := g()") {
		t.Errorf("Expected hoisted 'a2 := g()', got:\n%s", formatted)
	}
	if strings.Count(formatted, "a = h()") != 1 {
		t.Errorf("Expected exactly one 'a = h()', got:\n%s", formatted)
	}
	if strings.Contains(formatted, "a := h()") {
		t.Errorf("must not leave a bare 'a := h()', got:\n%s", formatted)
	}
}

// assertSkipped checks that src compiles, that its first if-init site is
// reported skipped for a reason containing wantReason, and that no edit is
// produced for that site.
func assertSkipped(t *testing.T, src, wantReason string) {
	t.Helper()
	errs := typeCheckErrors(t, src)
	if len(errs) > 0 {
		t.Fatalf("test source does not type-check: %v", errs)
	}
	ws := createTypeCheckedWorkspace(t, src)
	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}
	results, _ := rr.Result.([]*ifinit.Result)
	if len(results) == 0 {
		t.Fatal("expected a violation")
	}
	if !results[0].Skipped {
		formatted := mustFormat(t, src, analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics))
		t.Fatalf("expected the first site skipped, got fix:\n%s", formatted)
	}
	if !strings.Contains(results[0].SkipReason, wantReason) {
		t.Errorf("SkipReason = %q, want it to contain %q", results[0].SkipReason, wantReason)
	}
	if len(rr.Diagnostics[0].SuggestedFixes) != 0 {
		t.Errorf("a skipped site must carry no fix")
	}
}

// A hoist puts the name into the enclosing scope, so anything the block
// declares or refers to under the same name later must make it skip.
func TestIfInit_HoistConflictsWithLaterCode_Skipped(t *testing.T) {
	cases := []struct{ name, body, reason string }{
		{"later var", `if err := a(); err != nil {
		return err
	}
	var err error
	return err`, "redeclare"},
		{"later const", `if n := f(); n > 0 {
		return nil
	}
	const n = 3
	_ = n
	return nil`, "redeclare"},
		{"later type", `if T := f(); T > 0 {
		return nil
	}
	type T int
	return nil`, "redeclare"},
		{"later define of another type", `if n := f(); n > 0 {
		return nil
	}
	n := "s"
	_ = n
	return nil`, "reuse the hoisted variable"},
		{"later partial define of another type", `if s := g(); s != "" {
		return nil
	}
	n, s := f(), 1
	_, _ = n, s
	return nil`, "reuse the hoisted variable"},
		{"outer variable read after", `var n int
	{
		if n := f(); n > 0 {
			return nil
		}
		_ = n
	}
	return nil`, "refer to the hoisted variable"},
		{"package variable read after", `if pkgVar := f(); pkgVar > 0 {
		return nil
	}
	_ = pkgVar
	return nil`, "refer to the hoisted variable"},
		{"imported package name used after", `if strings := f(); strings > 0 {
		return nil
	}
	_ = strings.ToUpper("x")
	return nil`, "refer to the hoisted variable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := `package testpkg

import "strings"

var pkgVar = 1

func foo() error {
	` + tc.body + `
}

func a() error   { return nil }
func f() int     { return 0 }
func g() string  { return "" }

var _ = strings.ToUpper
`
			assertSkipped(t, src, tc.reason)
		})
	}
}

// Reusing a name with '=' may only touch a variable an earlier hoist put there.
func TestIfInit_ReuseOverwritesOriginalVariable_Skipped(t *testing.T) {
	cases := []struct{ name, body string }{
		{"earlier := read after", `n := f()
	if n := f(); n > 0 {
		return nil
	}
	_ = n
	return nil`},
		{"partial collision with an original variable", `n := f()
	if n, s := h(); s != "" {
		_ = n
	}
	_ = n
	return nil`},
		{"var declaration read after", `var n int
	if n := f(); n > 0 {
		return nil
	}
	_ = n
	return nil`},
		{"hoisted variable of a different type", `if v := e(); v != nil {
		return nil
	}
	if v := m(); v != nil {
		return nil
	}
	return nil`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := `package testpkg

type myErr struct{}

func (*myErr) Error() string { return "" }

func foo() error {
	` + tc.body + `
}

func f() int                 { return 0 }
func e() error               { return nil }
func m() *myErr              { return nil }
func h() (int, string)       { return 0, "" }
`
			ws := createTypeCheckedWorkspace(t, src)
			errs := typeCheckErrors(t, src)
			if len(errs) > 0 {
				t.Fatalf("test source does not type-check: %v", errs)
			}
			rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
			if err != nil {
				t.Fatal(err)
			}
			results, _ := rr.Result.([]*ifinit.Result)
			last := results[len(results)-1]
			if !last.Skipped {
				t.Fatalf("expected the last site skipped, got %+v", last)
			}
			mustCompile(t, src, analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics))
		})
	}
}
