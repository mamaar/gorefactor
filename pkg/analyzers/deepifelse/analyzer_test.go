package deepifelse_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

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

// createTypeCheckedWorkspace builds a workspace like createTestWorkspace, but
// actually runs go/types over the parsed file so pass.TypesInfo is populated.
// createTestWorkspace hands the fixer an empty *types.Info, which is fine for
// most rewrites but silently disables any guard that consults operand types —
// notably PushNeg's NaN-safety check on float comparisons. Tests that exercise
// that guard must use this helper instead.
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
		Types:     make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:      make(map[*ast.Ident]gotypes.Object),
		Uses:      make(map[*ast.Ident]gotypes.Object),
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

// TestDeepIfElse_FixableChainReportsRealEarlyReturnCount covers the counting
// bug: EarlyReturns must be the number of guards the rewrite actually emits,
// not an estimate derived from nesting depth.
func TestDeepIfElse_FixableChainReportsRealEarlyReturnCount(t *testing.T) {
	src := `package testpkg

func process(x int) string {
	if x > 0 {
		if x < 100 {
			if x%2 == 0 {
				return "even"
			} else {
				return "odd"
			}
		} else {
			return "too large"
		}
	} else {
		return "negative"
	}
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, deepifelse.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}

	got := results[0]
	if !got.Fixable {
		t.Fatalf("expected a fixable chain, reason: %s", got.Reason)
	}
	if got.EarlyReturns != 3 {
		t.Errorf("EarlyReturns = %d, want 3 (one guard per else branch)", got.EarlyReturns)
	}
	if got.Reason != "" {
		t.Errorf("a fixable finding should carry no reason, got %q", got.Reason)
	}
}

// The old fixer refused any chain using `else if`, reporting it detected but
// unfixable. The rewriter now flattens `else if` chains too - elision doesn't
// negate anything, so it isn't confined to the last link the way inversion is.
// This pins the exact rewritten text: every link's body already returns, so
// every link elides and the whole chain comes out fully flat.
func TestDeepIfElse_ElseIfChainFlattened(t *testing.T) {
	src := `package testpkg

func categorize(x int) string {
	if x < 0 {
		if x < -100 {
			if x < -1000 {
				return "extremely negative"
			} else {
				return "very negative"
			}
		} else {
			return "negative"
		}
	} else if x == 0 {
		return "zero"
	} else {
		return "positive"
	}
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, deepifelse.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}
	if !results[0].Fixable {
		t.Fatalf("expected the else-if chain to be fixable, reason: %s", results[0].Reason)
	}
	if results[0].Reason != "" {
		t.Errorf("a fully flattened chain should carry no reason, got %q", results[0].Reason)
	}
	if results[0].EarlyReturns != 4 {
		t.Errorf("EarlyReturns = %d, want 4", results[0].EarlyReturns)
	}

	fix := onlySuggestedFix(t, rr.Diagnostics)
	want := `if x < 0 {
		if x >= -100 {
			return "negative"
		}
		if x < -1000 {
			return "extremely negative"
		}
		return "very negative"
	}
	if x == 0 {
		return "zero"
	}
	return "positive"`
	got := string(fix)
	if got != want {
		t.Errorf("suggested fix text mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// A body's own last statement can be a nested if-else rather than a bare
// return, and that still hands control away by the spec definition of
// Terminates (an if-else whose arms both return). classify's first link
// exercises exactly that: its body ends in `if x > 0 {...} else {...}`, and
// the `else` after the whole link is elided on the strength of that, not a
// plain return.
func TestDeepIfElse_TerminatingIfElseBodyElided(t *testing.T) {
	src := `package testpkg

func classify(a bool, x int) string {
	if a {
		if x > 0 {
			return "positive"
		} else {
			return "non-positive"
		}
	} else if x == 0 {
		return "zero"
	} else {
		return "other"
	}
}
`
	ws := createTestWorkspace(t, src)
	a := deepifelse.NewAnalyzer(deepifelse.WithMaxNesting(1), deepifelse.WithMinElseLines(1))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}
	if !results[0].Fixable {
		t.Fatalf("expected classify to be fixable, reason: %s", results[0].Reason)
	}
	if results[0].Reason != "" {
		t.Errorf("a fully flattened chain should carry no reason, got %q", results[0].Reason)
	}

	fix := onlySuggestedFix(t, rr.Diagnostics)
	want := `if a {
		if x > 0 {
			return "positive"
		}
		return "non-positive"
	}
	if x == 0 {
		return "zero"
	}
	return "other"`
	got := string(fix)
	if got != want {
		t.Errorf("suggested fix text mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// An `if k, ok := m[x]; ok { ... } else { ... }` at the last link cannot be
// inverted - its init has nowhere to go once the condition flips - but that
// does not block elision, which never moves the init anywhere. The chain
// still comes out flat, one level of nesting short of what inversion would
// have bought, and the fixer says so: Fixable is true and Reason is
// non-empty, naming fix_if_init_assignments as the tool that finishes the
// job.
func TestDeepIfElse_IfInitLastLinkElidesWithReason(t *testing.T) {
	src := `package testpkg

func find(a bool, m map[string]int, x string) int {
	if a {
		return -1
	} else if k, ok := m[x]; ok {
		return k
	} else {
		return -2
	}
}
`
	ws := createTestWorkspace(t, src)
	a := deepifelse.NewAnalyzer(deepifelse.WithMaxNesting(1), deepifelse.WithMinElseLines(1))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}
	v := results[0]
	if !v.Fixable {
		t.Fatalf("expected find to be (partially) fixable, reason: %s", v.Reason)
	}
	if v.Reason == "" || !strings.Contains(v.Reason, "fix_if_init_assignments") {
		t.Errorf("Reason = %q, want it to name fix_if_init_assignments", v.Reason)
	}

	fix := onlySuggestedFix(t, rr.Diagnostics)
	want := `if a {
		return -1
	}
	if k, ok := m[x]; ok {
		return k
	}
	return -2`
	got := string(fix)
	if got != want {
		t.Errorf("suggested fix text mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(string(fix), "!") {
		t.Errorf("the if-init link must not be inverted, got:\n%s", fix)
	}
}

// A link's init-declared names are scoped to the whole if statement, so a
// later `else if` can legally read them - but only while the else stays
// attached. Eliding the else after this link would confine k and ok to this
// branch alone, changing what the next condition means, so the elision is
// refused rather than silently dropping the else.
func TestDeepIfElse_LaterLinkReadingInitNameRefused(t *testing.T) {
	src := `package testpkg

func find(m map[string]int, x string) int {
	if k, ok := m[x]; ok {
		return k
	} else if k > 10 {
		return 0
	} else {
		return -1
	}
}
`
	ws := createTestWorkspace(t, src)
	a := deepifelse.NewAnalyzer(deepifelse.WithMaxNesting(1), deepifelse.WithMinElseLines(1))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}
	v := results[0]
	if v.Fixable {
		t.Error("expected the chain to be refused: eliding past the init would change what `k > 10` reads")
	}
	if v.Reason == "" || !strings.Contains(v.Reason, "fix_if_init_assignments") {
		t.Errorf("Reason = %q, want it to name fix_if_init_assignments", v.Reason)
	}
	if v.EarlyReturns != 0 {
		t.Errorf("EarlyReturns = %d, want 0 for an unfixable finding", v.EarlyReturns)
	}

	for _, d := range rr.Diagnostics {
		if len(d.SuggestedFixes) != 0 {
			t.Error("a wholly unfixable finding must not carry a suggested fix")
		}
	}
}

// Dedenting a branch's declaration into the enclosing scope is refused when
// it would redeclare a name already declared there - `var result int` inside
// the else colliding with `var result int` in the function body - even though
// both branches already terminate and elision would otherwise be free.
func TestDeepIfElse_DedentRedeclarationRefused(t *testing.T) {
	src := `package testpkg

func lookup(x int) int {
	var result int
	if x > 0 {
		return x
	} else {
		var result int
		return -1
	}
}
`
	ws := createTestWorkspace(t, src)
	a := deepifelse.NewAnalyzer(deepifelse.WithMaxNesting(0), deepifelse.WithMinElseLines(0))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}
	v := results[0]
	if v.Fixable {
		t.Error("expected the redeclaration hazard to block the rewrite entirely")
	}
	want := `dedenting the branch would redeclare "result" in the enclosing scope`
	if v.Reason != want {
		t.Errorf("Reason = %q, want %q", v.Reason, want)
	}

	for _, d := range rr.Diagnostics {
		if len(d.SuggestedFixes) != 0 {
			t.Error("a wholly unfixable finding must not carry a suggested fix")
		}
	}
}

// The fixer lifts source text verbatim rather than re-printing the AST
// (go/printer would drop or reposition comments), and this is the regression
// guard for that: every comment in this chain, including one attached to a
// branch that gets recursively flattened, must survive the rewrite unchanged.
func TestDeepIfElse_CommentsSurviveRewrite(t *testing.T) {
	src := `package testpkg

func process(x int) string {
	if x > 0 {
		if x < 100 {
			// even numbers get a dedicated label
			if x%2 == 0 {
				return "even"
			} else {
				// odd branch comment
				return "odd"
			}
		} else {
			return "too large"
		}
	} else {
		// negative comment
		return "negative"
	}
}
`
	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, deepifelse.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	fix := onlySuggestedFix(t, rr.Diagnostics)
	want := `if x <= 0 {
		// negative comment
		return "negative"
	}
	if x >= 100 {
		return "too large"
	}
	// even numbers get a dedicated label
	if x%2 == 0 {
		return "even"
	}
	// odd branch comment
	return "odd"`
	got := string(fix)
	if got != want {
		t.Errorf("suggested fix text mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Flattening moves statements between nesting levels, and every level costs a
// tab of re-indentation. A back-quoted multi-line string literal's leading
// whitespace is not layout, it is the value, so there is no safe amount of
// re-indentation to apply - the chain must be reported unfixable rather than
// silently corrupting the literal.
func TestDeepIfElse_MultiLineRawStringLiteralRefused(t *testing.T) {
	src := "package testpkg\n\n" +
		"func render(x int) string {\n" +
		"\tif x > 0 {\n" +
		"\t\tif x < 100 {\n" +
		"\t\t\treturn \"small\"\n" +
		"\t\t} else {\n" +
		"\t\t\treturn \"large\"\n" +
		"\t\t}\n" +
		"\t} else {\n" +
		"\t\treturn `multi\nline`\n" +
		"\t}\n" +
		"}\n"

	ws := createTestWorkspace(t, src)
	a := deepifelse.NewAnalyzer(deepifelse.WithMaxNesting(1), deepifelse.WithMinElseLines(1))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}
	v := results[0]
	if v.Fixable {
		t.Error("expected a chain spanning a multi-line raw string literal to be refused")
	}
	want := "the chain contains a multi-line raw string literal, whose indentation is part of its value"
	if v.Reason != want {
		t.Errorf("Reason = %q, want %q", v.Reason, want)
	}

	for _, d := range rr.Diagnostics {
		if len(d.SuggestedFixes) != 0 {
			t.Error("a chain refused for its raw string literal must not carry a suggested fix")
		}
	}
}

// InvertCondition falls back to a blanket `!` wrap for a float ordering
// comparison, since flipping the operator changes behavior for NaN operands.
// That guard only fires when it can see the operand types, so this test needs
// a type-checked workspace - createTestWorkspace leaves pass.TypesInfo empty,
// which would let the guard silently not fire and the condition get flipped
// anyway. Inversion (rather than elision) is forced here by making the final
// else strictly shorter than the body, which is the case where inverting
// "pays".
func TestDeepIfElse_FloatComparisonKeepsBlanketNegation(t *testing.T) {
	src := `package testpkg

func compare(x, y float64) string {
	if x < y {
		result := "small"
		return result
	} else {
		return "large"
	}
}
`
	ws := createTypeCheckedWorkspace(t, src)
	a := deepifelse.NewAnalyzer(deepifelse.WithMaxNesting(0), deepifelse.WithMinElseLines(0))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatal(err)
	}

	results, _ := rr.Result.([]*deepifelse.Result)
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(results))
	}
	if !results[0].Fixable {
		t.Fatalf("expected compare to be fixable, reason: %s", results[0].Reason)
	}

	fix := onlySuggestedFix(t, rr.Diagnostics)
	want := `if !(x < y) {
		return "large"
	}
	result := "small"
	return result`
	got := string(fix)
	if got != want {
		t.Errorf("suggested fix text mismatch (float condition should keep its blanket !, not flip to >=)\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// onlySuggestedFix returns the single text edit's new text across all
// diagnostics, failing the test if there isn't exactly one.
func onlySuggestedFix(t *testing.T, diags []analysis.Diagnostic) []byte {
	t.Helper()
	var found [][]byte
	for _, d := range diags {
		for _, fix := range d.SuggestedFixes {
			for _, edit := range fix.TextEdits {
				found = append(found, edit.NewText)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 suggested text edit, got %d", len(found))
	}
	return found[0]
}
