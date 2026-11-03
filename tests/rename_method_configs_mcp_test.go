package tests_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	methodTDefault = "//go:build !tagx\n\npackage p\n\ntype T struct{}\n\nfunc (T) M() {}\n"
	methodTTagx    = "//go:build tagx\n\npackage p\n\ntype T struct{}\n\nfunc (T) M() {}\n"
)

func renameMethod(t *testing.T, sess *mcptest.Session, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "rename_method", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func renameTMToN(extra map[string]any) map[string]any {
	args := map[string]any{"type_name": "T", "method_name": "M", "new_method_name": "N"}
	maps.Copy(args, extra)
	return args
}

func TestRenameMethod_RenamesCallInTaggedTestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":             "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/use.go":           "package p\n\nfunc Use(t T) { t.M() }\n",
		"p/use_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { T{}.M() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_method", renameTMToN(nil))

	assertFileContains(t, dir, "p/t.go", "func (T) N()")
	assertFileContains(t, dir, "p/use.go", "t.N()")
	assertFileContains(t, dir, "p/use_tagx_test.go", "T{}.N()")
	goVets(t, dir, "", "tagx")
}

func TestRenameMethod_TagSiblingsBothRenamedAndSharedCallerOnce(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t_default.go": methodTDefault,
		"p/t_tagx.go":    methodTTagx,
		"p/use.go":       "package p\n\nfunc Use(t T) { t.M() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "rename_method", renameTMToN(nil))

	configs, _ := out["build_configs"].([]any)
	if len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	for _, f := range []string{"p/t_default.go", "p/t_tagx.go"} {
		got := fileText(t, dir, f)
		if strings.Count(got, "func (T) N()") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	got := fileText(t, dir, "p/use.go")
	if strings.Count(got, "t.N()") != 1 {
		t.Errorf("use.go:\n%s", got)
	}
	goVets(t, dir, "", "tagx")
}

func TestRenameMethod_FindsTypeDeclaredOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t_tagx.go":   methodTTagx,
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use(t T) { t.M() }\n",
		"p/plain.go":    "package p\n\nfunc plain() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_method", renameTMToN(nil))

	assertFileContains(t, dir, "p/t_tagx.go", "func (T) N()")
	assertFileContains(t, dir, "p/use_tagx.go", "t.N()")
	assertFileUnchanged(t, dir, "p/plain.go", "package p\n\nfunc plain() int { return 1 }\n")
	goVets(t, dir, "tagx")
}

func TestRenameMethod_MissingTypeErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameMethod(t, sess, renameTMToN(map[string]any{"type_name": "Nope"}))

	want := "rename method operation validation failed: type Nope not found"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestRenameMethod_MissingMethodErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameMethod(t, sess, renameTMToN(map[string]any{"method_name": "Nope"}))

	want := "rename method operation validation failed: method Nope not found on type T"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestRenameMethod_ReportsBrokenTaggedTestFileAsUncovered(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t_default.go":     methodTDefault,
		"p/t_tagx.go":        methodTTagx,
		"p/bad_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/this-module-does-not-exist/thirdparty\"\n)\n\nfunc TestBad(t *testing.T) {\n\tT{}.M()\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	preview := decodePlanResult(t, renameMethod(t, sess, renameTMToN(map[string]any{"dry_run": true})))
	assertFileUnchanged(t, dir, "p/t_default.go", methodTDefault)
	applied := decodePlanResult(t, renameMethod(t, sess, renameTMToN(nil)))

	assertBrokenTaggedTestReported(t, preview)
	assertBrokenTaggedTestReported(t, applied)
	assertFileContains(t, dir, "p/t_tagx.go", "func (T) N()")
	assertFileContains(t, dir, "p/bad_tagx_test.go", "T{}.M()")
}

func TestRenameMethod_SingleConfigOmitsBuildConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go": "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n\nfunc Use(t T) { t.M() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, dryRun := range []bool{true, false} {
		out := callJSON(t, sess, "rename_method", renameTMToN(map[string]any{"dry_run": dryRun}))
		_, ok := out["build_configs"]
		if ok {
			t.Errorf("dry_run=%v: build_configs = %v, want the field omitted", dryRun, out["build_configs"])
		}
	}
}

func TestRenameMethod_InterfaceRenamesTagOnlyImplementationAndTaggedTestCall(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go":       "package p\n\ntype Runner interface{ Run(n int) int }\n\nfunc Use(r Runner) int { return r.Run(1) }\n",
		"p/a.go":           "//go:build !tagx\n\npackage p\n\ntype A struct{}\n\nfunc (A) Run(n int) int { return n }\n",
		"p/b_tagx.go":      "//go:build tagx\n\npackage p\n\ntype B struct{}\n\nfunc (B) Run(n int) int { return n + 1 }\n",
		"p/b_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {\n\tvar r Runner = B{}\n\tr.Run(2)\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_method", map[string]any{"type_name": "Runner", "method_name": "Run", "new_method_name": "Go"})

	assertFileContains(t, dir, "p/iface.go", "interface{ Go(n int) int }")
	assertFileContains(t, dir, "p/iface.go", "r.Go(1)")
	assertFileContains(t, dir, "p/a.go", "func (A) Go(")
	assertFileContains(t, dir, "p/b_tagx.go", "func (B) Go(")
	assertFileContains(t, dir, "p/b_tagx_test.go", "r.Go(2)")
	goVets(t, dir, "", "tagx")
}
