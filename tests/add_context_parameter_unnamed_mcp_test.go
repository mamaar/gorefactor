package tests_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestAddContextParameter_Thread_UnnamedParametersOfInterfaceImplementation(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go": "package p\n\ntype Searcher interface {\n\tFind(string) (int, error)\n}\n\ntype fake struct{}\n\n" +
			"func (fake) Find(string) (int, error) { return 0, nil }\n\n" +
			"func leaf(s string) (int, error) { return 0, nil }\n\ntype real struct{}\n\n" +
			"func (real) Find(s string) (int, error) { return leaf(s) }\n\nfunc Use(s Searcher) { s.Find(\"x\") }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", map[string]any{
		"function_name": "leaf", "source_file": filepath.Join(dir, "p/p.go"), "thread": true,
	})

	assertFileContains(t, dir, "p/p.go", "\tFind(context.Context, string) (int, error)\n")
	assertFileContains(t, dir, "p/p.go", "func (fake) Find(context.Context, string) (int, error)")
	assertFileContains(t, dir, "p/p.go", "func (real) Find(ctx context.Context, s string) (int, error)")
	goVets(t, dir, "")
}
