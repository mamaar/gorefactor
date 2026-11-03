package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestRenameSymbol_WithPackageLeavesOtherObjectsOfTheSameNameAlone(t *testing.T) {
	const (
		cSrc = "package c\n\nfunc Run() {}\n\nfunc Use() { Run() }\n"
		bSrc = "package b\n\ntype T struct{}\n\nfunc (T) Run() {}\n\nfunc Use(t T) { t.Run() }\n"
	)
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nfunc Run() {}\n\nfunc Use() { Run() }\n",
		"b/b.go": bSrc,
		"c/c.go": cSrc,
		"d/d.go": "package d\n\nimport (\n\t\"example.com/threadmcp/a\"\n\t\"example.com/threadmcp/c\"\n)\n\nfunc Use() { a.Run(); c.Run(); run := func() {}; run() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "Run", "new_name": "Go", "package": "example.com/threadmcp/a"})

	assertFileContains(t, dir, "a/a.go", "func Go() {}")
	assertFileContains(t, dir, "a/a.go", "func Use() { Go() }")
	assertFileContains(t, dir, "d/d.go", "a.Go(); c.Run()")
	assertFileUnchanged(t, dir, "b/b.go", bSrc)
	assertFileUnchanged(t, dir, "c/c.go", cSrc)
	goVets(t, dir, "")
}

func TestRenameSymbol_WithPackageLeavesSameNamedMethodAndLocalAlone(t *testing.T) {
	const src = "package a\n\ntype T struct{}\n\nfunc (T) Run() {}\n\nfunc Run() {}\n\nfunc Use(t T) { t.Run(); Run() }\n\nfunc Local() { Run := 1; _ = Run }\n"
	dir := writeInlineWorkspace(t, map[string]string{"a/a.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "Run", "new_name": "Go", "package": "example.com/threadmcp/a"})

	assertFileContains(t, dir, "a/a.go", "func (T) Run() {}")
	assertFileContains(t, dir, "a/a.go", "func Go() {}")
	assertFileContains(t, dir, "a/a.go", "t.Run(); Go()")
	assertFileContains(t, dir, "a/a.go", "Run := 1; _ = Run")
	goVets(t, dir, "")
}
