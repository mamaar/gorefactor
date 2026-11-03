package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestRenameMethod_UpdatesOnlyUsesOfTheTargetMethod(t *testing.T) {
	const qUse = "package q\n\nimport \"example.com/threadmcp/common\"\n\ntype Q struct{}\n\nfunc (Q) M() {}\n\nfunc Use(q Q) { common.M(); q.M() }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"common/c.go": "package common\n\nfunc M() {}\n",
		"p/p.go":      "package p\n\nimport \"example.com/threadmcp/common\"\n\ntype P struct{}\n\nfunc (*P) M() {}\n\nfunc Use(p *P) { common.M(); p.M() }\n",
		"q/q.go":      qUse,
		"w/w.go":      "package w\n\nimport \"example.com/threadmcp/p\"\n\ntype W struct{ *p.P }\n\nfunc Use(w W) { w.M(); w.P.M() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_method", map[string]any{
		"type_name": "P", "method_name": "M", "new_method_name": "N", "package_path": "example.com/threadmcp/p",
	})

	assertFileContains(t, dir, "p/p.go", "func (*P) N()")
	assertFileContains(t, dir, "p/p.go", "common.M(); p.N()")
	assertFileContains(t, dir, "w/w.go", "w.N(); w.P.N()")
	assertFileUnchanged(t, dir, "common/c.go", "package common\n\nfunc M() {}\n")
	assertFileUnchanged(t, dir, "q/q.go", qUse)
	goVets(t, dir, "")
}
