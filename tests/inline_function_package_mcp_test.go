package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const inlUse = "package p\n\nfunc Use() int { return f(1) }\n"

func TestInlineFunction_SameNameInAnotherPackageIsLeftAlone(t *testing.T) {
	const other = "package q\n\nfunc f(n int) int { return n * 100 }\n\nfunc Use() int { return f(2) }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": sdcF, "p/use.go": inlUse, "q/doc.go": "package q\n", "q/q.go": other, "p/doc.go": "package p\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "inline_function", map[string]any{"function_name": "f", "source_file": "p/f.go"})

	assertFileContains(t, dir, "p/use.go", "return 1")
	assertFileUnchanged(t, dir, "q/q.go", other)
	assertFileUnchanged(t, dir, "q/doc.go", "package q\n")
}
