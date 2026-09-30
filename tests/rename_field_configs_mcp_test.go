package tests_test

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	fieldTDefault = "//go:build !tagx\n\npackage p\n\ntype T struct{ A int }\n"
	fieldTTagx    = "//go:build tagx\n\npackage p\n\ntype T struct{ A int }\n"
)

func renameField(t *testing.T, sess *mcptest.Session, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "rename_field", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func renameTAToB(extra map[string]any) map[string]any {
	args := map[string]any{"type_name": "T", "field_name": "A", "new_field_name": "B"}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestRenameField_RenamesKeyedLiteralInTaggedTestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":             "package p\n\ntype T struct{ A int }\n",
		"p/use.go":           "package p\n\nfunc Use() T { return T{A: 1} }\n",
		"p/use_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { _ = T{A: 2} }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_field", renameTAToB(nil))

	assertFileContains(t, dir, "p/t.go", "type T struct{ B int }")
	assertFileContains(t, dir, "p/use.go", "T{B: 1}")
	assertFileContains(t, dir, "p/use_tagx_test.go", "T{B: 2}")
	goVets(t, dir, "", "tagx")
}

func TestRenameField_SiblingStructDeclarationsBothRenamedAndSharedUseOnce(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t_default.go": fieldTDefault,
		"p/t_tagx.go":    fieldTTagx,
		"p/use.go":       "package p\n\nfunc Use(t T) int { return t.A }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "rename_field", renameTAToB(nil))

	if configs, _ := out["build_configs"].([]any); len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	for _, f := range []string{"p/t_default.go", "p/t_tagx.go"} {
		if got := fileText(t, dir, f); strings.Count(got, "type T struct{ B int }") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	if got := fileText(t, dir, "p/use.go"); strings.Count(got, "t.B") != 1 {
		t.Errorf("use.go:\n%s", got)
	}
	goVets(t, dir, "", "tagx")
}

func TestRenameField_FindsTypeDeclaredOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t_tagx.go":   fieldTTagx,
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use() T { return T{A: 1} }\n",
		"p/plain.go":    "package p\n\nfunc plain() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_field", renameTAToB(nil))

	assertFileContains(t, dir, "p/t_tagx.go", "type T struct{ B int }")
	assertFileContains(t, dir, "p/use_tagx.go", "T{B: 1}")
	assertFileUnchanged(t, dir, "p/plain.go", "package p\n\nfunc plain() int { return 1 }\n")
	goVets(t, dir, "tagx")
}

func TestRenameField_MissingTypeErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": "package p\n\ntype T struct{ A int }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameField(t, sess, renameTAToB(map[string]any{"type_name": "Nope"}))

	want := "rename field operation validation failed: type Nope not found in workspace"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestRenameField_MissingFieldErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": "package p\n\ntype T struct{ A int }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameField(t, sess, renameTAToB(map[string]any{"field_name": "Nope"}))

	want := "rename field operation validation failed: field Nope not found on type T"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestRenameField_ReportsBrokenTaggedTestFileAsUncovered(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t_default.go":     fieldTDefault,
		"p/t_tagx.go":        fieldTTagx,
		"p/bad_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/this-module-does-not-exist/thirdparty\"\n)\n\nfunc TestBad(t *testing.T) {\n\t_ = T{A: 3}\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	preview := decodePlanResult(t, renameField(t, sess, renameTAToB(map[string]any{"dry_run": true})))
	assertFileUnchanged(t, dir, "p/t_default.go", fieldTDefault)
	applied := decodePlanResult(t, renameField(t, sess, renameTAToB(nil)))

	assertBrokenTaggedTestReported(t, preview)
	assertBrokenTaggedTestReported(t, applied)
	assertFileContains(t, dir, "p/t_tagx.go", "type T struct{ B int }")
	assertFileContains(t, dir, "p/bad_tagx_test.go", "T{A: 3}")
}

func TestRenameField_SingleConfigOmitsBuildConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go": "package p\n\ntype T struct{ A int }\n\nfunc Use(t T) int { return t.A }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, dryRun := range []bool{true, false} {
		out := callJSON(t, sess, "rename_field", renameTAToB(map[string]any{"dry_run": dryRun}))
		if _, ok := out["build_configs"]; ok {
			t.Errorf("dry_run=%v: build_configs = %v, want the field omitted", dryRun, out["build_configs"])
		}
	}
}
