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

	configs, _ := out["build_configs"].([]any)
	if len(configs) != 2 {
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

	if strings.Contains(msg, ":0:0:") {
		t.Errorf("error carries a bogus position: %s", msg)
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

	diffs, _ := out["diffs"].([]any)
	if len(diffs) != 2 {
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
	goVets(t, dir, "")
}

const batchQ = "package q\n\nfunc Q() int { return 1 }\n"

func batchPackageStepWorkspace(t *testing.T) (string, map[string]string) {
	t.Helper()

	files := map[string]string{
		"src/s.go":   "package src\n\nfunc One() int { return 1 }\n\ntype T struct{ V int }\n\nfunc (T) M() {}\n",
		"q/q.go":     batchQ,
		"use/use.go": "package use\n\nimport (\n\t\"example.com/threadmcp/q\"\n\t\"example.com/threadmcp/src\"\n)\n\nfunc Use() int { return src.One() + q.Q() }\n",
		"other/o.go": "package other\n\nfunc Z() int { return 1 }\n",
	}

	return writeInlineWorkspace(t, files), files
}

func assertBatchRefusedUnchanged(t *testing.T, dir string, files map[string]string, msg string, wantSteps ...string) {
	t.Helper()

	for _, step := range wantSteps {
		if !strings.Contains(msg, step) {
			t.Errorf("error does not mention %q: %s", step, msg)
		}
	}

	if !strings.Contains(msg, "separate") {
		t.Errorf("error does not tell the user to run separate calls: %s", msg)
	}

	for rel, want := range files {
		if rel != "go.mod" {
			assertFileUnchanged(t, dir, rel, want)
		}
	}
}

func TestBatchOperations_RenamePackageAndMoveSymbolIntoItIsRefused(t *testing.T) {
	dir, files := batchPackageStepWorkspace(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := batchError(t, sess, batchArgs(false,
		`{"type":"rename_package","package_path":"q","new_name":"qq"}`,
		`{"type":"move_symbol","symbol":"One","from_package":"src","to_package":"q"}`))

	assertBatchRefusedUnchanged(t, dir, files, msg, "step 1", "step 2")

	if strings.Contains(msg, ":0:0:") {
		t.Errorf("error carries a bogus position: %s", msg)
	}
}

func TestBatchOperations_RenamePackageAndStepOnAnotherPackageBuilds(t *testing.T) {
	dir, _ := batchPackageStepWorkspace(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "batch_operations", batchArgs(false,
		`{"type":"rename_package","package_path":"q","new_name":"qq"}`,
		`{"type":"rename_symbol","symbol":"Z","new_name":"Y","package":"other"}`,
		`{"type":"rename_method","type_name":"T","method_name":"M","new_method_name":"N","package_path":"src"}`))

	assertFileContains(t, dir, "q/q.go", "package qq")
	assertFileContains(t, dir, "other/o.go", "func Y(")
	assertFileContains(t, dir, "src/s.go", "func (T) N()")
	goVets(t, dir, "")
}

func TestBatchOperations_MovePackageAndRenameSymbolInIsRefused(t *testing.T) {
	dir, files := batchPackageStepWorkspace(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := batchError(t, sess, batchArgs(false,
		`{"type":"move_package","source":"q","target":"moved/q"}`,
		`{"type":"rename_symbol","symbol":"Q","new_name":"R","package":"q"}`))

	assertBatchRefusedUnchanged(t, dir, files, msg, "step 1", "step 2")
}

func TestBatchOperations_TwoPackageStepsOnSamePackageAreRefused(t *testing.T) {
	dir, files := batchPackageStepWorkspace(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := batchError(t, sess, batchArgs(false,
		`{"type":"rename_package","package_path":"q","new_name":"qq"}`,
		`{"type":"move_package","source":"q","target":"moved/q"}`))

	assertBatchRefusedUnchanged(t, dir, files, msg, "step 1", "step 2")
}

func TestBatchOperations_PackageStepWithWorkspaceWideStepIsRefused(t *testing.T) {
	dir, files := batchPackageStepWorkspace(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := batchError(t, sess, batchArgs(false,
		`{"type":"rename_method","type_name":"T","method_name":"M","new_method_name":"N"}`,
		`{"type":"rename_package","package_path":"q","new_name":"qq"}`))

	assertBatchRefusedUnchanged(t, dir, files, msg, "step 1", "step 2")
}
