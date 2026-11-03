package refactor

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// The extract operations are type-aware: they resolve captured variables and
// their types through go/types (see pkg/refactor/extractsig) and gate the
// resulting plan on VerifyPlanTypesWithImporter. Both require a workspace that was
// actually loaded from disk — a hand-built *types.Workspace has no FileSet,
// no ASTs and no type information, and the operations correctly refuse on
// one. Every test here therefore writes a real module to a temp dir and goes
// through the engine, which is also what wires the parser the lazy
// type-checking needs.

func writeExtractWorkspace(t *testing.T, files map[string]string) (RefactorEngine, *types.Workspace) {
	t.Helper()
	dir := t.TempDir()

	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/extracttest\n\ngo 1.21\n"
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	engine := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true, AllowBreaking: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}
	return engine, ws
}

// changeText returns the NewText of the single change whose description
// contains substr.
func changeText(t *testing.T, plan *types.RefactoringPlan, substr string) string {
	t.Helper()
	for _, c := range plan.Changes {
		if strings.Contains(c.Description, substr) {
			return c.NewText
		}
	}
	t.Fatalf("no change with a description containing %q; got %d changes", substr, len(plan.Changes))
	return ""
}

// declOf returns the generated declaration, without the blank-line padding
// the change carries so it lands cleanly after the enclosing function.
func declOf(t *testing.T, plan *types.RefactoringPlan) string {
	t.Helper()
	return strings.TrimSpace(changeText(t, plan, "Add extracted"))
}

func callOf(t *testing.T, plan *types.RefactoringPlan) string {
	t.Helper()
	return changeText(t, plan, "Replace extracted code with call to")
}

// TestExtractFunctionHeadlineRepro is the shape that motivated the rewrite.
// The old, regex-based implementation turned this block into
//
//	func appendBaseChecks(auth interface{}, orDefault interface{}, res map[string]string, feed interface{})
//
// — the package-level function orDefault captured as a parameter, every type
// erased to interface{}, res given a type it does not have, no result at all
// (so the struct mutation was silently dropped) and a call site that threw
// the value away — while reporting success.
func TestExtractFunctionHeadlineRepro(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"main.go": `package main

type sourceInfo struct {
	Kind string
}

type feedInfo struct {
	Label string
}

type scanReport struct {
	Checks []string
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func analyze(auth sourceInfo, feed feedInfo) (res scanReport) {
	res.Checks = append(res.Checks,
		"kind="+orDefault(auth.Kind, "<empty>"),
		"label="+feed.Label,
	)
	return res
}
`,
	})

	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       23,
		EndLine:         26,
		NewFunctionName: "appendBaseChecks",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}

	decl := declOf(t, plan)
	t.Logf("generated declaration:\n%s", decl)

	wantSig := "func appendBaseChecks(res scanReport, auth sourceInfo, feed feedInfo) scanReport {"
	if !strings.Contains(decl, wantSig) {
		t.Errorf("expected signature %q in:\n%s", wantSig, decl)
	}
	if strings.Contains(decl, "interface{}") {
		t.Errorf("generated declaration still contains interface{}:\n%s", decl)
	}
	if strings.Contains(wantSig, "orDefault") {
		t.Fatal("test bug: orDefault must not be in the expected signature")
	}
	if strings.Contains(decl, "orDefault ") || strings.Contains(decl, "orDefault,") {
		t.Errorf("package-level func orDefault was captured as a parameter:\n%s", decl)
	}
	if !strings.HasSuffix(strings.TrimSpace(decl), "return res\n}") {
		t.Errorf("expected the body to end in `return res`:\n%s", decl)
	}

	if call := callOf(t, plan); call != "res = appendBaseChecks(res, auth, feed)" {
		t.Errorf("call site = %q, want %q", call, "res = appendBaseChecks(res, auth, feed)")
	}
}

// TestExtractFunctionTailReturns covers a block that returns from the
// enclosing function on every path: the extracted function must take over
// that function's result signature and the call site becomes `return f(...)`.
func TestExtractFunctionTailReturns(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"main.go": `package main

import "errors"

func check(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	return nil
}
`,
	})

	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       6,
		EndLine:         9,
		NewFunctionName: "validate",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}

	decl := declOf(t, plan)
	t.Logf("generated declaration:\n%s", decl)
	if !strings.Contains(decl, "func validate(n int) error {") {
		t.Errorf("expected the enclosing function's result signature, got:\n%s", decl)
	}
	if call := callOf(t, plan); call != "return validate(n)" {
		t.Errorf("call site = %q, want %q", call, "return validate(n)")
	}
}

// TestExtractFunctionRefusesFallThroughReturns: a block where some paths
// return from the enclosing function and others fall through cannot be
// replaced by a single call, in either direction. Refusing is the correct
// outcome; the old implementation rewrote the returns and emitted code that
// changed behaviour.
func TestExtractFunctionRefusesFallThroughReturns(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"main.go": `package main

func f(n int) int {
	if n < 0 {
		return 0
	}
	n = n * 2
	return n
}
`,
	})

	_, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       4,
		EndLine:         6,
		NewFunctionName: "guard",
	})
	if err == nil {
		t.Fatal("expected a refusal for a block mixing early returns with fall-through")
	}
	if !strings.Contains(err.Error(), "fall-through") {
		t.Errorf("refusal should explain the fall-through problem, got: %v", err)
	}
}

// TestExtractFunctionRefusesDefer: a defer moved into an extracted function
// runs at that function's return instead of the original's. Nothing about
// the result fails to type-check, so only an explicit refusal catches it.
func TestExtractFunctionRefusesDefer(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"main.go": `package main

import "os"

func g(path string) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	_ = fh
	return nil
}
`,
	})

	_, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       10,
		EndLine:         11,
		NewFunctionName: "closeIt",
	})
	if err == nil {
		t.Fatal("expected a refusal for a selection containing a defer")
	}
	if !strings.Contains(err.Error(), "defer") {
		t.Errorf("refusal should mention the defer, got: %v", err)
	}
}

// TestExtractFunctionVerificationBlocksBadPlan proves the VerifyPlanTypesWithImporter
// gate does real work. The signature here is derived correctly — v really
// does have type *b.impl — but that type is unexported, so the declaration
// this produces cannot compile in package main. The operation must report
// that instead of writing the file and claiming success.
func TestExtractFunctionVerificationBlocksBadPlan(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"b/b.go": `package b

type impl struct{}

func (i *impl) Do() {}

func New() *impl { return &impl{} }
`,
		"main.go": `package main

import "example.com/extracttest/b"

func Run() {
	v := b.New()
	v.Do()
	v.Do()
}
`,
	})

	_, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       7,
		EndLine:         8,
		NewFunctionName: "doTwice",
	})
	if err == nil {
		t.Fatal("expected the type-check gate to reject a plan referencing an unexported type")
	}
	if !strings.Contains(err.Error(), "impl") {
		t.Errorf("expected the reported type error to mention impl, got: %v", err)
	}
}

// TestExtractFunctionMixedResults covers the call-site form that needs both
// assignment operators at once: sum already existed, so it must be assigned
// with `=`, while label is first declared inside the block and would need
// `:=`. A single statement cannot do both, so the new name gets an explicit
// `var` on a preceding line.
func TestExtractFunctionMixedResults(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"main.go": `package main

import "fmt"

func run(n int) {
	sum := 0
	sum = sum + n
	label := "x"
	fmt.Println(sum, label)
}
`,
	})

	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       7,
		EndLine:         8,
		NewFunctionName: "shift",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}

	decl := declOf(t, plan)
	t.Logf("generated declaration:\n%s", decl)
	if !strings.Contains(decl, "func shift(sum int, n int) (int, string) {") {
		t.Errorf("unexpected signature:\n%s", decl)
	}

	want := "var label string\nsum, label = shift(sum, n)"
	if call := callOf(t, plan); call != want {
		t.Errorf("call site = %q, want %q", call, want)
	}
}

// TestExtractFunctionAddsMissingImport: the rendered signature can name a
// package the source file does not import yet (here time, reached only
// through timeout's result type, which is declared in a sibling file). The
// plan has to add that import, or the declaration it inserts does not
// compile — and the type-check gate would reject the whole thing.
func TestExtractFunctionAddsMissingImport(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"helper.go": `package main

import "time"

func timeout() time.Duration { return 0 }
`,
		"main.go": `package main

import "fmt"

func run() {
	d := timeout()
	fmt.Println(d)
	fmt.Println(d)
}
`,
	})

	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       7,
		EndLine:         8,
		NewFunctionName: "printTwice",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}

	decl := declOf(t, plan)
	if !strings.Contains(decl, "func printTwice(d time.Duration) {") {
		t.Errorf("expected the qualified time.Duration type, got:\n%s", decl)
	}
	imports := changeText(t, plan, "Add imports to")
	if !strings.Contains(imports, `"time"`) {
		t.Errorf("expected the plan to add the time import, got: %q", imports)
	}
}

// TestExtractMethodTypeInference is the original type-inference regression
// test, now run against a real workspace: params must carry their real types,
// a compound-assigned outer variable must come back as a result, and a map
// (whose mutations the caller already sees) must not.
func TestExtractMethodTypeInference(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"example/example.go": `package example

type MyStruct struct {
	Name string
}

func (m *MyStruct) ProcessData(items []string, count int) error {
	total := 0
	seen := make(map[string]bool)

	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		total += count
	}

	if total > 100 {
		return nil
	}
	return nil
}
`,
	})

	plan, err := eng.ExtractMethod(ws, types.ExtractMethodRequest{
		SourceFile:    filepath.Join(ws.RootPath, "example", "example.go"),
		StartLine:     11, // for _, item := range items {
		EndLine:       17, // }
		NewMethodName: "processItems",
		TargetStruct:  "MyStruct",
	})
	if err != nil {
		t.Fatalf("ExtractMethod: %v", err)
	}

	decl := declOf(t, plan)
	t.Logf("generated method:\n%s", decl)

	if strings.Contains(decl, "interface{}") {
		t.Errorf("generated method contains interface{} types:\n%s", decl)
	}
	sigLine, _, _ := strings.Cut(decl, "\n")
	if !strings.Contains(sigLine, "items []string") {
		t.Errorf("expected items to keep its []string type, got:\n%s", sigLine)
	}
	if !strings.Contains(sigLine, "seen map[string]bool") {
		t.Errorf("expected seen to be a parameter with its real type, got:\n%s", sigLine)
	}
	if !strings.HasSuffix(sigLine, ") int {") {
		t.Errorf("expected an int result for total, got:\n%s", sigLine)
	}
	// The receiver must not also be passed as a parameter.
	if strings.Contains(sigLine, "m *MyStruct,") || strings.Count(sigLine, "MyStruct") != 1 {
		t.Errorf("receiver leaked into the parameter list:\n%s", sigLine)
	}
	if call := callOf(t, plan); call != "total = m.processItems(items, seen, total, count)" {
		t.Errorf("call site = %q", call)
	}
}

// TestExtractMethodPointerFieldAndLoopScope: mutations through a pointer are
// already visible to the caller and must not be returned, and a loop variable
// scoped to the extracted loop must never become a result.
func TestExtractMethodPointerFieldAndLoopScope(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{
		"example/example.go": `package example

type Result struct {
	Items []string
	Count int
}

type Processor struct {
	Name string
}

func (p *Processor) Run(result *Result, data []string) {
	counter := 0

	for _, d := range data {
		result.Items = append(result.Items, d)
		counter += 1
	}

	result.Count = counter
	_ = counter
}
`,
	})

	plan, err := eng.ExtractMethod(ws, types.ExtractMethodRequest{
		SourceFile:    filepath.Join(ws.RootPath, "example", "example.go"),
		StartLine:     15, // for _, d := range data {
		EndLine:       18, // }
		NewMethodName: "processData",
		TargetStruct:  "Processor",
	})
	if err != nil {
		t.Fatalf("ExtractMethod: %v", err)
	}

	decl := declOf(t, plan)
	t.Logf("generated method:\n%s", decl)
	sigLine, _, _ := strings.Cut(decl, "\n")

	if strings.Contains(decl, "interface{}") {
		t.Errorf("generated method contains interface{} types:\n%s", decl)
	}
	if strings.Count(sigLine, "*Result") != 1 {
		t.Errorf("result must be a parameter only, not also a return:\n%s", sigLine)
	}
	if !strings.HasSuffix(sigLine, ") int {") {
		t.Errorf("expected a single int result for counter, got:\n%s", sigLine)
	}
	if call := callOf(t, plan); call != "counter = p.processData(data, result, counter)" {
		t.Errorf("call site = %q", call)
	}
}
