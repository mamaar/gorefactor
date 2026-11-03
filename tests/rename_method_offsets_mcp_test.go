package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestRenameMethod_RenamesCallInSecondFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/a.go": "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/b.go": "package p\n\nfunc Use(t T) { t.M() }\n",
		"p/c.go": "package p\n\nfunc Use2(t T) { t.M() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_method", map[string]any{"type_name": "T", "method_name": "M", "new_method_name": "N"})

	assertFileContains(t, dir, "p/a.go", "func (T) N()")
	assertFileContains(t, dir, "p/b.go", "t.N()")
	assertFileContains(t, dir, "p/c.go", "t.N()")
	goVets(t, dir, "")
}
