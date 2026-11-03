package tests_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func renameSymbol(t *testing.T, sess *mcptest.Session, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "rename_symbol", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	return res
}

func renameFToG(extra map[string]any) map[string]any {
	args := map[string]any{"symbol": "f", "new_name": "g"}
	maps.Copy(args, extra)

	return args
}

func TestRenameSymbol_RenamesReferenceInTaggedTestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":             "package p\n\nfunc f(n int) int { return n }\n",
		"p/use.go":           "package p\n\nfunc Use() int { return f(1) }\n",
		"p/use_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { f(2) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", renameFToG(nil))

	assertFileContains(t, dir, "p/f.go", "func g(n int) int")
	assertFileContains(t, dir, "p/use.go", "g(1)")
	assertFileContains(t, dir, "p/use_tagx_test.go", "g(2)")
	goVets(t, dir, "", "tagx")
}

func TestRenameSymbol_TagSiblingsBothRenamedAndSharedCallerOnce(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/use.go":       "package p\n\nfunc Use() int { return f(1) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "rename_symbol", renameFToG(nil))

	configs, _ := out["build_configs"].([]any)
	if len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}

	for _, f := range []string{"p/f_default.go", "p/f_tagx.go"} {
		got := fileText(t, dir, f)
		if strings.Count(got, "func g(n int) int") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}

	got := fileText(t, dir, "p/use.go")
	if strings.Count(got, "g(1)") != 1 {
		t.Errorf("use.go:\n%s", got)
	}

	goVets(t, dir, "", "tagx")
}

func TestRenameSymbol_FindsTargetDeclaredOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_tagx.go":   ctxFTagx,
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use() int { return f(1) }\n",
		"p/plain.go":    "package p\n\nfunc plain() int { return 1 }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", renameFToG(nil))

	assertFileContains(t, dir, "p/f_tagx.go", "func g(n int) int")
	assertFileContains(t, dir, "p/use_tagx.go", "g(1)")
	assertFileUnchanged(t, dir, "p/plain.go", "package p\n\nfunc plain() int { return 1 }\n")
	goVets(t, dir, "tagx")
}

func TestRenameSymbol_MissingSymbolErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": "package p\n\nfunc f(n int) int { return n }\n"})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameSymbol(t, sess, map[string]any{"symbol": "nope", "new_name": "g"})

	want := "rename operation validation failed: no symbols found with name: nope"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestRenameSymbol_ReportsBrokenTaggedTestFileAsUncovered(t *testing.T) {
	dir := brokenTaggedTestWorkspace(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	preview := decodePlanResult(t, renameSymbol(t, sess, renameFToG(map[string]any{"dry_run": true})))
	assertFileUnchanged(t, dir, "p/f_default.go", ctxFDefault)
	applied := decodePlanResult(t, renameSymbol(t, sess, renameFToG(nil)))

	assertBrokenTaggedTestReported(t, preview)
	assertBrokenTaggedTestReported(t, applied)
	assertFileContains(t, dir, "p/f_tagx.go", "func g(n int) int")
	assertFileContains(t, dir, "p/bad_tagx_test.go", "f(3)")
}

func TestRenameSymbol_SingleConfigOmitsBuildConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nfunc f(n int) int { return n }\n\nfunc Use() int { return f(1) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, dryRun := range []bool{true, false} {
		out := callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "f", "new_name": "g", "dry_run": dryRun})

		_, ok := out["build_configs"]
		if ok {
			t.Errorf("dry_run=%v: build_configs = %v, want the field omitted", dryRun, out["build_configs"])
		}
	}
}

func TestRenameSymbol_NewNameTakenOnlyUnderTagAborts(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":      "package p\n\nfunc f(n int) int { return n }\n",
		"p/g_tagx.go": "//go:build tagx\n\npackage p\n\nfunc g() {}\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameSymbol(t, sess, renameFToG(nil))

	msg := errText(res)
	if !res.IsError || !strings.Contains(msg, "tags=tagx") || !strings.Contains(msg, "name conflict") {
		t.Errorf("error = %q (IsError %v), want a name conflict naming tags=tagx", msg, res.IsError)
	}

	assertFileUnchanged(t, dir, "p/f.go", "package p\n\nfunc f(n int) int { return n }\n")
}

func TestRenameSymbol_NewNameTakenByTagOnlyAlternativeIsNotAConflict(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/g_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc g() {}\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", renameFToG(nil))

	assertFileContains(t, dir, "p/f_default.go", "func g(n int) int")
	goVets(t, dir, "", "tagx")
}

func TestRenameSymbol_TypeUsedOnlyInTypeExpressionInTaggedFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":        "package p\n\ntype T struct{}\n",
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use(x T) {}\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "T", "new_name": "U"})

	assertFileContains(t, dir, "p/t.go", "type U struct{}")
	assertFileContains(t, dir, "p/use_tagx.go", "func Use(x U)")
	goVets(t, dir, "", "tagx")
}
