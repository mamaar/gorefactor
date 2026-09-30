package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	batchF       = "package p\n\nfunc f(n int) int { return n }\n"
	batchUse     = "package p\n\nfunc Use() int { return f(1) }\n"
	batchUseTagx = "//go:build tagx\n\npackage p\n\nfunc UseTagx() int { return f(2) }\n"
)

func batchArgs(dryRun bool, steps ...string) map[string]any {
	args := map[string]any{"operations": steps}
	if dryRun {
		args["dry_run"] = true
	}
	return args
}

func batchError(t *testing.T, sess *mcptest.Session, args map[string]any) string {
	t.Helper()
	res := callTool(t, sess, "batch_operations", args)
	if !res.IsError {
		t.Fatalf("batch_operations succeeded, want an error: %s", errText(res))
	}
	return errText(res)
}

func TestBatchOperations_RenameSymbolRewritesFileOnlyATagCompiles(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": batchF, "p/use.go": batchUse, "p/use_tagx.go": batchUseTagx,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "batch_operations", batchArgs(false,
		`{"type":"rename_symbol","symbol":"f","new_name":"g"}`))

	assertFileContains(t, dir, "p/f.go", "func g(n int) int")
	assertFileContains(t, dir, "p/use.go", "g(1)")
	assertFileContains(t, dir, "p/use_tagx.go", "g(2)")
	if configs, _ := out["build_configs"].([]any); len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	goVets(t, dir, "", "tagx")
}

func TestBatchOperations_RenameMethodOnInterfaceRenamesImplementationsAndCalls(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/i.go":   "package p\n\ntype I interface{ M() }\n\ntype A struct{}\n\nfunc (A) M() {}\n",
		"p/use.go": "package p\n\nfunc Use(i I) { i.M() }\n\nfunc UseA(a A) { a.M() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "batch_operations", batchArgs(false,
		`{"type":"rename_method","type_name":"I","method_name":"M","new_method_name":"N"}`))

	assertFileContains(t, dir, "p/i.go", "type I interface{ N() }")
	assertFileContains(t, dir, "p/i.go", "func (A) N()")
	assertFileContains(t, dir, "p/use.go", "i.N()")
	assertFileContains(t, dir, "p/use.go", "a.N()")
	goVets(t, dir, "")
}

func TestBatchOperations_OverlappingStepsFailAndWriteNothing(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": batchF, "p/use.go": batchUse})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := batchError(t, sess, batchArgs(false,
		`{"type":"rename_symbol","symbol":"f","new_name":"g"}`,
		`{"type":"rename_symbol","symbol":"f","new_name":"h"}`))

	if !strings.Contains(msg, "step 1") || !strings.Contains(msg, "step 2") {
		t.Errorf("error does not name both steps: %s", msg)
	}
	assertFileUnchanged(t, dir, "p/f.go", batchF)
	assertFileUnchanged(t, dir, "p/use.go", batchUse)
}

func TestBatchOperations_FailingStepNamesItAndWritesNothing(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": batchF, "p/use.go": batchUse})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := batchError(t, sess, batchArgs(false,
		`{"type":"rename_symbol","symbol":"f","new_name":"g"}`,
		`{"type":"rename_symbol","symbol":"doesNotExist","new_name":"h"}`))

	if !strings.Contains(msg, "step 2") {
		t.Errorf("error does not name step 2: %s", msg)
	}
	assertFileUnchanged(t, dir, "p/f.go", batchF)
	assertFileUnchanged(t, dir, "p/use.go", batchUse)
}

func TestBatchOperations_DryRunReturnsDiffsAndWritesNothing(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": batchF, "p/use.go": batchUse})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "batch_operations", batchArgs(true,
		`{"type":"rename_symbol","symbol":"f","new_name":"g"}`))

	if diffs, _ := out["diffs"].([]any); len(diffs) != 2 {
		t.Errorf("diffs = %v, want one per changed file", out["diffs"])
	}
	assertFileUnchanged(t, dir, "p/f.go", batchF)
	assertFileUnchanged(t, dir, "p/use.go", batchUse)
}

func TestBatchOperations_RenamePackageStepAndRenameSymbolStepCombine(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   batchF,
		"q/use.go": "package q\n\nimport \"example.com/threadmcp/p\"\n\nfunc Use() int { return p.Exported() }\n",
		"p/e.go":   "package p\n\nfunc Exported() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "batch_operations", batchArgs(false,
		`{"type":"rename_symbol","symbol":"f","new_name":"g","package":"p"}`,
		`{"type":"rename_package","package_path":"q","new_name":"qq"}`))

	assertFileContains(t, dir, "p/f.go", "func g(")
	assertFileContains(t, dir, "p/e.go", "g(1)")
	assertFileContains(t, dir, "q/use.go", "package qq")
}
