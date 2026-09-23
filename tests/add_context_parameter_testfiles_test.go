package tests_test

import (
	"context"
	"os"
	"path/filepath"
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

// testFilesExcludedWorkspace builds a package whose test files cannot all be
// type-checked together with the package: foo_test.go has a normal, valid
// call site, but the sibling bad_test.go imports a third-party module that
// can never resolve (mirroring the diamond-memoization test in
// pkg/analysis/parser_test.go). That failure costs the WHOLE package's test
// files their type information — not just bad_test.go's — via
// (*analysis.GoParser).TypeCheckPackage's "retry without tests" fallback.
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

func TestBad(t *testing.T) {}
`,
	})
}

// TestAddContextParameter_Thread_AbortsWhenTestFilesExcluded covers the
// residual case the loadExternal/dedup fix cannot help with: there is no
// external module to discover here, the import in bad_test.go simply does
// not exist anywhere. Before this guard, that failure was invisible by the
// time add_context_parameter ran (TypeCheckPackage's fallback retry
// succeeds without the test files, so TypesErr goes back to nil) and the
// operation proceeded to silently thread past every call site in the
// package's test files. It must abort instead, since the seed's signature
// is about to change and those call sites — invisible to this operation —
// would be left uncompilable.
func TestAddContextParameter_Thread_AbortsWhenTestFilesExcluded(t *testing.T) {
	dir := testFilesExcludedWorkspace(t)

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
	if !result.IsError {
		t.Fatalf("expected an error result when the package's test files could not be type-checked, got success: %s",
			errText(result))
	}
	msg := errText(result)
	if !strings.Contains(msg, "test file") {
		t.Errorf("expected error message to mention the excluded test files, got: %s", msg)
	}

	content, err := os.ReadFile(filepath.Join(dir, "foo", "foo.go"))
	if err != nil {
		t.Fatalf("reading foo.go: %v", err)
	}
	if strings.Contains(string(content), "context.Context") {
		t.Errorf("expected no changes to foo.go after an aborted thread request, got:\n%s", content)
	}
}

// TestAddContextParameter_NoThread_AbortsWhenTestFilesExcluded is the same
// scenario through the non-threaded path (default_value / context.TODO() at
// call sites), which is just as unsafe here: it also relies on the
// reference index to find every call site, and a call site hidden by
// TestTypesExcluded is just as invisible to it as to the threaded path.
func TestAddContextParameter_NoThread_AbortsWhenTestFilesExcluded(t *testing.T) {
	dir := testFilesExcludedWorkspace(t)

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	// The non-threaded path, unlike thread: true, never calls
	// EnsureTypeChecked itself — so force a type-check of package foo first,
	// the way any other analysis tool naturally would before this one runs.
	// Without this, TestTypesExcluded would not yet be set and this test
	// would not be exercising the guard at all.
	dci, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "detect_if_init_assignments",
		Arguments: map[string]any{"package": "example.com/threadmcp/foo"},
	})
	if err != nil {
		t.Fatalf("CallTool detect_if_init_assignments: %v", err)
	}
	if dci.IsError {
		t.Fatalf("detect_if_init_assignments returned error: %s", errText(dci))
	}

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "add_context_parameter",
		Arguments: map[string]any{
			"function_name": "Process",
			"source_file":   "foo/foo.go",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error result when the package's test files could not be type-checked, got success: %s",
			errText(result))
	}
	msg := errText(result)
	if !strings.Contains(msg, "test file") {
		t.Errorf("expected error message to mention the excluded test files, got: %s", msg)
	}

	content, err := os.ReadFile(filepath.Join(dir, "foo", "foo.go"))
	if err != nil {
		t.Fatalf("reading foo.go: %v", err)
	}
	if strings.Contains(string(content), "context.Context") {
		t.Errorf("expected no changes to foo.go after an aborted non-threaded request, got:\n%s", content)
	}
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
