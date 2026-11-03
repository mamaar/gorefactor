package tests_test

import (
	"context"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func changeSig(t *testing.T, sess *mcptest.Session, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "change_signature", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func changeSigOK(t *testing.T, sess *mcptest.Session, args map[string]any) map[string]any {
	t.Helper()
	return callJSON(t, sess, "change_signature", args)
}

func intParams(names ...string) []map[string]any {
	out := make([]map[string]any, len(names))
	for i, n := range names {
		out[i] = map[string]any{"name": n, "type": "int"}
	}
	return out
}

func addMArgs(file string, extra map[string]any) map[string]any {
	args := map[string]any{
		"function_name": "f",
		"source_file":   file,
		"subcommand":    "add_param",
		"params":        intParams("n", "m"),
		"default_value": "0",
		"position":      1,
	}
	maps.Copy(args, extra)
	return args
}

func TestChangeSignature_RewritesCallSiteInTaggedTestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":             "package p\n\nfunc f(n int) int { return n }\n",
		"p/use.go":           "package p\n\nfunc Use() int { return f(1) }\n",
		"p/use_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { f(2) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, addMArgs("p/f.go", nil))

	assertFileContains(t, dir, "p/f.go", "func f(n int, m int) int")
	assertFileContains(t, dir, "p/use.go", "f(1, 0)")
	assertFileContains(t, dir, "p/use_tagx_test.go", "f(2, 0)")
	goVets(t, dir, "", "tagx")
}

func TestChangeSignature_TagSiblingsBothChangeAndSharedCallerOnce(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/use.go":       "package p\n\nfunc Use() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := changeSigOK(t, sess, addMArgs("p/f_default.go", nil))

	configs, _ := out["build_configs"].([]any)
	if len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	for _, f := range []string{"p/f_default.go", "p/f_tagx.go"} {
		got := fileText(t, dir, f)
		if strings.Count(got, "func f(n int, m int) int") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	got := fileText(t, dir, "p/use.go")
	if strings.Count(got, "f(1, 0)") != 1 {
		t.Errorf("use.go:\n%s", got)
	}
	goVets(t, dir, "", "tagx")
}

func TestChangeSignature_FindsTargetDeclaredOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_tagx.go":   ctxFTagx,
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use() int { return f(1) }\n",
		"p/plain.go":    "package p\n\nfunc plain() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, addMArgs("p/f_tagx.go", nil))

	assertFileContains(t, dir, "p/f_tagx.go", "func f(n int, m int) int")
	assertFileContains(t, dir, "p/use_tagx.go", "f(1, 0)")
	got := fileText(t, dir, "p/plain.go")
	if got != "package p\n\nfunc plain() int { return 1 }\n" {
		t.Errorf("plain.go changed:\n%s", got)
	}
	goVets(t, dir, "tagx")
}

func TestChangeSignature_MissingFunctionErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": "package p\n\nfunc f(n int) int { return n }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, addMArgs("p/f.go", map[string]any{"function_name": "nope"}))

	want := "change signature operation validation failed: function nope not found in " + filepath.Join(dir, "p/f.go")
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestChangeSignature_MissingFileErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": "package p\n\nfunc f(n int) int { return n }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, addMArgs("p/gone.go", nil))

	want := "change signature operation validation failed: source file not found: " + filepath.Join(dir, "p/gone.go")
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestChangeSignature_ReportsBrokenTaggedTestFileAsUncovered(t *testing.T) {
	dir := brokenTaggedTestWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	preview := decodePlanResult(t, changeSig(t, sess, addMArgs("p/f_default.go", map[string]any{"dry_run": true})))
	assertFileUnchanged(t, dir, "p/f_default.go", ctxFDefault)
	applied := decodePlanResult(t, changeSig(t, sess, addMArgs("p/f_default.go", nil)))

	assertBrokenTaggedTestReported(t, preview)
	assertBrokenTaggedTestReported(t, applied)
	assertFileContains(t, dir, "p/f_tagx.go", "func f(n int, m int) int")
	assertFileContains(t, dir, "p/bad_tagx_test.go", "f(3)")
}

func assertFileUnchanged(t *testing.T, dir, rel, want string) {
	t.Helper()
	got := fileText(t, dir, rel)
	if got != want {
		t.Errorf("%s changed:\n%s", rel, got)
	}
}

func TestChangeSignature_SingleConfigOmitsBuildConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nfunc f(n int) int { return n }\n\nfunc Use() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, dryRun := range []bool{true, false} {
		out := changeSigOK(t, sess, addMArgs("p/f.go", map[string]any{"dry_run": dryRun}))
		_, ok := out["build_configs"]
		if ok {
			t.Errorf("dry_run=%v: build_configs = %v, want the field omitted", dryRun, out["build_configs"])
		}
	}
}

func TestChangeSignature_RemoveParamReachesTaggedCallSite(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":             "package p\n\nfunc f(n int, m int) int { return n }\n",
		"p/use.go":           "package p\n\nfunc Use() int { return f(1, 5) }\n",
		"p/use_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { f(2, 6) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, map[string]any{
		"function_name": "f", "source_file": "p/f.go", "subcommand": "remove_param", "params": intParams("n"),
	})

	assertFileContains(t, dir, "p/f.go", "func f(n int) int")
	assertFileContains(t, dir, "p/use.go", "f(1)")
	assertFileContains(t, dir, "p/use_tagx_test.go", "f(2)")
	goVets(t, dir, "", "tagx")
}

func TestChangeSignature_SiblingWithDifferentSignatureAborts(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc f(s string) int { return len(s) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, addMArgs("p/f_default.go", nil))

	msg := errText(res)
	if !res.IsError || !strings.Contains(msg, "p/f_tagx.go") || !strings.Contains(msg, "tags=tagx") {
		t.Errorf("error = %q (IsError %v), want one naming p/f_tagx.go and tags=tagx", msg, res.IsError)
	}
	assertFileUnchanged(t, dir, "p/f_default.go", ctxFDefault)
}

func TestChangeSignature_SiblingAlreadyHavingRequestedSignatureIsLeftAlone(t *testing.T) {
	tagx := "//go:build tagx\n\npackage p\n\nfunc f(n int, m int) int { return n + 1 }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    tagx,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, addMArgs("p/f_default.go", nil))

	assertFileContains(t, dir, "p/f_default.go", "func f(n int, m int) int")
	assertFileUnchanged(t, dir, "p/f_tagx.go", tagx)
}

func TestChangeSignature_ImportsNewParameterTypeInSiblingWithoutCallSite(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": "//go:build !tagx\n\npackage p\n\nfunc f(n int) int { return n }\n",
		"p/f_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc f(n int) int { return n + 1 }\n",
		"p/use.go":       "package p\n\nimport \"time\"\n\nvar _ time.Duration\n\nfunc Use() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, map[string]any{
		"function_name": "f", "source_file": "p/f_default.go", "subcommand": "add_param",
		"params":        []map[string]any{{"name": "n", "type": "int"}, {"name": "d", "type": "time.Duration"}},
		"default_value": "0", "position": 1,
	})

	for _, f := range []string{"p/f_default.go", "p/f_tagx.go"} {
		assertFileContains(t, dir, f, "func f(n int, d time.Duration) int")
		assertFileContains(t, dir, f, "\"time\"")
	}
	goVets(t, dir, "", "tagx")
}

func TestChangeSignature_PropagateReachesImplementationOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go":  "package p\n\ntype Runner interface{ Run(n int) int }\n\nfunc Use(r Runner) int { return r.Run(1) }\n",
		"p/a.go":      "//go:build !tagx\n\npackage p\n\ntype A struct{}\n\nfunc (A) Run(n int) int { return n }\n",
		"p/b_tagx.go": "//go:build tagx\n\npackage p\n\ntype B struct{}\n\nfunc (B) Run(n int) int { return n + 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, map[string]any{
		"function_name": "A.Run", "source_file": "p/a.go", "subcommand": "add_param",
		"params": intParams("n", "m"), "default_value": "0", "position": 1, "propagate": true,
	})

	for _, f := range []string{"p/iface.go", "p/a.go", "p/b_tagx.go"} {
		got := fileText(t, dir, f)
		if strings.Count(got, "Run(n int, m int) int") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	assertFileContains(t, dir, "p/iface.go", "r.Run(1, 0)")
	goVets(t, dir, "", "tagx")
}
