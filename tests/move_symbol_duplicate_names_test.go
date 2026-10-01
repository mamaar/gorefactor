package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestMoveSymbol_MovesASymbolNamedTwiceOnce(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n",
		"use/use.go": "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc U() int { return src.A() + src.B() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolsCall(t, sess, "src", "dst", "A", "B", "A"); res.IsError {
		t.Fatalf("move: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst.go", "func A()")
	assertFileContains(t, dir, "dst/dst.go", "func B()")
	assertFileContains(t, dir, "use/use.go", "dst.A()")
	goVets(t, dir, "")
}
