package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestRenameSymbol_LeavesParameterNamedLikeTheSymbolInExternalTestFile(t *testing.T) {
	ext := "package a_test\n\nfunc check(parse func() int) int { return parse() }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go":          "package a\n\nfunc parse() int { return 1 }\n\nfunc Use() int { return parse() }\n",
		"a/a_ext_test.go": ext,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "parse", "new_name": "parseAll", "package": "a"})

	assertFileContains(t, dir, "a/a.go", "func parseAll()")
	assertFileUnchanged(t, dir, "a/a_ext_test.go", ext)
	goVets(t, dir, "")
}

func TestRenameSymbol_RenamesMapKeyConstantUsedThroughDotImportInExternalTestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go":          "package a\n\ntype Kinds map[int]string\n\nconst Foo = 1\n",
		"a/a_ext_test.go": "package a_test\n\nimport . \"example.com/threadmcp/a\"\n\nvar _ = Kinds{Foo: \"x\"}\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "Foo", "new_name": "Bar", "package": "a"})

	assertFileContains(t, dir, "a/a_ext_test.go", "Kinds{Bar:")
	goVets(t, dir, "")
}

func TestMoveSymbol_LeavesLocalNamedLikeTheSymbolInExternalTestFile(t *testing.T) {
	ext := "package a_test\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tFoo := 2\n\t_ = Foo\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go":          "package a\n\nfunc Foo() int { return 1 }\n",
		"a/a_ext_test.go": ext,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Foo", "a", "dst")
	if res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileUnchanged(t, dir, "a/a_ext_test.go", ext)
	goVets(t, dir, "")
}

func TestRenameSymbol_RenamesCallInTestOnlyPackage(t *testing.T) {
	for _, pkgName := range []string{"integration", "integration_test"} {
		t.Run(pkgName, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"a/a.go":                  "package a\n\nfunc Helper() int { return 1 }\n",
				"integration/run_test.go": "package " + pkgName + "\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/a\"\n)\n\nfunc TestRun(t *testing.T) { _ = a.Helper() }\n",
			})

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "Helper", "new_name": "Assist", "package": "a"})

			assertFileContains(t, dir, "integration/run_test.go", "a.Assist()")
			goVets(t, dir, "")
		})
	}
}
