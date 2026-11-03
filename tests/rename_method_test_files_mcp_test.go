package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestRenameMethod_RenamesCallInTestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":      "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/use.go":    "package p\n\nfunc Use(t T) { t.M() }\n",
		"p/t_test.go": "package p\n\nimport \"testing\"\n\nfunc TestT(t *testing.T) { T{}.M() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_method", map[string]any{"type_name": "T", "method_name": "M", "new_method_name": "N"})

	assertFileContains(t, dir, "p/t.go", "func (T) N()")
	assertFileContains(t, dir, "p/use.go", "t.N()")
	assertFileContains(t, dir, "p/t_test.go", "T{}.N()")
	goVets(t, dir, "")
}
