package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const siCheck = "package check\n\nfunc Ok() {}\n"

const siAliasedTest = `package p

import (
	"testing"

	assert "example.com/threadmcp/check"
)

func TestA(t *testing.T) {
	assert.Ok()
	assert := 1
	_ = assert
}
`

func siRule(alias string) map[string]any {
	return map[string]any{"package_pattern": "example.com/threadmcp/check", "alias": alias}
}

func TestStandardizeImports_DropsRedundantAliasAndRewritesUsesInTestFiles(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"check/check.go": siCheck,
		"p/p.go":         "package p\n",
		"p/a_test.go":    siAliasedTest,
		"p/b_test.go":    strings.Replace(siAliasedTest, "TestA", "TestB", 1),
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "standardize_imports", map[string]any{
		"rules": []any{siRule("check")},
		"files": []any{"p/a_test.go"},
	})

	assertFileContains(t, dir, "p/a_test.go", "\t\"example.com/threadmcp/check\"\n")
	assertFileContains(t, dir, "p/a_test.go", "\tcheck.Ok()\n")
	// The shadowing local variable is not the package and stays.
	assertFileContains(t, dir, "p/a_test.go", "assert := 1")
	assertFileContains(t, dir, "p/b_test.go", "assert \"example.com/threadmcp/check\"")
	goVets(t, dir, "")
}

func TestStandardizeImports_AddsAliasAndRewritesUses(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"check/check.go": siCheck,
		"p/p.go":         "package p\n\nimport \"example.com/threadmcp/check\"\n\nfunc F() { check.Ok() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "standardize_imports", map[string]any{"rules": []any{siRule("chk")}})

	assertFileContains(t, dir, "p/p.go", "chk \"example.com/threadmcp/check\"")
	assertFileContains(t, dir, "p/p.go", "chk.Ok()")
	goVets(t, dir, "")
}

func TestStandardizeImports_SkipsFileWhenNewNameIsTakenAtAUse(t *testing.T) {
	src := "package p\n\nimport assert \"example.com/threadmcp/check\"\n\nfunc F(check int) {\n\tassert.Ok()\n\t_ = check\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"check/check.go": siCheck,
		"p/p.go":         src,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "standardize_imports", map[string]any{"rules": []any{siRule("check")}})

	assertFileUnchanged(t, dir, "p/p.go", src)
	sites, _ := out["skipped_sites"].([]any)
	if len(sites) != 1 {
		t.Errorf("skipped_sites = %v, want one entry", out["skipped_sites"])
	}
}

func TestStandardizeImports_UnknownFileIsAnError(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"check/check.go": siCheck})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "standardize_imports", map[string]any{
		"rules": []any{siRule("check")}, "files": []any{"nope_test.go"},
	})
	if !res.IsError {
		t.Fatalf("want an error for a file not in the workspace, got %s", errText(res))
	}
}
