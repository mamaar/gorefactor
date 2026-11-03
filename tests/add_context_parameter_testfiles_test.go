package tests_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestAddContextParameter_Thread_IncludesTestFileCallSite is a regression
// test for add_context_parameter (thread: true) silently omitting a call
// site that lives in a _test.go file: the dry run used to rewrite the
// declaration and any non-test call site, report an empty skipped_sites,
// and no error — while leaving the test file that called the seed function
// on the OLD signature, which does not compile once the seed gains a ctx
// parameter. It must now find and rewrite that call site, passing
// t.Context() since the call sits directly inside a *testing.T-scoped Test
// function.
func TestAddContextParameter_Thread_IncludesTestFileCallSite(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"foo/foo.go": `package foo

func Process(name string) string { return name }
`,
		"foo/foo_test.go": `package foo

import "testing"

func TestProcess(t *testing.T) {
	Process("x")
}
`,
	})

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "add_context_parameter",
		Arguments: map[string]any{
			"function_name": "Process",
			"source_file":   "foo/foo.go",
			"thread":        true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned error: %s", errText(result))
	}

	declContent, err := os.ReadFile(filepath.Join(dir, "foo", "foo.go"))
	if err != nil {
		t.Fatalf("reading foo.go: %v", err)
	}
	if !strings.Contains(string(declContent), "func Process(ctx context.Context, name string) string") {
		t.Errorf("expected Process to gain a ctx context.Context parameter, got:\n%s", declContent)
	}

	testContent, err := os.ReadFile(filepath.Join(dir, "foo", "foo_test.go"))
	if err != nil {
		t.Fatalf("reading foo_test.go: %v", err)
	}
	got := string(testContent)
	if !strings.Contains(got, `Process(t.Context(), "x")`) {
		t.Errorf("expected the _test.go call site to be rewritten with t.Context(), got:\n%s", got)
	}
}

// testFilesExcludedWorkspace builds a package where foo_test.go calls Process
// and type-checks, and bad_test.go calls it too but imports a package that
// does not resolve.
func testFilesExcludedWorkspace(t *testing.T) string {
	t.Helper()
	return writeInlineWorkspace(t, map[string]string{
		"foo/foo.go": `package foo

func Process(name string) string { return name }
`,
		"foo/foo_test.go": `package foo

import "testing"

func TestProcess(t *testing.T) {
	Process("x")
}
`,
		"foo/bad_test.go": `package foo

import (
	"testing"

	_ "example.com/this-module-does-not-exist/thirdparty"
)

func TestBad(t *testing.T) {
	Process("y")
}
`,
	})
}

// unattributedTestsWorkspace builds a package whose test-file errors the
// type-checker cannot shed within its pass limit.
func unattributedTestsWorkspace(t *testing.T) string {
	t.Helper()
	files := map[string]string{
		"foo/foo.go":      "package foo\n\nfunc Process(name string) string { return name }\n",
		"foo/foo_test.go": "package foo\n\nimport \"testing\"\n\nfunc TestProcess(t *testing.T) {\n\tProcess(\"x\")\n}\n",
		"foo/t0_test.go":  "package foo\n\nimport _ \"example.com/this-module-does-not-exist/thirdparty\"\n\nfunc h0() {}\n",
	}
	for i := 1; i <= 4; i++ {
		files[fmt.Sprintf("foo/t%d_test.go", i)] = fmt.Sprintf("package foo\n\nfunc h%d() { h%d() }\n", i, i-1)
	}
	return writeInlineWorkspace(t, files)
}

type planResultJSON struct {
	Success       bool   `json:"success"`
	DryRun        bool   `json:"dry_run"`
	BlockingIssue string `json:"blocking_issue"`
	Diffs         []struct {
		File string `json:"file"`
	} `json:"diffs"`
	UncoveredFiles []struct {
		File    string   `json:"file"`
		Configs []string `json:"configs"`
		Reason  string   `json:"reason"`
	} `json:"uncovered_files"`
	SkippedSites []struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Reason string `json:"reason"`
	} `json:"skipped_sites"`
}

func addContextParameter(t *testing.T, sess *mcptest.Session, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	args["function_name"] = "Process"
	args["source_file"] = "foo/foo.go"
	result, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "add_context_parameter", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return result
}

func decodePlanResult(t *testing.T, result *mcpsdk.CallToolResult) planResultJSON {
	t.Helper()
	if result.IsError {
		t.Fatalf("CallTool returned error: %s", errText(result))
	}
	var out planResultJSON
	err := json.Unmarshal([]byte(errText(result)), &out)
	if err != nil {
		t.Fatalf("decode result: %v\n%s", err, errText(result))
	}
	return out
}

// typeCheckFoo type-checks package foo through another tool: the non-thread
// path of add_context_parameter does not type-check by itself.
func typeCheckFoo(t *testing.T, sess *mcptest.Session) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "detect_if_init_assignments",
		Arguments: map[string]any{"package": "example.com/threadmcp/foo"},
	})
	if err != nil {
		t.Fatalf("CallTool detect_if_init_assignments: %v", err)
	}
	if res.IsError {
		t.Fatalf("detect_if_init_assignments returned error: %s", errText(res))
	}
}

func assertBadTestFileUncovered(t *testing.T, out planResultJSON) {
	t.Helper()
	if len(out.UncoveredFiles) != 1 {
		t.Fatalf("uncovered_files = %+v, want foo/bad_test.go", out.UncoveredFiles)
	}
	u := out.UncoveredFiles[0]
	if u.File != "foo/bad_test.go" || len(u.Configs) != 1 || !strings.Contains(u.Reason, "could not import") {
		t.Errorf("uncovered file = %+v, want foo/bad_test.go under one configuration with the import error", u)
	}
}

func assertUnchanged(t *testing.T, dir, rel string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	if strings.Contains(string(content), "context") {
		t.Errorf("%s was modified:\n%s", rel, content)
	}
}

func assertFileContains(t *testing.T, dir, rel, want string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	if !strings.Contains(string(content), want) {
		t.Errorf("%s does not contain %q:\n%s", rel, want, content)
	}
}

func TestAddContextParameter_Thread_RewritesGoodTestFileAndReportsExcludedOne(t *testing.T) {
	dir := testFilesExcludedWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := decodePlanResult(t, addContextParameter(t, sess, map[string]any{"thread": true}))

	assertFileContains(t, dir, "foo/foo.go", "func Process(ctx context.Context, name string) string")
	assertFileContains(t, dir, "foo/foo_test.go", `Process(t.Context(), "x")`)
	assertUnchanged(t, dir, "foo/bad_test.go")
	assertBadTestFileUncovered(t, out)
	if len(out.SkippedSites) != 1 || !strings.HasSuffix(out.SkippedSites[0].File, "foo/bad_test.go") || out.SkippedSites[0].Line != 10 {
		t.Errorf("skipped_sites = %+v, want the Process call at bad_test.go:10", out.SkippedSites)
	}
}

func TestAddContextParameter_Thread_DryRunReportsTheSameUncoveredFilesAsApply(t *testing.T) {
	dir := testFilesExcludedWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	preview := decodePlanResult(t, addContextParameter(t, sess, map[string]any{"thread": true, "dry_run": true}))
	if !preview.DryRun {
		t.Fatal("dry_run not reported")
	}
	assertUnchanged(t, dir, "foo/foo.go")
	applied := decodePlanResult(t, addContextParameter(t, sess, map[string]any{"thread": true}))

	assertBadTestFileUncovered(t, preview)
	if !reflect.DeepEqual(preview.UncoveredFiles, applied.UncoveredFiles) {
		t.Errorf("dry run uncovered_files = %+v, apply = %+v", preview.UncoveredFiles, applied.UncoveredFiles)
	}
	if !reflect.DeepEqual(preview.SkippedSites, applied.SkippedSites) {
		t.Errorf("dry run skipped_sites = %+v, apply = %+v", preview.SkippedSites, applied.SkippedSites)
	}
}

func TestAddContextParameter_NoThread_RewritesGoodTestFileAndReportsExcludedOne(t *testing.T) {
	dir := testFilesExcludedWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	typeCheckFoo(t, sess)

	out := decodePlanResult(t, addContextParameter(t, sess, map[string]any{}))

	assertFileContains(t, dir, "foo/foo.go", "func Process(ctx context.Context, name string) string")
	assertFileContains(t, dir, "foo/foo_test.go", `Process(context.TODO(), "x")`)
	assertBadTestFileUncovered(t, out)
}

func TestAddContextParameter_Thread_AbortsWhenTestFilesAreUnattributed(t *testing.T) {
	dir := unattributedTestsWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	result := addContextParameter(t, sess, map[string]any{"thread": true})

	if !result.IsError {
		t.Fatalf("expected an abort, got: %s", errText(result))
	}
	msg := errText(result)
	if !strings.Contains(msg, "example.com/threadmcp/foo") || !strings.Contains(msg, "_test.go") {
		t.Errorf("abort does not name the package and its test files: %s", msg)
	}
	assertUnchanged(t, dir, "foo/foo.go")
}

func TestAddContextParameter_NoThread_AbortsWhenTestFilesAreUnattributed(t *testing.T) {
	dir := unattributedTestsWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	typeCheckFoo(t, sess)

	result := addContextParameter(t, sess, map[string]any{})

	if !result.IsError {
		t.Fatalf("expected an abort, got: %s", errText(result))
	}
	msg := errText(result)
	if !strings.Contains(msg, "example.com/threadmcp/foo") || !strings.Contains(msg, "_test.go") {
		t.Errorf("abort does not name the package and its test files: %s", msg)
	}
	assertUnchanged(t, dir, "foo/foo.go")
}

// TestAddContextParameter_Thread_TestOnlyImportDiamond is an end-to-end
// regression test for the false "cycle" TypeCheckPackage's always-fold-
// test-files design can manufacture even for entirely valid Go — see
// pkg/analysis/parser_test.go's
// TestParser_TypeCheckPackage_DiamondRescueIsConsistentAcrossImporters for
// the full topology explanation and why it is NOT the (Go-forbidden)
// "a_test.go imports b, b imports a" shape.
//
// This reproduces the exact failure mode first found against a real,
// large workspace: dep.Seed is threaded, dep's own combined check reaches
// bridge nested (through slow, then mid's folded-in test file), bridge's
// own production import of dep finds dep still on the stack and receives
// the production-only rescue, and — the part that used to fail — entry,
// checked later and unrelated to the cycle itself, imports dep directly
// AND imports other, passing a *dep.Thing between them. Before the
// consistency fix, entry.Field (resolved once dep was no longer "in
// progress") and other.Use's parameter (resolved earlier, nested, while
// dep was still in progress) were two nominally distinct types, so
// entry.go itself failed to type-check with a spurious "cannot use ... as
// ... value" error — which, being a production file's error, cost dep's
// entire test suite its call-site visibility via the "retrying without"
// fallback, exactly as add_context_parameter first observed it.
func TestAddContextParameter_Thread_TestOnlyImportDiamond(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"dep/dep.go": `package dep

type Thing struct{}

func Seed(name string) string { return name }
`,
		"dep/dep_test.go": `package dep

import (
	"testing"

	"example.com/threadmcp/slow"
)

func TestDep(t *testing.T) {
	Seed("x")
	slow.Ping()
}
`,
		"slow/slow.go": `package slow

import "example.com/threadmcp/mid"

func Ping() { mid.Noop() }
`,
		"mid/mid.go": `package mid

func Noop() {}
`,
		// mid's OWN in-package test file is what reaches bridge — folded in
		// only because TypeCheckPackage checks mid at all (triggered by
		// slow's plain, production-only need for it), not because slow's
		// import actually needed mid's tests. Real Go never compiles a
		// dependency's test files for someone else's sake, so this shape is
		// valid, ordinary Go with no cycle — confirmed against the real Go
		// toolchain (go build/go vet/go test all succeed on it) while
		// writing the topology this test is based on.
		"mid/mid_test.go": `package mid

import (
	"testing"

	"example.com/threadmcp/bridge"
)

func TestMid(t *testing.T) {
	bridge.Run(&bridge.Bridge{})
}
`,
		"bridge/bridge.go": `package bridge

import (
	"example.com/threadmcp/dep"
	"example.com/threadmcp/other"
)

type Bridge struct {
	Field *dep.Thing
}

func Run(b *Bridge) {
	other.Use(b.Field)
}
`,
		"other/other.go": `package other

import "example.com/threadmcp/dep"

func Use(t *dep.Thing) {}
`,
		"entry/entry.go": `package entry

import (
	"example.com/threadmcp/dep"
	"example.com/threadmcp/other"
)

type Entry struct {
	Field *dep.Thing
}

func Run(e *Entry) {
	other.Use(e.Field)
}
`,
	})

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "add_context_parameter",
		Arguments: map[string]any{
			"function_name": "Seed",
			"source_file":   "dep/dep.go",
			"thread":        true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned error: %s", errText(result))
	}

	declContent, err := os.ReadFile(filepath.Join(dir, "dep", "dep.go"))
	if err != nil {
		t.Fatalf("reading dep.go: %v", err)
	}
	if !strings.Contains(string(declContent), "func Seed(ctx context.Context, name string) string") {
		t.Errorf("expected Seed to gain a ctx context.Context parameter, got:\n%s", declContent)
	}

	testContent, err := os.ReadFile(filepath.Join(dir, "dep", "dep_test.go"))
	if err != nil {
		t.Fatalf("reading dep_test.go: %v", err)
	}
	got := string(testContent)
	if !strings.Contains(got, `Seed(t.Context(), "x")`) {
		t.Errorf("expected the _test.go call site to be rewritten with t.Context(), got:\n%s", got)
	}

	// entry.go, bridge.go and other.go never called Seed and must be left
	// alone — this also implicitly confirms the workspace still type-checked
	// cleanly enough for threading to run at all (a two-views-of-dep
	// mismatch anywhere in this workspace would have aborted the whole
	// operation before it got this far).
	for _, untouched := range []string{"entry/entry.go", "bridge/bridge.go", "other/other.go"} {
		content, err := os.ReadFile(filepath.Join(dir, untouched))
		if err != nil {
			t.Fatalf("reading %s: %v", untouched, err)
		}
		if strings.Contains(string(content), "context.Context") {
			t.Errorf("expected %s to be untouched, got:\n%s", untouched, content)
		}
	}
}
