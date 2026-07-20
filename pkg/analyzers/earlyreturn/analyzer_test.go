package earlyreturn_test

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/earlyreturn"
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

// run analyses src and returns the findings plus the diagnostics carrying fixes.
func run(t *testing.T, src string, opts ...earlyreturn.Option) ([]*earlyreturn.Result, []analysis.Diagnostic) {
	t.Helper()

	ws := createTestWorkspace(t, src)
	rr, err := analyzers.Run(ws, earlyreturn.NewAnalyzer(opts...), "")
	if err != nil {
		t.Fatal(err)
	}

	results, ok := rr.Result.([]*earlyreturn.Result)
	if !ok && rr.Result != nil {
		t.Fatalf("unexpected result type %T", rr.Result)
	}
	return results, rr.Diagnostics
}

// fixedText returns the replacement text of the single suggested fix, failing
// if the diagnostics do not carry exactly one.
func fixedText(t *testing.T, diags []analysis.Diagnostic) string {
	t.Helper()

	var texts []string
	for _, d := range diags {
		for _, fix := range d.SuggestedFixes {
			for _, edit := range fix.TextEdits {
				texts = append(texts, string(edit.NewText))
			}
		}
	}
	if len(texts) != 1 {
		t.Fatalf("expected exactly 1 suggested fix, got %d", len(texts))
	}
	return texts[0]
}

// assertParses checks that the replacement text is valid Go statement text.
// This is the property the serializer depends on: it runs format.Source over
// the patched file and, when that fails, writes the file unformatted anyway.
func assertParses(t *testing.T, stmts string) {
	t.Helper()

	src := "package p\n\nfunc wrapper() {\nfor {\n" + stmts + "\n}\n}\n"
	if _, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ParseComments); err != nil {
		t.Fatalf("replacement text does not parse: %v\n%s", err, stmts)
	}
}

func onlyResult(t *testing.T, results []*earlyreturn.Result) *earlyreturn.Result {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(results), results)
	}
	return results[0]
}

func TestGuardClause_Detected(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	if enabled {
		a := 1
		b := 2
		println(a + b)
	}
}
`
	results, diags := run(t, src)

	got := onlyResult(t, results)
	if got.Pattern != earlyreturn.PatternGuardClause {
		t.Errorf("pattern = %q, want %q", got.Pattern, earlyreturn.PatternGuardClause)
	}
	if !got.Fixable {
		t.Errorf("expected fixable finding, reason: %s", got.Reason)
	}
	if got.Function != "handle" {
		t.Errorf("function = %q, want handle", got.Function)
	}

	text := fixedText(t, diags)
	assertParses(t, text)
	if !strings.HasPrefix(text, "if !enabled {\nreturn\n}\n") {
		t.Errorf("unexpected guard text:\n%s", text)
	}
	if !strings.Contains(text, "println(a + b)") {
		t.Errorf("body not carried through:\n%s", text)
	}
}

func TestGuardClause_PreservesComments(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	if enabled {
		// why this matters
		a := 1
		println(a) // trailing note
		// closing note
	}
}
`
	_, diags := run(t, src)

	text := fixedText(t, diags)
	assertParses(t, text)
	for _, want := range []string{"// why this matters", "// trailing note", "// closing note"} {
		if !strings.Contains(text, want) {
			t.Errorf("comment %q dropped:\n%s", want, text)
		}
	}
}

func TestGuardClause_NotLastStatement(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	if enabled {
		a := 1
		b := 2
		println(a + b)
	}
	println("after")
}
`
	results, _ := run(t, src)

	if len(results) != 0 {
		t.Errorf("expected no findings when the if is not the final statement, got %+v", results)
	}
}

func TestGuardClause_BelowMinBodyLines(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	if enabled {
		println("x")
	}
}
`
	if results, _ := run(t, src); len(results) != 0 {
		t.Errorf("expected no findings below the body-line threshold, got %+v", results)
	}

	results, _ := run(t, src, earlyreturn.WithMinBodyLines(1))
	if len(results) != 1 {
		t.Errorf("expected a finding once the threshold is lowered, got %+v", results)
	}
}

func TestGuardClause_WithInitStatementSkipped(t *testing.T) {
	src := `package testpkg

func handle() {
	if v := compute(); v > 0 {
		a := 1
		b := 2
		println(a + b + v)
	}
}

func compute() int { return 1 }
`
	if results, _ := run(t, src); len(results) != 0 {
		t.Errorf("expected no findings for an if with an init statement, got %+v", results)
	}
}

// A dedented body must not redeclare a name that already exists in the scope it
// lands in — `a := 1` twice in one scope does not compile.
func TestGuardClause_RedeclarationReportedUnfixable(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	a := 0
	println(a)
	if enabled {
		a := 1
		b := 2
		println(a + b)
	}
}
`
	results, diags := run(t, src)

	got := onlyResult(t, results)
	if got.Fixable {
		t.Error("expected the finding to be reported unfixable")
	}
	if !strings.Contains(got.Reason, "redeclare") {
		t.Errorf("reason = %q, want it to mention redeclaration", got.Reason)
	}
	for _, d := range diags {
		if len(d.SuggestedFixes) != 0 {
			t.Error("an unfixable finding must not carry a suggested fix")
		}
	}
}

func TestGuardClause_ShadowingParameterReportedUnfixable(t *testing.T) {
	src := `package testpkg

func handle(count int, enabled bool) {
	if enabled {
		count := 1
		b := 2
		println(count + b)
	}
}
`
	got := onlyResult(t, mustResults(t, src))
	if got.Fixable {
		t.Errorf("expected unfixable: a parameter shares the function body scope")
	}
	if !strings.Contains(got.Reason, "count") {
		t.Errorf("reason = %q, want it to name the clashing identifier", got.Reason)
	}
}

// Defensive: in code that compiles this cannot occur, because a function with
// results must end in a terminating statement. The analyzer still refuses
// rather than inventing return values.
func TestGuardClause_UnnamedResultsReportedUnfixable(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) error {
	if enabled {
		a := 1
		b := 2
		println(a + b)
	}
}
`
	got := onlyResult(t, mustResults(t, src))
	if got.Fixable {
		t.Error("expected unfixable for unnamed results")
	}
	if !strings.Contains(got.Reason, "unnamed results") {
		t.Errorf("reason = %q, want it to mention unnamed results", got.Reason)
	}
}

func TestGuardClause_NamedResultsUseBareReturn(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) (err error) {
	if enabled {
		a := 1
		b := 2
		println(a + b)
	}
}
`
	_, diags := run(t, src)

	text := fixedText(t, diags)
	assertParses(t, text)
	if !strings.HasPrefix(text, "if !enabled {\nreturn\n}\n") {
		t.Errorf("expected a bare return for named results:\n%s", text)
	}
}

func TestLoopContinue_Detected(t *testing.T) {
	src := `package testpkg

func handle(items []int) {
	for _, item := range items {
		if item > 0 {
			a := item * 2
			b := a + 1
			println(b)
		}
	}
}
`
	results, diags := run(t, src)

	got := onlyResult(t, results)
	if got.Pattern != earlyreturn.PatternLoopContinue {
		t.Errorf("pattern = %q, want %q", got.Pattern, earlyreturn.PatternLoopContinue)
	}
	if !got.Fixable {
		t.Errorf("expected fixable, reason: %s", got.Reason)
	}

	text := fixedText(t, diags)
	assertParses(t, text)
	if !strings.HasPrefix(text, "if item <= 0 {\ncontinue\n}\n") {
		t.Errorf("unexpected continue guard:\n%s", text)
	}
}

// A loop inside a function with unnamed results still yields a continue guard:
// the exit needs no values, so the signature is irrelevant.
func TestLoopContinue_IndependentOfResults(t *testing.T) {
	src := `package testpkg

func handle(items []int) error {
	for _, item := range items {
		if item > 0 {
			a := item * 2
			b := a + 1
			println(b)
		}
	}
	return nil
}
`
	got := onlyResult(t, mustResults(t, src))
	if got.Pattern != earlyreturn.PatternLoopContinue || !got.Fixable {
		t.Errorf("expected a fixable loop_continue finding, got %+v", got)
	}
}

func TestElseAfterReturn_Detected(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) int {
	if !enabled {
		return 0
	} else {
		x := 1
		return x
	}
}
`
	results, diags := run(t, src)

	got := onlyResult(t, results)
	if got.Pattern != earlyreturn.PatternElseAfterReturn {
		t.Errorf("pattern = %q, want %q", got.Pattern, earlyreturn.PatternElseAfterReturn)
	}

	text := fixedText(t, diags)
	assertParses(t, text)
	if strings.Contains(text, "else") {
		t.Errorf("else branch should be gone:\n%s", text)
	}
	if !strings.HasPrefix(text, "if !enabled {\n") {
		t.Errorf("condition should be left untouched:\n%s", text)
	}
}

// Collapsing an else is worthwhile however short the branch, so the body-line
// threshold must not apply to it.
func TestElseAfterReturn_IgnoresMinBodyLines(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) int {
	if !enabled {
		return 0
	} else {
		return 1
	}
}
`
	got := onlyResult(t, mustResults(t, src, earlyreturn.WithMinBodyLines(10)))
	if !got.Fixable {
		t.Errorf("expected fixable, reason: %s", got.Reason)
	}
}

func TestElseAfterReturn_PanicTerminates(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	if !enabled {
		panic("nope")
	} else {
		println("ok")
	}
}
`
	got := onlyResult(t, mustResults(t, src))
	if got.Pattern != earlyreturn.PatternElseAfterReturn || !got.Fixable {
		t.Errorf("expected a fixable else_after_return finding, got %+v", got)
	}
}

func TestElseAfterReturn_NonTerminatingIfBodySkipped(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	if enabled {
		println("a")
	} else {
		println("b")
	}
}
`
	if results, _ := run(t, src); len(results) != 0 {
		t.Errorf("expected no findings when the if branch falls through, got %+v", results)
	}
}

func TestElseAfterReturn_ElseIfSkipped(t *testing.T) {
	src := `package testpkg

func handle(x int) int {
	if x == 0 {
		return 0
	} else if x == 1 {
		return 1
	}
	return 2
}
`
	if results, _ := run(t, src); len(results) != 0 {
		t.Errorf("expected no findings for an else-if ladder, got %+v", results)
	}
}

func TestElseAfterReturn_InitStatementSkipped(t *testing.T) {
	src := `package testpkg

func handle() int {
	if v := compute(); v == 0 {
		return 0
	} else {
		return v
	}
}

func compute() int { return 1 }
`
	if results, _ := run(t, src); len(results) != 0 {
		t.Errorf("expected no findings for an if with an init statement, got %+v", results)
	}
}

// Two findings that nest would produce overlapping text edits, which the
// serializer rejects. The outer match must win and the walk must not descend.
func TestNestedMatchesDoNotOverlap(t *testing.T) {
	src := `package testpkg

func handle(a, b bool) {
	if a {
		x := 1
		println(x)
		if b {
			y := 2
			z := 3
			println(y + z)
		}
	}
}
`
	results, diags := run(t, src)

	if len(results) != 1 {
		t.Fatalf("expected only the outer finding, got %d: %+v", len(results), results)
	}

	text := fixedText(t, diags)
	assertParses(t, text)
	// The inner if survives verbatim inside the hoisted body.
	if !strings.Contains(text, "if b {") {
		t.Errorf("inner conditional should be carried through untouched:\n%s", text)
	}
}

func TestWithPatterns_RestrictsDetection(t *testing.T) {
	src := `package testpkg

func handle(items []int) {
	for _, item := range items {
		if item > 0 {
			a := item * 2
			b := a + 1
			println(b)
		}
	}
}
`
	if results, _ := run(t, src, earlyreturn.WithPatterns(earlyreturn.PatternGuardClause)); len(results) != 0 {
		t.Errorf("loop_continue should be filtered out, got %+v", results)
	}
	if results, _ := run(t, src, earlyreturn.WithPatterns(earlyreturn.PatternLoopContinue)); len(results) != 1 {
		t.Errorf("loop_continue should be reported when selected, got %+v", results)
	}
}

func TestFuncLiteralBodyIsItsOwnRoot(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	go func() {
		if enabled {
			a := 1
			b := 2
			println(a + b)
		}
	}()
}
`
	got := onlyResult(t, mustResults(t, src))
	if got.Pattern != earlyreturn.PatternGuardClause || !got.Fixable {
		t.Errorf("expected a fixable guard inside the literal, got %+v", got)
	}
	if !strings.Contains(got.Function, "handle") {
		t.Errorf("function = %q, want it to reference the enclosing function", got.Function)
	}
}

// A trailing if inside a switch case cannot become a return: falling out of the
// case continues after the switch.
func TestSwitchCaseIsNotAGuardSite(t *testing.T) {
	src := `package testpkg

func handle(x int, enabled bool) {
	switch x {
	case 1:
		if enabled {
			a := 1
			b := 2
			println(a + b)
		}
	}
}
`
	if results, _ := run(t, src); len(results) != 0 {
		t.Errorf("expected no findings inside a case clause, got %+v", results)
	}
}

func TestParsePattern(t *testing.T) {
	if p, ok := earlyreturn.ParsePattern("loop_continue"); !ok || p != earlyreturn.PatternLoopContinue {
		t.Errorf("ParsePattern(loop_continue) = %q, %v", p, ok)
	}
	if _, ok := earlyreturn.ParsePattern("nonsense"); ok {
		t.Error("ParsePattern should reject unknown names")
	}
}

func mustResults(t *testing.T, src string, opts ...earlyreturn.Option) []*earlyreturn.Result {
	t.Helper()
	results, _ := run(t, src, opts...)
	return results
}

// A type switch guard declares its variable in the implicit block of each
// clause - the same scope as the clause body - so a block dedented into that
// body collides with it.
func TestTypeSwitchGuardIsInClauseScope(t *testing.T) {
	src := `package testpkg

func handle(x any, enabled bool) {
	switch v := x.(type) {
	case int:
		if enabled {
			return
		} else {
			v := 2
			println(v)
		}
	}
}
`
	got := onlyResult(t, mustResults(t, src))
	if got.Fixable {
		t.Error("expected unfixable: the type switch guard shares the clause body scope")
	}
	if !strings.Contains(got.Reason, "v") {
		t.Errorf("reason = %q, want it to name the clashing identifier", got.Reason)
	}
}

// A select receive declares its variable in the same clause block as the body.
func TestSelectReceiveIsInClauseScope(t *testing.T) {
	src := `package testpkg

func handle(ch chan int, enabled bool) {
	select {
	case v := <-ch:
		if enabled {
			return
		} else {
			v := 2
			println(v)
		}
	}
}
`
	got := onlyResult(t, mustResults(t, src))
	if got.Fixable {
		t.Error("expected unfixable: the received variable shares the clause body scope")
	}
}

// By contrast a switch init statement declares its variable in the switch's own
// implicit block, which merely encloses each clause, so shadowing is legal and
// the rewrite is safe.
func TestSwitchInitIsNotInClauseScope(t *testing.T) {
	src := `package testpkg

func handle(enabled bool) {
	switch v := compute(); v {
	case 1:
		if enabled {
			return
		} else {
			v := 2
			println(v)
		}
	}
}

func compute() int { return 1 }
`
	got := onlyResult(t, mustResults(t, src))
	if !got.Fixable {
		t.Errorf("expected fixable: a clause body may shadow the switch init variable; reason: %s", got.Reason)
	}
}
