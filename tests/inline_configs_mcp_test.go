package tests_test

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func callTool(t *testing.T, sess *mcptest.Session, tool string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}

	return res
}

func TestInlineFunction_CallSiteOnlyInTaggedFileIsInlinedAndDeclarationKept(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": sdcF, "p/use_tagx.go": sdcUseTagx})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "inline_function", map[string]any{"function_name": "f", "source_file": "p/f.go"})

	assertFileContains(t, dir, "p/use_tagx.go", "func Use() int { return 1 }")
	assertFileUnchanged(t, dir, "p/f.go", sdcF)

	configs, _ := out["build_configs"].([]any)
	if len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}

	goVets(t, dir, "", "tagx")
}

func TestInlineFunction_SiblingsWithDifferentBodiesAbortNamingBoth(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f_default.go": ctxFDefault, "p/f_tagx.go": ctxFTagx, "p/use.go": inlUse})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "inline_function", map[string]any{"function_name": "f", "source_file": "p/f_default.go"})

	msg := errText(res)
	if !res.IsError {
		t.Fatalf("inline_function succeeded, want it refused: %s", msg)
	}

	for _, want := range []string{"p/f_default.go", "p/f_tagx.go", "default", "tags=tagx"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}

	assertFileUnchanged(t, dir, "p/use.go", inlUse)
}

func TestInlineFunction_SiblingsWithTheSameBodyAreInlinedOnce(t *testing.T) {
	same := strings.Replace(ctxFTagx, "n + 1", "n", 1)
	dir := writeInlineWorkspace(t, map[string]string{"p/f_default.go": ctxFDefault, "p/f_tagx.go": same, "p/use.go": inlUse})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "inline_function", map[string]any{"function_name": "f", "source_file": "p/f_default.go"})

	assertFileUnchanged(t, dir, "p/use.go", "package p\n\nfunc Use() int { return 1 }\n")
	goVets(t, dir, "", "tagx")
}

func TestInlineFunction_MentionInUncheckedFileIsReportedAndDeclarationKept(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":             sdcF,
		"p/use.go":           inlUse,
		"p/bad_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/this-module-does-not-exist/thirdparty\"\n)\n\nfunc TestBad(t *testing.T) {\n\tf(3)\n}\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := decodePlanResult(t, callTool(t, sess, "inline_function", map[string]any{"function_name": "f", "source_file": "p/f.go"}))

	assertBrokenTaggedTestReported(t, out)
	assertFileContains(t, dir, "p/use.go", "return 1")
	assertFileUnchanged(t, dir, "p/f.go", sdcF)
}

func TestInlineFunction_SingleConfigOmitsBuildConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": sdcF, "p/use.go": inlUse})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "inline_function", map[string]any{"function_name": "f", "source_file": "p/f.go"})

	_, ok := out["build_configs"]
	if ok {
		t.Errorf("build_configs = %v, want the field omitted", out["build_configs"])
	}

	assertFileContains(t, dir, "p/use.go", "return 1")

	res := callTool(t, sess, "inline_function", map[string]any{"function_name": "nope", "source_file": "p/f.go"})

	want := "failed to generate inline function plan: function implementation not found: nope"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

const inlT = "package p\n\ntype T struct{ n int }\n"

func inlMethod(build, body string) string {
	return build + "package p\n\nfunc (t T) Get(d int) int { return " + body + " }\n"
}

func TestInlineMethod_TargetFileOnlyUnderTagIsInlined(t *testing.T) {
	const use = "//go:build tagx\n\npackage p\n\nfunc Use(t T) int { return t.Get(1) }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": inlT, "p/get.go": inlMethod("", "t.n + d"), "p/use_tagx.go": use})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "inline_method", map[string]any{"method_name": "Get", "source_struct": "T", "target_file": "p/use_tagx.go"})

	assertFileContains(t, dir, "p/use_tagx.go", "func Use(t T) int { return t.n + 1 }")
	goVets(t, dir, "", "tagx")
}

func TestInlineMethod_SiblingsWithDifferentBodiesAbortNamingBoth(t *testing.T) {
	const use = "package p\n\nfunc Use(t T) int { return t.Get(1) }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":           inlT,
		"p/get_default.go": inlMethod("//go:build !tagx\n\n", "t.n + d"),
		"p/get_tagx.go":    inlMethod("//go:build tagx\n\n", "t.n - d"),
		"p/use.go":         use,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "inline_method", map[string]any{"method_name": "Get", "source_struct": "T", "target_file": "p/use.go"})

	msg := errText(res)
	if !res.IsError {
		t.Fatalf("inline_method succeeded, want it refused: %s", msg)
	}

	for _, want := range []string{"p/get_default.go", "p/get_tagx.go", "tags=tagx"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}

	assertFileUnchanged(t, dir, "p/use.go", use)
}

func TestInlineMethod_SingleConfigOmitsBuildConfigsAndKeepsErrorText(t *testing.T) {
	const use = "package p\n\nfunc Use(t T) int { return t.Get(1) }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": inlT, "p/get.go": inlMethod("", "t.n + d"), "p/use.go": use})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "inline_method", map[string]any{"method_name": "Nope", "source_struct": "T", "target_file": "p/use.go"})

	want := "inline method operation validation failed: method Nope not found on struct T"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}

	out := callJSON(t, sess, "inline_method", map[string]any{"method_name": "Get", "source_struct": "T", "target_file": "p/use.go"})

	_, ok := out["build_configs"]
	if ok {
		t.Errorf("build_configs = %v, want the field omitted", out["build_configs"])
	}

	assertFileContains(t, dir, "p/use.go", "return t.n + 1")
}
