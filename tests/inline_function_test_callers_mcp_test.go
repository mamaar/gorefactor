package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestInlineFunction_InlinesCallersInTestFiles(t *testing.T) {
	const useTest = "package p\n\nimport \"testing\"\n\nfunc TestUse(t *testing.T) {\n\tif f(2) != 2 {\n\t\tt.Fatal()\n\t}\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": sdcF, "p/use_test.go": useTest})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "inline_function", map[string]any{"function_name": "f", "source_file": "p/f.go"})

	assertFileContains(t, dir, "p/use_test.go", "if 2 != 2")
}

func TestInlineFunction_RefusesWhenCallersExistButNoneCanBeInlined(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nfunc F(n int) int { return n }\n",
		"q/q.go": "package q\n\nimport \"example.com/threadmcp/p\"\n\nfunc Use() int { return p.F(1) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "inline_function", map[string]any{"function_name": "F", "source_file": "p/f.go", "dry_run": true})

	if !res.IsError {
		t.Fatalf("inline_function succeeded with no changes though F has a caller: %s", errText(res))
	}

	assertFileUnchanged(t, dir, "q/q.go", "package q\n\nimport \"example.com/threadmcp/p\"\n\nfunc Use() int { return p.F(1) }\n")
}
