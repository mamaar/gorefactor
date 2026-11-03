package refactor

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

	_, ok := files["go.mod"]
	if !ok {
		files["go.mod"] = "module example.com/extracttest\n\ngo 1.21\n"
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		err := os.MkdirAll(filepath.Dir(full), 0o755)
		if err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		err = os.WriteFile(full, []byte(content), 0o644)
		if err != nil {
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

	call := callOf(t, plan)
	if call != "res = appendBaseChecks(res, auth, feed)" {
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
	call := callOf(t, plan)
	if call != "return validate(n)" {
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
	call := callOf(t, plan)
	if call != want {
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
	call := callOf(t, plan)
	if call != "total = m.processItems(items, seen, total, count)" {
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
	call := callOf(t, plan)
	if call != "counter = p.processData(data, result, counter)" {
		t.Errorf("call site = %q", call)
	}
}

// extractLines returns the 1-based line range from the line containing
// startMark to the line containing endMark (both marks must be unique).
func extractLines(t *testing.T, src, startMark, endMark string) (int, int) {
	t.Helper()
	find := func(mark string) int {
		found := 0
		for i, line := range strings.Split(src, "\n") {
			if !strings.Contains(line, mark) {
				continue
			}
			if found != 0 {
				t.Fatalf("mark %q appears on lines %d and %d", mark, found, i+1)
			}
			found = i + 1
		}
		if found == 0 {
			t.Fatalf("mark %q not found", mark)
		}
		return found
	}
	return find(startMark), find(endMark)
}

// vetPlan applies the plan to main.go on disk and requires go vet to pass:
// the plan's own verification is not enough to catch copied locks.
func vetPlan(t *testing.T, ws *types.Workspace, plan *types.RefactoringPlan) {
	t.Helper()
	path := filepath.Join(ws.RootPath, "main.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changes := append([]types.Change(nil), plan.Changes...)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Start > changes[j].Start })
	out := string(content)
	for _, c := range changes {
		out = out[:c.Start] + c.NewText + out[c.End:]
	}
	err = os.WriteFile(path, []byte(out), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = ws.RootPath
	vetOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go vet failed after applying the plan: %v\n%s\n--- result ---\n%s", err, vetOut, out)
	}
}

// TestExtractFunctionPassesNoCopyValuesByPointer: a strings.Builder or a
// lock-holding struct must reach the extracted function by pointer, both in
// the signature and at the call site, or the copy panics / trips go vet.
func TestExtractFunctionPassesNoCopyValuesByPointer(t *testing.T) {
	cases := []struct {
		name, src, start, end string
		wantSig, wantCall     string
		wantBodyHas           string
	}{
		{
			name: "builder",
			src: `package main

import "strings"

func render(names []string) string {
	var sb strings.Builder
	for _, n := range names { // start
		sb.WriteString(n)
	} // end
	return sb.String()
}
`,
			start: "// start", end: "// end",
			wantSig:  "func writeAll(names []string, sb *strings.Builder) {",
			wantCall: "writeAll(names, &sb)",
		},
		{
			name: "address of builder in the body",
			src: `package main

import (
	"fmt"
	"strings"
)

func render(names []string) string {
	var sb strings.Builder
	for _, n := range names { // start
		fmt.Fprintf(&sb, "%s,", n)
	} // end
	return sb.String()
}
`,
			start: "// start", end: "// end",
			wantSig:     "func writeAll(names []string, sb *strings.Builder) {",
			wantCall:    "writeAll(names, &sb)",
			wantBodyHas: "fmt.Fprintf(sb,",
		},
		{
			name: "struct holding a mutex",
			src: `package main

import "sync"

type counter struct {
	mu sync.Mutex
	n  int
}

func bump() int {
	var c counter
	c.mu.Lock() // start
	c.n++
	c.mu.Unlock() // end
	return c.n
}
`,
			start: "// start", end: "// end",
			wantSig:  "func writeAll(c *counter) {",
			wantCall: "writeAll(&c)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, ws := writeExtractWorkspace(t, map[string]string{"main.go": tc.src})
			startLine, endLine := extractLines(t, tc.src, tc.start, tc.end)
			plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
				SourceFile:      filepath.Join(ws.RootPath, "main.go"),
				StartLine:       startLine,
				EndLine:         endLine,
				NewFunctionName: "writeAll",
			})
			if err != nil {
				t.Fatalf("ExtractFunction: %v", err)
			}
			decl := declOf(t, plan)
			if !strings.Contains(decl, tc.wantSig) {
				t.Errorf("signature: want %q in:\n%s", tc.wantSig, decl)
			}
			if tc.wantBodyHas != "" && !strings.Contains(decl, tc.wantBodyHas) {
				t.Errorf("body: want %q in:\n%s", tc.wantBodyHas, decl)
			}
			if call := callOf(t, plan); call != tc.wantCall {
				t.Errorf("call site = %q, want %q", call, tc.wantCall)
			}
			vetPlan(t, ws, plan)
		})
	}
}

// TestExtractFunctionHoistsNoCopyDeclaration: a Builder declared in the block
// and used after it is declared at the call site and passed by pointer, not
// returned by value. A declaration that cannot move is refused.
func TestExtractFunctionHoistsNoCopyDeclaration(t *testing.T) {
	const hoistable = `package main

import "strings"

func render(names []string) string {
	var sb strings.Builder // start
	for _, n := range names {
		sb.WriteString(n)
	} // end
	return sb.String()
}
`
	eng, ws := writeExtractWorkspace(t, map[string]string{"main.go": hoistable})
	startLine, endLine := extractLines(t, hoistable, "// start", "// end")
	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       startLine,
		EndLine:         endLine,
		NewFunctionName: "build",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}
	decl := declOf(t, plan)
	if !strings.Contains(decl, "func build(names []string, sb *strings.Builder) {") || strings.Contains(decl, "var sb") {
		t.Errorf("want sb as a pointer parameter with its declaration removed:\n%s", decl)
	}
	if call, want := callOf(t, plan), "var sb strings.Builder\nbuild(names, &sb)"; call != want {
		t.Errorf("call site = %q, want %q", call, want)
	}
	vetPlan(t, ws, plan)

	const initialised = `package main

import "strings"

func render() string {
	sb := strings.Builder{} // start
	sb.WriteString("x") // end
	return sb.String()
}
`
	eng, ws = writeExtractWorkspace(t, map[string]string{"main.go": initialised})
	startLine, endLine = extractLines(t, initialised, "// start", "// end")
	_, err = eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       startLine,
		EndLine:         endLine,
		NewFunctionName: "build",
	})
	if err == nil || !strings.Contains(err.Error(), "must not be copied") {
		t.Fatalf("want a refusal that mentions the copy, got %v", err)
	}
}

// TestExtractFunctionWriteFirstVariablesAreNotInputs: a variable the block
// assigns before reading is a result, not an in/out parameter; one that is
// only conditionally assigned keeps its incoming value and stays an input.
func TestExtractFunctionWriteFirstVariablesAreNotInputs(t *testing.T) {
	cases := []struct {
		name, src, start, end string
		wantSig, wantCall     string
		wantBodyHas           string
	}{
		{
			name: "assigned before any read",
			src: `package main

func span(items []int) (int, int, bool) { return 0, 0, len(items) > 0 }

func width(items []int) int {
	var lo, hi int
	var ok bool
	lo, hi, ok = span(items) // start // end
	if !ok {
		return 0
	}
	return hi - lo
}
`,
			start: "// start", end: "// end",
			wantSig:     "func measure(items []int) (int, int, bool) {",
			wantCall:    "lo, hi, ok = measure(items)",
			wantBodyHas: "var lo int",
		},
		{
			name: "assigned on one path only",
			src: `package main

func pick(c bool) int {
	x := 7
	if c { // start
		x = 1
	} // end
	return x
}
`,
			start: "// start", end: "// end",
			wantSig:  "func measure(c bool, x int) int {",
			wantCall: "x = measure(c, x)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, ws := writeExtractWorkspace(t, map[string]string{"main.go": tc.src})
			startLine, endLine := extractLines(t, tc.src, tc.start, tc.end)
			plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
				SourceFile:      filepath.Join(ws.RootPath, "main.go"),
				StartLine:       startLine,
				EndLine:         endLine,
				NewFunctionName: "measure",
			})
			if err != nil {
				t.Fatalf("ExtractFunction: %v", err)
			}
			decl := declOf(t, plan)
			if !strings.Contains(decl, tc.wantSig) {
				t.Errorf("signature: want %q in:\n%s", tc.wantSig, decl)
			}
			if tc.wantBodyHas != "" && !strings.Contains(decl, tc.wantBodyHas) {
				t.Errorf("body: want %q in:\n%s", tc.wantBodyHas, decl)
			}
			if call := callOf(t, plan); call != tc.wantCall {
				t.Errorf("call site = %q, want %q", call, tc.wantCall)
			}
			vetPlan(t, ws, plan)
		})
	}
}

const errGuardSrc = `package main

import (
	"errors"
	"fmt"
)

func read(path string) (string, error) { return path, nil }
func parse(s string) (int, error)      { return len(s), nil }

func load(path string) (int, error) {
	data, err := read(path)
	if err != nil {
		return 0, err
	}
	if data == "" { // start
		return 0, errors.New("empty")
	}
	n, perr := parse(data)
	if perr != nil {
		return 0, fmt.Errorf("parse: %w", perr)
	} // end
	return n * 2, nil
}
`

// TestExtractFunctionErrorGuards: a block whose early returns are all
// (zero, err) and whose other paths fall through becomes a function that
// returns its outputs plus an error, and the call site returns on non-nil.
func TestExtractFunctionErrorGuards(t *testing.T) {
	eng, ws := writeExtractWorkspace(t, map[string]string{"main.go": errGuardSrc})
	startLine, endLine := extractLines(t, errGuardSrc, "// start", "// end")
	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       startLine,
		EndLine:         endLine,
		NewFunctionName: "parseData",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}
	decl := declOf(t, plan)
	for _, want := range []string{
		"func parseData(data string) (int, error) {",
		`return 0, errors.New("empty")`,
		`return 0, fmt.Errorf("parse: %w", perr)`,
		"return n, nil",
	} {
		if !strings.Contains(decl, want) {
			t.Errorf("want %q in:\n%s", want, decl)
		}
	}
	wantCall := "var n int\nn, err = parseData(data)\nif err != nil {\n\treturn 0, err\n}"
	if call := callOf(t, plan); call != wantCall {
		t.Errorf("call site = %q, want %q", call, wantCall)
	}
	vetPlan(t, ws, plan)
}

// TestExtractFunctionRefusesNonGuardReturns: only (zero..., err) returns can be
// folded into the new error result; anything else keeps the refusal.
func TestExtractFunctionRefusesNonGuardReturns(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"non-zero value", `return 0, errors.New("empty")`, `return 1, errors.New("empty")`, "not a zero value"},
		{"nil error", `return 0, errors.New("empty")`, `return 0, nil`, "may return a nil error"},
		{"unguarded err", `return 0, errors.New("empty")`, `return 0, err`, "may return a nil error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(errGuardSrc, tc.from, tc.to, 1)
			eng, ws := writeExtractWorkspace(t, map[string]string{"main.go": src})
			startLine, endLine := extractLines(t, src, "// start", "// end")
			_, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
				SourceFile:      filepath.Join(ws.RootPath, "main.go"),
				StartLine:       startLine,
				EndLine:         endLine,
				NewFunctionName: "parseData",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal containing %q, got %v", tc.want, err)
			}
		})
	}
}

// extractAndVet extracts the marked lines from src into measure, checks the
// signature and call site, and vets the applied plan.
func extractAndVet(t *testing.T, src, wantSig, wantCall string) *types.RefactoringPlan {
	t.Helper()
	eng, ws := writeExtractWorkspace(t, map[string]string{"main.go": src})
	startLine, endLine := extractLines(t, src, "// start", "// end")
	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(ws.RootPath, "main.go"),
		StartLine:       startLine,
		EndLine:         endLine,
		NewFunctionName: "measure",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}
	decl := declOf(t, plan)
	if !strings.Contains(decl, wantSig) {
		t.Errorf("signature: want %q in:\n%s", wantSig, decl)
	}
	if call := callOf(t, plan); call != wantCall {
		t.Errorf("call site = %q, want %q", call, wantCall)
	}
	vetPlan(t, ws, plan)
	return plan
}

// TestExtractFunctionGuardCallSiteDoesNotRedeclareErr: a guard with no
// outputs whose err is not read afterwards uses an if-init, so a later
// `err :=` in the same scope still compiles; a live err is reused with `=`.
func TestExtractFunctionGuardCallSiteDoesNotRedeclareErr(t *testing.T) {
	cases := []struct{ name, src, wantCall string }{
		{
			name: "later err declaration",
			src: `package main

import "errors"

func step(n int) error { return nil }

func run(n int) error {
	if n < 0 { // start
		return errors.New("negative")
	} // end
	err := step(n)
	return err
}
`,
			wantCall: "if err := measure(n); err != nil {\n\treturn err\n}",
		},
		{
			name: "err read afterwards",
			src: `package main

import "errors"

func step(n int) error { return nil }

func run(n int) error {
	var err error
	if n < 0 { // start
		return errors.New("negative")
	} // end
	if err != nil {
		return err
	}
	return step(n)
}
`,
			wantCall: "err = measure(n)\nif err != nil {\n\treturn err\n}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extractAndVet(t, tc.src, "func measure(n int) error {", tc.wantCall)
		})
	}
	t.Run("outputs with err declared later in the scope", func(t *testing.T) {
		src := `package main

import "errors"

func step(n int) error { return nil }

func run(n int) ([]int, error) {
	var out []int
	for i := 0; i < n; i++ { // start
		if i > 5 {
			return nil, errors.New("too many")
		}
		out = append(out, i)
	} // end
	var err error
	err = step(n)
	return out, err
}
`
		extractAndVet(t, src, "func measure(n int) ([]int, error) {",
			"out, err2 := measure(n)\nif err2 != nil {\n\treturn nil, err2\n}")
	})
}

// TestExtractFunctionResultsNeedAReadThatSeesTheWrite: an outer variable the
// block writes is a result only if a later read can observe that write.
func TestExtractFunctionResultsNeedAReadThatSeesTheWrite(t *testing.T) {
	cases := []struct {
		name, src, wantSig, wantCall string
	}{
		{
			name: "overwritten before the next read",
			src: `package main

func step(n int) error { return nil }
func note(error)       {}

func run(n int) (err error) {
	err = step(n) // start
	if err != nil {
		note(err)
	} // end
	err = step(n + 1)
	return err
}
`,
			wantSig:  "func measure(n int) {",
			wantCall: "measure(n)",
		},
		{
			name: "bare return reads the named result",
			src: `package main

func step(n int) error { return nil }
func note(error)       {}

func run(n int) (err error) {
	err = step(n) // start
	if err != nil {
		note(err)
	} // end
	return
}
`,
			wantSig:  "func measure(n int) error {",
			wantCall: "err = measure(n)",
		},
		{
			name: "written on one path, read only inside, never afterwards",
			src: `package main

func run(c bool) int {
	n := 7
	_ = n
	total := 0
	if c { // start
		n = 1
		total += n
	} // end
	return total
}
`,
			wantSig:  "func measure(c bool, total int) int {",
			wantCall: "total = measure(c, total)",
		},
		{
			name: "in/out parameter never read afterwards",
			src: `package main

func run(items []int) int {
	total, count := 0, 0
	_ = count
	for _, it := range items { // start
		total += it
		count++
	} // end
	return total
}
`,
			wantSig:  "func measure(items []int, total int, count int) int {",
			wantCall: "total = measure(items, total, count)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extractAndVet(t, tc.src, tc.wantSig, tc.wantCall)
		})
	}
}

// TestExtractFunctionKnownZeroLocalsAreDeclaredInside: a variable still at its
// zero value when the block starts is not a parameter; its now-redundant
// `var` line goes when the call site can declare it.
func TestExtractFunctionKnownZeroLocalsAreDeclaredInside(t *testing.T) {
	t.Run("declared inside, declarations dropped", func(t *testing.T) {
		src := `package main

func run(items []string) int {
	var out []string
	var n int
	for _, it := range items { // start
		out = append(out, it)
		n += len(it)
	} // end
	return n + len(out)
}
`
		plan := extractAndVet(t, src, "func measure(items []string) ([]string, int) {", "out, n := measure(items)")
		var dropped int
		for _, c := range plan.Changes {
			if c.NewText == "" {
				dropped++
			}
		}
		if dropped != 2 {
			t.Errorf("want the two var declarations removed, got %d deletions", dropped)
		}
	})
	t.Run("assigned earlier stays a parameter", func(t *testing.T) {
		src := `package main

func run(items []string) int {
	var n int
	n = 5
	for _, it := range items { // start
		n += len(it)
	} // end
	return n
}
`
		extractAndVet(t, src, "func measure(items []string, n int) int {", "n = measure(items, n)")
	})
	t.Run("declared outside the loop around the block stays a parameter", func(t *testing.T) {
		src := `package main

func run(items []string) int {
	var n int
	for _, it := range items {
		n += len(it) // start
		n++          // end
	}
	return n
}
`
		extractAndVet(t, src, "func measure(n int, it string) int {", "n = measure(n, it)")
	})
}
